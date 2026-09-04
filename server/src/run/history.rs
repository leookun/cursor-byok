//! Guarantees provider history ends with a user message before dispatch.

use crate::model::{ContentPart, ProjectedContent, ProjectedMessage, Role};

/// Appends a transient user message when the history ends with the assistant.
///
/// Providers read an assistant-terminated history as a prefill request, and
/// Anthropic refuses it outright: "This model does not support assistant
/// message prefill. The conversation must end with a user message." The
/// appended message is provider-visible only; it is never persisted, so the
/// committed checkpoint stays an exact prefix of the next turn.
pub(super) fn user_terminated(
    mut history: Vec<ProjectedMessage>,
    message_id: &str,
    text: &str,
) -> Vec<ProjectedMessage> {
    if history
        .last()
        .is_none_or(|message| message.role != Role::Assistant)
    {
        return history;
    }
    history.push(ProjectedMessage {
        message_id: message_id.into(),
        role: Role::User,
        content: ProjectedContent::Parts(vec![ContentPart::Text { text: text.into() }]),
    });
    history
}

#[cfg(test)]
mod tests {
    use super::*;

    fn message(id: &str, role: Role) -> ProjectedMessage {
        ProjectedMessage {
            message_id: id.into(),
            role,
            content: ProjectedContent::Parts(vec![ContentPart::Text { text: id.into() }]),
        }
    }

    #[test]
    fn assistant_terminated_history_gains_a_user_tail() {
        let history = vec![message("u1", Role::User), message("a1", Role::Assistant)];
        let terminated = user_terminated(history.clone(), "tail", "continue");
        assert_eq!(terminated[..2], history[..]);
        assert_eq!(terminated.last().unwrap().role, Role::User);
        assert_eq!(terminated.last().unwrap().message_id, "tail");
    }

    #[test]
    fn user_and_tool_terminated_histories_are_untouched() {
        let user = vec![message("a1", Role::Assistant), message("u2", Role::User)];
        assert_eq!(user_terminated(user.clone(), "tail", "continue"), user);
        let tool = vec![message("a1", Role::Assistant), message("t1", Role::Tool)];
        assert_eq!(user_terminated(tool.clone(), "tail", "continue"), tool);
        assert!(user_terminated(Vec::new(), "tail", "continue").is_empty());
    }
}
