//! Coordinates construction of a complete Cursor checkpoint.
use std::collections::HashSet;

use prost::Message;

use crate::{
    cursor::{
        checkpoint::{messages, PendingSteps},
        protocol::proto::agent::v1 as pb,
        services::blob_sync::BlobSynchronizer,
        transport::TransportHandle,
    },
    model::{CanonicalMessage, ToolCall, ToolDefinition, ToolRoundAssistant},
    store::Store,
    Result,
};

use super::{derived, roots::RootFrontier, turns::TurnFrontier};

const DEFAULT_CONTEXT_WINDOW_TOKENS: u64 = 200_000;

#[derive(Clone)]
pub struct CheckpointBuilder {
    pub(super) store: Store,
    pub(super) sync: BlobSynchronizer,
    pub(super) parent_tool_call_id: Option<String>,
    pub(super) base: pb::ConversationStateStructure,
    pub(super) model: String,
    pub(super) max_context_tokens: Option<u64>,
    pub(super) instructions: String,
    pub(super) tool_definitions: Vec<ToolDefinition>,
    pub(super) allowed_tools: Vec<String>,
    pub(super) dynamic_tools: HashSet<String>,
    pub(super) turn_user: Option<pb::UserMessage>,
    pub(super) roots: Option<RootFrontier>,
    pub(super) turn: Option<TurnFrontier>,
    pub(super) turns_initialized: bool,
}

impl CheckpointBuilder {
    pub fn new(
        store: Store,
        sync: BlobSynchronizer,
        parent_tool_call_id: Option<String>,
        base: Option<pb::ConversationStateStructure>,
    ) -> Self {
        Self {
            store,
            sync,
            parent_tool_call_id,
            base: base.unwrap_or_default(),
            model: String::new(),
            max_context_tokens: None,
            instructions: String::new(),
            tool_definitions: Vec::new(),
            allowed_tools: Vec::new(),
            dynamic_tools: HashSet::new(),
            turn_user: None,
            roots: None,
            turn: None,
            turns_initialized: false,
        }
    }

    pub fn configure(
        &mut self,
        model: String,
        max_context_tokens: Option<u64>,
        instructions: String,
        tool_definitions: Vec<ToolDefinition>,
        dynamic_tools: HashSet<String>,
        turn_user: Option<pb::UserMessage>,
    ) {
        self.model = model;
        self.max_context_tokens = max_context_tokens;
        self.instructions = instructions;
        self.allowed_tools = tool_definitions
            .iter()
            .map(|tool| tool.name.clone())
            .collect();
        self.tool_definitions = tool_definitions;
        self.dynamic_tools = dynamic_tools;
        self.turn_user = turn_user;
    }

    pub(crate) fn record_context_tokens(&mut self, used_tokens: Option<u64>) {
        let previous = self
            .base
            .token_details
            .as_ref()
            .map(|details| details.max_tokens as u64);
        let max_tokens = context_limit(self.max_context_tokens, previous);
        let Some(max_tokens) = max_tokens else {
            return;
        };
        let details = self.base.token_details.get_or_insert_with(Default::default);
        if let Some(used_tokens) = used_tokens {
            details.used_tokens = used_tokens.min(u32::MAX as u64) as u32;
        }
        details.max_tokens = max_tokens.min(u32::MAX as u64) as u32;
        details.prompt_context_usage_tree = None;
        details.prompt_context_usage_snapshot_blob_id = None;
    }

    pub async fn settled(
        &mut self,
        messages: &[CanonicalMessage],
        mode: i32,
        presentation: &PendingSteps,
    ) -> Result<pb::ConversationStateStructure> {
        self.build_state(messages, mode, Vec::new(), presentation)
            .await
    }

    pub async fn staged_tool_round(
        &mut self,
        stable_messages: &[CanonicalMessage],
        mode: i32,
        assistant: &ToolRoundAssistant,
        calls: &[ToolCall],
        started_at_ms: u64,
        presentation: &PendingSteps,
    ) -> Result<pb::ConversationStateStructure> {
        let pending = messages::staged_tool_round(
            assistant,
            calls,
            &self.model,
            &self.allowed_tools,
            &self.dynamic_tools,
            started_at_ms,
        )?;
        self.build_state(stable_messages, mode, vec![pending], presentation)
            .await
    }

    pub async fn staged_final(
        &mut self,
        stable_messages: &[CanonicalMessage],
        mode: i32,
        assistant: &CanonicalMessage,
        started_at_ms: u64,
        presentation: &PendingSteps,
    ) -> Result<pb::ConversationStateStructure> {
        let pending = messages::staged_final(
            assistant,
            &self.model,
            &self.allowed_tools,
            &self.dynamic_tools,
            started_at_ms,
        )?;
        self.build_state(stable_messages, mode, vec![pending], presentation)
            .await
    }

    async fn build_state(
        &mut self,
        messages: &[CanonicalMessage],
        mode: i32,
        pending_tool_calls: Vec<String>,
        presentation: &PendingSteps,
    ) -> Result<pb::ConversationStateStructure> {
        self.record_background_subagents(presentation);
        let root_ids = self.project_roots(messages).await?;
        let turn_ids = self.project_turns(mode, presentation).await?;
        let (todo_ids, plan_id) = self.build_derived_state(messages).await?;
        self.base.todos = todo_ids.iter().map(|id| id.as_bytes().to_vec()).collect();
        self.base.plan = plan_id.as_ref().map(|id| id.as_bytes().to_vec());
        let communicate_update_states_by_parent_tool_call_id = self
            .parent_tool_call_id
            .as_ref()
            .and_then(|parent| {
                derived::update_current_step_state(messages).map(|state| (parent.clone(), state))
            })
            .into_iter()
            .collect();

        for path in &presentation.read_paths {
            if !self.base.read_paths.contains(path) {
                self.base.read_paths.push(path.clone());
            }
        }
        let mut checkpoint = self.base.clone();
        checkpoint.root_prompt_messages_json =
            root_ids.iter().map(|id| id.as_bytes().to_vec()).collect();
        checkpoint.turns = turn_ids.iter().map(|id| id.as_bytes().to_vec()).collect();
        checkpoint.pending_tool_calls = pending_tool_calls;
        checkpoint.mode = Some(mode);
        checkpoint.communicate_update_states_by_parent_tool_call_id =
            communicate_update_states_by_parent_tool_call_id;
        if let Some(details) = checkpoint.token_details.as_mut() {
            details.breakdown = Some(crate::cursor::services::usage::breakdown(
                details.used_tokens,
                details.max_tokens,
                details.breakdown.as_ref(),
                &self.instructions,
                &self.tool_definitions,
                &self.dynamic_tools,
                messages,
            )?);
        }
        Ok(checkpoint)
    }

    fn record_background_subagents(&mut self, presentation: &PendingSteps) {
        for step in &presentation.steps {
            let Some(pb::conversation_step::Message::ToolCall(call)) = step.message.as_ref() else {
                continue;
            };
            let Some(pb::tool_call::Tool::TaskToolCall(task)) = call.tool.as_ref() else {
                continue;
            };
            let (Some(args), Some(result)) = (task.args.as_ref(), task.result.as_ref()) else {
                continue;
            };
            let Some(pb::task_result::Result::Success(success)) = result.result.as_ref() else {
                continue;
            };
            if !success.is_background {
                continue;
            }
            let Some(agent_id) = success.agent_id.as_ref().filter(|id| !id.is_empty()) else {
                continue;
            };
            let Some(tool_call_id) = call.tool_call_id.as_ref().filter(|id| !id.is_empty()) else {
                continue;
            };
            let started_at_ms = call
                .started_at_ms
                .unwrap_or_else(crate::cursor::tools::runtime::now_ms);
            let last_used_timestamp_ms = call.completed_at_ms.unwrap_or(started_at_ms);
            self.base
                .subagent_states
                .entry(agent_id.clone())
                .and_modify(|state| state.last_used_timestamp_ms = last_used_timestamp_ms)
                .or_insert_with(|| pb::SubagentPersistedState {
                    conversation_state: None,
                    created_timestamp_ms: started_at_ms,
                    last_used_timestamp_ms,
                    subagent_type: args.subagent_type.clone(),
                    model_id: args.model.clone(),
                    environment: args.environment,
                    cloud_subagent: None,
                    first_class_bc_id: None,
                    cloud_requested_environment_build_id: None,
                    machine: args.machine.clone(),
                });
            self.base.subagent_runs_by_parent_tool_call_id.insert(
                tool_call_id.clone(),
                pb::SubagentRunState {
                    parent_tool_call_id: tool_call_id.clone(),
                    subagent_id: Some(agent_id.clone()),
                    environment: args.environment,
                    status: pb::SubagentRunStatus::Backgrounded as i32,
                    title: Some(args.description.clone()),
                    detail: success.result_suffix.clone(),
                    transcript_path: success.transcript_path.clone(),
                    output_path: None,
                    completed_timestamp_ms: None,
                    completion_reason: None,
                },
            );
        }
    }

    pub async fn publish(
        &self,
        handle: &TransportHandle,
        checkpoint: &pb::ConversationStateStructure,
    ) -> Result<()> {
        tracing::debug!(
            request_id = self.sync.request_id(),
            stable_roots = checkpoint.root_prompt_messages_json.len(),
            pending_assistants = checkpoint.pending_tool_calls.len(),
            "publishing Cursor checkpoint"
        );
        let result = handle.emit(&pb::AgentServerMessage {
            ttft_breakdown: None,
            message: Some(
                pb::agent_server_message::Message::ConversationCheckpointUpdate(checkpoint.clone()),
            ),
        });
        if let Some(trace) = handle.trace() {
            trace.artifact(
                "checkpoint",
                "byok_server",
                &checkpoint.encode_to_vec(),
                serde_json::json!({
                    "root_message_count": checkpoint.root_prompt_messages_json.len(),
                    "turn_count": checkpoint.turns.len(),
                    "pending_tool_call_count": checkpoint.pending_tool_calls.len(),
                    "emit_status": if result.is_ok() { "sent" } else { "error" },
                }),
            );
        }
        result
    }
}

fn context_limit(selected: Option<u64>, previous: Option<u64>) -> Option<u64> {
    selected
        .or(previous.filter(|tokens| *tokens != 0))
        .or(Some(DEFAULT_CONTEXT_WINDOW_TOKENS))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn context_limit_defaults_to_legacy_window() {
        assert_eq!(context_limit(None, None), Some(200_000));
        assert_eq!(context_limit(None, Some(0)), Some(200_000));
    }

    #[test]
    fn context_limit_prefers_selected_then_previous_window() {
        assert_eq!(context_limit(Some(64_000), Some(100_000)), Some(64_000));
        assert_eq!(context_limit(None, Some(100_000)), Some(100_000));
    }
}
