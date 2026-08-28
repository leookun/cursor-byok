use std::collections::HashSet;
use std::{sync::Arc, time::Duration};

use tokio_util::sync::CancellationToken;

use crate::{
    client::{
        ClientCommand, ClientEvent, ClientPort, CommitBarrier, CommitCause, MessageInsertion,
        StateCommitted,
    },
    model::{
        CanonicalMessage, MessageContent, Origin, PreparedRun, Role, RunAction, ToolRoundAssistant,
        ToolRoundId, Usage,
    },
    provider::Provider,
    store::{RunStatus, Store},
};

use super::{consume_model_cycle, ModelCycleFailure, RunFailure, RunOutcome};

const COMPACTION_MIN_RESERVE_TOKENS: u64 = 10_000;
const COMPACTION_OUTPUT_TOKENS: u64 = 2_048;
const COMPACTION_OUTPUT_SAFETY_TOKENS: u64 = 4_096;
const AUTO_COMPACTION_TIMEOUT: Duration = Duration::from_secs(30);
const COMPACTION_FALLBACK_CHARS: usize = 12_000;
const COMPACTION_INSTRUCTIONS: &str = "Summarize the conversation for the next model turn. Preserve goals, constraints, decisions, files, commands, errors, results, and unfinished work. Do not call tools. Return only the concise durable summary.";

pub struct RunEngine {
    store: Store,
    provider: Arc<dyn Provider>,
}

impl RunEngine {
    pub fn new(store: Store, provider: Arc<dyn Provider>) -> Self {
        Self { store, provider }
    }

    #[tracing::instrument(
        skip_all,
        fields(run_id = %prepared.run_id, conversation_id = %prepared.conversation_id)
    )]
    pub async fn run(
        &self,
        prepared: PreparedRun,
        mut client: ClientPort,
        cancellation: CancellationToken,
    ) -> RunOutcome {
        let claimed = match self.store.claim_run(&prepared).await {
            Ok(claimed) => claimed,
            Err(error) => {
                let outcome = RunOutcome::Failed(error.into());
                let _ = client
                    .events
                    .send(ClientEvent::Ended(outcome.clone()))
                    .await;
                tracing::info!(outcome = ?outcome, "Run claim failed");
                return outcome;
            }
        };
        let outcome = self
            .run_claimed(
                &prepared,
                claimed.head_revision_id,
                &mut client,
                &cancellation,
            )
            .await;
        let usage = outcome.1;
        let outcome = outcome.0;
        let (status, failure) = match &outcome {
            RunOutcome::Completed => (RunStatus::Completed, None),
            RunOutcome::Cancelled => (RunStatus::Cancelled, None),
            RunOutcome::Failed(failure) => (
                RunStatus::Failed,
                Some((failure.category(), failure_message(failure))),
            ),
        };
        let failure_ref = failure
            .as_ref()
            .map(|(category, summary)| (*category, summary.as_str()));
        if let Err(error) = self
            .store
            .finish_run(&prepared.run_id, status, usage, failure_ref)
            .await
        {
            tracing::error!(run_id = %prepared.run_id, %error, "failed to persist Run outcome");
        }
        let _ = client
            .events
            .send(ClientEvent::Ended(outcome.clone()))
            .await;
        tracing::info!(outcome = ?outcome, usage = ?usage, "Run ended");
        outcome
    }

    async fn run_claimed(
        &self,
        prepared: &PreparedRun,
        mut revision: crate::model::RevisionId,
        client: &mut ClientPort,
        cancellation: &CancellationToken,
    ) -> (RunOutcome, Option<Usage>) {
        let mut usage = None;
        tracing::info!(
            revision_id = revision.0,
            "Run claimed conversation ownership"
        );
        if !prepared.initial_messages.is_empty() {
            let mut changed = false;
            for message in &prepared.initial_messages {
                match self
                    .store
                    .append_message_once(
                        &prepared.conversation_id,
                        &prepared.run_id,
                        revision,
                        message,
                    )
                    .await
                {
                    Ok((next, inserted)) => {
                        revision = next;
                        changed |= inserted;
                    }
                    Err(error) => return (RunOutcome::Failed(error.into()), usage),
                }
            }
            if changed {
                let (barrier, ready) = CommitBarrier::before_continue();
                if emit(
                    client,
                    ClientEvent::StateCommitted(StateCommitted {
                        revision_id: revision,
                        tool_round_version: 0,
                        cause: CommitCause::InitialMessages,
                        barrier,
                    }),
                )
                .await
                .is_err()
                {
                    return (client_failure(), usage);
                }
                if let Err(outcome) = wait_for_state_ready(ready, cancellation).await {
                    return (outcome, usage);
                }
            }
        }

        if let RunAction::Resume {
            pending_tool_round: Some(round),
        } = &prepared.action
        {
            revision = match super::tool_round::execute(
                &self.store,
                prepared,
                client,
                cancellation,
                revision,
                super::tool_round::ToolRound {
                    id: ToolRoundId::new(format!("{}:round:resume", prepared.run_id)),
                    assistant: round.assistant.clone(),
                    calls: round.calls.clone(),
                    recovered_started_at_ms: Some(round.started_at_ms),
                },
                Vec::new(),
            )
            .await
            {
                Ok(revision) => revision,
                Err(outcome) => return (outcome, usage),
            };
        }

        let mut last_auto_compaction_revision = None;
        let mut provider_completed_this_run = false;
        'model: loop {
            if cancellation.is_cancelled() {
                return (RunOutcome::Cancelled, usage);
            }
            let messages = match self.store.load_revision_messages(revision).await {
                Ok(messages) => messages,
                Err(error) => return (RunOutcome::Failed(error.into()), usage),
            };
            let can_auto_compact =
                auto_compaction_allowed(&prepared.action, revision, last_auto_compaction_revision);
            // A Resume can start from a freshly compacted checkpoint while the latest
            // completed provider usage still describes the pre-compaction history.
            // Estimate the recovered state directly until this run has a fresh call.
            let may_use_usage_anchor =
                prepared.action == RunAction::Start || provider_completed_this_run;
            let context_anchor = if can_auto_compact && may_use_usage_anchor {
                match self
                    .store
                    .latest_llm_call_usage_anchor(
                        &prepared.conversation_id,
                        &prepared.model.model_id,
                    )
                    .await
                {
                    Ok(anchor) => anchor.and_then(ContextUsageAnchor::from_llm_call),
                    Err(error) => return (RunOutcome::Failed(error.into()), usage),
                }
            } else {
                None
            };
            // Keep upstream's projected-message accounting: provider usage anchors
            // count projected messages, not canonical tool-round fragments.
            let history = match crate::model::project_messages(&messages) {
                Ok(history) => history,
                Err(error) => return (RunOutcome::Failed(error.into()), usage),
            };
            if can_auto_compact
                && should_auto_compact(prepared, &messages, &history, context_anchor)
            {
                match self
                    .auto_compact(prepared, revision, &messages, client, cancellation)
                    .await
                {
                    Ok((next_revision, compaction_usage)) => {
                        revision = next_revision;
                        last_auto_compaction_revision = Some(revision);
                        if let Some(compaction_usage) = compaction_usage {
                            accumulate_usage(&mut usage, compaction_usage);
                        }
                        continue 'model;
                    }
                    Err(outcome) => return (outcome, usage),
                }
            }
            let provider_call_index = match self.store.begin_provider_call(&prepared.run_id).await {
                Ok(index) => index,
                Err(error) => return (RunOutcome::Failed(error.into()), usage),
            };
            tracing::debug!(
                provider_call_index,
                revision_id = revision.0,
                "starting model call"
            );
            let mut history = history;
            if let Err(error) = hydrate_tool_images(&self.store, &mut history).await {
                return (RunOutcome::Failed(error.into()), usage);
            }
            let request = crate::model::ModelRequest {
                prompt: prepared.prompt.clone(),
                model: prepared.model.clone(),
                history,
            };
            let invocation = crate::model::ModelInvocation {
                call_id: format!("{}:{provider_call_index}", prepared.run_id),
                run_id: prepared.run_id.to_string(),
                conversation_id: prepared.conversation_id.to_string(),
                provider_call_index,
                request,
            };
            let cycle_cancellation = cancellation.child_token();
            let cycle_events = client.events.clone();
            let cycle = consume_model_cycle(
                self.provider.stream(invocation, cycle_cancellation.clone()),
                &cycle_events,
                &cycle_cancellation,
            );
            tokio::pin!(cycle);
            let mut pending_insertions = Vec::new();
            let cycle = loop {
                tokio::select! {
                    biased;
                    command = client.commands.recv() => {
                        let message = match command {
                            Some(ClientCommand::InsertMessages(insertion)) => {
                                pending_insertions.push(insertion);
                                continue;
                            }
                            Some(ClientCommand::InterruptWithMessage(message)) => message,
                            Some(ClientCommand::RuntimeEvent(event)) => event.into_message(),
                            Some(ClientCommand::Cancel) => {
                                cycle_cancellation.cancel();
                                return (RunOutcome::Cancelled, usage);
                            }
                            Some(ClientCommand::ClientClosed { error }) => {
                                cycle_cancellation.cancel();
                                return (RunOutcome::Failed(RunFailure::Client(error)), usage);
                            }
                            Some(ClientCommand::ToolResult(_)) => {
                                cycle_cancellation.cancel();
                                return (
                                    RunOutcome::Failed(RunFailure::Protocol(
                                        "received a tool result while the model was running".into(),
                                    )),
                                    usage,
                                );
                            }
                            None => {
                                cycle_cancellation.cancel();
                                return (client_failure(), usage);
                            }
                        };
                        cycle_cancellation.cancel();
                        let interrupted = cycle.await;
                        match interrupted {
                            Ok(cycle) => {
                                if let Some(cycle_usage) = cycle.usage {
                                    accumulate_usage(&mut usage, cycle_usage);
                                }
                            }
                            Err(failure) => {
                                if let Some(cycle_usage) = failure.usage {
                                    accumulate_usage(&mut usage, cycle_usage);
                                }
                            }
                        }
                        revision = match append_insertions(
                            &self.store,
                            prepared,
                            client,
                            cancellation,
                            revision,
                            std::mem::take(&mut pending_insertions),
                        )
                        .await
                        {
                            Ok((revision, _)) => revision,
                            Err(outcome) => return (outcome, usage),
                        };
                        revision = match append_runtime_message(
                            &self.store,
                            prepared,
                            client,
                            cancellation,
                            revision,
                            message,
                        )
                        .await
                        {
                            Ok((revision, _)) => revision,
                            Err(outcome) => return (outcome, usage),
                        };
                        continue 'model;
                    },
                    result = &mut cycle => break result,
                }
            };
            let cycle = match cycle {
                Ok(cycle) => cycle,
                Err(ModelCycleFailure {
                    failure,
                    usage: cycle_usage,
                    ..
                }) => {
                    if let Some(cycle_usage) = cycle_usage {
                        accumulate_usage(&mut usage, cycle_usage);
                    }
                    if cancellation.is_cancelled() {
                        return (RunOutcome::Cancelled, usage);
                    }
                    return (RunOutcome::Failed(failure), usage);
                }
            };
            provider_completed_this_run = true;
            if let Some(cycle_usage) = cycle.usage {
                accumulate_usage(&mut usage, cycle_usage);
            }

            if prepared.action == RunAction::Compact {
                if !cycle.calls.is_empty() {
                    return (
                        RunOutcome::Failed(RunFailure::Protocol(
                            "compaction model returned tool calls".into(),
                        )),
                        usage,
                    );
                }
                let summary = cycle.text.trim().to_string();
                if summary.is_empty() {
                    return (
                        RunOutcome::Failed(RunFailure::Protocol(
                            "compaction model returned an empty summary".into(),
                        )),
                        usage,
                    );
                }
                let event_id = format!("summary:{}", prepared.run_id);
                let summary_message = CanonicalMessage {
                    message_id: format!("runtime:{event_id}"),
                    role: Role::User,
                    origin: Origin::Runtime,
                    content: MessageContent::Parts {
                        parts: vec![crate::model::ContentPart::Text {
                            text: format!(
                                "<conversation_summary>\n{summary}\n</conversation_summary>"
                            ),
                        }],
                    },
                    runtime_event_id: Some(event_id),
                };
                revision = match self
                    .store
                    .replace_revision(
                        &prepared.conversation_id,
                        &prepared.run_id,
                        revision,
                        &[summary_message],
                    )
                    .await
                {
                    Ok(revision) => revision,
                    Err(error) => return (RunOutcome::Failed(error.into()), usage),
                };
                let (barrier, ready) = CommitBarrier::before_continue();
                if emit(
                    client,
                    ClientEvent::StateCommitted(StateCommitted {
                        revision_id: revision,
                        tool_round_version: 0,
                        cause: CommitCause::Compaction { summary },
                        barrier,
                    }),
                )
                .await
                .is_err()
                {
                    return (client_failure(), usage);
                }
                if let Err(outcome) = wait_for_state_ready(ready, cancellation).await {
                    return (outcome, usage);
                }
                return (RunOutcome::Completed, usage);
            }

            if cycle.calls.is_empty() {
                let assistant = CanonicalMessage {
                    message_id: format!("{}:assistant:{provider_call_index}", prepared.run_id),
                    role: Role::Assistant,
                    origin: Origin::Assistant,
                    content: MessageContent::Assistant {
                        text: cycle.text,
                        thinking: cycle.reasoning,
                        tool_round_id: None,
                        replay_state: cycle.replay_state,
                        tool_calls: Vec::new(),
                    },
                    runtime_event_id: None,
                };
                revision = match self
                    .store
                    .append_revision(
                        &prepared.conversation_id,
                        &prepared.run_id,
                        revision,
                        &[assistant],
                    )
                    .await
                {
                    Ok(revision) => revision,
                    Err(error) => return (RunOutcome::Failed(error.into()), usage),
                };
                if !pending_insertions.is_empty() {
                    let inserted = match append_insertions(
                        &self.store,
                        prepared,
                        client,
                        cancellation,
                        revision,
                        pending_insertions,
                    )
                    .await
                    {
                        Ok((next, inserted)) => {
                            revision = next;
                            inserted
                        }
                        Err(outcome) => return (outcome, usage),
                    };
                    if inserted {
                        continue 'model;
                    }
                }
                let (barrier, ready) = CommitBarrier::before_continue();
                if emit(
                    client,
                    ClientEvent::StateCommitted(StateCommitted {
                        revision_id: revision,
                        tool_round_version: 0,
                        cause: CommitCause::FinalTurn,
                        barrier,
                    }),
                )
                .await
                .is_err()
                {
                    return (client_failure(), usage);
                }
                if let Err(outcome) = wait_for_state_ready(ready, cancellation).await {
                    return (outcome, usage);
                }
                return (RunOutcome::Completed, usage);
            }

            let round_id =
                ToolRoundId::new(format!("{}:round:{provider_call_index}", prepared.run_id));
            revision = match super::tool_round::execute(
                &self.store,
                prepared,
                client,
                cancellation,
                revision,
                super::tool_round::ToolRound {
                    id: round_id,
                    assistant: ToolRoundAssistant {
                        text: cycle.text,
                        thinking: cycle.reasoning,
                        model_call_id: cycle.model_call_id,
                        replay_state: cycle.replay_state,
                    },
                    calls: cycle.calls,
                    recovered_started_at_ms: None,
                },
                pending_insertions,
            )
            .await
            {
                Ok(revision) => revision,
                Err(outcome) => return (outcome, usage),
            };
        }
    }

    async fn auto_compact(
        &self,
        prepared: &PreparedRun,
        revision: crate::model::RevisionId,
        messages: &[CanonicalMessage],
        client: &mut ClientPort,
        cancellation: &CancellationToken,
    ) -> std::result::Result<(crate::model::RevisionId, Option<Usage>), RunOutcome> {
        let current_ids = prepared
            .initial_messages
            .iter()
            .map(|message| message.message_id.as_str())
            .collect::<HashSet<_>>();
        let (compactable, retained_request_context) =
            auto_compaction_partition(messages, &current_ids);
        if compactable.is_empty() {
            return Ok((revision, None));
        }

        emit(client, ClientEvent::AutoCompactionStarted)
            .await
            .map_err(|_| client_failure())?;
        let provider_call_index = self
            .store
            .begin_provider_call(&prepared.run_id)
            .await
            .map_err(|error| RunOutcome::Failed(error.into()))?;
        let history = crate::model::project_messages(&compactable)
            .map_err(|error| RunOutcome::Failed(error.into()))?;
        let mut model = prepared.model.clone();
        model.max_output_tokens = Some(COMPACTION_OUTPUT_TOKENS);
        model.reasoning.enabled = false;
        model.reasoning.effort = None;
        let invocation = crate::model::ModelInvocation {
            call_id: format!("{}:{provider_call_index}", prepared.run_id),
            run_id: prepared.run_id.to_string(),
            conversation_id: prepared.conversation_id.to_string(),
            provider_call_index,
            request: crate::model::ModelRequest {
                prompt: crate::model::PromptSpec {
                    instructions: COMPACTION_INSTRUCTIONS.into(),
                    tools: Vec::new(),
                },
                model,
                history,
            },
        };
        let cycle_cancellation = cancellation.child_token();
        let (silent_events, mut discarded_events) = tokio::sync::mpsc::channel(256);
        let drain = tokio::spawn(async move { while discarded_events.recv().await.is_some() {} });
        let mut pending_insertions = Vec::new();
        let mut interrupted_message = None;
        let mut compaction_timed_out = false;
        let cycle = {
            let cycle = consume_model_cycle(
                self.provider.stream(invocation, cycle_cancellation.clone()),
                &silent_events,
                &cycle_cancellation,
            );
            tokio::pin!(cycle);
            let timeout = tokio::time::sleep(AUTO_COMPACTION_TIMEOUT);
            tokio::pin!(timeout);
            loop {
                tokio::select! {
                    biased;
                    command = client.commands.recv() => match command {
                        Some(ClientCommand::InsertMessages(insertion)) => {
                            pending_insertions.push(insertion);
                        }
                        Some(ClientCommand::InterruptWithMessage(message)) => {
                            cycle_cancellation.cancel();
                            interrupted_message = Some(message);
                            break Some(cycle.await);
                        }
                        Some(ClientCommand::RuntimeEvent(event)) => {
                            cycle_cancellation.cancel();
                            interrupted_message = Some(event.into_message());
                            break Some(cycle.await);
                        }
                        Some(ClientCommand::Cancel) => {
                            cycle_cancellation.cancel();
                            return Err(RunOutcome::Cancelled);
                        }
                        Some(ClientCommand::ClientClosed { error }) => {
                            cycle_cancellation.cancel();
                            return Err(RunOutcome::Failed(RunFailure::Client(error)));
                        }
                        Some(ClientCommand::ToolResult(_)) => {
                            cycle_cancellation.cancel();
                            return Err(RunOutcome::Failed(RunFailure::Protocol(
                                "received a tool result while automatic compaction was running".into(),
                            )));
                        }
                        None => {
                            cycle_cancellation.cancel();
                            return Err(client_failure());
                        }
                    },
                    result = &mut cycle => break Some(result),
                    _ = &mut timeout => {
                        compaction_timed_out = true;
                        cycle_cancellation.cancel();
                        break None;
                    },
                }
            }
        };
        drop(silent_events);
        let _ = drain.await;
        let (summary, compaction_usage) = match (
            interrupted_message.is_some(),
            compaction_timed_out,
            cycle,
        ) {
            (false, true, timed_out_cycle) => {
                tracing::warn!(
                    timeout_seconds = AUTO_COMPACTION_TIMEOUT.as_secs(),
                    "automatic compaction timed out; using fallback"
                );
                (
                    fallback_summary(&compactable),
                    match timed_out_cycle {
                        Some(Ok(cycle)) => cycle.usage,
                        Some(Err(failure)) => failure.usage,
                        None => None,
                    },
                )
            }
            (true, _, Some(Ok(cycle))) => (fallback_summary(&compactable), cycle.usage),
            (true, _, Some(Err(failure))) => (fallback_summary(&compactable), failure.usage),
            (false, false, Some(Ok(cycle)))
                if cycle.calls.is_empty() && !cycle.text.trim().is_empty() =>
            {
                (cycle.text.trim().to_string(), cycle.usage)
            }
            (false, false, Some(Ok(cycle))) => {
                tracing::warn!("automatic compaction returned no usable summary; using fallback");
                (fallback_summary(&compactable), cycle.usage)
            }
            (false, false, Some(Err(failure))) => {
                tracing::warn!(error = ?failure.failure, "automatic compaction model failed; using fallback");
                (fallback_summary(&compactable), failure.usage)
            }
            (_, _, None) => {
                tracing::warn!("automatic compaction ended without a model result; using fallback");
                (fallback_summary(&compactable), None)
            }
        };
        let event_id = format!("summary:auto:{}", prepared.run_id);
        let summary_message = CanonicalMessage {
            message_id: format!("runtime:{event_id}"),
            role: Role::User,
            origin: Origin::Runtime,
            content: MessageContent::Parts {
                parts: vec![crate::model::ContentPart::Text {
                    text: format!("<conversation_summary>\n{summary}\n</conversation_summary>"),
                }],
            },
            runtime_event_id: Some(event_id),
        };
        let mut replacement = retained_request_context.into_iter().collect::<Vec<_>>();
        replacement.push(summary_message);
        replacement.extend(prepared.initial_messages.iter().cloned());
        let mut revision = self
            .store
            .replace_revision(
                &prepared.conversation_id,
                &prepared.run_id,
                revision,
                &replacement,
            )
            .await
            .map_err(|error| RunOutcome::Failed(error.into()))?;
        let (barrier, ready) = CommitBarrier::before_continue();
        emit(
            client,
            ClientEvent::StateCommitted(StateCommitted {
                revision_id: revision,
                tool_round_version: 0,
                cause: CommitCause::Compaction { summary },
                barrier,
            }),
        )
        .await
        .map_err(|_| client_failure())?;
        wait_for_state_ready(ready, cancellation).await?;
        emit(client, ClientEvent::AutoCompactionCompleted)
            .await
            .map_err(|_| client_failure())?;
        revision = append_insertions(
            &self.store,
            prepared,
            client,
            cancellation,
            revision,
            pending_insertions,
        )
        .await?
        .0;
        if let Some(message) = interrupted_message {
            revision = append_runtime_message(
                &self.store,
                prepared,
                client,
                cancellation,
                revision,
                message,
            )
            .await?
            .0;
        }
        Ok((revision, compaction_usage))
    }
}

fn auto_compaction_partition(
    messages: &[CanonicalMessage],
    current_ids: &HashSet<&str>,
) -> (Vec<CanonicalMessage>, Option<CanonicalMessage>) {
    let latest_request_context = messages
        .iter()
        .rposition(|message| message.message_id.starts_with("request-context:"));
    let compactable = messages
        .iter()
        .enumerate()
        .filter(|(index, message)| {
            Some(*index) != latest_request_context
                && !current_ids.contains(message.message_id.as_str())
        })
        .map(|(_, message)| message.clone())
        .collect();
    let retained = latest_request_context
        .and_then(|index| messages.get(index))
        .filter(|message| !current_ids.contains(message.message_id.as_str()))
        .cloned();
    (compactable, retained)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct ContextUsageAnchor {
    input_tokens: u64,
    message_count: usize,
    tool_count: usize,
}

impl ContextUsageAnchor {
    fn from_llm_call(anchor: crate::model::LlmCallUsageAnchor) -> Option<Self> {
        Some(Self {
            input_tokens: anchor.usage.context_input_tokens(anchor.request_type)?,
            message_count: anchor.message_count,
            tool_count: anchor.tool_count,
        })
    }
}

fn auto_compaction_allowed(
    action: &RunAction,
    revision: crate::model::RevisionId,
    last_auto_compaction_revision: Option<crate::model::RevisionId>,
) -> bool {
    !matches!(action, RunAction::Compact) && last_auto_compaction_revision != Some(revision)
}

fn compaction_input_limit(prepared: &PreparedRun) -> Option<u64> {
    let context_window = prepared.model.context_window_tokens?;
    let reserve = prepared
        .model
        .max_output_tokens
        .unwrap_or_default()
        .saturating_add(COMPACTION_OUTPUT_SAFETY_TOKENS)
        .max(COMPACTION_MIN_RESERVE_TOKENS);
    Some(context_window.saturating_sub(reserve))
}

fn should_auto_compact(
    prepared: &PreparedRun,
    messages: &[CanonicalMessage],
    projected_messages: &[crate::model::ProjectedMessage],
    anchor: Option<ContextUsageAnchor>,
) -> bool {
    if prepared.action == RunAction::Compact || messages.len() <= prepared.initial_messages.len() {
        return false;
    }
    let Some(input_limit) = compaction_input_limit(prepared) else {
        return false;
    };
    let estimated_input = anchor
        .filter(|anchor| {
            anchor.message_count <= projected_messages.len()
                && anchor.tool_count == prepared.prompt.tools.len()
        })
        .map(|anchor| {
            anchor
                .input_tokens
                .saturating_add(crate::model::estimate_message_tokens(
                    &projected_messages[anchor.message_count..],
                ))
        })
        .unwrap_or_else(|| crate::model::estimate_context_tokens(&prepared.prompt, messages));
    estimated_input >= input_limit
}

fn fallback_summary(messages: &[CanonicalMessage]) -> String {
    let serialized = serde_json::to_string(messages).unwrap_or_default();
    let start = serialized
        .char_indices()
        .rev()
        .nth(COMPACTION_FALLBACK_CHARS.saturating_sub(1))
        .map_or(0, |(index, _)| index);
    format!(
        "Durable recent conversation state:\n{}",
        &serialized[start..]
    )
}

pub(super) async fn append_insertions(
    store: &Store,
    prepared: &PreparedRun,
    client: &mut ClientPort,
    cancellation: &CancellationToken,
    mut revision: crate::model::RevisionId,
    insertions: Vec<MessageInsertion>,
) -> std::result::Result<(crate::model::RevisionId, bool), RunOutcome> {
    let mut inserted_any = false;
    for insertion in insertions {
        for message in insertion.messages {
            let (next, inserted) =
                append_runtime_message(store, prepared, client, cancellation, revision, message)
                    .await?;
            revision = next;
            inserted_any |= inserted;
        }
        let _ = insertion.delivered.send(());
    }
    Ok((revision, inserted_any))
}

pub(super) async fn append_runtime_message(
    store: &Store,
    prepared: &PreparedRun,
    client: &mut ClientPort,
    cancellation: &CancellationToken,
    revision: crate::model::RevisionId,
    message: CanonicalMessage,
) -> std::result::Result<(crate::model::RevisionId, bool), RunOutcome> {
    let event_id = message.runtime_event_id.clone().ok_or_else(|| {
        RunOutcome::Failed(RunFailure::Protocol(
            "runtime message has no event identity".into(),
        ))
    })?;
    let (revision, inserted) = store
        .append_message_once(
            &prepared.conversation_id,
            &prepared.run_id,
            revision,
            &message,
        )
        .await
        .map_err(|error| RunOutcome::Failed(error.into()))?;
    if !inserted {
        return Ok((revision, false));
    }
    let (barrier, ready) = CommitBarrier::before_continue();
    emit(
        client,
        ClientEvent::StateCommitted(StateCommitted {
            revision_id: revision,
            tool_round_version: 0,
            cause: CommitCause::RuntimeEvent { event_id },
            barrier,
        }),
    )
    .await
    .map_err(|_| client_failure())?;
    wait_for_state_ready(ready, cancellation).await?;
    Ok((revision, true))
}

async fn hydrate_tool_images(
    store: &Store,
    messages: &mut [crate::model::ProjectedMessage],
) -> crate::Result<()> {
    use crate::{
        model::{ContentPart, ProjectedContent},
        store::BlobId,
        Error,
    };

    for message in messages {
        let ProjectedContent::ToolResult(result) = &mut message.content else {
            continue;
        };
        let Some(image) = &result.image else {
            continue;
        };
        let id = BlobId::from_base64(&image.blob_id)?;
        let data = store.get_blob(&id).await?.ok_or_else(|| {
            Error::Protocol(format!("Read image Blob is missing: {}", image.blob_id))
        })?;
        result.provider_parts = vec![
            ContentPart::Text {
                text: result.content.clone(),
            },
            ContentPart::Image {
                mime_type: image.mime_type.clone(),
                data,
            },
        ];
    }
    Ok(())
}

fn accumulate_usage(total: &mut Option<Usage>, usage: Usage) {
    match total {
        Some(total) => *total += usage,
        None => *total = Some(usage),
    }
}

pub(super) async fn wait_for_state_ready(
    ready: tokio::sync::oneshot::Receiver<std::result::Result<(), String>>,
    cancellation: &CancellationToken,
) -> std::result::Result<(), RunOutcome> {
    let result = tokio::select! {
        biased;
        result = ready => result,
        _ = cancellation.cancelled() => return Err(RunOutcome::Cancelled),
    };
    match result {
        Ok(Ok(())) => Ok(()),
        Ok(Err(error)) => Err(RunOutcome::Failed(RunFailure::Client(error))),
        Err(_) => Err(client_failure()),
    }
}

async fn emit(client: &ClientPort, event: ClientEvent) -> Result<(), ()> {
    client.events.send(event).await.map_err(|_| ())
}

fn client_failure() -> RunOutcome {
    RunOutcome::Failed(RunFailure::Client("client event channel closed".into()))
}

fn failure_message(failure: &RunFailure) -> String {
    match failure {
        RunFailure::Protocol(message)
        | RunFailure::Provider(message)
        | RunFailure::Store(message)
        | RunFailure::Client(message) => message.clone(),
    }
}

#[cfg(test)]
mod tests {
    use super::{
        auto_compaction_allowed, auto_compaction_partition, compaction_input_limit,
        hydrate_tool_images, should_auto_compact, ContextUsageAnchor,
    };
    use crate::{
        model::{
            estimate_context_tokens, CanonicalMessage, ContentPart, ConversationId, MessageContent,
            ModelSpec, Origin, PreparedRun, ProjectedContent, ProjectedMessage, PromptSpec,
            RevisionId, Role, RunAction, RunId, RunKind, ToolCallContent, ToolImageReference,
            ToolResultContent, ToolRoundId,
        },
        store::Store,
    };
    use std::collections::HashSet;

    #[test]
    fn context_estimate_grows_with_prompt_history() {
        let prompt = PromptSpec {
            instructions: "system".into(),
            tools: Vec::new(),
        };
        let short = vec![CanonicalMessage::text(
            "short",
            Role::User,
            Origin::User,
            "hello",
        )];
        let long = vec![CanonicalMessage::text(
            "long",
            Role::User,
            Origin::User,
            "x".repeat(100_000),
        )];

        assert!(estimate_context_tokens(&prompt, &long) > 25_000);
        assert!(estimate_context_tokens(&prompt, &long) > estimate_context_tokens(&prompt, &short));
    }

    #[test]
    fn real_previous_input_only_estimates_messages_added_after_the_anchor() {
        let old_history =
            CanonicalMessage::text("old-history", Role::User, Origin::User, "x".repeat(698_641));
        let current_runtime = CanonicalMessage::text(
            "runtime:current",
            Role::User,
            Origin::Runtime,
            "current request",
        );
        let messages = vec![old_history, current_runtime.clone()];
        let prepared = PreparedRun {
            run_id: RunId::new("run"),
            cursor_request_id: None,
            conversation_id: ConversationId::new("conversation"),
            kind: RunKind::Root,
            model: ModelSpec {
                context_window_tokens: Some(200_000),
                ..ModelSpec::new("model")
            },
            prompt: PromptSpec {
                instructions: "system".into(),
                tools: Vec::new(),
            },
            initial_messages: vec![current_runtime],
            action: RunAction::Start,
            base_revision_id: RevisionId(1),
        };
        let anchor = ContextUsageAnchor {
            input_tokens: 140_649,
            message_count: 1,
            tool_count: 0,
        };

        assert_eq!(
            estimate_context_tokens(&prepared.prompt, &messages),
            190_813
        );
        let projected = crate::model::project_messages(&messages).unwrap();
        assert!(!should_auto_compact(
            &prepared,
            &messages,
            &projected,
            Some(anchor)
        ));
    }

    #[test]
    fn real_previous_input_compacts_after_the_new_message_crosses_the_reserve() {
        let old_history =
            CanonicalMessage::text("old-history", Role::User, Origin::User, "old history");
        let current_runtime = CanonicalMessage::text(
            "runtime:current",
            Role::User,
            Origin::Runtime,
            "x".repeat(190_000),
        );
        let messages = vec![old_history, current_runtime.clone()];
        let prepared = PreparedRun {
            run_id: RunId::new("run"),
            cursor_request_id: None,
            conversation_id: ConversationId::new("conversation"),
            kind: RunKind::Root,
            model: ModelSpec {
                context_window_tokens: Some(200_000),
                ..ModelSpec::new("model")
            },
            prompt: PromptSpec {
                instructions: "system".into(),
                tools: Vec::new(),
            },
            initial_messages: vec![current_runtime],
            action: RunAction::Start,
            base_revision_id: RevisionId(1),
        };
        let anchor = ContextUsageAnchor {
            input_tokens: 140_649,
            message_count: 1,
            tool_count: 0,
        };

        let projected = crate::model::project_messages(&messages).unwrap();
        assert!(should_auto_compact(
            &prepared,
            &messages,
            &projected,
            Some(anchor)
        ));
    }

    #[test]
    fn projected_anchor_does_not_recount_canonical_tool_round_fragments() {
        let assistant = |message_id: &str, call_id: &str, text: String, index| CanonicalMessage {
            message_id: message_id.into(),
            role: Role::Assistant,
            origin: Origin::Assistant,
            content: MessageContent::Assistant {
                text,
                thinking: String::new(),
                tool_round_id: Some(ToolRoundId::new("round")),
                replay_state: None,
                tool_calls: vec![ToolCallContent {
                    index,
                    call_id: call_id.into(),
                    name: "Shell".into(),
                    arguments: serde_json::json!({}),
                }],
            },
            runtime_event_id: None,
        };
        let result = |message_id: &str, call_id: &str| CanonicalMessage {
            message_id: message_id.into(),
            role: Role::Tool,
            origin: Origin::Tool,
            content: MessageContent::ToolResult(ToolResultContent {
                call_id: call_id.into(),
                name: "Shell".into(),
                content: "ok".into(),
                is_error: false,
                image: None,
                provider_parts: Vec::new(),
            }),
            runtime_event_id: None,
        };
        let messages = vec![
            assistant("assistant-1", "call-1", "first".into(), 0),
            result("result-1", "call-1"),
            assistant("assistant-2", "call-2", "x".repeat(100_000), 1),
            result("result-2", "call-2"),
        ];
        let projected = crate::model::project_messages(&messages).unwrap();
        assert_eq!(messages.len(), 4);
        assert_eq!(projected.len(), 3);

        let prepared = PreparedRun {
            run_id: RunId::new("run"),
            cursor_request_id: None,
            conversation_id: ConversationId::new("conversation"),
            kind: RunKind::Root,
            model: ModelSpec {
                context_window_tokens: Some(20_000),
                ..ModelSpec::new("model")
            },
            prompt: PromptSpec {
                instructions: "system".into(),
                tools: Vec::new(),
            },
            initial_messages: Vec::new(),
            action: RunAction::Start,
            base_revision_id: RevisionId(1),
        };
        let anchor = ContextUsageAnchor {
            input_tokens: 1_000,
            message_count: 1,
            tool_count: 0,
        };

        assert!(!should_auto_compact(
            &prepared,
            &messages,
            &projected,
            Some(anchor)
        ));
    }

    #[test]
    fn configured_output_budget_is_reserved_for_the_next_model_call() {
        let current = CanonicalMessage::text(
            "runtime:current",
            Role::User,
            Origin::Runtime,
            "current request",
        );
        let mut prepared = PreparedRun {
            run_id: RunId::new("run"),
            cursor_request_id: None,
            conversation_id: ConversationId::new("conversation"),
            kind: RunKind::Root,
            model: ModelSpec {
                context_window_tokens: Some(100_000),
                ..ModelSpec::new("model")
            },
            prompt: PromptSpec {
                instructions: "system".into(),
                tools: Vec::new(),
            },
            initial_messages: vec![current],
            action: RunAction::Start,
            base_revision_id: RevisionId(1),
        };

        assert_eq!(compaction_input_limit(&prepared), Some(90_000));
        prepared.model.max_output_tokens = Some(32_000);
        assert_eq!(compaction_input_limit(&prepared), Some(63_904));
    }

    #[test]
    fn automatic_compaction_is_revision_scoped_and_allows_resume() {
        assert!(auto_compaction_allowed(
            &RunAction::Start,
            RevisionId(2),
            None
        ));
        assert!(!auto_compaction_allowed(
            &RunAction::Start,
            RevisionId(2),
            Some(RevisionId(2)),
        ));
        assert!(auto_compaction_allowed(
            &RunAction::Start,
            RevisionId(3),
            Some(RevisionId(2)),
        ));
        assert!(auto_compaction_allowed(
            &RunAction::Resume {
                pending_tool_round: None,
            },
            RevisionId(3),
            Some(RevisionId(2)),
        ));
        assert!(!auto_compaction_allowed(
            &RunAction::Compact,
            RevisionId(3),
            None,
        ));
    }

    #[test]
    fn automatic_compaction_threshold_is_inclusive() {
        let old = CanonicalMessage::text("old", Role::User, Origin::User, "old history");
        let current = CanonicalMessage::text(
            "runtime:current",
            Role::User,
            Origin::Runtime,
            "current request",
        );
        let messages = vec![old, current.clone()];
        let prepared = PreparedRun {
            run_id: RunId::new("run"),
            cursor_request_id: None,
            conversation_id: ConversationId::new("conversation"),
            kind: RunKind::Root,
            model: ModelSpec {
                context_window_tokens: Some(100_000),
                ..ModelSpec::new("model")
            },
            prompt: PromptSpec {
                instructions: "system".into(),
                tools: Vec::new(),
            },
            initial_messages: vec![current],
            action: RunAction::Start,
            base_revision_id: RevisionId(1),
        };
        let projected = crate::model::project_messages(&messages).unwrap();
        let anchor = ContextUsageAnchor {
            input_tokens: compaction_input_limit(&prepared).unwrap(),
            message_count: projected.len(),
            tool_count: 0,
        };

        assert!(should_auto_compact(
            &prepared,
            &messages,
            &projected,
            Some(anchor),
        ));
    }

    #[test]
    fn auto_compaction_preserves_only_the_latest_request_context() {
        let first_context = CanonicalMessage::text(
            "request-context:first",
            Role::User,
            Origin::Prompt,
            "old rules",
        );
        let old_runtime =
            CanonicalMessage::text("runtime:first", Role::User, Origin::Runtime, "old query");
        let latest_context = CanonicalMessage::text(
            "request-context:second",
            Role::User,
            Origin::Prompt,
            "new rules",
        );
        let current_runtime = CanonicalMessage::text(
            "runtime:current",
            Role::User,
            Origin::Runtime,
            "current query",
        );
        let messages = vec![
            first_context.clone(),
            old_runtime.clone(),
            latest_context.clone(),
            current_runtime,
        ];
        let current_ids = HashSet::from(["runtime:current"]);

        let (compactable, retained) = auto_compaction_partition(&messages, &current_ids);

        assert_eq!(compactable, vec![first_context, old_runtime]);
        assert_eq!(retained, Some(latest_context));
    }

    #[tokio::test]
    async fn read_image_is_loaded_only_for_the_provider_projection() {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        let data = b"\x89PNG\r\n\x1a\nimage";
        let id = store.put_blob(data, &[]).await.unwrap();
        let mut messages = vec![ProjectedMessage {
            message_id: "result".into(),
            role: Role::Tool,
            content: ProjectedContent::ToolResult(ToolResultContent {
                call_id: "call".into(),
                name: "Read".into(),
                content: "Read image file: /tmp/image.png".into(),
                is_error: false,
                image: Some(ToolImageReference {
                    blob_id: id.to_base64(),
                    mime_type: "image/png".into(),
                    path: "/tmp/image.png".into(),
                }),
                provider_parts: Vec::new(),
            }),
        }];

        hydrate_tool_images(&store, &mut messages).await.unwrap();
        let ProjectedContent::ToolResult(result) = &messages[0].content else {
            panic!("not a tool result");
        };
        assert_eq!(
            result.provider_parts,
            vec![
                ContentPart::Text {
                    text: "Read image file: /tmp/image.png".into()
                },
                ContentPart::Image {
                    mime_type: "image/png".into(),
                    data: data.to_vec()
                }
            ]
        );
        let persisted = serde_json::to_value(result).unwrap();
        assert!(persisted.get("provider_parts").is_none());
    }
}
