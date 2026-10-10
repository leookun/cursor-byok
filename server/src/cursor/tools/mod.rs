//! Exposes the extensible Cursor Tool system.
use std::{
    collections::{BTreeMap, HashSet},
    sync::Arc,
};

use tokio::sync::Mutex;

pub mod auto_review;
pub mod codec;
pub(crate) mod compat;
pub(crate) mod edit;
pub(crate) mod registry;
pub mod runtime;
mod schedule;
pub(crate) mod stream;
mod tool_call_dispatch;
pub(crate) mod tool_call_result;

use crate::{
    model::{CanonicalMessage, MessageContent, Role, ToolCall},
    search::{WebCache, WebFetch, WebSearch},
    store::Store,
    Error, Result,
};

use self::auto_review::AutoReviewer;
use self::schedule::{DeferredEdit, EditSchedule};
use self::tool_call_result::{ToolCompletion, ToolResultSender};
use super::protocol::proto::agent::v1 as pb;
use runtime::{CursorToolRuntime, ExecContext};

#[derive(Clone)]
pub struct ToolDispatcher {
    runtime: CursorToolRuntime,
    results: ToolResultSender,
    search: WebSearch,
    fetch: WebFetch,
    store: Option<Store>,
    edit_schedule: Arc<Mutex<EditSchedule>>,
    auto_review: Option<AutoReviewer>,
}

pub struct DispatchedTool {
    pub messages: Vec<pb::AgentServerMessage>,
    pub completion: Option<ToolCompletion>,
}

pub struct ToolBatchState<'a> {
    pub completed: &'a HashSet<String>,
    pub started: &'a HashSet<String>,
    pub response_text: &'a str,
    pub response_thinking: &'a str,
}

pub enum ClientToolEvent {
    Completed(Box<ToolCompletion>),
    Pending,
}

impl ToolDispatcher {
    pub fn new(runtime: CursorToolRuntime) -> Self {
        let (results, _) = tool_call_result::tool_result_channel();
        Self {
            runtime,
            results,
            search: WebSearch::built_in(),
            fetch: WebFetch::built_in(),
            store: None,
            edit_schedule: Arc::new(Mutex::new(EditSchedule::default())),
            auto_review: None,
        }
    }

    pub fn with_results(
        runtime: CursorToolRuntime,
        results: ToolResultSender,
        store: Store,
        web_cache: WebCache,
        auto_review: AutoReviewer,
    ) -> Self {
        Self {
            runtime,
            results,
            search: WebSearch::managed(store.clone()),
            fetch: WebFetch::managed(store.clone(), web_cache),
            store: Some(store),
            edit_schedule: Arc::new(Mutex::new(EditSchedule::default())),
            auto_review: Some(auto_review),
        }
    }

    pub async fn start_batch(
        &self,
        calls: &[ToolCall],
        state: ToolBatchState<'_>,
        messages: &[CanonicalMessage],
        dynamic_mcp: &BTreeMap<String, pb::McpToolDefinition>,
        context: &ExecContext,
    ) -> Result<Vec<DispatchedTool>> {
        let first_tool_index = current_turn_step_count(messages)
            + usize::from(!state.response_thinking.is_empty())
            + usize::from(!state.response_text.is_empty())
            + 1;
        let mut dispatched = Vec::new();
        for (position, call) in calls.iter().enumerate() {
            if state.completed.contains(&call.call_id) {
                continue;
            }
            let message_index = first_tool_index + position;
            let publish_started = !state.started.contains(&call.call_id);
            if let Some(error) = &call.argument_error {
                dispatched.push(validation_failure(call, error.clone()));
                continue;
            }
            let edit_path = if dynamic_mcp.contains_key(&call.name) {
                None
            } else {
                match edit::execution_path(call) {
                    Ok(path) => path,
                    Err(error) => {
                        dispatched.push(recover_validation_failure(call, error)?);
                        continue;
                    }
                }
            };
            if let Some(path) = edit_path {
                let next = self.edit_schedule.lock().await.start_or_defer(
                    path,
                    DeferredEdit {
                        call: call.clone(),
                        message_index,
                        publish_started,
                        context: context.clone(),
                    },
                );
                let Some(next) = next else {
                    continue;
                };
                let started = self
                    .start(
                        &next.call,
                        next.message_index,
                        next.publish_started,
                        dynamic_mcp,
                        &next.context,
                        messages,
                    )
                    .await;
                dispatched.push(match started {
                    Ok(started) => started,
                    Err(error) => recover_validation_failure(&next.call, error)?,
                });
                continue;
            }
            let started = self
                .start(
                    call,
                    message_index,
                    publish_started,
                    dynamic_mcp,
                    context,
                    messages,
                )
                .await;
            dispatched.push(match started {
                Ok(started) => started,
                Err(error) => recover_validation_failure(call, error)?,
            });
        }
        Ok(dispatched)
    }

    pub(crate) async fn continue_after(&self, call_id: &str) -> Result<Option<DispatchedTool>> {
        let next = self.edit_schedule.lock().await.complete(call_id)?;
        let Some(next) = next else {
            return Ok(None);
        };
        match self
            .start(
                &next.call,
                next.message_index,
                next.publish_started,
                &BTreeMap::new(),
                &next.context,
                &[],
            )
            .await
        {
            Ok(started) => Ok(Some(started)),
            Err(error) => recover_validation_failure(&next.call, error).map(Some),
        }
    }

    pub async fn interrupt_for_message(&self) -> Vec<u32> {
        self.edit_schedule.lock().await.clear();
        self.runtime.interrupt_for_message().await
    }

    async fn start(
        &self,
        call: &ToolCall,
        message_index: usize,
        publish_started: bool,
        dynamic_mcp: &BTreeMap<String, pb::McpToolDefinition>,
        context: &ExecContext,
        conversation: &[CanonicalMessage],
    ) -> Result<DispatchedTool> {
        let call = context.prepare_call(call)?;
        let mut messages = if publish_started {
            vec![codec::tool_started(&call, dynamic_mcp.get(&call.name))?]
        } else {
            Vec::new()
        };
        let started = tool_call_dispatch::start(
            &self.runtime,
            &self.results,
            &call,
            message_index,
            dynamic_mcp,
            context,
            self.store.as_ref(),
            self.auto_review
                .as_ref()
                .map(|reviewer| tool_call_dispatch::ToolReview {
                    reviewer,
                    messages: conversation,
                }),
        )
        .await?;
        messages.extend(started.messages);
        Ok(DispatchedTool {
            messages,
            completion: started.completion,
        })
    }

    pub async fn interaction_response(
        &self,
        response: &pb::InteractionResponse,
    ) -> Result<ClientToolEvent> {
        if self.runtime.is_interrupted(response.id).await {
            return Ok(ClientToolEvent::Pending);
        }
        let pending = match self.runtime.take_interaction(response.id).await {
            Some(pending) => pending,
            None if self.runtime.completed_call(response.id).await.is_some() => {
                tracing::warn!(
                    id = response.id,
                    "ignoring duplicate terminal interaction response"
                );
                return Ok(ClientToolEvent::Pending);
            }
            None => {
                tracing::warn!(
                    id = response.id,
                    "ignoring response for unknown interaction"
                );
                return Ok(ClientToolEvent::Pending);
            }
        };
        let call = pending.call.clone();
        let continuation = match tool_call_dispatch::resume_interaction(
            &self.results,
            &self.search,
            &self.fetch,
            pending,
            response,
        )
        .await
        {
            Ok(continuation) => continuation,
            Err(Error::Protocol(message)) => {
                return Ok(ClientToolEvent::Completed(Box::new(
                    compat::failure_with_message(&call, message),
                )));
            }
            Err(Error::Json(error)) => {
                return Ok(ClientToolEvent::Completed(Box::new(
                    compat::failure_with_message(&call, error.to_string()),
                )));
            }
            Err(error) => return Err(error),
        };
        Ok(match continuation {
            tool_call_dispatch::InteractionContinuation::Completed(completion) => {
                ClientToolEvent::Completed(completion)
            }
            tool_call_dispatch::InteractionContinuation::Pending => ClientToolEvent::Pending,
        })
    }
}

fn validation_failure(call: &ToolCall, message: String) -> DispatchedTool {
    DispatchedTool {
        messages: Vec::new(),
        completion: Some(compat::failure_with_message(call, message)),
    }
}

fn recover_validation_failure(call: &ToolCall, error: Error) -> Result<DispatchedTool> {
    match error {
        Error::Protocol(message) => Ok(validation_failure(call, message)),
        Error::Json(error) => Ok(validation_failure(call, error.to_string())),
        error => Err(error),
    }
}

fn current_turn_step_count(messages: &[CanonicalMessage]) -> usize {
    let turn_start = messages
        .iter()
        .rposition(|message| message.role == Role::User)
        .map_or(0, |position| position + 1);
    messages[turn_start..]
        .iter()
        .map(|message| match &message.content {
            MessageContent::Assistant {
                text,
                thinking,
                tool_calls,
                ..
            } => {
                usize::from(!thinking.is_empty()) + usize::from(!text.is_empty()) + tool_calls.len()
            }
            _ => 0,
        })
        .sum()
}
