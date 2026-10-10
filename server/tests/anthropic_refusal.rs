//! Verifies refusal handling from Anthropic HTTP/SSE through the execution gate.
#[path = "support/fixtures.rs"]
mod fixtures;

use std::sync::{
    atomic::{AtomicUsize, Ordering},
    Arc,
};
use std::time::Duration;

use axum::{http::header, routing::post, Router};
use base64::{engine::general_purpose::STANDARD_NO_PAD, Engine};
use cursor_server::{
    config::{ProviderConfig, ProviderKind},
    cursor::{
        prompting::{PromptAssets, PromptCompiler},
        protocol::{
            connect,
            proto::{agent::v1 as pb, aiserver::v1 as ai},
        },
        TransportCommand, TransportRegistry,
    },
    model::{ModelConfigInput, ModelInvocation, ModelRequest, ModelSpec, PromptSpec},
    network::NetworkClients,
    plugin::{PluginRegistry, PluginRuntime},
    provider::{AnthropicProvider, FinishReason, Provider, ProviderRouter},
    run::{consume_model_cycle, ModelCycleFailure, ModelCycleResult, RunFailure},
};
use prost::Message;
use serde_json::{json, Value};
use tokio_util::sync::CancellationToken;

struct MockAnthropic {
    provider: Arc<AnthropicProvider>,
    url: String,
    requests: Arc<AtomicUsize>,
    server: tokio::task::JoinHandle<()>,
}

impl Drop for MockAnthropic {
    fn drop(&mut self) {
        self.server.abort();
    }
}

async fn mock(events: Vec<Value>) -> MockAnthropic {
    let sse = events
        .iter()
        .map(|event| {
            format!(
                "event: {}\ndata: {event}\n\n",
                event["type"].as_str().unwrap()
            )
        })
        .collect::<String>();
    let requests = Arc::new(AtomicUsize::new(0));
    let count = requests.clone();
    let app = Router::new().route(
        "/v1/messages",
        post(move || {
            let sse = sse.clone();
            count.fetch_add(1, Ordering::SeqCst);
            async move { ([(header::CONTENT_TYPE, "text/event-stream")], sse) }
        }),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    let provider = AnthropicProvider::new(
        reqwest::Client::builder().no_proxy().build().unwrap(),
        ProviderConfig {
            kind: ProviderKind::Anthropic,
            request_url: format!("http://{address}/v1/messages"),
            api_key: "isolated-test-key".into(),
            custom_headers: Default::default(),
            max_output_tokens: None,
            request_timeout: Duration::from_secs(5),
            allowed_body_fields: None,
        },
    );
    MockAnthropic {
        provider: Arc::new(provider),
        url: format!("http://{address}/v1/messages"),
        requests,
        server,
    }
}

fn response(reasons: &[&str], tool: bool, close_block: bool, terminal: bool) -> Vec<Value> {
    let block = if tool {
        json!({"type":"tool_use","id":"refusal-tool","name":"Read","input":{}})
    } else {
        json!({"type":"text","text":""})
    };
    let delta = if tool {
        json!({"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/pr423-not-read\"}"})
    } else {
        json!({"type":"text_delta","text":"I cannot do that."})
    };
    let mut events = vec![
        json!({"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":0}}}),
        json!({"type":"content_block_start","index":0,"content_block":block}),
        json!({"type":"content_block_delta","index":0,"delta":delta}),
    ];
    if close_block {
        events.push(json!({"type":"content_block_stop","index":0}));
    }
    for reason in reasons {
        events.push(json!({"type":"message_delta","delta":{"stop_reason":reason},"usage":{"output_tokens":2}}));
    }
    if terminal {
        events.push(json!({"type":"message_stop"}));
    }
    events
}

async fn cycle(events: Vec<Value>) -> Result<ModelCycleResult, Box<ModelCycleFailure>> {
    let fixture = mock(events).await;
    let cancellation = CancellationToken::new();
    let invocation = ModelInvocation {
        call_id: "pr423-call".into(),
        run_id: "pr423-run".into(),
        conversation_id: "pr423-conversation".into(),
        provider_call_index: 0,
        request: ModelRequest {
            prompt: PromptSpec {
                instructions: String::new(),
                tools: vec![],
            },
            model: ModelSpec::new("test-model"),
            history: vec![],
        },
    };
    let (events, _receiver) = tokio::sync::mpsc::channel(64);
    tokio::time::timeout(
        Duration::from_secs(5),
        consume_model_cycle(
            fixture.provider.stream(invocation, cancellation.clone()),
            &events,
            &cancellation,
        ),
    )
    .await
    .unwrap()
}

#[tokio::test]
async fn refusal_with_tools_fails_the_execution_gate_for_both_stream_endings() {
    for terminal in [true, false] {
        for close_block in [true, false] {
            let result = cycle(response(&["refusal"], true, close_block, terminal)).await;
            let failure = result.expect_err("refused tools must never pass the execution gate");
            assert!(
                !failure.retryable,
                "an explicit refusal must not be retried"
            );
            assert!(
                matches!(failure.failure, RunFailure::ProviderRefusal(ref message) if message == "Anthropic refused to execute tool calls")
            );
        }
    }
}

#[tokio::test]
async fn a_later_stop_does_not_erase_an_earlier_refusal() {
    let failure = cycle(response(&["refusal", "end_turn"], true, true, true))
        .await
        .expect_err("the refusal must remain sticky");
    assert!(
        !failure.retryable,
        "an explicit refusal must not be retried"
    );
    assert!(matches!(failure.failure, RunFailure::ProviderRefusal(_)));
}

#[tokio::test]
async fn ordinary_stops_still_allow_complete_tool_calls() {
    for reason in ["end_turn", "stop_sequence", "pause_turn", "tool_use"] {
        for terminal in [true, false] {
            let result = cycle(response(&[reason], true, true, terminal))
                .await
                .unwrap();
            assert_eq!(result.finish_reason, FinishReason::ToolUse);
            assert_eq!(result.calls.len(), 1);
            assert_eq!(
                result.calls[0].arguments,
                json!({"path":"/tmp/pr423-not-read"})
            );
        }
    }
}

#[tokio::test]
async fn a_refusal_without_tools_preserves_its_text() {
    for terminal in [true, false] {
        let result = cycle(response(&["refusal"], false, true, terminal))
            .await
            .unwrap();
        assert_eq!(result.finish_reason, FinishReason::Stop);
        assert_eq!(result.text, "I cannot do that.");
        assert!(result.calls.is_empty());
    }
}

#[tokio::test]
async fn refused_tools_preserve_partial_text_and_usage_without_retrying() {
    let mut events = response(&["refusal"], true, true, true);
    events.splice(1..1, [
        json!({"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}),
        json!({"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"I cannot do that."}}),
        json!({"type":"content_block_stop","index":1}),
    ]);
    let failure = cycle(events).await.unwrap_err();
    assert!(!failure.retryable);
    assert_eq!(failure.partial_text, "I cannot do that.");
    let usage = failure.usage.expect("refused attempts still have usage");
    assert_eq!(usage.input_tokens, Some(3));
    assert_eq!(usage.output_tokens, Some(2));
}

#[tokio::test]
async fn token_limits_still_reject_incomplete_tool_calls() {
    for reason in ["max_tokens", "model_context_window_exceeded"] {
        for terminal in [true, false] {
            let failure = cycle(response(&[reason], true, false, terminal))
                .await
                .unwrap_err();
            assert!(
                matches!(failure.failure, RunFailure::Provider(ref message) if message == "model stopped before completing the response")
            );
        }
    }
}

#[tokio::test]
async fn eof_without_a_stop_reason_does_not_authorize_tools() {
    let failure = cycle(response(&[], true, true, false)).await.unwrap_err();
    assert!(matches!(failure.failure, RunFailure::Provider(_)));
}

#[tokio::test]
async fn refused_tools_never_reach_cursor_exec_messages() {
    let fixture = mock(response(&["refusal"], true, true, true)).await;
    let (_directory, store) = fixtures::temp_store().await;
    let config: ModelConfigInput = serde_json::from_value(json!({
        "display_name":"Refusal test", "type":"anthropic", "base_url":fixture.url,
        "use_full_url":true, "api_key":"isolated-test-key", "tooltip_data":"isolated refusal test",
        "model_id":"test-model"
    }))
    .unwrap();
    let model = store.create_model(&config).await.unwrap();
    let plugins = PluginRegistry::managed(
        store.clone(),
        PluginRuntime::managed().unwrap(),
        "1.0.4".into(),
    )
    .unwrap();
    let provider = ProviderRouter::new(
        store.clone(),
        plugins,
        NetworkClients::new(store.clone()),
        Duration::from_secs(5),
        Duration::from_secs(5),
    );
    let assets =
        PromptAssets::load(&std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("prompt/cursor"))
            .unwrap();
    let registry = TransportRegistry::new(
        store.clone(),
        Arc::new(provider),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create("pr423-transport").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(pb::AgentClientMessage {
                message: Some(pb::agent_client_message::Message::RunRequest(
                    pb::AgentRunRequest {
                        conversation_id: Some("pr423-conversation".into()),
                        run_id: Some("pr423-run".into()),
                        requested_model: Some(pb::RequestedModel {
                            model_id: model.model_hash.clone(),
                            ..Default::default()
                        }),
                        action: Some(pb::ConversationAction {
                            action: Some(pb::conversation_action::Action::UserMessageAction(
                                pb::UserMessageAction {
                                    user_message: Some(pb::UserMessage {
                                        text: "test refusal handling".into(),
                                        message_id: "pr423-user".into(),
                                        mode: pb::AgentMode::Agent as i32,
                                        ..Default::default()
                                    }),
                                    ..Default::default()
                                },
                            )),
                            ..Default::default()
                        }),
                        ..Default::default()
                    },
                )),
            }),
        })
        .await
        .unwrap();
    let mut seqno = 1;
    tokio::time::timeout(Duration::from_secs(3), async {
        loop {
            let frame = output.recv().await.expect("terminal error frame");
            for (flags, payload) in connect::decode_frames(&frame).unwrap() {
                if flags & connect::END_STREAM_FLAG != 0 {
                    let end: Value = serde_json::from_slice(&payload).unwrap();
                    assert!(end["error"]["message"]
                        .as_str()
                        .unwrap()
                        .contains("Anthropic refused to execute tool calls"));
                    assert_eq!(fixture.requests.load(Ordering::SeqCst), 1);
                    let detail = &end["error"]["details"][0];
                    assert_eq!(detail["type"], "aiserver.v1.ErrorDetails");
                    let bytes = STANDARD_NO_PAD
                        .decode(detail["value"].as_str().unwrap())
                        .unwrap();
                    let detail = ai::ErrorDetails::decode(bytes.as_slice()).unwrap();
                    let custom = detail.details.unwrap();
                    assert_eq!(custom.is_retryable, Some(false));
                    assert_eq!(custom.should_show_immediate_error, Some(true));
                    return;
                }
                let server = pb::AgentServerMessage::decode(payload).unwrap();
                match server.message {
                    Some(pb::agent_server_message::Message::ExecServerMessage(exec)) => {
                        panic!("refused tool reached Cursor execution: {}", exec.exec_id);
                    }
                    Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                        handle
                            .command(TransportCommand::Append {
                                seqno,
                                message: Box::new(pb::AgentClientMessage {
                                    message: Some(
                                        pb::agent_client_message::Message::KvClientMessage(
                                            pb::KvClientMessage {
                                                id: kv.id,
                                                message: Some(
                                                    pb::kv_client_message::Message::SetBlobResult(
                                                        pb::SetBlobResult { error: None },
                                                    ),
                                                ),
                                            },
                                        ),
                                    ),
                                }),
                            })
                            .await
                            .unwrap();
                        seqno += 1;
                    }
                    _ => {}
                }
            }
        }
    })
    .await
    .expect("refusal must terminate before the first retry");
    eprintln!(
        "refusal transport requests={}, Cursor exec messages=0",
        fixture.requests.load(Ordering::SeqCst)
    );
    let calls = store.llm_calls(10).await.unwrap();
    assert_eq!(calls.len(), 1);
    assert_eq!(calls[0].status, "error");
    assert_eq!(calls[0].error_kind.as_deref(), Some("provider"));
    assert!(calls[0]
        .error_message
        .as_deref()
        .unwrap()
        .contains("Anthropic refused to execute tool calls"));
    assert_eq!(calls[0].input_tokens, Some(3));
    assert_eq!(calls[0].output_tokens, Some(2));
    registry.shutdown().await;
}
