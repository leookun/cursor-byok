//! Verifies BreakMessages, cancellation, shutdown, and Finalizing races.
#[path = "support/fake_provider.rs"]
mod fake_provider;
#[path = "support/fixtures.rs"]
mod fixtures;

use std::sync::Arc;

use bytes::Bytes;
use cursor_server::{
    cursor::prompting::{PromptAssets, PromptCompiler},
    cursor::protocol::{connect, proto::agent::v1 as pb},
    cursor::{TransportCommand, TransportRegistry},
    model::{
        CanonicalMessage, ConversationId, ModelConfigInput, ModelSpec, ModelType, Origin,
        PreparedRun, PromptSpec, Role, RunAction, RunId, RunKind, Usage, OPENAI_CHAT_ENDPOINT,
    },
    provider::{FinishReason, ModelEvent},
    run::{self, CommandResult, CommitCause, RunEngine, RunEvent, RunOutcome, RunPhase},
    store::RunStatus,
};
use prost::Message;

#[tokio::test]
async fn finalizing_rejects_late_messages_for_the_next_run() {
    let (_directory, store) = fixtures::temp_store().await;
    let conversation_id = ConversationId::new("finalizing-conversation");
    let base_checkpoint_id = store.ensure_conversation(&conversation_id).await.unwrap();
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "final-call".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("done".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let prepared = PreparedRun {
        run_id: RunId::new("finalizing-run"),
        cursor_request_id: None,
        conversation_id,
        kind: RunKind::Root,
        model: ModelSpec::new("model"),
        prompt: PromptSpec {
            instructions: String::new(),
            tools: Vec::new(),
        },
        initial_messages: Vec::new(),
        action: RunAction::Resume {
            pending_tool_round: None,
        },
        base_checkpoint_id,
    };
    let (port, mut session, handle) = run::channel(prepared.run_id.clone(), 32);
    let cancellation = handle.cancellation();
    let task = tokio::spawn(async move {
        RunEngine::new(store, Arc::new(provider))
            .run(prepared, port, cancellation)
            .await
    });

    loop {
        match session.events.recv().await.unwrap() {
            RunEvent::MessagesCommitted(committed) if committed.cause == CommitCause::FinalTurn => {
                assert_eq!(handle.phase(), RunPhase::Finalizing);
                assert_eq!(
                    handle
                        .insert_messages(
                            "late-event".into(),
                            vec![CanonicalMessage::text(
                                "late-message",
                                Role::User,
                                Origin::Runtime,
                                "late",
                            )],
                        )
                        .await,
                    CommandResult::RunClosing
                );
                committed.barrier.complete(Ok(()));
            }
            RunEvent::Ended(outcome) => {
                assert_eq!(outcome, RunOutcome::Completed);
                break;
            }
            _ => {}
        }
    }
    assert_eq!(task.await.unwrap(), RunOutcome::Completed);
}

#[tokio::test]
async fn break_messages_emits_one_cycle_boundary_before_the_runtime_commit() {
    let (_directory, store) = fixtures::temp_store().await;
    let conversation_id = ConversationId::new("cycle-boundary-conversation");
    let base_checkpoint_id = store.ensure_conversation(&conversation_id).await.unwrap();
    let provider = fake_provider::FakeProvider::default();
    provider.push_pending();
    provider.push(text_response("continued"));
    let prepared = PreparedRun {
        run_id: RunId::new("cycle-boundary-run"),
        cursor_request_id: None,
        conversation_id,
        kind: RunKind::Root,
        model: ModelSpec::new("model"),
        prompt: PromptSpec {
            instructions: String::new(),
            tools: Vec::new(),
        },
        initial_messages: Vec::new(),
        action: RunAction::Resume {
            pending_tool_round: None,
        },
        base_checkpoint_id,
    };
    let (port, mut session, handle) = run::channel(prepared.run_id.clone(), 32);
    let cancellation = handle.cancellation();
    let engine_store = store.clone();
    let engine_provider = provider.clone();
    let engine = tokio::spawn(async move {
        RunEngine::new(engine_store, Arc::new(engine_provider))
            .run(prepared, port, cancellation)
            .await
    });
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(1);
    while provider.requests().is_empty() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "first model cycle did not start"
        );
        tokio::task::yield_now().await;
    }
    let mut message = CanonicalMessage::text(
        "cycle-boundary-message",
        Role::User,
        Origin::Runtime,
        "new information",
    );
    message.runtime_event_id = Some("cycle-boundary-event".into());
    let command = tokio::spawn({
        let handle = handle.clone();
        async move {
            handle
                .break_messages("cycle-boundary-event".into(), vec![message])
                .await
        }
    });

    let mut lifecycle = Vec::new();
    loop {
        match session.events.recv().await.unwrap() {
            RunEvent::CycleInterrupted => lifecycle.push("interrupted"),
            RunEvent::MessagesCommitted(committed) => {
                if matches!(committed.cause, CommitCause::RuntimeEvent { .. }) {
                    lifecycle.push("runtime-committed");
                }
                committed.barrier.complete(Ok(()));
            }
            RunEvent::Ended(outcome) => {
                assert_eq!(outcome, RunOutcome::Completed);
                break;
            }
            _ => {}
        }
    }

    assert_eq!(lifecycle, ["interrupted", "runtime-committed"]);
    assert_eq!(command.await.unwrap(), CommandResult::Applied);
    assert_eq!(engine.await.unwrap(), RunOutcome::Completed);
}

#[tokio::test]
async fn a_replaced_run_cannot_overwrite_its_cancelled_status() {
    let (_directory, store) = fixtures::temp_store().await;
    let conversation_id = ConversationId::new("conversation");
    let base_checkpoint_id = store.ensure_conversation(&conversation_id).await.unwrap();
    let prepared = |run_id: &str| PreparedRun {
        run_id: RunId::new(run_id),
        cursor_request_id: None,
        conversation_id: conversation_id.clone(),
        kind: RunKind::Root,
        model: ModelSpec::new("model"),
        prompt: PromptSpec {
            instructions: String::new(),
            tools: Vec::new(),
        },
        initial_messages: Vec::new(),
        action: RunAction::Resume {
            pending_tool_round: None,
        },
        base_checkpoint_id,
    };
    let first = prepared("first");
    let second = prepared("second");

    store.claim_run(&first).await.unwrap();
    sqlx::query(
        "INSERT INTO llm_calls(
            call_id, run_id, conversation_id, provider_call_index,
            provider_type, provider_url, request_type, request_url,
            model_id, display_name, status,
            created_at_ms, message_count, tool_count, detailed
         ) VALUES (
            'first:0', 'first', 'conversation', 0,
            'openai-chat', 'https://example.com/v1',
            'openai-chat', 'https://example.com/v1/chat/completions',
            'model', 'Model', 'running',
            unixepoch('subsec') * 1000, 1, 0, 0
         )",
    )
    .execute(store.pool())
    .await
    .unwrap();
    store.claim_run(&second).await.unwrap();
    assert!(!store
        .finish_run(&first.run_id, RunStatus::Completed, None, None,)
        .await
        .unwrap());

    let status: String = sqlx::query_scalar("SELECT status FROM runs WHERE run_id = 'first'")
        .fetch_one(store.pool())
        .await
        .unwrap();
    let active: Option<String> = sqlx::query_scalar(
        "SELECT active_run_id FROM conversations WHERE conversation_id = 'conversation'",
    )
    .fetch_one(store.pool())
    .await
    .unwrap();
    assert_eq!(status, "cancelled");
    assert_eq!(active.as_deref(), Some("second"));
    let call: (String, Option<i64>, Option<i64>) = sqlx::query_as(
        "SELECT status, finished_at_ms, duration_ms FROM llm_calls WHERE call_id = 'first:0'",
    )
    .fetch_one(store.pool())
    .await
    .unwrap();
    assert_eq!(call.0, "cancelled");
    assert!(call.1.is_some());
    assert!(call.2.is_some());
}

#[tokio::test]
async fn registry_shutdown_cancels_runs_and_closes_run_sse_outputs() {
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
    let handle = registry.get_or_create("active-run").await.unwrap();
    let mut output = handle.subscribe();

    registry.shutdown().await;

    let terminal = output.recv().await.expect("canceled EndStream");
    let (flags, payload) = connect::decode_frames(&terminal).unwrap().pop().unwrap();
    assert_eq!(flags, connect::END_STREAM_FLAG);
    let payload: serde_json::Value = serde_json::from_slice(&payload).unwrap();
    assert_eq!(payload["error"]["code"], "canceled");
    assert_eq!(output.recv().await, None);
}

#[tokio::test]
async fn client_heartbeat_returns_a_server_protocol_heartbeat() {
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
    let handle = registry.get_or_create("heartbeat-run").await.unwrap();
    let mut output = handle.subscribe();

    cursor_server::api::cursor::bidi::append(
        &registry,
        cursor_server::api::cursor::bidi::DecodedAppend {
            request_id: "heartbeat-run".into(),
            // A transport heartbeat must not wait for missing application messages.
            seqno: 1,
            message: pb::AgentClientMessage {
                message: Some(pb::agent_client_message::Message::ClientHeartbeat(
                    pb::ClientHeartbeat {},
                )),
            },
        },
        None,
    )
    .await
    .unwrap();

    let frame = tokio::time::timeout(std::time::Duration::from_secs(1), output.recv())
        .await
        .unwrap()
        .unwrap();
    let (_, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
    let message = pb::AgentServerMessage::decode(payload).unwrap();
    assert!(matches!(
        message.message,
        Some(pb::agent_server_message::Message::InteractionUpdate(
            pb::InteractionUpdate {
                message: Some(pb::interaction_update::Message::Heartbeat(_)),
            }
        ))
    ));

    registry.shutdown().await;
}

#[tokio::test]
async fn runtime_cancel_action_aborts_active_exec_before_canceled_end_stream() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "ignored".into(),
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
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(store, Arc::new(provider), PromptCompiler::new(assets));
    let handle = registry.get_or_create("cancel-request").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run()),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let exec_id = loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .unwrap();
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        assert_eq!(
            flags & connect::END_STREAM_FLAG,
            0,
            "Run ended before Exec: {}",
            String::from_utf8_lossy(&payload)
        );
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
            Some(pb::agent_server_message::Message::ExecServerMessage(exec)) => break exec.id,
            _ => {}
        }
    };

    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_cancel_action()),
        })
        .await
        .unwrap();
    let mut saw_abort = false;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before canceled EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            let json: serde_json::Value = serde_json::from_slice(&payload).unwrap();
            assert_eq!(json["error"]["code"], "canceled");
            assert!(saw_abort, "ExecServerAbort must precede canceled EndStream");
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::ExecServerControlMessage(control)) =
            server.message
        {
            let Some(pb::exec_server_control_message::Message::Abort(abort)) = control.message
            else {
                panic!("expected ExecServerAbort")
            };
            assert_eq!(abort.id, exec_id);
            saw_abort = true;
        }
    }
    assert_eq!(output.recv().await, None);
}

#[tokio::test]
async fn queued_user_message_after_turn_ended_starts_the_next_turn() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(text_response("first turn"));
    provider.push(text_response("queued turn"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create("queued-after-turn").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "queued-after-turn",
                "queued-after-turn-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    wait_for_turn_ended(&handle, &mut output, &mut append_seqno).await;
    assert_transport_remains_open(&handle, &mut output, &mut append_seqno).await;

    cursor_server::api::cursor::bidi::append(
        &registry,
        cursor_server::api::cursor::bidi::DecodedAppend {
            request_id: "queued-after-turn".into(),
            seqno: append_seqno,
            message: runtime_user_message(),
        },
        None,
    )
    .await
    .unwrap();
    append_seqno += 1;

    let mut text = String::new();
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("queued turn closed without EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(payload.as_ref(), b"{}");
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::InteractionUpdate(update)) = server.message {
            if let Some(pb::interaction_update::Message::TextDelta(delta)) = update.message {
                text.push_str(&delta.text);
            }
        }
        acknowledge_kv(&handle, &mut append_seqno, &frame).await;
    }

    assert!(text.contains("queued turn"));
    let requests = provider.requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(
        &requests[1].history[..requests[0].history.len()],
        requests[0].history.as_slice(),
        "queued continuation must preserve the first provider request as a prefix"
    );
    let history = serde_json::to_string(&requests[1].history).unwrap();
    assert!(history.contains("queued follow-up"));
}

#[tokio::test]
async fn runtime_user_message_action_interrupts_and_continues_with_new_message() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push_pending();
    provider.push(text_response("continued after user interruption"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry
        .get_or_create("user-message-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "user-message-request",
                "user-message-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    while provider.requests().is_empty() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "provider did not start"
        );
        if let Ok(Some(frame)) =
            tokio::time::timeout(std::time::Duration::from_millis(20), output.recv()).await
        {
            let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
            if flags & connect::END_STREAM_FLAG != 0 {
                panic!("initial run ended: {}", String::from_utf8_lossy(&payload));
            }
            acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        }
    }
    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_user_message()),
        })
        .await
        .unwrap();

    let mut saw_continued = false;
    let mut append_seqno = append_seqno + 1;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before successful EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(payload.as_ref(), b"{}");
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::InteractionUpdate(update)) = server.message {
            if let Some(pb::interaction_update::Message::TextDelta(delta)) = update.message {
                saw_continued |= delta.text.contains("continued after user interruption");
            }
        }
        acknowledge_kv(&handle, &mut append_seqno, &frame).await;
    }
    assert!(saw_continued);
    assert_eq!(provider.requests().len(), 2);
    let history = serde_json::to_string(&provider.requests()[1].history).unwrap();
    assert!(history.contains("queued follow-up"));
}

#[tokio::test]
async fn runtime_user_message_reports_delivered_and_appended() {
    // A runtime user message is queued into `pending_injections` under a
    // `user-message:{id}` key, but the commit correlation only handled the
    // `inject-context:` prefix, so the entry was never cleared: the client
    // never saw Delivered/UserMessageAppended and every later tool round was
    // detached (a hang). This asserts the full delivery sequence.
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push_pending();
    provider.push(text_response("continued after user interruption"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry
        .get_or_create("user-message-events-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "user-message-events-request",
                "user-message-events-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    while provider.requests().is_empty() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "provider did not start"
        );
        if let Ok(Some(frame)) =
            tokio::time::timeout(std::time::Duration::from_millis(20), output.recv()).await
        {
            acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        }
    }
    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_user_message()),
        })
        .await
        .unwrap();
    append_seqno += 1;

    let mut protocol_events = Vec::new();
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before successful EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(payload.as_ref(), b"{}");
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::InteractionUpdate(update)) = server.message {
            match update.message {
                Some(pb::interaction_update::Message::ContextInjectionState(update)) => {
                    assert_eq!(update.injection_id, "user-message:queued-user");
                    match update.state.and_then(|state| state.state) {
                        Some(pb::context_injection_state::State::Queued(_)) => {
                            protocol_events.push("queued")
                        }
                        Some(pb::context_injection_state::State::Delivered(delivered)) => {
                            assert!(!delivered.delivery_batch_id.is_empty());
                            assert!(delivered.delivered_at_ms > 0);
                            protocol_events.push("delivered");
                        }
                        _ => {}
                    }
                }
                Some(pb::interaction_update::Message::UserMessageAppended(update)) => {
                    let user = update.user_message.expect("appended user message");
                    assert_eq!(user.message_id, "queued-user");
                    assert_eq!(user.text, "queued follow-up");
                    protocol_events.push("user_message_appended");
                }
                Some(pb::interaction_update::Message::TextDelta(update))
                    if update.text.contains("continued after user interruption") =>
                {
                    protocol_events.push("continued_output");
                }
                _ => {}
            }
        }
        acknowledge_kv(&handle, &mut append_seqno, &frame).await;
    }

    assert_eq!(
        protocol_events,
        [
            "queued",
            "delivered",
            "user_message_appended",
            "continued_output"
        ]
    );
}

#[tokio::test]
async fn tool_call_with_empty_arguments_does_not_fail_the_run() {
    // A tool call that carries no arguments streams no argument text. Parsing it
    // as JSON must yield an empty object (as the model cycle already does), not
    // fail the run with `EOF while parsing a value`.
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(tool_response("call-1", "UpdateCurrentStep", ""));
    provider.push(text_response("done after empty-argument tool"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create("empty-args-request").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "empty-args-request",
                "empty-args-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let mut saw_done = false;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before successful EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(
                payload.as_ref(),
                b"{}",
                "run failed: {}",
                String::from_utf8_lossy(&payload)
            );
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::InteractionUpdate(update)) = server.message {
            if let Some(pb::interaction_update::Message::TextDelta(delta)) = update.message {
                saw_done |= delta.text.contains("done after empty-argument tool");
            }
        }
        acknowledge_kv(&handle, &mut append_seqno, &frame).await;
    }
    assert!(saw_done);
    assert_eq!(provider.requests().len(), 2);
}

#[tokio::test]
async fn injected_user_context_restarts_only_the_active_model_cycle() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push_pending();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "continued".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("continued after injection".into()),
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
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create("inject-request").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for("inject-request", "inject-conversation")),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    while provider.requests().is_empty() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "provider did not start"
        );
        if let Ok(Some(frame)) =
            tokio::time::timeout(std::time::Duration::from_millis(20), output.recv()).await
        {
            acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        }
    }
    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_injection()),
        })
        .await
        .unwrap();
    append_seqno += 1;

    let mut protocol_events = Vec::new();
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before successful EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(payload.as_ref(), b"{}");
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::InteractionUpdate(update)) = server.message {
            match update.message {
                Some(pb::interaction_update::Message::ContextInjectionState(update)) => {
                    assert_eq!(update.injection_id, "injection-1");
                    match update.state.and_then(|state| state.state) {
                        Some(pb::context_injection_state::State::Queued(_)) => {
                            protocol_events.push("queued")
                        }
                        Some(pb::context_injection_state::State::Delivered(delivered)) => {
                            assert!(!delivered.delivery_batch_id.is_empty());
                            assert!(delivered.delivered_at_ms > 0);
                            protocol_events.push("delivered");
                        }
                        _ => {}
                    }
                }
                Some(pb::interaction_update::Message::UserMessageAppended(update)) => {
                    let user = update.user_message.expect("appended user message");
                    assert_eq!(user.message_id, "injected-user");
                    assert_eq!(user.text, "injected follow-up");
                    protocol_events.push("user_message_appended");
                }
                Some(pb::interaction_update::Message::TextDelta(update))
                    if update.text.contains("continued after injection") =>
                {
                    protocol_events.push("continued_output");
                }
                _ => {}
            }
        }
        acknowledge_kv(&handle, &mut append_seqno, &frame).await;
    }

    let requests = provider.requests();
    assert_eq!(requests.len(), 2);
    let continued_history = serde_json::to_string(&requests[1].history).unwrap();
    assert!(continued_history.contains("injected follow-up"));
    assert_eq!(
        protocol_events,
        [
            "queued",
            "delivered",
            "user_message_appended",
            "continued_output"
        ]
    );
}

#[tokio::test]
async fn injected_user_context_aborts_pending_tools_and_ignores_late_results() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(tool_response("call-1", "Read", "{\"path\":\"/tmp/a\"}"));
    let release = provider.push_gated(text_response("continued after tool interruption"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry
        .get_or_create("interrupt-tool-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "interrupt-tool-request",
                "interrupt-tool-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let exec_id = wait_for_exec(&handle, &mut output, &mut append_seqno, "Read").await;
    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_injection_for(
                "tool-injection",
                "interrupt-tool-request",
            )),
        })
        .await
        .unwrap();
    append_seqno += 1;

    let mut saw_abort = false;
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    while provider.requests().len() < 2 || !saw_abort {
        assert!(
            tokio::time::Instant::now() < deadline,
            "root model did not restart after tool interruption"
        );
        if let Ok(Some(frame)) =
            tokio::time::timeout(std::time::Duration::from_millis(20), output.recv()).await
        {
            let (_, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
            let server = pb::AgentServerMessage::decode(payload).unwrap();
            if let Some(pb::agent_server_message::Message::ExecServerControlMessage(control)) =
                server.message
            {
                if let Some(pb::exec_server_control_message::Message::Abort(abort)) =
                    control.message
                {
                    assert_eq!(abort.id, exec_id);
                    saw_abort = true;
                }
            }
            acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        }
    }

    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(read_success(exec_id)),
        })
        .await
        .unwrap();
    append_seqno += 1;
    release.notify_one();

    drain_successfully(&handle, &mut output, &mut append_seqno).await;

    let requests = provider.requests();
    assert_eq!(
        requests[0].history,
        requests[1].history[..requests[0].history.len()]
    );
    let history = serde_json::to_string(&requests[1].history).unwrap();
    let interrupted = history
        .find("Tool execution was interrupted by a newer user message.")
        .expect("interrupted tool result missing from provider history");
    let injected = history
        .find("injected follow-up")
        .expect("injected message missing from provider history");
    assert!(interrupted < injected);
}

#[tokio::test]
async fn injected_user_context_detaches_subagents_without_cancelling_them() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(tool_response(
        "task-call",
        "Task",
        &serde_json::json!({
            "description": "Inspect protocol",
            "prompt": "Inspect the protocol",
            "subagent_type": "generalPurpose",
            "run_in_background": false
        })
        .to_string(),
    ));
    let release = provider.push_gated(text_response("continued while subagent runs"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry
        .get_or_create("detach-subagent-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "detach-subagent-request",
                "detach-subagent-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let exec_id = wait_for_exec(&handle, &mut output, &mut append_seqno, "Task").await;
    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_injection_for(
                "subagent-injection",
                "detach-subagent-request",
            )),
        })
        .await
        .unwrap();
    append_seqno += 1;

    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    while provider.requests().len() < 2 {
        assert!(
            tokio::time::Instant::now() < deadline,
            "root model did not restart while subagent remained active"
        );
        if let Ok(Some(frame)) =
            tokio::time::timeout(std::time::Duration::from_millis(20), output.recv()).await
        {
            let (_, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
            let server = pb::AgentServerMessage::decode(payload).unwrap();
            if let Some(pb::agent_server_message::Message::ExecServerControlMessage(control)) =
                server.message
            {
                if let Some(pb::exec_server_control_message::Message::Abort(abort)) =
                    control.message
                {
                    assert_ne!(abort.id, exec_id, "Task must not be aborted by injection");
                }
            }
            acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        }
    }

    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(subagent_success(exec_id)),
        })
        .await
        .unwrap();
    append_seqno += 1;
    release.notify_one();

    drain_successfully(&handle, &mut output, &mut append_seqno).await;

    let history = serde_json::to_string(&provider.requests()[1].history).unwrap();
    assert!(history.contains("Tool execution was interrupted by a newer user message."));
    assert!(history.contains("injected follow-up"));
}

#[tokio::test]
async fn injected_user_context_interrupts_automatic_compaction() {
    let (_directory, store) = fixtures::temp_store().await;
    let model = store
        .create_model(&ModelConfigInput {
            sort_order: 0,
            display_name: "Test Model".into(),
            group_name: None,
            model_type: ModelType::OpenAi,
            base_url: "https://example.com/v1/chat/completions".into(),
            use_full_url: true,
            api_key: "test-key".into(),
            supports_images: None,
            supports_tools: None,
            tooltip_data: "Test Model".into(),
            model_id: "test-model".into(),
            reasoning_effort: None,
            openai_endpoint: OPENAI_CHAT_ENDPOINT.into(),
            openai_extra_params_enabled: false,
            openai_extra_params: serde_json::json!({}),
            custom_headers_enabled: false,
            custom_headers: serde_json::json!({}),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: serde_json::json!({}),
            context_window_tokens: Some(100_000),
            max_completion_tokens: None,
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
        })
        .await
        .unwrap();
    let provider = fake_provider::FakeProvider::default();
    let seed_answer = "x".repeat(400_000);
    provider.push(text_response(&seed_answer));
    provider.push_pending();
    provider.push(text_response("continued after compacting injection"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );

    run_to_end(
        &registry,
        "seed-request",
        client_run_for_model(
            "seed-request",
            "compaction-injection-conversation",
            &model.model_hash,
        ),
    )
    .await;

    let handle = registry
        .get_or_create("inject-during-compaction")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    let mut compacting_request = client_run_for_model_with_state(
        "inject-during-compaction",
        "compaction-injection-conversation",
        &model.model_hash,
        None,
    );
    let Some(pb::agent_client_message::Message::RunRequest(request)) =
        compacting_request.message.as_mut()
    else {
        panic!("expected RunRequest")
    };
    request.requested_model.as_mut().unwrap().parameters.push(
        pb::requested_model::ModelParameterValue {
            id: "context".into(),
            value: "100000".into(),
        },
    );
    let Some(pb::conversation_action::Action::UserMessageAction(action)) = request
        .action
        .as_mut()
        .and_then(|action| action.action.as_mut())
    else {
        panic!("expected UserMessageAction")
    };
    action.user_message.as_mut().unwrap().message_id = "compaction-user".into();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(compacting_request),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    while provider.requests().len() < 2 {
        assert!(
            tokio::time::Instant::now() < deadline,
            "automatic compaction did not start"
        );
        if let Ok(Some(frame)) =
            tokio::time::timeout(std::time::Duration::from_millis(20), output.recv()).await
        {
            acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        }
    }
    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_injection_for(
                "compaction-injection",
                "inject-during-compaction",
            )),
        })
        .await
        .unwrap();
    append_seqno += 1;

    let mut saw_continued = false;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before successful EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(payload.as_ref(), b"{}");
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::InteractionUpdate(update)) = server.message {
            if let Some(pb::interaction_update::Message::TextDelta(delta)) = update.message {
                saw_continued |= delta.text.contains("continued after compacting injection");
            }
        }
        acknowledge_kv(&handle, &mut append_seqno, &frame).await;
    }

    let requests = provider.requests();
    assert_eq!(requests.len(), 3);
    assert!(
        requests[1]
            .prompt
            .instructions
            .starts_with("Summarize the conversation for the next model turn."),
        "second request was not compaction: instructions={:?}, history={:?}",
        requests[1].prompt.instructions,
        requests[1].history
    );
    assert!(!serde_json::to_string(&requests[1].history)
        .unwrap()
        .contains("injected follow-up"));
    assert!(serde_json::to_string(&requests[2].history)
        .unwrap()
        .contains("injected follow-up"));
    assert!(saw_continued);
}

#[tokio::test]
async fn stale_context_injection_is_rejected_without_failing_the_active_run() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    let release = provider.push_gated(vec![
        ModelEvent::Start {
            model_call_id: "active-cycle".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("active run completed".into()),
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
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry.get_or_create("active-request").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "active-request",
                "stale-injection-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    while provider.requests().is_empty() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "provider did not start"
        );
        if let Ok(Some(frame)) =
            tokio::time::timeout(std::time::Duration::from_millis(20), output.recv()).await
        {
            acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        }
    }
    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_injection_for("stale-injection", "replaced-request")),
        })
        .await
        .unwrap();
    append_seqno += 1;
    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_injection_for("stale-injection", "replaced-request")),
        })
        .await
        .unwrap();
    append_seqno += 1;

    let mut rejection_count = 0;
    let mut released = false;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before successful EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(payload.as_ref(), b"{}");
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        let rejected = match server.message {
            Some(pb::agent_server_message::Message::InteractionUpdate(pb::InteractionUpdate {
                message:
                    Some(pb::interaction_update::Message::ContextInjectionState(
                        pb::ContextInjectionStateUpdate {
                            injection_id,
                            state:
                                Some(pb::ContextInjectionState {
                                    state:
                                        Some(pb::context_injection_state::State::Rejected(rejected)),
                                }),
                        },
                    )),
                ..
            })) if injection_id == "stale-injection" => {
                assert_eq!(
                    rejected.reason,
                    "InjectContextAction expected run replaced-request, active run is active-request"
                );
                true
            }
            _ => false,
        };
        acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        if rejected {
            rejection_count += 1;
            if !released {
                released = true;
                release.notify_one();
            }
        }
    }

    assert!(released, "stale injection was not rejected");
    assert_eq!(rejection_count, 1);
    assert_eq!(provider.requests().len(), 1);
}

#[tokio::test]
async fn unsupported_runtime_action_is_ignored_without_failing_the_active_run() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    let release = provider.push_gated(text_response("active run completed"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry
        .get_or_create("unsupported-action-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "unsupported-action-request",
                "unsupported-action-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    while provider.requests().is_empty() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "provider did not start"
        );
        if let Ok(Some(frame)) =
            tokio::time::timeout(std::time::Duration::from_millis(20), output.recv()).await
        {
            acknowledge_kv(&handle, &mut append_seqno, &frame).await;
        }
    }

    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_unsupported_action()),
        })
        .await
        .unwrap();
    append_seqno += 1;
    tokio::task::yield_now().await;
    release.notify_one();

    drain_successfully(&handle, &mut output, &mut append_seqno).await;
    assert_eq!(provider.requests().len(), 1);
}

#[tokio::test]
async fn cancel_subagent_action_aborts_the_target_task_and_keeps_the_parent_running() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "task-cycle".into(),
        },
        ModelEvent::ToolCallStart {
            index: 0,
            call_id: "task-call".into(),
            name: "Task".into(),
        },
        ModelEvent::ToolCallArgumentsDelta {
            index: 0,
            delta: serde_json::json!({
                "description": "Inspect protocol",
                "prompt": "Inspect the protocol",
                "subagent_type": "generalPurpose",
                "run_in_background": false
            })
            .to_string(),
        },
        ModelEvent::ToolCallEnd { index: 0 },
        ModelEvent::Done(FinishReason::ToolUse),
    ]);
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "continued".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("continued after subagent cancellation".into()),
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
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let handle = registry
        .get_or_create("cancel-subagent-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for(
                "cancel-subagent-request",
                "cancel-subagent-conversation",
            )),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let exec_id = loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before Task exec");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        assert_eq!(flags & connect::END_STREAM_FLAG, 0);
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
                let Some(pb::exec_server_message::Message::SubagentArgs(args)) = exec.message
                else {
                    continue;
                };
                assert_eq!(args.tool_call_id, "task-call");
                break exec.id;
            }
            _ => {}
        }
    };

    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(runtime_cancel_subagent("task-call")),
        })
        .await
        .unwrap();
    append_seqno += 1;

    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before Task abort");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        assert_eq!(flags & connect::END_STREAM_FLAG, 0);
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::ExecServerControlMessage(control)) => {
                let Some(pb::exec_server_control_message::Message::Abort(abort)) = control.message
                else {
                    continue;
                };
                assert_eq!(abort.id, exec_id);
                break;
            }
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
            _ => {}
        }
    }

    handle
        .command(TransportCommand::Append {
            seqno: append_seqno,
            message: Box::new(subagent_aborted(exec_id)),
        })
        .await
        .unwrap();
    append_seqno += 1;

    let mut saw_continued = false;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before successful EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(payload.as_ref(), b"{}");
            break;
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
            Some(pb::agent_server_message::Message::InteractionUpdate(update)) => {
                if let Some(pb::interaction_update::Message::TextDelta(delta)) = update.message {
                    saw_continued |= delta.text.contains("continued after subagent cancellation");
                }
            }
            _ => {}
        }
    }

    assert!(saw_continued);
    assert_eq!(provider.requests().len(), 2);
}

fn client_run() -> pb::AgentClientMessage {
    client_run_for("cancel-request", "cancel-conversation")
}

fn client_run_for(request_id: &str, conversation_id: &str) -> pb::AgentClientMessage {
    client_run_for_model(request_id, conversation_id, "test-model")
}

fn client_run_for_model(
    request_id: &str,
    conversation_id: &str,
    model_id: &str,
) -> pb::AgentClientMessage {
    client_run_for_model_with_state(request_id, conversation_id, model_id, None)
}

fn client_run_for_model_with_state(
    request_id: &str,
    conversation_id: &str,
    model_id: &str,
    state: Option<pb::ConversationStateStructure>,
) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::RunRequest(
            pb::AgentRunRequest {
                action: Some(pb::ConversationAction {
                    action: Some(pb::conversation_action::Action::UserMessageAction(
                        pb::UserMessageAction {
                            user_message: Some(pb::UserMessage {
                                text: "read".into(),
                                message_id: "cancel-user".into(),
                                mode: pb::AgentMode::Agent as i32,
                                ..Default::default()
                            }),
                            ..Default::default()
                        },
                    )),
                    ..Default::default()
                }),
                conversation_id: Some(conversation_id.into()),
                run_id: Some(request_id.into()),
                requested_model: Some(pb::RequestedModel {
                    model_id: model_id.into(),
                    ..Default::default()
                }),
                conversation_state: state,
                ..Default::default()
            },
        )),
    }
}

fn text_response(text: &str) -> Vec<ModelEvent> {
    vec![
        ModelEvent::Start {
            model_call_id: format!("call-{text}"),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta(text.into()),
        ModelEvent::TextEnd,
        ModelEvent::Usage(Usage {
            input_tokens: Some(1),
            context_input_tokens: Some(1),
            output_tokens: Some(1),
            total_tokens: Some(2),
            ..Default::default()
        }),
        ModelEvent::Done(FinishReason::Stop),
    ]
}

fn tool_response(call_id: &str, name: &str, arguments: &str) -> Vec<ModelEvent> {
    vec![
        ModelEvent::Start {
            model_call_id: format!("call-{call_id}"),
        },
        ModelEvent::ToolCallStart {
            index: 0,
            call_id: call_id.into(),
            name: name.into(),
        },
        ModelEvent::ToolCallArgumentsDelta {
            index: 0,
            delta: arguments.into(),
        },
        ModelEvent::ToolCallEnd { index: 0 },
        ModelEvent::Done(FinishReason::ToolUse),
    ]
}

async fn wait_for_exec(
    handle: &cursor_server::cursor::TransportHandle,
    output: &mut tokio::sync::mpsc::UnboundedReceiver<Bytes>,
    append_seqno: &mut i64,
    tool: &str,
) -> u32 {
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before Exec");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        assert_eq!(flags & connect::END_STREAM_FLAG, 0);
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::ExecServerMessage(exec)) = server.message {
            let matches = match exec.message.as_ref() {
                Some(pb::exec_server_message::Message::ReadArgs(_)) => tool == "Read",
                Some(pb::exec_server_message::Message::SubagentArgs(_)) => tool == "Task",
                _ => false,
            };
            if matches {
                return exec.id;
            }
        }
        acknowledge_kv(handle, append_seqno, &frame).await;
    }
}

async fn drain_successfully(
    handle: &cursor_server::cursor::TransportHandle,
    output: &mut tokio::sync::mpsc::UnboundedReceiver<Bytes>,
    append_seqno: &mut i64,
) {
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before successful EndStream");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(payload.as_ref(), b"{}");
            return;
        }
        acknowledge_kv(handle, append_seqno, &frame).await;
    }
}

fn read_success(id: u32) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::ExecClientMessage(
            pb::ExecClientMessage {
                id,
                message: Some(pb::exec_client_message::Message::ReadResult(
                    pb::ReadResult {
                        result: Some(pb::read_result::Result::Success(pb::ReadSuccess {
                            path: "/tmp/a".into(),
                            total_lines: 1,
                            file_size: 1,
                            output: Some(pb::read_success::Output::Content("late".into())),
                            ..Default::default()
                        })),
                    },
                )),
                ..Default::default()
            },
        )),
    }
}

fn subagent_success(id: u32) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::ExecClientMessage(
            pb::ExecClientMessage {
                id,
                message: Some(pb::exec_client_message::Message::SubagentResult(
                    pb::SubagentResult {
                        result: Some(pb::subagent_result::Result::Success(pb::SubagentSuccess {
                            agent_id: "detached-child".into(),
                            ..Default::default()
                        })),
                    },
                )),
                ..Default::default()
            },
        )),
    }
}

async fn run_to_end(
    registry: &TransportRegistry,
    request_id: &str,
    request: pb::AgentClientMessage,
) -> pb::ConversationStateStructure {
    let handle = registry.get_or_create(request_id).await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(TransportCommand::Append {
            seqno: 0,
            message: Box::new(request),
        })
        .await
        .unwrap();
    let mut append_seqno = 1;
    let mut state = None;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before EndStream");
        let (flags, _) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            return state.expect("Run ended without a checkpoint");
        }
        let (_, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        if let Some(pb::agent_server_message::Message::ConversationCheckpointUpdate(update)) =
            server.message
        {
            state = Some(update);
        }
        acknowledge_kv(&handle, &mut append_seqno, &frame).await;
    }
}

async fn wait_for_turn_ended(
    handle: &cursor_server::cursor::TransportHandle,
    output: &mut tokio::sync::mpsc::UnboundedReceiver<Bytes>,
    append_seqno: &mut i64,
) {
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .expect("RunSSE closed before turnEnded");
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        assert_eq!(flags & connect::END_STREAM_FLAG, 0);
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        let turn_ended = matches!(
            server.message,
            Some(pb::agent_server_message::Message::InteractionUpdate(
                pb::InteractionUpdate {
                    message: Some(pb::interaction_update::Message::TurnEnded(_)),
                }
            ))
        );
        acknowledge_kv(handle, append_seqno, &frame).await;
        if turn_ended {
            return;
        }
    }
}

async fn assert_transport_remains_open(
    handle: &cursor_server::cursor::TransportHandle,
    output: &mut tokio::sync::mpsc::UnboundedReceiver<Bytes>,
    append_seqno: &mut i64,
) {
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_millis(100);
    loop {
        let remaining = deadline.saturating_duration_since(tokio::time::Instant::now());
        let Ok(Some(frame)) = tokio::time::timeout(remaining, output.recv()).await else {
            return;
        };
        let (flags, _) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        assert_eq!(
            flags & connect::END_STREAM_FLAG,
            0,
            "turnEnded closed the transport before the queued action arrived"
        );
        acknowledge_kv(handle, append_seqno, &frame).await;
    }
}

async fn acknowledge_kv(
    handle: &cursor_server::cursor::TransportHandle,
    append_seqno: &mut i64,
    frame: &[u8],
) {
    let (flags, payload) = connect::decode_frames(frame).unwrap().pop().unwrap();
    if flags & connect::END_STREAM_FLAG != 0 {
        return;
    }
    let server = pb::AgentServerMessage::decode(payload).unwrap();
    if let Some(pb::agent_server_message::Message::KvServerMessage(kv)) = server.message {
        handle
            .command(TransportCommand::Append {
                seqno: *append_seqno,
                message: Box::new(kv_ack(kv.id)),
            })
            .await
            .unwrap();
        *append_seqno += 1;
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

fn runtime_cancel_action() -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::ConversationAction(
            pb::ConversationAction {
                action: Some(pb::conversation_action::Action::CancelAction(
                    pb::CancelAction::default(),
                )),
                ..Default::default()
            },
        )),
    }
}

fn runtime_unsupported_action() -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::ConversationAction(
            pb::ConversationAction {
                action: Some(pb::conversation_action::Action::ResumeAction(
                    pb::ResumeAction::default(),
                )),
                ..Default::default()
            },
        )),
    }
}

fn runtime_user_message() -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::ConversationAction(
            pb::ConversationAction {
                action: Some(pb::conversation_action::Action::UserMessageAction(
                    pb::UserMessageAction {
                        user_message: Some(pb::UserMessage {
                            text: "queued follow-up".into(),
                            message_id: "queued-user".into(),
                            mode: pb::AgentMode::Agent as i32,
                            ..Default::default()
                        }),
                        ..Default::default()
                    },
                )),
                ..Default::default()
            },
        )),
    }
}

fn runtime_injection() -> pb::AgentClientMessage {
    runtime_injection_for("injection-1", "inject-request")
}

fn runtime_injection_for(injection_id: &str, expected_run_id: &str) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::ConversationAction(
            pb::ConversationAction {
                action: Some(pb::conversation_action::Action::InjectContextAction(
                    pb::InjectContextAction {
                        injection_id: injection_id.into(),
                        expected_run_id: expected_run_id.into(),
                        payload: Some(pb::inject_context_action::Payload::UserContext(
                            pb::UserContextInjection {
                                user_message: Some(pb::UserMessage {
                                    text: "injected follow-up".into(),
                                    message_id: "injected-user".into(),
                                    ..Default::default()
                                }),
                                request_context: Some(Default::default()),
                            },
                        )),
                    },
                )),
                ..Default::default()
            },
        )),
    }
}

fn runtime_cancel_subagent(tool_call_id: &str) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::ConversationAction(
            pb::ConversationAction {
                action: Some(pb::conversation_action::Action::CancelSubagentAction(
                    pb::CancelSubagentAction {
                        subagent_id: tool_call_id.into(),
                    },
                )),
                ..Default::default()
            },
        )),
    }
}

fn subagent_aborted(id: u32) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::ExecClientMessage(
            pb::ExecClientMessage {
                id,
                message: Some(pb::exec_client_message::Message::SubagentResult(
                    pb::SubagentResult {
                        result: Some(pb::subagent_result::Result::Error(pb::SubagentError {
                            agent_id: None,
                            error: "Subagent was aborted by the user".into(),
                        })),
                    },
                )),
                ..Default::default()
            },
        )),
    }
}
