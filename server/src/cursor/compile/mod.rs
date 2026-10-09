//! Compiles Cursor requests and actions into provider-independent Run inputs.

mod action;
mod break_messages;
mod context;
mod images;
mod insert_messages;
mod model;
mod run;

pub use action::*;
pub(crate) use break_messages::{compile_injection, compile_user_message_action, RuntimeAction};
pub use run::*;

pub(crate) use insert_messages::task_completion;
