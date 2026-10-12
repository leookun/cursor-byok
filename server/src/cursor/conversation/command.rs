//! Defines commands accepted by a Conversation runtime.

use crate::{cursor::protocol::proto::agent::v1 as pb, Error};

#[derive(Debug)]
pub enum RunFinish {
    TurnCompleted(Box<pb::ConversationStateStructure>),
    Transport(TransportFinish),
}

#[derive(Debug)]
pub enum TransportFinish {
    Success,
    Failed(Error),
    Cancelled,
}

#[derive(Debug)]
pub enum TransportCommand {
    Append {
        seqno: i64,
        message: Box<pb::AgentClientMessage>,
    },
    RunFinished {
        generation: u64,
        finish: RunFinish,
    },
    /// A detached Task result, owned by the original transport execution scope.
    TaskCompleted {
        owner: u64,
        message: crate::model::CanonicalMessage,
    },
    TaskDelivered {
        owner: u64,
        event_id: String,
        result: crate::run::CommandResult,
    },
    Disconnect,
}
