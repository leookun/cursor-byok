use std::collections::BTreeMap;

use async_stream::try_stream;
use base64::{engine::general_purpose::STANDARD, Engine};
use eventsource_stream::Eventsource;
use futures_util::StreamExt;
use serde_json::{json, Map, Value};

use crate::{
    config::ProviderConfig,
    model::{
        ContentPart, ModelInvocation, ModelLatency, ProjectedContent, ProjectedMessage, Role,
        ToolCallContent, Usage,
    },
    Error, Result,
};

use super::{
    apply_chatgpt_codex_headers, apply_openai_prompt_cache_key, merge_extra_params,
    recorder::recorded_headers, CallRecorder, FinishReason, ModelEvent, Provider, ProviderStream,
};

#[derive(Default)]
struct ChatToolState {
    call_id: String,
    name: String,
    arguments: String,
    emitted_arguments: usize,
    started: bool,
}

pub struct OpenAiChatProvider {
    client: reqwest::Client,
    config: ProviderConfig,
    recorder: Option<CallRecorder>,
}

impl OpenAiChatProvider {
    pub fn new(client: reqwest::Client, config: ProviderConfig) -> Self {
        Self {
            client,
            config,
            recorder: None,
        }
    }

    pub fn with_recorder(mut self, recorder: Option<CallRecorder>) -> Self {
        self.recorder = recorder;
        self
    }
}

impl Provider for OpenAiChatProvider {
    fn stream(
        &self,
        invocation: ModelInvocation,
        cancellation: tokio_util::sync::CancellationToken,
    ) -> ProviderStream {
        let client = self.client.clone();
        let config = self.config.clone();
        let recorder = self.recorder.clone();
        Box::pin(try_stream! {
            let ModelInvocation { call_id, request, .. } = invocation;
            let messages = openai_chat_messages(&request.prompt.instructions, &request.history)?;
            let mut body = json!({
                "model": request.model.model_id,
                "messages": messages,
                "tools": request.prompt.tools.iter().map(|tool| json!({"type":"function","function":{
                    "name": tool.name, "description": tool.description, "parameters": tool.parameters
                }})).collect::<Vec<_>>(),
                "stream": true,
                "stream_options": {"include_usage": true}
            });
            apply_model(&mut body, &request.model, config.max_output_tokens)?;
            merge_extra_params(&mut body, &request.model.extra_params)?;
            apply_openai_prompt_cache_key(&mut body, &request.model.model_id)?;
            if let Some(recorder) = &recorder {
                recorder.request(recorded_headers(&config, &[("content-type", "application/json")]), &body).await?;
            }
            let request = apply_chatgpt_codex_headers(
                client.post(&config.request_url)
                    .bearer_auth(&config.api_key)
                    .headers(config.custom_headers.clone()),
                &config.request_url,
                &config.api_key,
            )
            .json(&body)
            .send();
            let response = tokio::select! {
                _ = cancellation.cancelled() => return,
                response = request => response,
            };
            let response = response?;
            if let Some(recorder) = &recorder {
                recorder.response_headers(response.status().as_u16()).await?;
            }
            if !response.status().is_success() {
                let status = response.status();
                let bytes = response.bytes().await?;
                if let Some(recorder) = &recorder { recorder.response_chunk(&bytes).await?; }
                let text = String::from_utf8_lossy(&bytes);
                Err(Error::Provider(format!("OpenAI Chat {status}: {text}")))?;
                return;
            }
            yield ModelEvent::Start { model_call_id: call_id };
            let chunk_recorder = recorder.clone();
            let chunks = response.bytes_stream()
                .map(|chunk| chunk.map_err(Error::from))
                .then(move |chunk| {
                    let recorder = chunk_recorder.clone();
                    async move {
                        let chunk = chunk?;
                        if let Some(recorder) = recorder { recorder.response_chunk(&chunk).await?; }
                        Ok::<_, Error>(chunk)
                    }
                });
            let source = chunks.eventsource();
            futures_util::pin_mut!(source);
            let mut text_open = false;
            let mut thinking_open = false;
            let mut reasoning = String::new();
            let mut tools = BTreeMap::<usize, ChatToolState>::new();
            let mut final_usage = None;
            let mut finish = None;
            let mut saw_done_marker = false;
            loop {
                let event = tokio::select! {
                    _ = cancellation.cancelled() => {
                        return;
                    }
                    event = source.next() => event,
                };
                let Some(event) = event else { break };
                let event = event.map_err(|error| Error::Provider(format!("OpenAI Chat SSE: {error}")))?;
                if event.data == "[DONE]" { saw_done_marker = true; break; }
                let value: Value = serde_json::from_str(&event.data)?;
                if let Some(usage) = value.get("usage").filter(|value| !value.is_null()) {
                    final_usage = Some(openai_usage(usage));
                }
                let Some(choice) = value.get("choices").and_then(Value::as_array).and_then(|values| values.first()) else { continue; };
                let delta = choice.get("delta").unwrap_or(&Value::Null);
                if let Some(reasoning_delta) = delta.get("reasoning_content").and_then(Value::as_str).filter(|text| !text.is_empty()) {
                    if !thinking_open { thinking_open = true; yield ModelEvent::ThinkingStart; }
                    reasoning.push_str(reasoning_delta);
                    yield ModelEvent::ThinkingDelta(reasoning_delta.into());
                }
                if let Some(content) = delta.get("content").and_then(Value::as_str).filter(|text| !text.is_empty()) {
                    if thinking_open { thinking_open = false; yield ModelEvent::ThinkingEnd; }
                    if !text_open { text_open = true; yield ModelEvent::TextStart; }
                    yield ModelEvent::TextDelta(content.into());
                }
                if let Some(tool_deltas) = delta.get("tool_calls").and_then(Value::as_array) {
                    for (position, tool) in tool_deltas.iter().enumerate() {
                        let index = tool.get("index").and_then(Value::as_u64).map_or(position, |index| index as usize);
                        let id = tool.get("id").and_then(Value::as_str);
                        let function = tool.get("function").unwrap_or(&Value::Null);
                        let name = function.get("name").and_then(Value::as_str);
                        let arguments = function.get("arguments").and_then(Value::as_str);
                        for event in update_chat_tool(index, id, name, arguments, &mut tools) { yield event; }
                    }
                }
                if let Some(reason) = choice.get("finish_reason").and_then(Value::as_str) {
                    finish = Some(map_finish(reason, !tools.is_empty()));
                }
            }
            if thinking_open { yield ModelEvent::ThinkingEnd; }
            if text_open { yield ModelEvent::TextEnd; }
            for (index, tool) in &mut tools {
                if !tool.started {
                    if tool.name.is_empty() {
                        Err(Error::Provider("OpenAI Chat tool call is missing name".into()))?;
                    }
                    if tool.call_id.is_empty() {
                        tool.call_id = format!("call-{index}");
                    }
                    tool.started = true;
                    yield ModelEvent::ToolCallStart { index: *index, call_id: tool.call_id.clone(), name: tool.name.clone() };
                    if !tool.arguments.is_empty() {
                        tool.emitted_arguments = tool.arguments.len();
                        yield ModelEvent::ToolCallArgumentsDelta { index: *index, delta: tool.arguments.clone() };
                    }
                }
                yield ModelEvent::ToolCallEnd { index: *index };
            }
            if let Some(usage) = final_usage { yield ModelEvent::Usage(usage); }
            if !reasoning.is_empty() {
                yield ModelEvent::ProviderReplayState(crate::model::ProviderReplayState {
                    provider_kind: "openai_chat".into(),
                    value: json!({"reasoning_content": reasoning}),
                });
            }
            let finish = finish.or_else(|| saw_done_marker.then_some(if tools.is_empty() { FinishReason::Stop } else { FinishReason::ToolUse }))
                .ok_or_else(|| Error::Provider("OpenAI Chat stream ended without finish_reason".into()))?;
            yield ModelEvent::Done(finish);
        })
    }
}

fn apply_model(
    body: &mut Value,
    model: &crate::model::ModelSpec,
    route_max_output_tokens: Option<u64>,
) -> Result<()> {
    let object = body
        .as_object_mut()
        .ok_or_else(|| Error::Provider("OpenAI Chat request body is not an object".into()))?;
    if let Some(max) = model.max_output_tokens.or(route_max_output_tokens) {
        object.insert("max_completion_tokens".into(), json!(max));
    }
    if let Some(effort) = &model.reasoning.effort {
        object.insert("reasoning_effort".into(), json!(effort));
    }
    if model.latency == ModelLatency::Fast {
        object.insert("service_tier".into(), json!("fast"));
    }
    Ok(())
}

fn openai_chat_messages(instructions: &str, messages: &[ProjectedMessage]) -> Result<Vec<Value>> {
    let mut output = Vec::with_capacity(messages.len() + usize::from(!instructions.is_empty()));
    if !instructions.is_empty() {
        output.push(json!({"role": "system", "content": instructions}));
    }
    for message in messages {
        let mut value = Map::new();
        value.insert(
            "role".into(),
            Value::String(role_name(&message.role).into()),
        );
        match &message.content {
            ProjectedContent::Parts(parts) => {
                value.insert("content".into(), chat_content(&message.role, parts)?);
            }
            ProjectedContent::Assistant {
                text,
                replay_state,
                calls,
                ..
            } => {
                value.insert("content".into(), Value::String(text.clone()));
                let replay_reasoning = replay_state
                    .as_ref()
                    .filter(|state| state.provider_kind == "openai_chat")
                    .and_then(|state| state.value.get("reasoning_content"))
                    .and_then(Value::as_str);
                if let Some(reasoning) = replay_reasoning {
                    value.insert("reasoning_content".into(), Value::String(reasoning.into()));
                }
                if !calls.is_empty() {
                    value.insert(
                        "tool_calls".into(),
                        Value::Array(
                            calls
                                .iter()
                                .map(openai_tool_call)
                                .collect::<Result<Vec<_>>>()?,
                        ),
                    );
                }
            }
            ProjectedContent::ToolResult(result) => {
                value.insert(
                    "content".into(),
                    if result.provider_parts.is_empty() {
                        Value::String(result.content.clone())
                    } else {
                        chat_content(&Role::User, &result.provider_parts)?
                    },
                );
                value.insert("tool_call_id".into(), Value::String(result.call_id.clone()));
            }
        }
        output.push(Value::Object(value));
    }
    Ok(output)
}

fn chat_content(_role: &Role, parts: &[ContentPart]) -> Result<Value> {
    let mut text = String::new();
    let mut only_text = true;
    for part in parts {
        match part {
            ContentPart::Text { text: part } => text.push_str(part),
            ContentPart::Image { .. } => {
                only_text = false;
                break;
            }
        }
    }
    if only_text {
        return Ok(Value::String(text));
    }
    Ok(Value::Array(
        parts
            .iter()
            .map(|part| match part {
                ContentPart::Text { text } => Ok(json!({"type":"text", "text":text})),
                ContentPart::Image { mime_type, data } => Ok(json!({
                    "type":"image_url",
                    "image_url":{"url":format!(
                        "data:{mime_type};base64,{}",
                        STANDARD.encode(data)
                    )},
                })),
            })
            .collect::<Result<Vec<_>>>()?,
    ))
}

fn openai_tool_call(call: &ToolCallContent) -> Result<Value> {
    Ok(json!({
        "id": call.call_id,
        "type": "function",
        "function": {
            "name": call.name,
            "arguments": serde_json::to_string(&call.arguments)?,
        }
    }))
}

fn role_name(role: &Role) -> &'static str {
    match role {
        Role::System => "system",
        Role::User => "user",
        Role::Assistant => "assistant",
        Role::Tool => "tool",
    }
}

fn map_finish(value: &str, has_tools: bool) -> FinishReason {
    match value {
        "tool_calls" | "function_call" => FinishReason::ToolUse,
        "length" => FinishReason::Length,
        "stop" | "content_filter" => FinishReason::Stop,
        _ if has_tools => FinishReason::ToolUse,
        _ => FinishReason::Stop,
    }
}

fn update_chat_tool(
    index: usize,
    call_id: Option<&str>,
    name: Option<&str>,
    arguments: Option<&str>,
    tools: &mut BTreeMap<usize, ChatToolState>,
) -> Vec<ModelEvent> {
    let tool = tools.entry(index).or_default();
    if let Some(call_id) = call_id {
        merge_chat_fragment(&mut tool.call_id, call_id);
    }
    if let Some(name) = name {
        merge_chat_fragment(&mut tool.name, name);
    }
    if let Some(arguments) = arguments {
        tool.arguments.push_str(arguments);
    }

    let mut events = Vec::new();
    if !tool.started && !tool.call_id.is_empty() && !tool.name.is_empty() {
        tool.started = true;
        events.push(ModelEvent::ToolCallStart {
            index,
            call_id: tool.call_id.clone(),
            name: tool.name.clone(),
        });
    }
    if tool.started && tool.emitted_arguments < tool.arguments.len() {
        let delta = tool.arguments[tool.emitted_arguments..].to_string();
        tool.emitted_arguments = tool.arguments.len();
        events.push(ModelEvent::ToolCallArgumentsDelta { index, delta });
    }
    events
}

fn merge_chat_fragment(target: &mut String, fragment: &str) {
    if target == fragment || target.ends_with(fragment) {
        return;
    }
    if fragment.starts_with(target.as_str()) {
        *target = fragment.into();
    } else {
        target.push_str(fragment);
    }
}

pub(crate) fn openai_usage(value: &Value) -> Usage {
    Usage {
        input_tokens: value.get("prompt_tokens").and_then(Value::as_u64),
        output_tokens: value.get("completion_tokens").and_then(Value::as_u64),
        total_tokens: value.get("total_tokens").and_then(Value::as_u64),
        cache_read_tokens: value
            .pointer("/prompt_tokens_details/cached_tokens")
            .and_then(Value::as_u64),
        cache_write_tokens: None,
        reasoning_tokens: value
            .pointer("/completion_tokens_details/reasoning_tokens")
            .and_then(Value::as_u64),
    }
}

#[cfg(test)]
mod tests {
    use super::openai_chat_messages;
    use crate::{
        model::{ContentPart, ProjectedContent, ProjectedMessage, ToolResultContent},
        model::{ProviderReplayState, Role, ToolCallContent},
    };
    use serde_json::json;

    #[test]
    fn chat_replay_state_is_encoded_as_reasoning_content() {
        let messages = openai_chat_messages(
            "",
            &[ProjectedMessage {
                message_id: "test".into(),
                role: Role::Assistant,
                content: ProjectedContent::Assistant {
                    text: "visible answer".into(),
                    thinking: "private reasoning".into(),
                    replay_state: Some(ProviderReplayState {
                        provider_kind: "openai_chat".into(),
                        value: json!({"reasoning_content": "private reasoning"}),
                    }),
                    calls: vec![ToolCallContent {
                        index: 0,
                        call_id: "call-1".into(),
                        name: "Read".into(),
                        arguments: json!({}),
                    }],
                },
            }],
        )
        .unwrap();

        assert_eq!(messages[0]["content"], "visible answer");
        assert_eq!(messages[0]["reasoning_content"], "private reasoning");
        assert_eq!(messages[0]["tool_calls"][0]["id"], "call-1");
    }

    #[test]
    fn another_provider_replay_does_not_invent_chat_reasoning_content() {
        let messages = openai_chat_messages(
            "",
            &[ProjectedMessage {
                message_id: "test".into(),
                role: Role::Assistant,
                content: ProjectedContent::Assistant {
                    text: "visible answer".into(),
                    thinking: "display-only summary".into(),
                    replay_state: Some(ProviderReplayState {
                        provider_kind: "anthropic".into(),
                        value: json!({"blocks": []}),
                    }),
                    calls: vec![],
                },
            }],
        )
        .unwrap();

        assert!(messages[0].get("reasoning_content").is_none());
    }

    #[test]
    fn read_image_stays_in_its_tool_message() {
        let messages = openai_chat_messages(
            "",
            &[ProjectedMessage {
                message_id: "result".into(),
                role: Role::Tool,
                content: ProjectedContent::ToolResult(ToolResultContent {
                    call_id: "call".into(),
                    name: "Read".into(),
                    content: "Read image file: image.png".into(),
                    is_error: false,
                    image: None,
                    provider_parts: vec![
                        ContentPart::Text {
                            text: "Read image file: image.png".into(),
                        },
                        ContentPart::Image {
                            mime_type: "image/png".into(),
                            data: b"png".to_vec(),
                        },
                    ],
                }),
            }],
        )
        .unwrap();

        assert_eq!(messages[0]["role"], "tool");
        assert_eq!(messages[0]["tool_call_id"], "call");
        assert_eq!(messages[0]["content"][1]["type"], "image_url");
    }
}
