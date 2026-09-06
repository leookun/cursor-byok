//! Executes and consumes one streaming provider call.
use std::{collections::btree_map::Entry, collections::BTreeMap, time::Instant};

use futures_util::StreamExt;
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

use crate::{
    model::{normalize_tool_name, ProviderReplayState, ToolCall, Usage},
    provider::{FinishReason, ModelEvent, ProviderStream},
};

use super::{RunEvent, RunFailure};

#[derive(Clone, Debug, PartialEq)]
pub struct ModelCycleResult {
    pub model_call_id: String,
    pub text: String,
    pub reasoning: String,
    pub replay_state: Option<ProviderReplayState>,
    pub calls: Vec<ToolCall>,
    pub usage: Option<Usage>,
    pub finish_reason: FinishReason,
}

#[derive(Clone, Debug, PartialEq)]
pub struct ModelCycleFailure {
    pub failure: RunFailure,
    pub partial_text: String,
    pub partial_reasoning: String,
    pub usage: Option<Usage>,
    pub retryable: bool,
}

struct OpenTool {
    call: ToolCall,
    ended: bool,
}

pub async fn consume_model_cycle(
    mut stream: ProviderStream,
    client: &mpsc::Sender<RunEvent>,
    cancellation: &CancellationToken,
) -> std::result::Result<ModelCycleResult, Box<ModelCycleFailure>> {
    let mut model_call_id = None;
    let mut text = String::new();
    let mut reasoning = String::new();
    let mut text_open = false;
    let mut thinking_started = None::<Instant>;
    let mut tools = BTreeMap::<usize, OpenTool>::new();
    let mut call_ids = std::collections::HashSet::new();
    let mut replay_state = None;
    let mut usage = None;
    let mut finish = None;

    loop {
        let next = tokio::select! {
            // Give the provider stream first chance to observe the shared token. Its
            // cancellation branch owns the HTTP response body and recorder cleanup.
            // The second branch remains a fallback for providers that ignore tokens.
            biased;
            next = stream.next() => next,
            _ = cancellation.cancelled() => {
                interrupt_cycle(client, text_open, thinking_started.take()).await;
                return Err(failure(
                    RunFailure::Client("run was cancelled".into()),
                    text,
                    reasoning,
                    usage,
                ));
            }
        };
        let Some(next) = next else {
            if cancellation.is_cancelled() {
                interrupt_cycle(client, text_open, thinking_started.take()).await;
                return Err(failure(
                    RunFailure::Client("run was cancelled".into()),
                    text,
                    reasoning,
                    usage,
                ));
            }
            break;
        };
        let event = match next {
            Ok(event) => event,
            Err(error) => {
                return Err(failure(error.into(), text, reasoning, usage));
            }
        };
        if finish.is_some() {
            return Err(failure(
                RunFailure::Protocol("provider emitted an event after Done".into()),
                text,
                reasoning,
                usage,
            ));
        }
        let result = match event {
            ModelEvent::Start { model_call_id: id } => {
                if model_call_id.replace(id).is_some() {
                    Err("provider emitted duplicate Start")
                } else {
                    Ok(())
                }
            }
            ModelEvent::TextStart => {
                if model_call_id.is_none() {
                    Err("provider emitted content before Start")
                } else if text_open {
                    Err("provider emitted duplicate TextStart")
                } else {
                    text_open = true;
                    send(client, RunEvent::TextStart).await
                }
            }
            ModelEvent::TextDelta(delta) => {
                if !text_open {
                    Err("provider emitted TextDelta before TextStart")
                } else {
                    text.push_str(&delta);
                    send(client, RunEvent::TextDelta(delta)).await
                }
            }
            ModelEvent::TextEnd => {
                if !text_open {
                    Err("provider emitted TextEnd before TextStart")
                } else {
                    text_open = false;
                    send(client, RunEvent::TextEnd).await
                }
            }
            ModelEvent::ThinkingStart => {
                if model_call_id.is_none() {
                    Err("provider emitted content before Start")
                } else if thinking_started.replace(Instant::now()).is_some() {
                    Err("provider emitted duplicate ThinkingStart")
                } else {
                    send(client, RunEvent::ThinkingStart).await
                }
            }
            ModelEvent::ThinkingDelta(delta) => {
                if thinking_started.is_none() {
                    Err("provider emitted ThinkingDelta before ThinkingStart")
                } else {
                    reasoning.push_str(&delta);
                    send(client, RunEvent::ThinkingDelta(delta)).await
                }
            }
            ModelEvent::ThinkingEnd => {
                if let Some(started) = thinking_started.take() {
                    send(
                        client,
                        RunEvent::ThinkingEnd {
                            duration: started.elapsed(),
                        },
                    )
                    .await
                } else {
                    Err("provider emitted ThinkingEnd before ThinkingStart")
                }
            }
            ModelEvent::ToolCallStart {
                index,
                call_id,
                name,
            } => {
                let name = normalize_tool_name(&name);
                let Some(model_call_id) = model_call_id.as_ref() else {
                    return Err(failure(
                        RunFailure::Protocol("provider emitted content before Start".into()),
                        text,
                        reasoning,
                        usage,
                    ));
                };
                match tools.entry(index) {
                    Entry::Occupied(_) => Err("provider reused a tool index"),
                    Entry::Vacant(_) if !call_ids.insert(call_id.clone()) => {
                        Err("provider reused a tool call_id")
                    }
                    Entry::Vacant(entry) => {
                        entry.insert(OpenTool {
                            call: ToolCall {
                                index,
                                call_id: call_id.clone(),
                                model_call_id: model_call_id.clone(),
                                name: name.clone(),
                                arguments_text: String::new(),
                                arguments: serde_json::Value::Null,
                                argument_error: None,
                            },
                            ended: false,
                        });
                        send(
                            client,
                            RunEvent::ToolCallStart {
                                index,
                                call_id,
                                name,
                                model_call_id: model_call_id.clone(),
                            },
                        )
                        .await
                    }
                }
            }
            ModelEvent::ToolCallArgumentsDelta { index, delta } => match tools.get_mut(&index) {
                Some(tool) if !tool.ended => {
                    tool.call.arguments_text.push_str(&delta);
                    send(client, RunEvent::ToolCallArgumentsDelta { index, delta }).await
                }
                Some(_) => Err("provider emitted tool arguments after ToolCallEnd"),
                None => Err("provider emitted tool arguments for an unknown index"),
            },
            ModelEvent::ToolCallEnd { index } => match tools.get_mut(&index) {
                Some(tool) if !tool.ended => {
                    let arguments = if tool.call.arguments_text.trim().is_empty() {
                        Ok(serde_json::json!({}))
                    } else {
                        serde_json::from_str(&tool.call.arguments_text)
                    };
                    match arguments {
                        Ok(arguments) if arguments.is_object() => {
                            tool.call.arguments = arguments;
                        }
                        Ok(_) => {
                            tool.call.arguments = serde_json::json!({});
                            tool.call.argument_error = Some(format!(
                                "{} arguments must be a JSON object",
                                tool.call.name
                            ));
                        }
                        Err(error) => {
                            tool.call.arguments = serde_json::json!({});
                            tool.call.argument_error = Some(format!(
                                "{} arguments are not valid JSON: {error}",
                                tool.call.name
                            ));
                        }
                    }
                    tool.ended = true;
                    send(client, RunEvent::ToolCallEnd { index }).await
                }
                Some(_) => Err("provider emitted duplicate ToolCallEnd"),
                None => Err("provider ended an unknown tool index"),
            },
            ModelEvent::ProviderReplayState(state) => {
                if replay_state.replace(state).is_some() {
                    Err("provider emitted duplicate ProviderReplayState")
                } else {
                    Ok(())
                }
            }
            ModelEvent::Usage(value) => {
                if usage.replace(value).is_some() {
                    Err("provider emitted duplicate Usage")
                } else if send(client, RunEvent::Usage(value)).await.is_err() {
                    return Err(failure(
                        RunFailure::Client("client event channel closed".into()),
                        text,
                        reasoning,
                        usage,
                    ));
                } else {
                    Ok(())
                }
            }
            ModelEvent::Done(reason) => {
                if model_call_id.is_none() {
                    Err("provider emitted content before Start")
                } else if text_open
                    || thinking_started.is_some()
                    || tools.values().any(|tool| !tool.ended)
                {
                    Err("provider emitted Done with an open content block")
                } else {
                    finish = Some(reason);
                    Ok(())
                }
            }
        };
        if let Err(message) = result {
            return Err(failure(
                RunFailure::Protocol(message.into()),
                text,
                reasoning,
                usage,
            ));
        }
    }

    let Some(finish_reason) = finish else {
        return Err(failure(
            RunFailure::Provider("provider stream reached EOF before Done".into()),
            text,
            reasoning,
            usage,
        ));
    };
    let calls = tools
        .into_values()
        .map(|tool| tool.call)
        .collect::<Vec<_>>();
    if finish_reason == FinishReason::Length {
        return Err(terminal_failure(
            RunFailure::Provider("model stopped before completing the response".into()),
            text,
            reasoning,
            usage,
        ));
    }
    let has_tool_calls = !calls.is_empty();
    if matches!(finish_reason, FinishReason::ToolUse) != has_tool_calls {
        return Err(failure(
            RunFailure::Protocol("finish reason and tool calls disagree".into()),
            text,
            reasoning,
            usage,
        ));
    }
    let model_call_id = model_call_id.ok_or_else(|| {
        failure(
            RunFailure::Protocol("provider completed without Start".into()),
            text.clone(),
            reasoning.clone(),
            usage,
        )
    })?;
    Ok(ModelCycleResult {
        model_call_id,
        text,
        reasoning,
        replay_state,
        calls,
        usage,
        finish_reason,
    })
}

async fn send(
    client: &mpsc::Sender<RunEvent>,
    event: RunEvent,
) -> std::result::Result<(), &'static str> {
    client
        .send(event)
        .await
        .map_err(|_| "client event channel closed")
}

async fn interrupt_cycle(
    client: &mpsc::Sender<RunEvent>,
    text_open: bool,
    thinking_started: Option<Instant>,
) {
    if text_open {
        let _ = send(client, RunEvent::TextEnd).await;
    }
    if let Some(started) = thinking_started {
        let _ = send(
            client,
            RunEvent::ThinkingEnd {
                duration: started.elapsed(),
            },
        )
        .await;
    }
}

fn failure(
    failure: RunFailure,
    partial_text: String,
    partial_reasoning: String,
    usage: Option<Usage>,
) -> Box<ModelCycleFailure> {
    let retryable = match &failure {
        RunFailure::Protocol(_) => true,
        RunFailure::Provider(message) if is_rejected_request(message) => false,
        RunFailure::Provider(_) => true,
        RunFailure::Store(_) | RunFailure::Client(_) => false,
    };
    Box::new(ModelCycleFailure {
        failure,
        partial_text,
        partial_reasoning,
        usage,
        retryable,
    })
}

/// A rejected request (wrong key, unknown model, malformed or oversized body)
/// fails identically every time, so retrying it only delays the error the user
/// needs to see. Provider failures are formatted as `<label> <status>: <body>`,
/// so only the head before the body is inspected. 408 and 425 are timing
/// failures and stay retryable.
fn is_rejected_request(message: &str) -> bool {
    let head = message.split_once(": ").map_or(message, |(head, _)| head);
    head.split_whitespace().any(|token| {
        token.len() == 3
            && token.starts_with('4')
            && token.bytes().all(|byte| byte.is_ascii_digit())
            && !matches!(token, "408" | "425" | "429")
    })
}

fn terminal_failure(
    failure: RunFailure,
    partial_text: String,
    partial_reasoning: String,
    usage: Option<Usage>,
) -> Box<ModelCycleFailure> {
    Box::new(ModelCycleFailure {
        failure,
        partial_text,
        partial_reasoning,
        usage,
        retryable: false,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        model::Usage,
        provider::{FinishReason, ModelEvent},
    };
    use tokio_stream::wrappers::ReceiverStream;

    #[tokio::test]
    async fn provider_tool_names_are_normalized_when_received() {
        let events = vec![
            Ok(ModelEvent::Start {
                model_call_id: "call".into(),
            }),
            Ok(ModelEvent::ToolCallStart {
                index: 0,
                call_id: "tool-call".into(),
                name: "multi_tool_use.parallel".into(),
            }),
            Ok(ModelEvent::ToolCallEnd { index: 0 }),
            Ok(ModelEvent::Done(FinishReason::ToolUse)),
        ];
        let stream = Box::pin(tokio_stream::iter(events));
        let (event_tx, mut event_rx) = tokio::sync::mpsc::channel(4);

        let result = consume_model_cycle(stream, &event_tx, &CancellationToken::new())
            .await
            .unwrap();

        assert_eq!(result.calls[0].name, "multi_tool_use_parallel");
        assert!(matches!(
            event_rx.recv().await,
            Some(RunEvent::ToolCallStart { name, .. }) if name == "multi_tool_use_parallel"
        ));
    }

    #[tokio::test]
    async fn usage_is_forwarded_before_the_provider_call_finishes() {
        let (provider_tx, provider_rx) = tokio::sync::mpsc::channel(4);
        let stream = Box::pin(ReceiverStream::new(provider_rx));
        let (event_tx, mut event_rx) = tokio::sync::mpsc::channel(4);
        let cancellation = CancellationToken::new();
        let cycle_cancellation = cancellation.clone();
        let cycle = tokio::spawn(async move {
            consume_model_cycle(stream, &event_tx, &cycle_cancellation).await
        });
        let usage = Usage {
            input_tokens: Some(100),
            context_input_tokens: Some(100),
            output_tokens: Some(20),
            total_tokens: Some(120),
            ..Default::default()
        };

        provider_tx
            .send(Ok(ModelEvent::Start {
                model_call_id: "call".into(),
            }))
            .await
            .unwrap();
        provider_tx
            .send(Ok(ModelEvent::Usage(usage)))
            .await
            .unwrap();

        let event = tokio::time::timeout(std::time::Duration::from_secs(1), event_rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(event, RunEvent::Usage(value) if value == usage));
        assert!(!cycle.is_finished(), "usage must arrive before Done");

        provider_tx
            .send(Ok(ModelEvent::Done(FinishReason::Stop)))
            .await
            .unwrap();
        drop(provider_tx);
        let result = cycle.await.unwrap().unwrap();
        assert_eq!(result.usage, Some(usage));
        assert!(event_rx.try_recv().is_err(), "usage must be forwarded once");
    }

    #[test]
    fn other_provider_errors_are_retryable() {
        let result = failure(
            RunFailure::Provider("Anthropic 502 Bad Gateway".into()),
            String::new(),
            String::new(),
            None,
        );
        assert!(result.retryable);
        let timeout = failure(
            RunFailure::Provider("Anthropic 408 Request Timeout".into()),
            String::new(),
            String::new(),
            None,
        );
        assert!(timeout.retryable);
    }

    #[test]
    fn rejected_requests_are_not_retryable() {
        // Verbatim from a wedged conversation: eight retries of the same
        // over-limit prompt only delayed the error by forty seconds.
        let too_long = failure(
            RunFailure::Provider(
                "Anthropic 400 Bad Request: {\"type\":\"error\",\"error\":{\"type\":\
                 \"invalid_request_error\",\"message\":\"prompt is too long: 1002148 \
                 tokens > 1000000 maximum\"}}"
                    .into(),
            ),
            String::new(),
            String::new(),
            None,
        );
        assert!(!too_long.retryable);
        let unauthorized = failure(
            RunFailure::Provider("OpenAI Chat 401 Unauthorized: invalid api key".into()),
            String::new(),
            String::new(),
            None,
        );
        assert!(!unauthorized.retryable);
        // A status-looking number inside the body is not a status code.
        let body_number = failure(
            RunFailure::Provider("Anthropic 502 Bad Gateway: upstream returned 400".into()),
            String::new(),
            String::new(),
            None,
        );
        assert!(body_number.retryable);
    }
}
