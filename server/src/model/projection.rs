use std::collections::HashSet;

use serde::{Deserialize, Serialize};

use crate::{Error, Result};

use super::{
    truncate_edges, CanonicalMessage, ContentPart, MessageContent, ProviderReplayState, Role,
    ToolCallContent, ToolResultContent,
};

const KIB: usize = 1024;
const TOOL_RESULT_CONTENT_LIMIT: usize = 64 * KIB;

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub enum ProjectedContent {
    Parts(Vec<ContentPart>),
    Assistant {
        text: String,
        thinking: String,
        replay_state: Option<ProviderReplayState>,
        calls: Vec<ToolCallContent>,
    },
    ToolResult(ToolResultContent),
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct ProjectedMessage {
    pub message_id: String,
    pub role: Role,
    pub content: ProjectedContent,
}

pub fn project_messages(messages: &[CanonicalMessage]) -> Result<Vec<ProjectedMessage>> {
    let mut projected = Vec::new();
    let mut index = 0;
    while index < messages.len() {
        if let Some((group, next)) = project_tool_round(messages, index)? {
            projected.extend(group);
            index = next;
        } else {
            projected.push(project_message(&messages[index]));
            index += 1;
        }
    }
    Ok(projected)
}

fn project_tool_round(
    messages: &[CanonicalMessage],
    start: usize,
) -> Result<Option<(Vec<ProjectedMessage>, usize)>> {
    let MessageContent::Assistant {
        tool_round_id: Some(group_id),
        tool_calls,
        ..
    } = &messages[start].content
    else {
        return Ok(None);
    };
    if tool_calls.is_empty() {
        return Ok(None);
    }

    let mut cursor = start;
    let mut text = String::new();
    let mut thinking = String::new();
    let mut replay_state = None;
    let mut calls = Vec::new();
    let mut results = Vec::new();
    let mut result_ids = HashSet::new();

    while cursor < messages.len() {
        let MessageContent::Assistant {
            text: part_text,
            thinking: part_thinking,
            tool_round_id: Some(candidate_group),
            replay_state: part_replay,
            tool_calls: part_calls,
        } = &messages[cursor].content
        else {
            break;
        };
        if candidate_group != group_id || part_calls.is_empty() {
            break;
        }
        text.push_str(part_text);
        thinking.push_str(part_thinking);
        if replay_state.is_none() {
            replay_state = part_replay.clone();
        } else if part_replay.is_some() {
            return Err(Error::Protocol(
                "tool round repeats provider replay state".into(),
            ));
        }
        calls.extend(part_calls.iter().cloned());
        cursor += 1;

        while cursor < messages.len() {
            let MessageContent::ToolResult(result) = &messages[cursor].content else {
                break;
            };
            if !calls.iter().any(|call| call.call_id == result.call_id) {
                break;
            }
            if !result_ids.insert(result.call_id.clone()) {
                return Err(Error::Protocol(format!(
                    "duplicate tool result call_id: {}",
                    result.call_id
                )));
            }
            results.push((
                messages[cursor].message_id.clone(),
                project_tool_result(result),
            ));
            cursor += 1;
        }
    }

    calls.sort_by_key(|call| call.index);
    for call in &calls {
        if !result_ids.contains(&call.call_id) {
            return Err(Error::Protocol(format!(
                "assistant tool call has no result call_id: {}",
                call.call_id
            )));
        }
    }

    let mut output = Vec::with_capacity(results.len() + 1);
    output.push(ProjectedMessage {
        message_id: messages[start].message_id.clone(),
        role: Role::Assistant,
        content: ProjectedContent::Assistant {
            text,
            thinking,
            replay_state,
            calls,
        },
    });
    output.extend(
        results
            .into_iter()
            .map(|(message_id, result)| ProjectedMessage {
                message_id,
                role: Role::Tool,
                content: ProjectedContent::ToolResult(result),
            }),
    );
    Ok(Some((output, cursor)))
}

fn project_message(message: &CanonicalMessage) -> ProjectedMessage {
    let content = match &message.content {
        MessageContent::Parts { parts } => ProjectedContent::Parts(parts.clone()),
        MessageContent::Assistant {
            text,
            thinking,
            replay_state,
            tool_calls,
            ..
        } => ProjectedContent::Assistant {
            text: text.clone(),
            thinking: thinking.clone(),
            replay_state: replay_state.clone(),
            calls: tool_calls.clone(),
        },
        MessageContent::ToolResult(result) => {
            ProjectedContent::ToolResult(project_tool_result(result))
        }
    };
    ProjectedMessage {
        message_id: message.message_id.clone(),
        role: message.role.clone(),
        content,
    }
}

fn project_tool_result(result: &ToolResultContent) -> ToolResultContent {
    let mut projected = result.clone();
    let label = format!("{} tool", result.name);
    projected.content = truncate_edges(&label, &projected.content, TOOL_RESULT_CONTENT_LIMIT);
    for part in &mut projected.provider_parts {
        if let ContentPart::Text { text } = part {
            *text = truncate_edges(&label, text, TOOL_RESULT_CONTENT_LIMIT);
        }
    }
    projected
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::Origin;

    #[test]
    fn oversized_tool_results_are_bounded_only_in_provider_projection() {
        let original = format!("HEAD{}TAIL", "x".repeat(1024 * KIB));
        let message = CanonicalMessage {
            message_id: "result".into(),
            role: Role::Tool,
            origin: Origin::Tool,
            content: MessageContent::ToolResult(ToolResultContent {
                call_id: "call".into(),
                name: "Read".into(),
                content: original.clone(),
                is_error: false,
                image: None,
                provider_parts: vec![ContentPart::Text {
                    text: original.clone(),
                }],
            }),
            runtime_event_id: None,
        };

        let projected = project_messages(std::slice::from_ref(&message)).unwrap();
        let ProjectedContent::ToolResult(result) = &projected[0].content else {
            panic!("expected projected tool result");
        };

        assert!(result.content.len() <= TOOL_RESULT_CONTENT_LIMIT);
        assert!(result.content.starts_with("HEAD"));
        assert!(result.content.ends_with("TAIL"));
        assert!(result
            .content
            .contains("Re-run the tool with narrower scope"));
        let [ContentPart::Text { text }] = result.provider_parts.as_slice() else {
            panic!("expected projected text part");
        };
        assert!(text.len() <= TOOL_RESULT_CONTENT_LIMIT);
        assert!(text.starts_with("HEAD"));
        assert!(text.ends_with("TAIL"));

        let MessageContent::ToolResult(stored) = &message.content else {
            panic!("expected canonical tool result");
        };
        assert_eq!(stored.content, original);
        assert_eq!(
            stored.provider_parts[0],
            ContentPart::Text { text: original }
        );
    }
}
