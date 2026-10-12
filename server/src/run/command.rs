//! Defines Run commands and their explicit delivery results.

use tokio::sync::oneshot;

use crate::model::{CanonicalMessage, ToolResult, ToolRoundId};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CommandResult {
    Applied,
    Duplicate,
    RunClosing,
    RunEnded,
    StaleTarget,
}

#[derive(Debug)]
pub struct MessageBatch {
    pub event_id: String,
    pub messages: Vec<CanonicalMessage>,
    pub result: oneshot::Sender<CommandResult>,
    /// Results for executions that continue after this foreground round is broken.
    pub detached_results: Option<(ToolRoundId, Vec<ToolResult>)>,
}

impl MessageBatch {
    pub fn complete(self, result: CommandResult) {
        let _ = self.result.send(result);
    }
}

#[derive(Debug)]
pub enum RunCommand {
    ToolResult(ToolResult),
    InsertMessages(MessageBatch),
    BreakMessages(MessageBatch),
    Cancel,
}
