//! Translates between core model types and the plugin SDK wire contract.
use base64::{engine::general_purpose::STANDARD, Engine};

use crate::{
    model::{
        ContentPart, ModelInvocation, ModelLatency, ProjectedContent, ProjectedMessage,
        ProviderReplayState, Role, Usage,
    },
    provider::{FinishReason, ModelEvent},
    Error, Result,
};

/// 把一次核心模型调用投影成 SDK 的 LlmRequest。
pub fn llm_request(invocation: &ModelInvocation) -> Result<serde_json::Value> {
    let request = &invocation.request;
    let messages = request
        .history
        .iter()
        .map(wire_message)
        .collect::<Result<Vec<_>>>()?;
    Ok(serde_json::json!({
        "instructions": request.prompt.instructions,
        "messages": messages,
        "tools": request.prompt.tools.iter().map(|tool| serde_json::json!({
            "name": tool.name,
            "description": tool.description,
            "parameters": tool.parameters,
        })).collect::<Vec<_>>(),
        "reasoning": {
            "enabled": request.model.reasoning.enabled,
            "effort": request.model.reasoning.effort,
        },
        "latency": match request.model.latency {
            ModelLatency::Fast => "fast",
            _ => "standard",
        },
        "maxOutputTokens": request.model.max_output_tokens,
        "cacheKey": invocation.cache_key(),
    }))
}

fn wire_message(message: &ProjectedMessage) -> Result<serde_json::Value> {
    match &message.content {
        ProjectedContent::Parts(parts) => match message.role {
            Role::System | Role::User => Ok(serde_json::json!({
                "role": if message.role == Role::System { "system" } else { "user" },
                "content": wire_parts(parts),
            })),
            // 纯文本 assistant 历史消息投影成无工具调用的 assistant。
            Role::Assistant => Ok(serde_json::json!({
                "role": "assistant",
                "text": joined_text(parts),
                "thinking": "",
                "replayState": serde_json::Value::Null,
                "toolCalls": [],
            })),
            Role::Tool => Err(Error::Protocol(
                "tool messages must carry a tool result".into(),
            )),
        },
        ProjectedContent::Assistant {
            text,
            thinking,
            replay_state,
            calls,
        } => Ok(serde_json::json!({
            "role": "assistant",
            "text": text,
            "thinking": thinking,
            "replayState": replay_state.as_ref().map(|state| serde_json::json!({
                "providerKind": state.provider_kind,
                "value": state.value,
            })),
            "toolCalls": calls.iter().map(|call| serde_json::json!({
                "index": call.index,
                "callId": call.call_id,
                "name": call.name,
                "arguments": call.arguments,
            })).collect::<Vec<_>>(),
        })),
        ProjectedContent::ToolResult(result) => Ok(serde_json::json!({
            "role": "tool",
            "callId": result.call_id,
            "name": result.name,
            "content": result.content,
            "isError": result.is_error,
            "parts": wire_parts(&result.provider_parts),
        })),
    }
}

fn wire_parts(parts: &[ContentPart]) -> Vec<serde_json::Value> {
    parts
        .iter()
        .map(|part| match part {
            ContentPart::Text { text } => serde_json::json!({ "type": "text", "text": text }),
            ContentPart::Image { mime_type, data } => serde_json::json!({
                "type": "image",
                "mediaType": mime_type,
                "dataBase64": STANDARD.encode(data),
            }),
        })
        .collect()
}

fn joined_text(parts: &[ContentPart]) -> String {
    parts
        .iter()
        .filter_map(|part| match part {
            ContentPart::Text { text } => Some(text.as_str()),
            ContentPart::Image { .. } => None,
        })
        .collect()
}

/// 把插件发出的标准化事件解析为核心 ModelEvent。
pub fn model_event(value: &serde_json::Value) -> Result<ModelEvent> {
    let kind = value
        .get("type")
        .and_then(serde_json::Value::as_str)
        .ok_or_else(|| Error::Protocol("plugin model event requires type".into()))?;
    let event = match kind {
        "text-start" => ModelEvent::TextStart,
        "text-delta" => ModelEvent::TextDelta(required_str(value, "text")?.to_owned()),
        "text-end" => ModelEvent::TextEnd,
        "thinking-start" => ModelEvent::ThinkingStart,
        "thinking-delta" => ModelEvent::ThinkingDelta(required_str(value, "text")?.to_owned()),
        "thinking-end" => ModelEvent::ThinkingEnd,
        "tool-call-start" => ModelEvent::ToolCallStart {
            index: required_index(value)?,
            call_id: required_str(value, "callId")?.to_owned(),
            name: required_str(value, "name")?.to_owned(),
        },
        "tool-call-arguments-delta" => ModelEvent::ToolCallArgumentsDelta {
            index: required_index(value)?,
            delta: required_str(value, "delta")?.to_owned(),
        },
        "tool-call-end" => ModelEvent::ToolCallEnd {
            index: required_index(value)?,
        },
        "replay-state" => ModelEvent::ProviderReplayState(ProviderReplayState {
            provider_kind: required_str(value, "providerKind")?.to_owned(),
            value: value.get("value").cloned().unwrap_or_default(),
        }),
        "usage" => {
            let usage = value
                .get("usage")
                .ok_or_else(|| Error::Protocol("plugin usage event requires usage".into()))?;
            let tokens = |name: &str| usage.get(name).and_then(serde_json::Value::as_u64);
            let input_tokens = tokens("inputTokens");
            ModelEvent::Usage(Usage {
                input_tokens,
                context_input_tokens: input_tokens,
                output_tokens: tokens("outputTokens"),
                total_tokens: tokens("totalTokens"),
                cache_read_tokens: tokens("cacheReadTokens"),
                cache_write_tokens: tokens("cacheWriteTokens"),
                reasoning_tokens: tokens("reasoningTokens"),
            })
        }
        "done" => ModelEvent::Done(match required_str(value, "reason")? {
            "stop" => FinishReason::Stop,
            "length" => FinishReason::Length,
            "tool-use" => FinishReason::ToolUse,
            reason => {
                return Err(Error::Protocol(format!(
                    "unknown plugin finish reason: {reason}"
                )))
            }
        }),
        kind => {
            return Err(Error::Protocol(format!(
                "unknown plugin model event: {kind}"
            )))
        }
    };
    Ok(event)
}

fn required_str<'a>(value: &'a serde_json::Value, key: &str) -> Result<&'a str> {
    value
        .get(key)
        .and_then(serde_json::Value::as_str)
        .ok_or_else(|| Error::Protocol(format!("plugin model event requires string '{key}'")))
}

fn required_index(value: &serde_json::Value) -> Result<usize> {
    value
        .get("index")
        .and_then(serde_json::Value::as_u64)
        .map(|index| index as usize)
        .ok_or_else(|| Error::Protocol("plugin model event requires index".into()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{
        ModelRequest, ModelSpec, ProjectedContent, PromptSpec, ToolCallContent, ToolResultContent,
    };

    #[test]
    fn projects_history_into_wire_messages() {
        let invocation = ModelInvocation {
            call_id: "call".into(),
            run_id: "run".into(),
            conversation_id: "conversation".into(),
            prompt_cache_enabled: true,
            provider_call_index: 0,
            request: ModelRequest {
                prompt: PromptSpec {
                    instructions: "be brief".into(),
                    tools: Vec::new(),
                },
                model: ModelSpec::new("plugin:p/c/m"),
                history: vec![
                    ProjectedMessage {
                        message_id: "m1".into(),
                        role: Role::User,
                        content: ProjectedContent::Parts(vec![ContentPart::Text {
                            text: "hi".into(),
                        }]),
                    },
                    ProjectedMessage {
                        message_id: "m2".into(),
                        role: Role::Assistant,
                        content: ProjectedContent::Assistant {
                            text: "".into(),
                            thinking: "t".into(),
                            replay_state: Some(ProviderReplayState {
                                provider_kind: "openai_responses".into(),
                                value: serde_json::json!({"items": []}),
                            }),
                            calls: vec![ToolCallContent {
                                index: 0,
                                call_id: "c1".into(),
                                name: "read".into(),
                                arguments: serde_json::json!({"path":"a"}),
                            }],
                        },
                    },
                    ProjectedMessage {
                        message_id: "m3".into(),
                        role: Role::Tool,
                        content: ProjectedContent::ToolResult(ToolResultContent {
                            call_id: "c1".into(),
                            name: "read".into(),
                            content: "data".into(),
                            is_error: false,
                            image: None,
                            provider_parts: Vec::new(),
                        }),
                    },
                ],
            },
        };
        let request = llm_request(&invocation).unwrap();
        assert_eq!(request["instructions"], "be brief");
        assert_eq!(request["latency"], "standard");
        assert_eq!(request["cacheKey"], "cursor:conversation");
        let messages = request["messages"].as_array().unwrap();
        assert_eq!(messages[0]["role"], "user");
        assert_eq!(
            messages[1]["replayState"]["providerKind"],
            "openai_responses"
        );
        assert_eq!(messages[1]["toolCalls"][0]["callId"], "c1");
        assert_eq!(messages[2]["role"], "tool");
        assert_eq!(messages[2]["isError"], false);

        let mut model_test = invocation;
        model_test.prompt_cache_enabled = false;
        assert!(llm_request(&model_test).unwrap()["cacheKey"].is_null());
    }

    #[test]
    fn parses_plugin_events_into_model_events() {
        assert_eq!(
            model_event(&serde_json::json!({"type":"text-delta","text":"hi"})).unwrap(),
            ModelEvent::TextDelta("hi".into())
        );
        assert_eq!(
            model_event(&serde_json::json!({"type":"done","reason":"tool-use"})).unwrap(),
            ModelEvent::Done(FinishReason::ToolUse)
        );
        let usage = model_event(&serde_json::json!({
            "type":"usage",
            "usage":{"inputTokens":10,"outputTokens":2,"cacheReadTokens":4}
        }))
        .unwrap();
        assert_eq!(
            usage,
            ModelEvent::Usage(Usage {
                input_tokens: Some(10),
                context_input_tokens: Some(10),
                output_tokens: Some(2),
                total_tokens: None,
                cache_read_tokens: Some(4),
                cache_write_tokens: None,
                reasoning_tokens: None,
            })
        );
        assert!(model_event(&serde_json::json!({"type":"mystery"})).is_err());
    }
}
