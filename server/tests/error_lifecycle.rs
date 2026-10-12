//! Verifies unique persisted and streamed terminal outcomes.
#[path = "support/fake_provider.rs"]
mod fake_provider;
#[path = "support/fixtures.rs"]
mod fixtures;

use std::sync::Arc;

use base64::{engine::general_purpose::STANDARD_NO_PAD, Engine};
use cursor_server::{
    cursor::prompting::{PromptAssets, PromptCompiler},
    cursor::protocol::{
        connect,
        proto::{agent::v1 as pb, aiserver::v1 as ai},
    },
    cursor::{TransportCommand, TransportParent, TransportRegistry},
    model::{MessageContent, Role},
    provider::{FinishReason, ModelEvent},
    Error,
};
use prost::Message;

#[tokio::test]
async fn abort_command_cancels_the_run_and_closes_output() {
    let (_directory, store) = fixtures::temp_store().await;
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(fake_provider::FakeProvider::default()),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create("abort-request").await.unwrap();
    let mut output = handle.subscribe();

    handle.command(TransportCommand::Disconnect).await.unwrap();

    let frame = tokio::time::timeout(std::time::Duration::from_secs(1), output.recv())
        .await
        .unwrap()
        .expect("Abort must emit a terminal frame");
    let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
    assert_eq!(flags, connect::END_STREAM_FLAG);
    let payload: serde_json::Value = serde_json::from_slice(&payload).unwrap();
    assert_eq!(payload["error"]["code"], "canceled");
    assert_eq!(output.recv().await, None);
}

#[tokio::test]
async fn provider_failure_retries_from_the_current_checkpoint_without_hiding_partial_output() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push_results(vec![
        Ok(ModelEvent::Start {
            model_call_id: "attempt-0".into(),
        }),
        Ok(ModelEvent::TextStart),
        Ok(ModelEvent::TextDelta("partial ".into())),
        Ok(ModelEvent::ToolCallStart {
            index: 0,
            call_id: "failed-tool".into(),
            name: "Read".into(),
        }),
        Ok(ModelEvent::ToolCallArgumentsDelta {
            index: 0,
            delta: "{\"path\":".into(),
        }),
        Err(Error::Provider("stream disconnected".into())),
    ]);
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "attempt-1".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("completed".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create("retry-request").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(protocol_client_run("retry", "retry-user")),
        })
        .await
        .unwrap();

    let mut seqno = 1;
    let mut text = String::new();
    let mut failed_tool_completed = false;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(60), output.recv())
            .await
            .unwrap()
            .unwrap();
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(
                serde_json::from_slice::<serde_json::Value>(&payload).unwrap(),
                serde_json::json!({})
            );
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                handle
                    .command(TransportCommand::Append {
                        seqno,
                        message: Box::new(kv_ack(kv.id)),
                    })
                    .await
                    .unwrap();
                seqno += 1;
            }
            Some(pb::agent_server_message::Message::InteractionUpdate(update)) => {
                match update.message {
                    Some(pb::interaction_update::Message::TextDelta(delta)) => {
                        text.push_str(&delta.text);
                    }
                    Some(pb::interaction_update::Message::ToolCallCompleted(completed))
                        if completed.call_id == "failed-tool" =>
                    {
                        let tool = completed.tool_call.expect("failed tool completion");
                        failed_tool_completed = tool.completed_at_ms.is_some();
                    }
                    _ => {}
                }
            }
            _ => {}
        }
    }

    assert_eq!(text, "partial completed");
    assert!(
        failed_tool_completed,
        "failed attempt must terminate its partial tool card"
    );
    let requests = provider.requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[0], requests[1]);
    let messages = store
        .load_current_messages(&cursor_server::model::ConversationId::new(
            "protocol-failed-conversation",
        ))
        .await
        .unwrap();
    assert!(messages.iter().any(|message| {
        matches!(&message.content, MessageContent::Assistant { text, .. } if text == "completed")
    }));
    assert!(!messages.iter().any(|message| {
        matches!(&message.content, MessageContent::Assistant { text, .. } if text.contains("partial"))
    }));
}

#[tokio::test]
async fn provider_failure_keeps_the_initial_checkpoint_then_returns_structured_error() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "length-limited".into(),
        },
        ModelEvent::Done(FinishReason::Length),
    ]);
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create("failed-request").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run()),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let mut checkpoints = Vec::new();
    let mut saw_turn_ended = false;
    let error_json = loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            break serde_json::from_slice::<serde_json::Value>(&payload).unwrap();
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                handle
                    .command(TransportCommand::Append {
                        seqno: append_seqno,
                        message: Box::new(kv_ack(kv.id)),
                    })
                    .await
                    .unwrap();
                append_seqno += 1;
            }
            Some(pb::agent_server_message::Message::ConversationCheckpointUpdate(state)) => {
                checkpoints.push(state);
            }
            Some(pb::agent_server_message::Message::InteractionUpdate(update)) => {
                if matches!(
                    update.message,
                    Some(pb::interaction_update::Message::TurnEnded(_))
                ) {
                    saw_turn_ended = true;
                }
                if let Some(pb::interaction_update::Message::TextDelta(delta)) = update.message {
                    assert!(!delta.text.contains("Cursor server error"));
                }
            }
            _ => {}
        }
    };

    assert_eq!(
        checkpoints.len(),
        1,
        "the initial user state is checkpointed"
    );
    assert!(checkpoints[0].pending_tool_calls.is_empty());
    assert!(!saw_turn_ended);
    assert_eq!(error_json["error"]["code"], "unavailable");
    let detail = &error_json["error"]["details"][0];
    assert_eq!(detail["type"], "aiserver.v1.ErrorDetails");
    let encoded = detail["value"].as_str().unwrap();
    assert!(!encoded.ends_with('='));
    let decoded = STANDARD_NO_PAD.decode(encoded).unwrap();
    let decoded = ai::ErrorDetails::decode(decoded.as_slice()).unwrap();
    assert_eq!(
        decoded.error,
        ai::error_details::Error::ProviderError as i32
    );
    assert_eq!(decoded.is_expected, Some(true));
    let custom = decoded.details.unwrap();
    assert_eq!(custom.title, "Provider Error");
    assert_eq!(custom.is_retryable, Some(true));
    assert_eq!(custom.should_show_immediate_error, Some(false));
    assert_eq!(
        tokio::time::timeout(std::time::Duration::from_secs(1), output.recv())
            .await
            .unwrap(),
        None
    );

    assert_eq!(provider.requests().len(), 1, "Length must not retry");
    let messages = store
        .load_current_messages(&cursor_server::model::ConversationId::new(
            "failed-conversation",
        ))
        .await
        .unwrap();
    assert!(messages.iter().any(|message| message.role == Role::User));
    assert!(!messages.iter().any(|message| {
        matches!(
            &message.content,
            MessageContent::Assistant { text, .. } if text.contains("Cursor server error")
        )
    }));
}

#[tokio::test]
async fn unknown_tool_response_id_is_ignored_and_the_run_continues() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "model-call".into(),
        },
        ModelEvent::ToolCallStart {
            index: 0,
            call_id: "call-1".into(),
            name: "Read".into(),
        },
        ModelEvent::ToolCallArgumentsDelta {
            index: 0,
            delta: "{\"path\":\"/tmp/a\"}".into(),
        },
        ModelEvent::ToolCallEnd { index: 0 },
        ModelEvent::Done(FinishReason::ToolUse),
    ]);
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "model-call-2".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("done".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store.clone(),
        Arc::new(provider),
        PromptCompiler::new(assets),
    );
    let handle = registry
        .get_or_create("protocol-failed-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(protocol_client_run("read it", "protocol-failed-user")),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let mut saw_turn_ended = false;
    let end_stream = loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before Error EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            break serde_json::from_slice::<serde_json::Value>(&payload).unwrap();
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                handle
                    .command(TransportCommand::Append {
                        seqno: append_seqno,
                        message: Box::new(kv_ack(kv.id)),
                    })
                    .await
                    .unwrap();
                append_seqno += 1;
            }
            Some(pb::agent_server_message::Message::ExecServerMessage(exec)) => {
                // Unknown bridge ids are ignored; the valid response still completes the tool.
                for message in [
                    pb::ExecClientMessage {
                        id: exec.id + 1_000,
                        exec_id: String::new(),
                        message: None,
                        ..Default::default()
                    },
                    pb::ExecClientMessage {
                        id: exec.id,
                        exec_id: String::new(),
                        message: Some(pb::exec_client_message::Message::ReadResult(
                            pb::ReadResult {
                                result: Some(pb::read_result::Result::Success(pb::ReadSuccess {
                                    path: "/tmp/a".into(),
                                    output: Some(pb::read_success::Output::Content("value".into())),
                                    ..Default::default()
                                })),
                            },
                        )),
                        ..Default::default()
                    },
                ] {
                    handle
                        .command(TransportCommand::Append {
                            seqno: append_seqno,
                            message: Box::new(pb::AgentClientMessage {
                                message: Some(
                                    pb::agent_client_message::Message::ExecClientMessage(message),
                                ),
                            }),
                        })
                        .await
                        .unwrap();
                    append_seqno += 1;
                }
            }
            Some(pb::agent_server_message::Message::InteractionUpdate(update)) => {
                if matches!(
                    update.message,
                    Some(pb::interaction_update::Message::TurnEnded(_))
                ) {
                    saw_turn_ended = true;
                }
                if let Some(pb::interaction_update::Message::TextDelta(delta)) = update.message {
                    assert!(!delta.text.contains("unknown tool result"));
                    assert!(!delta.text.contains("protocol error"));
                }
            }
            _ => {}
        }
    };

    assert!(saw_turn_ended);
    assert_eq!(end_stream, serde_json::json!({}));
    assert_eq!(
        tokio::time::timeout(std::time::Duration::from_secs(1), output.recv())
            .await
            .unwrap(),
        None
    );

    let (status, failure_summary): (String, Option<String>) =
        sqlx::query_as("SELECT status, failure_summary FROM runs WHERE cursor_request_id = ?")
            .bind("protocol-failed-request")
            .fetch_one(store.pool())
            .await
            .unwrap();
    assert_eq!(status, "completed");
    assert_eq!(failure_summary, None);
}

#[tokio::test]
async fn newer_run_request_on_one_bidi_stream_replaces_the_active_run() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "model-call".into(),
        },
        ModelEvent::ToolCallStart {
            index: 0,
            call_id: "call-1".into(),
            name: "Read".into(),
        },
        ModelEvent::ToolCallArgumentsDelta {
            index: 0,
            delta: "{\"path\":\"/tmp/a\"}".into(),
        },
        ModelEvent::ToolCallEnd { index: 0 },
        ModelEvent::Done(FinishReason::ToolUse),
    ]);
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "replacement-model-call".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("replacement completed".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry
        .get_or_create("protocol-failed-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(protocol_client_run("read it", "first-user")),
        })
        .await
        .unwrap();

    let mut seqno = 1;
    let mut replacement_sent = false;
    let mut late_result_sent = false;
    let mut saw_abort = false;
    let mut cropped_state = None;
    let terminal_json = loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before Error EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            break serde_json::from_slice::<serde_json::Value>(&payload).unwrap();
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                handle
                    .command(TransportCommand::Append {
                        seqno,
                        message: Box::new(kv_ack(kv.id)),
                    })
                    .await
                    .unwrap();
                seqno += 1;
            }
            Some(pb::agent_server_message::Message::ConversationCheckpointUpdate(mut state)) => {
                state.root_prompt_messages_json.truncate(1);
                state.turns.clear();
                state.pending_tool_calls.clear();
                cropped_state = Some(state);
            }
            Some(pb::agent_server_message::Message::ExecServerMessage(exec))
                if !replacement_sent =>
            {
                replacement_sent = true;
                let mut replacement =
                    protocol_client_run("use the cropped history", "replacement-user");
                let Some(pb::agent_client_message::Message::RunRequest(request)) =
                    replacement.message.as_mut()
                else {
                    unreachable!()
                };
                request.conversation_state = Some(
                    cropped_state
                        .clone()
                        .expect("first Run must publish a checkpoint before tools"),
                );
                handle
                    .command(TransportCommand::Append {
                        seqno,
                        message: Box::new(replacement),
                    })
                    .await
                    .unwrap();
                seqno += 1;
                handle
                    .command(TransportCommand::Append {
                        seqno,
                        message: Box::new(pb::AgentClientMessage {
                            message: Some(pb::agent_client_message::Message::ExecClientMessage(
                                pb::ExecClientMessage {
                                    id: exec.id,
                                    message: Some(pb::exec_client_message::Message::ReadResult(
                                        pb::ReadResult::default(),
                                    )),
                                    ..Default::default()
                                },
                            )),
                        }),
                    })
                    .await
                    .unwrap();
                seqno += 1;
                late_result_sent = true;
            }
            Some(pb::agent_server_message::Message::ExecServerControlMessage(control)) => {
                if matches!(
                    control.message,
                    Some(pb::exec_server_control_message::Message::Abort(_))
                ) {
                    saw_abort = true;
                }
            }
            _ => {}
        }
    };

    assert!(replacement_sent);
    assert!(late_result_sent);
    assert!(saw_abort);
    assert!(terminal_json.get("error").is_none(), "{terminal_json}");
    assert_eq!(provider.requests().len(), 2);
    let replacement_history = serde_json::to_string(&provider.requests()[1].history).unwrap();
    assert!(replacement_history.contains("use the cropped history"));
    assert!(!replacement_history.contains("read it"));
    let statuses: Vec<String> = sqlx::query_scalar(
        "SELECT status FROM runs WHERE cursor_request_id = ? ORDER BY created_at_ms, run_id",
    )
    .bind("protocol-failed-request")
    .fetch_all(store.pool())
    .await
    .unwrap();
    assert_eq!(statuses, ["cancelled", "completed"]);
}

#[tokio::test]
async fn parent_request_does_not_need_to_resolve_to_an_active_run() {
    assert_run_starts_without_parent_dependency(
        "finished-parent-request",
        Some(TransportParent {
            request_id: Some("already-finished-parent".into()),
            tool_call_id: Some("original-tool-call".into()),
        }),
        None,
    )
    .await;
}

#[tokio::test]
async fn subagent_type_does_not_require_parent_metadata() {
    assert_run_starts_without_parent_dependency(
        "parentless-subagent-request",
        None,
        Some("generalPurpose"),
    )
    .await;
}

#[tokio::test]
async fn subagent_run_without_message_id_starts_and_completes() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "subagent-model-call".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("subagent finished".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let terminal = run_to_terminal(
        &registry,
        "windows-subagent",
        Some(TransportParent {
            request_id: None,
            tool_call_id: Some("parent-task-call".into()),
        }),
        subagent_run("explore the repository", "windows-subagent-conversation"),
    )
    .await;

    assert!(terminal.get("error").is_none(), "{terminal}");
    assert_eq!(provider.requests().len(), 1);
    let row: (String, Option<String>, Option<String>) = sqlx::query_as(
        "SELECT run_kind, parent_run_id, parent_tool_call_id FROM runs WHERE cursor_request_id = ?",
    )
    .bind("windows-subagent")
    .fetch_one(store.pool())
    .await
    .unwrap();
    assert_eq!(row, ("root".into(), None, None));
}

#[tokio::test]
async fn subagent_retries_without_message_id_stay_idempotent() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    let completed = || {
        vec![
            ModelEvent::Start {
                model_call_id: "retry-model-call".into(),
            },
            ModelEvent::TextStart,
            ModelEvent::TextDelta("done".into()),
            ModelEvent::TextEnd,
            ModelEvent::Done(FinishReason::Stop),
        ]
    };
    provider.push(completed());
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let conversation = "synthetic-retry-conversation";

    // First attempt and a client retry with fresh request ids but the same
    // id-less message must map onto one runtime event.
    let first = run_to_terminal(
        &registry,
        "synthetic-retry-a",
        None,
        subagent_run("same subagent task", conversation),
    )
    .await;
    assert!(first.get("error").is_none(), "{first}");
    wait_until_idle(store.pool(), conversation).await;
    provider.push(completed());
    let retry = run_to_terminal(
        &registry,
        "synthetic-retry-b",
        None,
        subagent_run("same subagent task", conversation),
    )
    .await;
    assert!(retry.get("error").is_none(), "{retry}");
    wait_until_idle(store.pool(), conversation).await;

    // A different id-less message in the same conversation keeps its own
    // identity instead of collapsing into the first event.
    provider.push(completed());
    let other = run_to_terminal(
        &registry,
        "synthetic-retry-c",
        None,
        subagent_run("a different subagent task", conversation),
    )
    .await;
    assert!(other.get("error").is_none(), "{other}");

    let runtime_messages: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM messages
         WHERE conversation_id = ? AND message_id LIKE 'runtime:%'",
    )
    .bind(conversation)
    .fetch_one(store.pool())
    .await
    .unwrap();
    assert_eq!(runtime_messages, 2);
}

async fn run_to_terminal(
    registry: &TransportRegistry,
    request_id: &str,
    parent: Option<TransportParent>,
    message: pb::AgentClientMessage,
) -> serde_json::Value {
    let handle = registry.get_or_create(request_id).await.unwrap();
    if let Some(parent) = parent {
        handle.set_parent(parent).unwrap();
    }
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(message),
        })
        .await
        .unwrap();

    let mut seqno = 1;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            break serde_json::from_slice::<serde_json::Value>(&payload).unwrap();
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::KvServerMessage(kv)) = server.message {
            handle
                .command(TransportCommand::Append {
                    seqno,
                    message: Box::new(kv_ack(kv.id)),
                })
                .await
                .unwrap();
            seqno += 1;
        }
    }
}

// Windows Cursor emits subagent kickoff messages without a message_id.
fn subagent_run(text: &str, conversation_id: &str) -> pb::AgentClientMessage {
    let mut message = protocol_client_run(text, "");
    let Some(pb::agent_client_message::Message::RunRequest(request)) = message.message.as_mut()
    else {
        unreachable!()
    };
    request.conversation_id = Some(conversation_id.into());
    request.subagent_type_name = Some("generalPurpose".into());
    message
}

async fn wait_until_idle(pool: &sqlx::SqlitePool, conversation_id: &str) {
    for _ in 0..50 {
        let active: Option<String> =
            sqlx::query_scalar("SELECT active_run_id FROM conversations WHERE conversation_id = ?")
                .bind(conversation_id)
                .fetch_optional(pool)
                .await
                .unwrap()
                .flatten();
        if active.is_none() {
            return;
        }
        tokio::time::sleep(std::time::Duration::from_millis(20)).await;
    }
    panic!("conversation {conversation_id} never became idle");
}

async fn assert_run_starts_without_parent_dependency(
    request_id: &str,
    parent: Option<TransportParent>,
    subagent_type_name: Option<&str>,
) {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "independent-model-call".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("continued independently".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create(request_id).await.unwrap();
    if let Some(parent) = parent {
        handle.set_parent(parent).unwrap();
    }
    let mut output = handle.subscribe();
    let mut message = protocol_client_run("continue", "independent-user");
    let Some(pb::agent_client_message::Message::RunRequest(request)) = message.message.as_mut()
    else {
        unreachable!()
    };
    request.conversation_id = Some(format!("{request_id}-conversation"));
    request.subagent_type_name = subagent_type_name.map(str::to_owned);
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(message),
        })
        .await
        .unwrap();

    let mut seqno = 1;
    let terminal_json = loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            break serde_json::from_slice::<serde_json::Value>(&payload).unwrap();
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::KvServerMessage(kv)) = server.message {
            handle
                .command(TransportCommand::Append {
                    seqno,
                    message: Box::new(kv_ack(kv.id)),
                })
                .await
                .unwrap();
            seqno += 1;
        }
    };

    assert!(terminal_json.get("error").is_none(), "{terminal_json}");
    assert_eq!(provider.requests().len(), 1);
    let row: (String, Option<String>, Option<String>) = sqlx::query_as(
        "SELECT run_kind, parent_run_id, parent_tool_call_id FROM runs WHERE cursor_request_id = ?",
    )
    .bind(request_id)
    .fetch_one(store.pool())
    .await
    .unwrap();
    assert_eq!(row, ("root".into(), None, None));
}

fn client_run() -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::RunRequest(
            pb::AgentRunRequest {
                action: Some(pb::ConversationAction {
                    action: Some(pb::conversation_action::Action::UserMessageAction(
                        pb::UserMessageAction {
                            user_message: Some(pb::UserMessage {
                                text: "hello".into(),
                                message_id: "failed-user".into(),
                                mode: pb::AgentMode::Agent as i32,
                                ..Default::default()
                            }),
                            ..Default::default()
                        },
                    )),
                    ..Default::default()
                }),
                conversation_id: Some("failed-conversation".into()),
                run_id: Some("failed-request".into()),
                requested_model: Some(pb::RequestedModel {
                    model_id: "test-model".into(),
                    ..Default::default()
                }),
                ..Default::default()
            },
        )),
    }
}

fn protocol_client_run(text: &str, message_id: &str) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::RunRequest(
            pb::AgentRunRequest {
                action: Some(pb::ConversationAction {
                    action: Some(pb::conversation_action::Action::UserMessageAction(
                        pb::UserMessageAction {
                            user_message: Some(pb::UserMessage {
                                text: text.into(),
                                message_id: message_id.into(),
                                mode: pb::AgentMode::Agent as i32,
                                ..Default::default()
                            }),
                            ..Default::default()
                        },
                    )),
                    ..Default::default()
                }),
                conversation_id: Some("protocol-failed-conversation".into()),
                run_id: Some("protocol-failed-request".into()),
                requested_model: Some(pb::RequestedModel {
                    model_id: "test-model".into(),
                    ..Default::default()
                }),
                ..Default::default()
            },
        )),
    }
}

fn kv_ack(id: u32) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::KvClientMessage(
            pb::KvClientMessage {
                id,
                message: Some(pb::kv_client_message::Message::SetBlobResult(
                    pb::SetBlobResult { error: None },
                )),
            },
        )),
    }
}
