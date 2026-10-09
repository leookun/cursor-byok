//! Provides phase-aware command submission and cancellation for an active Run.

use std::sync::Arc;

use parking_lot::Mutex;
use tokio::sync::{mpsc, oneshot};
use tokio_util::sync::CancellationToken;

use crate::model::{CanonicalMessage, RunId, ToolResult, ToolRoundId};

use super::{CommandResult, MessageBatch, RunCommand};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RunPhase {
    Running,
    Finalizing,
    Ended,
}

#[derive(Clone)]
pub(crate) struct RunPhaseControl {
    value: Arc<Mutex<RunPhase>>,
}

impl RunPhaseControl {
    pub(crate) fn new() -> Self {
        Self {
            value: Arc::new(Mutex::new(RunPhase::Running)),
        }
    }

    pub(crate) fn get(&self) -> RunPhase {
        *self.value.lock()
    }

    pub(crate) fn begin_finalizing(&self) {
        let mut phase = self.value.lock();
        if *phase == RunPhase::Running {
            *phase = RunPhase::Finalizing;
        }
    }

    pub(crate) fn resume_running(&self) {
        let mut phase = self.value.lock();
        if *phase == RunPhase::Finalizing {
            *phase = RunPhase::Running;
        }
    }

    pub(crate) fn finish(&self) {
        *self.value.lock() = RunPhase::Ended;
    }

    pub(crate) fn with_phase<T>(&self, action: impl FnOnce(RunPhase) -> T) -> T {
        action(*self.value.lock())
    }
}

#[derive(Clone)]
pub struct RunHandle {
    run_id: RunId,
    phase: RunPhaseControl,
    commands: mpsc::UnboundedSender<RunCommand>,
    cancellation: CancellationToken,
}

impl RunHandle {
    pub(crate) fn new(
        run_id: RunId,
        phase: RunPhaseControl,
        commands: mpsc::UnboundedSender<RunCommand>,
        cancellation: CancellationToken,
    ) -> Self {
        Self {
            run_id,
            phase,
            commands,
            cancellation,
        }
    }

    pub fn run_id(&self) -> &RunId {
        &self.run_id
    }

    pub fn phase(&self) -> RunPhase {
        self.phase.get()
    }

    pub async fn insert_messages(
        &self,
        event_id: String,
        messages: Vec<CanonicalMessage>,
    ) -> CommandResult {
        self.submit_messages(event_id, messages, false, None).await
    }

    pub async fn break_messages(
        &self,
        event_id: String,
        messages: Vec<CanonicalMessage>,
    ) -> CommandResult {
        self.submit_messages(event_id, messages, true, None).await
    }

    pub async fn break_messages_with_detached_results(
        &self,
        event_id: String,
        messages: Vec<CanonicalMessage>,
        detached_results: Option<(ToolRoundId, Vec<ToolResult>)>,
    ) -> CommandResult {
        self.submit_messages(event_id, messages, true, detached_results)
            .await
    }

    async fn submit_messages(
        &self,
        event_id: String,
        messages: Vec<CanonicalMessage>,
        should_break: bool,
        detached_results: Option<(ToolRoundId, Vec<ToolResult>)>,
    ) -> CommandResult {
        let (result, delivered) = oneshot::channel();
        let batch = MessageBatch {
            event_id,
            messages,
            result,
            detached_results,
        };
        let command = if should_break {
            RunCommand::BreakMessages(batch)
        } else {
            RunCommand::InsertMessages(batch)
        };
        let sent = self.phase.with_phase(|phase| match phase {
            RunPhase::Running if self.commands.send(command).is_ok() => CommandResult::Applied,
            RunPhase::Running | RunPhase::Ended => CommandResult::RunEnded,
            RunPhase::Finalizing => CommandResult::RunClosing,
        });
        match sent {
            CommandResult::RunClosing => return CommandResult::RunClosing,
            CommandResult::RunEnded => return CommandResult::RunEnded,
            CommandResult::Applied => {}
            CommandResult::Duplicate | CommandResult::StaleTarget => unreachable!(),
        }
        delivered.await.unwrap_or_else(|_| match self.phase() {
            RunPhase::Running => CommandResult::RunEnded,
            RunPhase::Finalizing => CommandResult::RunClosing,
            RunPhase::Ended => CommandResult::RunEnded,
        })
    }

    pub async fn tool_result(&self, result: ToolResult) -> CommandResult {
        self.phase.with_phase(|phase| match phase {
            RunPhase::Running if self.commands.send(RunCommand::ToolResult(result)).is_ok() => {
                CommandResult::Applied
            }
            RunPhase::Running | RunPhase::Ended => CommandResult::RunEnded,
            RunPhase::Finalizing => CommandResult::RunClosing,
        })
    }

    pub fn cancel(&self) {
        self.cancellation.cancel();
        let _ = self.commands.send(RunCommand::Cancel);
    }

    pub fn cancellation(&self) -> CancellationToken {
        self.cancellation.clone()
    }
}
