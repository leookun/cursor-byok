//! Executes one logical round of Tool calls and results.
use std::collections::HashSet;

use tokio_util::sync::CancellationToken;

use crate::{
    model::{CheckpointId, PreparedRun, ToolCall, ToolResult, ToolRoundAssistant, ToolRoundId},
    store::Store,
};

use super::{
    CommitBarrier, CommitCause, MessageBatch, MessagesCommitted, RunCommand, RunEvent, RunFailure,
    RunOutcome, RunPort,
};

pub(super) struct ToolRound {
    pub id: ToolRoundId,
    pub assistant: ToolRoundAssistant,
    pub calls: Vec<ToolCall>,
    pub recovered_started_at_ms: Option<u64>,
}

pub(super) async fn execute(
    store: &Store,
    prepared: &PreparedRun,
    client: &mut RunPort,
    cancellation: &CancellationToken,
    mut checkpoint: CheckpointId,
    round: ToolRound,
    insertions: Vec<MessageBatch>,
) -> std::result::Result<CheckpointId, RunOutcome> {
    let ToolRound {
        id: round_id,
        assistant,
        calls,
        recovered_started_at_ms,
    } = round;
    store
        .create_tool_round(
            &round_id,
            &prepared.run_id,
            checkpoint,
            &assistant,
            &calls,
            recovered_started_at_ms,
        )
        .await
        .map_err(failed)?;
    tracing::info!(
        round_id = %round_id,
        checkpoint_id = checkpoint.0,
        calls = calls.len(),
        "tool round started"
    );
    send(
        client,
        RunEvent::MessagesCommitted(MessagesCommitted {
            checkpoint_id: checkpoint,
            tool_round_version: 0,
            cause: CommitCause::ToolRoundStarted(round_id.clone()),
            barrier: CommitBarrier::None,
        }),
    )
    .await?;
    send(
        client,
        RunEvent::ExecuteToolRound {
            round_id: round_id.clone(),
            calls: calls.clone(),
        },
    )
    .await?;

    let mut remaining = calls.len();
    let mut completed_call_ids = HashSet::new();
    let mut pending_insertions = insertions;
    while remaining > 0 {
        let command = tokio::select! {
            _ = cancellation.cancelled() => return Err(RunOutcome::Cancelled),
            command = client.commands.recv() => command,
        };
        match command {
            Some(RunCommand::ToolResult(result)) => {
                let call_id = result.call_id.clone();
                if completed_call_ids.contains(&call_id) {
                    continue;
                }
                let committed = store
                    .commit_tool_result(
                        &prepared.conversation_id,
                        &prepared.run_id,
                        &round_id,
                        &result,
                    )
                    .await
                    .map_err(failed)?;
                checkpoint = committed.checkpoint_id;
                completed_call_ids.insert(call_id.clone());
                tracing::info!(
                    round_id = %round_id,
                    call_id,
                    checkpoint_id = checkpoint.0,
                    tool_round_version = committed.tool_round_version,
                    completion_seq = committed.completion_seq,
                    settled = committed.settled,
                    "tool result committed"
                );
                remaining -= 1;
                let (barrier, ready) = if committed.settled {
                    let (barrier, ready) = CommitBarrier::before_continue();
                    (barrier, Some(ready))
                } else {
                    (CommitBarrier::None, None)
                };
                send(
                    client,
                    RunEvent::MessagesCommitted(MessagesCommitted {
                        checkpoint_id: checkpoint,
                        tool_round_version: committed.tool_round_version,
                        cause: CommitCause::ToolResult {
                            call_id,
                            synthetic: false,
                        },
                        barrier,
                    }),
                )
                .await?;
                if let Some(ready) = ready {
                    super::engine::wait_for_state_ready(ready, cancellation).await?;
                }
            }
            Some(RunCommand::BreakMessages(messages)) => {
                for call in calls
                    .iter()
                    .filter(|call| !completed_call_ids.contains(&call.call_id))
                {
                    let result = messages
                        .detached_results
                        .as_ref()
                        .filter(|(id, _)| id == &round_id)
                        .and_then(|(_, results)| {
                            results.iter().find(|result| result.call_id == call.call_id)
                        })
                        .cloned()
                        .unwrap_or_else(|| ToolResult {
                            call_id: call.call_id.clone(),
                            content: "Tool execution was interrupted by a newer user message."
                                .into(),
                            is_error: true,
                            image: None,
                        });
                    let committed = store
                        .commit_tool_result(
                            &prepared.conversation_id,
                            &prepared.run_id,
                            &round_id,
                            &result,
                        )
                        .await
                        .map_err(failed)?;
                    checkpoint = committed.checkpoint_id;
                    let (barrier, ready) = if committed.settled {
                        let (barrier, ready) = CommitBarrier::before_continue();
                        (barrier, Some(ready))
                    } else {
                        (CommitBarrier::None, None)
                    };
                    send(
                        client,
                        RunEvent::MessagesCommitted(MessagesCommitted {
                            checkpoint_id: checkpoint,
                            tool_round_version: committed.tool_round_version,
                            cause: CommitCause::ToolResult {
                                call_id: call.call_id.clone(),
                                synthetic: true,
                            },
                            barrier,
                        }),
                    )
                    .await?;
                    if let Some(ready) = ready {
                        super::engine::wait_for_state_ready(ready, cancellation).await?;
                    }
                }
                checkpoint = super::messages::append_batches(
                    store,
                    prepared,
                    client,
                    cancellation,
                    checkpoint,
                    pending_insertions,
                )
                .await?
                .0;
                checkpoint = super::messages::append_batches(
                    store,
                    prepared,
                    client,
                    cancellation,
                    checkpoint,
                    vec![messages],
                )
                .await?
                .0;
                return Ok(checkpoint);
            }
            Some(RunCommand::InsertMessages(insertion)) => pending_insertions.push(insertion),
            Some(RunCommand::Cancel) => return Err(RunOutcome::Cancelled),
            None => return Err(client_failure()),
        }
    }
    checkpoint = super::messages::append_batches(
        store,
        prepared,
        client,
        cancellation,
        checkpoint,
        pending_insertions,
    )
    .await?
    .0;
    Ok(checkpoint)
}

async fn send(client: &RunPort, event: RunEvent) -> std::result::Result<(), RunOutcome> {
    client
        .events
        .send(event)
        .await
        .map_err(|_| client_failure())
}

fn failed(error: crate::Error) -> RunOutcome {
    RunOutcome::Failed(error.into())
}

fn client_failure() -> RunOutcome {
    RunOutcome::Failed(RunFailure::Client("client event channel closed".into()))
}
