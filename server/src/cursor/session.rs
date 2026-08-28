use std::collections::{BTreeMap, HashMap, HashSet, VecDeque};

use tokio::sync::{mpsc, oneshot};

use crate::{
    client::{ClientCommand, ClientEvent, ClientSession, CommitCause},
    cursor::{
        blob_sync::BlobSynchronizer,
        checkpoint::{
            worker::{CheckpointJob, CheckpointKind, CheckpointWorker, FinalCheckpoints},
            CheckpointBuilder,
        },
        interaction,
        presentation::Presentation,
        prompting::PromptCompiler,
        proto::agent::v1 as pb,
        request::{
            compile_injection, compile_user_message_action, CursorRunContext, RuntimeAction,
        },
        tools::{
            codec,
            result::{ToolCompletion, ToolResultReceiver},
            runtime::CursorToolRuntime,
            stream::ToolCallStream,
            ToolBatchState, ToolDispatcher,
        },
    },
    model::{ToolCall, ToolRoundId, Usage},
    run::{RunFailure, RunOutcome},
    store::{Store, ToolRoundStatus},
    Error, Result,
};

use super::CursorSessionHandle;

pub struct CursorSession {
    handle: CursorSessionHandle,
    store: Store,
    context: CursorRunContext,
    core: ClientSession,
    tools: ToolDispatcher,
    results: ToolResultReceiver,
    checkpoint: CheckpointBuilder,
    tool_runtime: CursorToolRuntime,
    runtime_actions: mpsc::UnboundedReceiver<RuntimeAction>,
    compiler: PromptCompiler,
    blob_sync: BlobSynchronizer,
    injection_ids: HashSet<String>,
    pending_injections: HashMap<String, PendingInjection>,
}

struct PendingInjection {
    user_message: Option<pb::UserMessage>,
    delivery_batch_id: String,
}

struct InjectionState<'a> {
    active_round: Option<&'a ToolRoundId>,
    active_tool_calls: &'a HashSet<String>,
    completions: &'a HashMap<String, ToolCompletion>,
    interrupted_rounds: &'a mut HashSet<ToolRoundId>,
    interrupted_tool_calls: &'a mut HashSet<String>,
}

pub(crate) struct CursorSessionRuntime {
    pub tools: ToolDispatcher,
    pub results: ToolResultReceiver,
    pub checkpoint: CheckpointBuilder,
    pub tool_runtime: CursorToolRuntime,
    pub runtime_actions: mpsc::UnboundedReceiver<RuntimeAction>,
    pub compiler: PromptCompiler,
    pub blob_sync: BlobSynchronizer,
}

impl CursorSession {
    pub(crate) fn new(
        handle: CursorSessionHandle,
        store: Store,
        context: CursorRunContext,
        core: ClientSession,
        runtime: CursorSessionRuntime,
    ) -> Self {
        Self {
            handle,
            store,
            context,
            core,
            tools: runtime.tools,
            results: runtime.results,
            checkpoint: runtime.checkpoint,
            tool_runtime: runtime.tool_runtime,
            runtime_actions: runtime.runtime_actions,
            compiler: runtime.compiler,
            blob_sync: runtime.blob_sync,
            injection_ids: HashSet::new(),
            pending_injections: HashMap::new(),
        }
    }

    pub async fn run(mut self) -> Result<()> {
        let result = self.run_inner().await;
        if let Err(error) = &result {
            self.abort_execs().await;
            let error = match error {
                Error::Protocol(message) => message.clone(),
                error => error.to_string(),
            };
            let _ = self
                .core
                .commands
                .send(ClientCommand::ClientClosed { error })
                .await;
        }
        result
    }

    async fn run_inner(&mut self) -> Result<()> {
        if self.context.compacting {
            self.handle.emit(&interaction::summary_started())?;
        }
        let mut worker = CheckpointWorker::spawn(
            self.store.clone(),
            self.checkpoint.clone(),
            self.handle.clone(),
            self.context.mode,
        );
        let mut checkpoint_worker_open = true;
        let mut calls = BTreeMap::<usize, ToolCall>::new();
        let mut streams = BTreeMap::<usize, ToolCallStream>::new();
        let mut completions = HashMap::<String, ToolCompletion>::new();
        let mut completed = HashSet::<String>::new();
        let mut response_text = String::new();
        let mut response_thinking = String::new();
        let mut active_round = None::<ToolRoundId>;
        let mut active_tool_calls = HashSet::<String>::new();
        let mut interrupted_rounds = HashSet::<ToolRoundId>::new();
        let mut interrupted_tool_calls = HashSet::<String>::new();
        let mut final_checkpoint = None::<FinalCheckpoints>;
        let mut compaction_checkpoint = None::<pb::ConversationStateStructure>;
        let mut turn_usage = None::<Usage>;
        let mut context_tokens = None::<u64>;
        let mut ready = VecDeque::new();
        let mut presentation = Presentation::default();

        loop {
            let input = if let Ok(action) = self.runtime_actions.try_recv() {
                Input::RuntimeAction(Some(Box::new(action)))
            } else if let Some(completion) = ready.pop_front() {
                Input::Completion(completion)
            } else {
                tokio::select! {
                    biased;
                    action = self.runtime_actions.recv() => Input::RuntimeAction(action.map(Box::new)),
                    event = self.core.events.recv() => Input::Event(event),
                    completion = self.results.recv() => Input::CompletionResult(completion),
                    failure = worker.failures.recv(), if checkpoint_worker_open => Input::CheckpointFailure(failure),
                }
            };
            match input {
                Input::CheckpointFailure(Some(error)) => return Err(error),
                Input::CheckpointFailure(None) => {
                    checkpoint_worker_open = false;
                }
                Input::Completion(completion) => {
                    if let Some(completion) = self
                        .forward_completion(completion, &mut completions, &interrupted_tool_calls)
                        .await?
                    {
                        ready.push_back(completion);
                    }
                }
                Input::CompletionResult(Some(result)) => {
                    if let Some(completion) = self
                        .forward_completion(result?, &mut completions, &interrupted_tool_calls)
                        .await?
                    {
                        ready.push_back(completion);
                    }
                }
                Input::CompletionResult(None) => {
                    return Err(Error::Protocol("tool result channel closed".into()));
                }
                Input::RuntimeAction(Some(action)) => match *action {
                    RuntimeAction::Inject(action) => {
                        self.forward_injection(
                            action,
                            active_round.as_ref(),
                            &active_tool_calls,
                            &completions,
                            &mut interrupted_rounds,
                            &mut interrupted_tool_calls,
                        )
                        .await?;
                    }
                    RuntimeAction::UserMessage(action) => {
                        self.forward_user_message(
                            action,
                            active_round.as_ref(),
                            &active_tool_calls,
                            &completions,
                            &mut interrupted_rounds,
                            &mut interrupted_tool_calls,
                        )
                        .await?;
                    }
                },
                Input::RuntimeAction(None) => {
                    return Err(Error::Protocol("runtime action channel closed".into()));
                }
                Input::Event(None) => {
                    worker.abort();
                    return Err(Error::Protocol("core event channel closed".into()));
                }
                Input::Event(Some(event)) => match event {
                    ClientEvent::AutoCompactionStarted => {
                        self.handle.emit(&interaction::summary_started())?;
                    }
                    ClientEvent::AutoCompactionCompleted => {
                        self.handle.emit(&interaction::summary_completed())?;
                    }
                    ClientEvent::TextStart => {}
                    ClientEvent::TextEnd => {
                        if !self.context.compacting {
                            presentation.finish_text();
                        }
                    }
                    ClientEvent::TextDelta(delta) => {
                        response_text.push_str(&delta);
                        if self.context.compacting {
                            self.handle.emit(&interaction::summary_delta(delta))?;
                        } else {
                            presentation.text_delta(&delta);
                            self.emit_model_event(
                                crate::provider::ModelEvent::TextDelta(delta),
                                "",
                            )?;
                        }
                    }
                    ClientEvent::ThinkingStart => {}
                    ClientEvent::ThinkingDelta(delta) => {
                        response_thinking.push_str(&delta);
                        if !self.context.compacting {
                            presentation.thinking_delta(&delta);
                            self.emit_model_event(
                                crate::provider::ModelEvent::ThinkingDelta(delta),
                                "",
                            )?;
                        }
                    }
                    ClientEvent::ThinkingEnd { duration } => {
                        if !self.context.compacting {
                            presentation.finish_thinking(duration);
                            self.handle
                                .emit(&interaction::thinking_completed(duration))?;
                        }
                    }
                    ClientEvent::ToolCallStart {
                        index,
                        call_id,
                        name,
                        model_call_id,
                    } => {
                        let call = ToolCall {
                            index,
                            call_id: call_id.clone(),
                            model_call_id: model_call_id.clone(),
                            name: name.clone(),
                            arguments_text: String::new(),
                            arguments: serde_json::Value::Null,
                        };
                        self.emit_model_event(
                            crate::provider::ModelEvent::ToolCallStart {
                                index,
                                call_id,
                                name: name.clone(),
                            },
                            &model_call_id,
                        )?;
                        streams.insert(
                            index,
                            ToolCallStream::new(&name, self.context.dynamic_tools.get(&name)),
                        );
                        calls.insert(index, call);
                    }
                    ClientEvent::ToolCallArgumentsDelta { index, delta } => {
                        let call = calls.get_mut(&index).ok_or_else(|| {
                            Error::Protocol(format!("unknown streaming tool index: {index}"))
                        })?;
                        call.arguments_text.push_str(&delta);
                        let stream = streams.get_mut(&index).ok_or_else(|| {
                            Error::Protocol(format!("missing Cursor tool stream: {index}"))
                        })?;
                        for message in stream.arguments_delta(call, &delta)? {
                            self.handle.emit(&message)?;
                        }
                    }
                    ClientEvent::ToolCallEnd { index } => {
                        let call = calls.get_mut(&index).ok_or_else(|| {
                            Error::Protocol(format!("unknown completed tool index: {index}"))
                        })?;
                        call.arguments = serde_json::from_str(&call.arguments_text)?;
                    }
                    ClientEvent::Usage(usage) => {
                        if !self.context.compacting {
                            if let Some(output_tokens) = usage.output_tokens {
                                self.handle.emit(&interaction::token_delta(output_tokens))?;
                            }
                        }
                        if !self.context.compacting {
                            context_tokens = usage
                                .input_tokens
                                .zip(usage.output_tokens)
                                .and_then(|(input, output)| input.checked_add(output));
                        }
                        match &mut turn_usage {
                            Some(total) => *total += usage,
                            None => turn_usage = Some(usage),
                        }
                    }
                    ClientEvent::ExecuteToolRound {
                        round_id,
                        calls: round_calls,
                    } => {
                        active_round = Some(round_id.clone());
                        active_tool_calls = round_calls
                            .iter()
                            .map(|call| call.call_id.clone())
                            .collect();
                        // Runtime actions are deliberately prioritized over core events. An
                        // injection can therefore be observed before the already-queued
                        // ToolRoundStarted event reaches this session. In that case the
                        // accepted injection is still pending delivery and this round must be
                        // detached without starting any root tools.
                        if interrupted_rounds.contains(&round_id)
                            || !self.pending_injections.is_empty()
                        {
                            interrupted_rounds.insert(round_id.clone());
                            interrupted_tool_calls.extend(active_tool_calls.iter().cloned());
                            continue;
                        }
                        for dispatched in self
                            .tools
                            .start_batch(
                                &round_calls,
                                ToolBatchState {
                                    completed: &completed,
                                    started: &HashSet::new(),
                                    response_text: &response_text,
                                    response_thinking: &response_thinking,
                                },
                                &self
                                    .store
                                    .load_current_messages(&crate::model::ConversationId::new(
                                        &self.context.exec.conversation_id,
                                    ))
                                    .await?,
                                &self.context.dynamic_tools,
                                &self.context.exec,
                            )
                            .await?
                        {
                            for message in dispatched.messages {
                                self.handle.emit(&message)?;
                            }
                            if let Some(completion) = dispatched.completion {
                                ready.push_back(completion);
                            }
                        }
                        response_text.clear();
                        response_thinking.clear();
                        calls.clear();
                        streams.clear();
                    }
                    ClientEvent::StateCommitted(state) => {
                        if matches!(&state.cause, CommitCause::RuntimeEvent { .. }) {
                            response_text.clear();
                            response_thinking.clear();
                            calls.clear();
                            streams.clear();
                        }
                        if let CommitCause::RuntimeEvent { event_id } = &state.cause {
                            if let Some(injection_id) = event_id.strip_prefix("inject-context:") {
                                if let Some(pending) = self.pending_injections.remove(injection_id)
                                {
                                    let delivered_at_ms = crate::cursor::tools::runtime::now_ms()
                                        .min(i64::MAX as u64)
                                        as i64;
                                    self.handle.emit(&interaction::context_injection_delivered(
                                        injection_id.to_owned(),
                                        pending.delivery_batch_id.clone(),
                                        delivered_at_ms,
                                    ))?;
                                    if let Some(user_message) = pending.user_message {
                                        self.handle.emit(&interaction::user_message_appended(
                                            user_message,
                                        ))?;
                                    }
                                }
                            }
                        }
                        if let CommitCause::ToolRoundStarted(round_id) = &state.cause {
                            active_round = Some(round_id.clone());
                        }
                        let mut tool_round_settled = false;
                        if let CommitCause::ToolResult {
                            call_id,
                            interrupted,
                        } = &state.cause
                        {
                            let snapshot = self
                                .store
                                .tool_round(active_round.as_ref().ok_or_else(|| {
                                    Error::Protocol("tool commit has no active round".into())
                                })?)
                                .await?
                                .ok_or_else(|| {
                                    Error::Store("active tool round disappeared".into())
                                })?;
                            let call = snapshot
                                .calls
                                .iter()
                                .find(|call| call.call_id == *call_id)
                                .ok_or_else(|| {
                                    Error::Protocol(format!(
                                        "committed call is absent from tool round: {call_id}"
                                    ))
                                })?;
                            if !interrupted {
                                let completion = completions.remove(call_id).ok_or_else(|| {
                                    Error::Protocol(format!(
                                        "core committed a tool result without typed Cursor state: {call_id}"
                                    ))
                                })?;
                                self.handle
                                    .emit(&interaction::tool_completed(call, &completion))?;
                                presentation.tool_completed(&completion);
                            }
                            completed.insert(call_id.clone());
                            tool_round_settled = snapshot.status == ToolRoundStatus::Settled;
                        }
                        let final_turn = state.cause == CommitCause::FinalTurn;
                        if let CommitCause::Compaction { summary } = &state.cause {
                            if !state.barrier.is_required() {
                                return Err(Error::Protocol(
                                    "compaction state has no completion barrier".into(),
                                ));
                            }
                            let (sender, receiver) = oneshot::channel();
                            worker
                                .jobs
                                .send(CheckpointJob {
                                    kind: CheckpointKind::Compaction {
                                        revision_id: state.revision_id,
                                        summary: summary.clone(),
                                        result: sender,
                                    },
                                    presentation: presentation.take(),
                                    context_tokens: None,
                                    ready: None,
                                })
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker closed".into()))?;
                            match receiver
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker stopped".into()))?
                            {
                                Ok(checkpoint) => {
                                    if self.context.compacting {
                                        compaction_checkpoint = Some(checkpoint);
                                    } else if let Err(error) =
                                        self.checkpoint.publish(&self.handle, &checkpoint).await
                                    {
                                        state.barrier.complete(Err(error.to_string()));
                                        return Err(error);
                                    }
                                    state.barrier.complete(Ok(()));
                                }
                                Err(error) => {
                                    state.barrier.complete(Err(error.to_string()));
                                    return Err(error);
                                }
                            }
                            continue;
                        }
                        if final_turn {
                            if !state.barrier.is_required() {
                                return Err(Error::Protocol(
                                    "final state has no completion barrier".into(),
                                ));
                            }
                            let (sender, receiver) = oneshot::channel();
                            worker
                                .jobs
                                .send(CheckpointJob {
                                    kind: CheckpointKind::Final {
                                        revision_id: state.revision_id,
                                        result: sender,
                                    },
                                    presentation: presentation.take(),
                                    context_tokens,
                                    ready: None,
                                })
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker closed".into()))?;
                            match receiver
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker stopped".into()))?
                            {
                                Ok(checkpoints) => {
                                    final_checkpoint = Some(checkpoints);
                                    state.barrier.complete(Ok(()));
                                }
                                Err(error) => {
                                    state.barrier.complete(Err(error.to_string()));
                                    return Err(error);
                                }
                            }
                        } else if let CommitCause::ToolRoundStarted(round_id) = &state.cause {
                            worker
                                .jobs
                                .send(CheckpointJob {
                                    kind: CheckpointKind::ToolStarted {
                                        round_id: round_id.clone(),
                                        stable_revision_id: state.revision_id,
                                    },
                                    presentation: presentation.take(),
                                    context_tokens,
                                    ready: None,
                                })
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker closed".into()))?;
                        } else if tool_round_settled {
                            if !state.barrier.is_required() {
                                return Err(Error::Protocol(
                                    "settled tool round has no completion barrier".into(),
                                ));
                            }
                            let (ready, published) = oneshot::channel();
                            worker
                                .jobs
                                .send(CheckpointJob {
                                    kind: CheckpointKind::ToolSettled(state.revision_id),
                                    presentation: presentation.take(),
                                    context_tokens,
                                    ready: Some(ready),
                                })
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker closed".into()))?;
                            let result = published
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker stopped".into()))?
                                .map_err(Error::Protocol);
                            match result {
                                Ok(()) => state.barrier.complete(Ok(())),
                                Err(error) => {
                                    state.barrier.complete(Err(error.to_string()));
                                    return Err(error);
                                }
                            }
                            if let Some(round_id) = active_round.take() {
                                interrupted_rounds.remove(&round_id);
                            }
                            active_tool_calls.clear();
                            self.tool_runtime.clear_completed().await;
                        } else if !matches!(&state.cause, CommitCause::ToolResult { .. })
                            && active_round.is_some()
                        {
                            let round_id = active_round.clone().ok_or_else(|| {
                                Error::Protocol("active tool round disappeared".into())
                            })?;
                            worker
                                .jobs
                                .send(CheckpointJob {
                                    kind: CheckpointKind::ToolStarted {
                                        round_id,
                                        stable_revision_id: state.revision_id,
                                    },
                                    presentation: presentation.take(),
                                    context_tokens,
                                    ready: None,
                                })
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker closed".into()))?;
                        } else if !matches!(&state.cause, CommitCause::ToolResult { .. }) {
                            let requires_ready = state.barrier.is_required();
                            let (ready, published) = oneshot::channel();
                            worker
                                .jobs
                                .send(CheckpointJob {
                                    kind: CheckpointKind::Settled(state.revision_id),
                                    presentation: presentation.take(),
                                    context_tokens,
                                    ready: requires_ready.then_some(ready),
                                })
                                .await
                                .map_err(|_| Error::Protocol("checkpoint worker closed".into()))?;
                            if requires_ready {
                                let result = published
                                    .await
                                    .map_err(|_| {
                                        Error::Protocol("checkpoint worker stopped".into())
                                    })?
                                    .map_err(Error::Protocol);
                                match result {
                                    Ok(()) => state.barrier.complete(Ok(())),
                                    Err(error) => {
                                        state.barrier.complete(Err(error.to_string()));
                                        return Err(error);
                                    }
                                }
                            }
                        }
                    }
                    ClientEvent::Ended(outcome) => {
                        return match outcome {
                            RunOutcome::Completed => {
                                if self.context.compacting {
                                    let checkpoint =
                                        compaction_checkpoint.take().ok_or_else(|| {
                                            Error::Protocol(
                                                "Completed compaction without checkpoint".into(),
                                            )
                                        })?;
                                    self.handle.emit(&interaction::summary_completed())?;
                                    self.handle.emit(&interaction::turn_ended(turn_usage))?;
                                    for _ in 0..3 {
                                        self.checkpoint.publish(&self.handle, &checkpoint).await?;
                                    }
                                    crate::cursor::lifecycle::finish_success(&self.handle);
                                    return Ok(());
                                }
                                let checkpoints = final_checkpoint.take().ok_or_else(|| {
                                    Error::Protocol("Completed without final state".into())
                                })?;
                                self.handle.emit(&interaction::turn_ended(turn_usage))?;
                                self.checkpoint
                                    .publish(&self.handle, &checkpoints.staged)
                                    .await?;
                                self.checkpoint
                                    .publish(&self.handle, &checkpoints.settled)
                                    .await?;
                                self.handle.emit(&pb::AgentServerMessage {
                                    ttft_breakdown: None,
                                    message: Some(pb::agent_server_message::Message::ConversationCheckpointUpdate(checkpoints.settled)),
                                })?;
                                crate::cursor::lifecycle::finish_success(&self.handle);
                                Ok(())
                            }
                            RunOutcome::Cancelled => {
                                worker.abort();
                                self.abort_execs().await;
                                crate::cursor::lifecycle::cancel(&self.handle)
                            }
                            RunOutcome::Failed(failure) => {
                                worker.abort();
                                self.abort_execs().await;
                                crate::cursor::lifecycle::fail(&self.handle, &cursor_error(failure))
                            }
                        };
                    }
                },
            }
        }
    }

    async fn abort_execs(&self) {
        for id in self.tool_runtime.drain_running().await {
            let _ = self.handle.emit(&codec::abort(id));
        }
    }

    async fn forward_completion(
        &self,
        mut completion: ToolCompletion,
        completions: &mut HashMap<String, ToolCompletion>,
        interrupted_tool_calls: &HashSet<String>,
    ) -> Result<Option<ToolCompletion>> {
        if interrupted_tool_calls.contains(&completion.result().call_id) {
            return Ok(None);
        }
        if let Some(image) = completion.take_read_image() {
            let blob_id = self.store.put_blob(&image.data, &[]).await?;
            completion.persist_read_image(&blob_id, &image)?;
        }
        let result = completion.result();
        if result.call_id.is_empty() {
            return Err(Error::Protocol("tool result call_id is empty".into()));
        }
        if completions
            .insert(result.call_id.clone(), completion.clone())
            .is_some()
        {
            return Err(Error::Protocol(format!(
                "duplicate tool result call_id: {}",
                result.call_id
            )));
        }
        self.core
            .commands
            .send(ClientCommand::ToolResult(result.clone()))
            .await
            .map_err(|_| Error::RunNotFound(self.context.request_id.clone()))?;
        let Some(dispatched) = self.tools.continue_after(&result.call_id).await? else {
            return Ok(None);
        };
        for message in dispatched.messages {
            self.handle.emit(&message)?;
        }
        Ok(dispatched.completion)
    }

    async fn forward_user_message(
        &mut self,
        action: pb::UserMessageAction,
        active_round: Option<&ToolRoundId>,
        active_tool_calls: &HashSet<String>,
        completions: &HashMap<String, ToolCompletion>,
        interrupted_rounds: &mut HashSet<ToolRoundId>,
        interrupted_tool_calls: &mut HashSet<String>,
    ) -> Result<()> {
        let user_message = action.user_message.clone().ok_or_else(|| {
            Error::Protocol("Cursor user message action has no UserMessage".into())
        })?;
        let injection_id = format!("user-message:{}", user_message.message_id);
        let message = compile_user_message_action(
            &action,
            self.context.mode,
            &self.compiler,
            &self.blob_sync,
        )
        .await?;
        self.queue_injection(
            injection_id,
            Some(user_message),
            message,
            InjectionState {
                active_round,
                active_tool_calls,
                completions,
                interrupted_rounds,
                interrupted_tool_calls,
            },
        )
        .await
    }

    async fn forward_injection(
        &mut self,
        action: pb::InjectContextAction,
        active_round: Option<&ToolRoundId>,
        active_tool_calls: &HashSet<String>,
        completions: &HashMap<String, ToolCompletion>,
        interrupted_rounds: &mut HashSet<ToolRoundId>,
        interrupted_tool_calls: &mut HashSet<String>,
    ) -> Result<()> {
        if action.injection_id.is_empty() {
            return Err(Error::Protocol(
                "InjectContextAction has no injection_id".into(),
            ));
        }
        if self.injection_ids.contains(&action.injection_id) {
            return Ok(());
        }
        if action.expected_run_id != self.context.request_id {
            let reason = format!(
                "InjectContextAction expected run {}, active run is {}",
                action.expected_run_id, self.context.request_id
            );
            self.handle.emit(&interaction::context_injection_rejected(
                action.injection_id.clone(),
                reason,
            ))?;
            self.injection_ids.insert(action.injection_id);
            return Ok(());
        }
        let user_message = match action.payload.as_ref() {
            Some(pb::inject_context_action::Payload::UserContext(context)) => {
                context.user_message.clone()
            }
            _ => None,
        };
        let message =
            compile_injection(&action, self.context.mode, &self.compiler, &self.blob_sync).await?;
        self.queue_injection(
            action.injection_id,
            user_message,
            message,
            InjectionState {
                active_round,
                active_tool_calls,
                completions,
                interrupted_rounds,
                interrupted_tool_calls,
            },
        )
        .await
    }

    async fn queue_injection(
        &mut self,
        injection_id: String,
        user_message: Option<pb::UserMessage>,
        message: crate::model::CanonicalMessage,
        state: InjectionState<'_>,
    ) -> Result<()> {
        let delivery_batch_id = injection_id.clone();
        self.injection_ids.insert(injection_id.clone());
        self.pending_injections.insert(
            injection_id.clone(),
            PendingInjection {
                user_message,
                delivery_batch_id,
            },
        );
        self.handle
            .emit(&interaction::context_injection_queued(injection_id.clone()))?;
        state.interrupted_tool_calls.extend(
            state
                .active_tool_calls
                .iter()
                .filter(|call_id| !state.completions.contains_key(*call_id))
                .cloned(),
        );
        if let Some(round_id) = state.active_round {
            state.interrupted_rounds.insert(round_id.clone());
        }
        self.interrupt_execs().await;
        if self
            .core
            .commands
            .send(ClientCommand::InterruptWithMessage(message))
            .await
            .is_err()
        {
            self.pending_injections.remove(&injection_id);
            return Err(Error::RunNotFound(self.context.request_id.clone()));
        }
        Ok(())
    }

    async fn interrupt_execs(&self) {
        for id in self.tools.interrupt_for_message().await {
            let _ = self.handle.emit(&codec::abort(id));
        }
    }

    fn emit_model_event(
        &self,
        event: crate::provider::ModelEvent,
        model_call_id: &str,
    ) -> Result<()> {
        if let Some(message) =
            interaction::response_event(&event, model_call_id, &self.context.dynamic_tools)?
        {
            self.handle.emit(&message)?;
        }
        Ok(())
    }
}

enum Input {
    Event(Option<ClientEvent>),
    Completion(ToolCompletion),
    CompletionResult(Option<Result<ToolCompletion>>),
    RuntimeAction(Option<Box<RuntimeAction>>),
    CheckpointFailure(Option<Error>),
}

fn cursor_error(failure: RunFailure) -> Error {
    match failure {
        RunFailure::Protocol(message) => Error::Protocol(message),
        RunFailure::Provider(message) => Error::Provider(message),
        RunFailure::Store(message) => Error::Store(message),
        RunFailure::Client(message) => Error::Protocol(message),
    }
}
