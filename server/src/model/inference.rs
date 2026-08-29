use serde::{Deserialize, Serialize};

use super::{ModelSpec, ProjectedContent, ProjectedMessage, ToolDefinition};

const PROVIDER_TOOL_CALL_ID_MAX_CHARS: usize = 256;

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct PromptSpec {
    pub instructions: String,
    pub tools: Vec<ToolDefinition>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct ModelRequest {
    pub prompt: PromptSpec,
    pub model: ModelSpec,
    pub history: Vec<ProjectedMessage>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct ModelInvocation {
    pub call_id: String,
    pub run_id: String,
    pub conversation_id: String,
    pub provider_call_index: u64,
    pub request: ModelRequest,
}

pub(crate) fn normalize_provider_tool_call_ids(history: &mut [ProjectedMessage]) {
    for message in history {
        match &mut message.content {
            ProjectedContent::Assistant { calls, .. } => {
                for call in calls {
                    truncate_tool_call_id(&mut call.call_id);
                }
            }
            ProjectedContent::ToolResult(result) => {
                truncate_tool_call_id(&mut result.call_id);
            }
            ProjectedContent::Parts(_) => {}
        }
    }
}

fn truncate_tool_call_id(call_id: &mut String) {
    if let Some((end, _)) = call_id.char_indices().nth(PROVIDER_TOOL_CALL_ID_MAX_CHARS) {
        call_id.truncate(end);
    }
}

#[cfg(test)]
mod tests {
    use super::normalize_provider_tool_call_ids;
    use crate::model::{
        ProjectedContent, ProjectedMessage, Role, ToolCallContent, ToolResultContent,
    };

    #[test]
    fn provider_tool_call_ids_are_truncated_once_for_every_provider() {
        let call_id = format!("cursor-tool-call:{}", "x".repeat(260));
        assert_eq!(call_id.len(), 277);
        let expected = call_id[..256].to_string();
        let mut history = vec![
            ProjectedMessage {
                message_id: "assistant".into(),
                role: Role::Assistant,
                content: ProjectedContent::Assistant {
                    text: String::new(),
                    thinking: String::new(),
                    replay_state: None,
                    calls: vec![ToolCallContent {
                        index: 0,
                        call_id: call_id.clone(),
                        name: "Shell".into(),
                        arguments: serde_json::json!({}),
                    }],
                },
            },
            ProjectedMessage {
                message_id: "result".into(),
                role: Role::Tool,
                content: ProjectedContent::ToolResult(ToolResultContent {
                    call_id,
                    name: "Shell".into(),
                    content: "done".into(),
                    is_error: false,
                    image: None,
                    provider_parts: Vec::new(),
                }),
            },
        ];

        normalize_provider_tool_call_ids(&mut history);

        let ProjectedContent::Assistant { calls, .. } = &history[0].content else {
            panic!("expected assistant message");
        };
        let ProjectedContent::ToolResult(result) = &history[1].content else {
            panic!("expected tool result");
        };
        assert_eq!(calls[0].call_id, expected);
        assert_eq!(result.call_id, expected);
    }

    #[test]
    fn provider_tool_call_id_truncation_counts_unicode_characters() {
        let mut history = vec![ProjectedMessage {
            message_id: "assistant".into(),
            role: Role::Assistant,
            content: ProjectedContent::Assistant {
                text: String::new(),
                thinking: String::new(),
                replay_state: None,
                calls: vec![ToolCallContent {
                    index: 0,
                    call_id: format!("{}界y", "x".repeat(255)),
                    name: "Read".into(),
                    arguments: serde_json::json!({}),
                }],
            },
        }];

        normalize_provider_tool_call_ids(&mut history);

        let ProjectedContent::Assistant { calls, .. } = &history[0].content else {
            panic!("expected assistant message");
        };
        assert_eq!(calls[0].call_id, format!("{}界", "x".repeat(255)));
    }
}
