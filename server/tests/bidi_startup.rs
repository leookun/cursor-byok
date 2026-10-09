//! Exercises HTTP appends that overtake a still-uploading initial RunRequest.
#[path = "support/fake_provider.rs"]
mod fake_provider;
#[path = "support/fixtures.rs"]
mod fixtures;

use std::{convert::Infallible, sync::Arc, time::Duration};

use axum::{
    body::{Body, Bytes},
    http::{header, Request, StatusCode},
    Router,
};
use cursor_server::{
    api::cursor,
    cursor::{
        prompting::{PromptAssets, PromptCompiler},
        protocol::{
            connect,
            proto::{agent::v1 as pb, aiserver::v1 as ai},
        },
        transport::TransportRegistry,
    },
    model::{ModelConfigInput, ModelType, OPENAI_CHAT_ENDPOINT},
    network::NetworkClients,
    provider::{FinishReason, ModelEvent},
};
use futures_util::StreamExt;
use prost::Message;
use tower::ServiceExt;

const REQUEST_ID: &str = "slow-subagent-start";
const APPEND: &str = "/aiserver.v1.BidiService/BidiAppend";

async fn setup() -> (
    tempfile::TempDir,
    TransportRegistry,
    fake_provider::FakeProvider,
    Router,
    String,
) {
    let (directory, store) = fixtures::temp_store().await;
    let model = store
        .create_model(&ModelConfigInput {
            sort_order: 0,
            display_name: "Test model".into(),
            group_name: None,
            model_type: ModelType::OpenAi,
            base_url: "http://unused.invalid/v1".into(),
            use_full_url: false,
            api_key: "test-key".into(),
            tooltip_data: "Test model".into(),
            model_id: "test-model".into(),
            reasoning_effort: None,
            openai_endpoint: OPENAI_CHAT_ENDPOINT.into(),
            openai_extra_params_enabled: false,
            openai_extra_params: serde_json::json!({}),
            custom_headers_enabled: false,
            custom_headers: serde_json::json!({}),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: serde_json::json!({}),
            context_window_tokens: None,
            max_completion_tokens: None,
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
        })
        .await
        .unwrap();
    let assets =
        PromptAssets::load(&std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("prompt/cursor"))
            .unwrap();
    let provider = fake_provider::FakeProvider::default();
    let clients = NetworkClients::new(store.clone());
    let registry = TransportRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
    );
    let router = cursor::router(registry.clone(), clients).unwrap();
    (directory, registry, provider, router, model.model_hash)
}

fn append_body(seqno: i64, message: pb::agent_client_message::Message) -> Bytes {
    connect::encode_message(&ai::BidiAppendRequest {
        request_id: Some(ai::BidiRequestId {
            request_id: REQUEST_ID.into(),
        }),
        append_seqno: seqno,
        data: hex::encode(
            pb::AgentClientMessage {
                message: Some(message),
            }
            .encode_to_vec(),
        ),
        ..Default::default()
    })
    .unwrap()
}

fn post(path: &str, body: impl Into<Body>) -> Request<Body> {
    Request::post(path)
        .header(header::CONTENT_TYPE, "application/proto")
        .body(body.into())
        .unwrap()
}

fn run_request(model_id: String) -> pb::agent_client_message::Message {
    pb::agent_client_message::Message::RunRequest(pb::AgentRunRequest {
        conversation_id: Some("startup-child".into()),
        subagent_type_name: Some("generalPurpose".into()),
        requested_model: Some(pb::RequestedModel {
            model_id,
            ..Default::default()
        }),
        action: Some(pb::ConversationAction {
            action: Some(pb::conversation_action::Action::UserMessageAction(
                pb::UserMessageAction {
                    user_message: Some(pb::UserMessage {
                        text: "Return the child result".into(),
                        message_id: "child-user".into(),
                        mode: pb::AgentMode::Agent as i32,
                        ..Default::default()
                    }),
                    ..Default::default()
                },
            )),
            ..Default::default()
        }),
        ..Default::default()
    })
}

#[tokio::test]
async fn heartbeat_overtaking_initial_upload_does_not_abort_subagent() {
    exercise_startup(0).await;
}

#[tokio::test]
async fn configured_hosted_alias_runs_byok_and_completes() {
    exercise_startup(1).await;
}

#[tokio::test]
async fn model_details_alias_runs_byok_and_completes() {
    exercise_startup(2).await;
}

async fn exercise_startup(use_alias: u8) {
    let (_directory, registry, provider, router, model_id) = setup().await;
    let selected = if use_alias > 0 {
        registry
            .store()
            .set_cursor_model_aliases(std::collections::BTreeMap::from([(
                "cursor-grok-4.6-high-fast".into(),
                model_id.clone(),
            )]))
            .await
            .unwrap();
        "cursor-grok-4.6-high-fast".to_string()
    } else {
        model_id.clone()
    };
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "child-call".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("child completed".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);

    // Hold the body mid-upload, without a large or timing-dependent fixture.
    let mut request = run_request(selected.clone());
    if use_alias == 2 {
        let pb::agent_client_message::Message::RunRequest(run) = &mut request else {
            unreachable!()
        };
        run.requested_model = None;
        run.model_details = Some(pb::ModelDetails {
            model_id: selected,
            ..Default::default()
        });
    }
    let wire = append_body(0, request);
    let (release, uploaded) = tokio::sync::oneshot::channel();
    let (reading, started) = tokio::sync::oneshot::channel();
    let body = Body::from_stream(async_stream::stream! {
        yield Ok::<_, Infallible>(wire.slice(..5));
        reading.send(()).unwrap();
        uploaded.await.unwrap();
        yield Ok::<_, Infallible>(wire.slice(5..));
    });
    let initial = tokio::spawn(router.clone().oneshot(post(APPEND, body)));
    started.await.unwrap();
    let heartbeat = append_body(
        1,
        pb::agent_client_message::Message::ClientHeartbeat(Default::default()),
    );
    let mut early = Box::pin(router.clone().oneshot(post(APPEND, heartbeat)));
    assert!(
        tokio::time::timeout(Duration::from_millis(50), &mut early)
            .await
            .is_err(),
        "a later append must wait for the initial model selection"
    );
    assert!(
        registry.local(REQUEST_ID).await.is_none(),
        "must not invent a local route"
    );
    release.send(()).unwrap();
    assert_eq!(initial.await.unwrap().unwrap().status(), StatusCode::OK);
    assert_eq!(
        tokio::time::timeout(Duration::from_secs(2), early)
            .await
            .unwrap()
            .unwrap()
            .status(),
        StatusCode::OK
    );

    let response = router
        .clone()
        .oneshot(post(
            "/agent.v1.AgentService/RunSSE",
            connect::encode_message(&pb::BidiRequestId {
                request_id: REQUEST_ID.into(),
            })
            .unwrap(),
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let mut output = response.into_body().into_data_stream();
    let mut seqno = 2;
    let mut ended = false;
    let mut text = String::new();
    loop {
        let frame = tokio::time::timeout(Duration::from_secs(5), output.next())
            .await
            .unwrap()
            .unwrap()
            .unwrap();
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            let terminal: serde_json::Value = serde_json::from_slice(&payload).unwrap();
            assert!(terminal.get("error").is_none(), "{terminal}");
            break;
        }
        let message = pb::AgentServerMessage::decode(payload).unwrap();
        match message.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                let ack = append_body(
                    seqno,
                    pb::agent_client_message::Message::KvClientMessage(pb::KvClientMessage {
                        id: kv.id,
                        message: Some(pb::kv_client_message::Message::SetBlobResult(
                            pb::SetBlobResult { error: None },
                        )),
                    }),
                );
                seqno += 1;
                assert_eq!(
                    router
                        .clone()
                        .oneshot(post(APPEND, ack))
                        .await
                        .unwrap()
                        .status(),
                    StatusCode::OK
                );
            }
            Some(pb::agent_server_message::Message::InteractionUpdate(update)) => {
                if let Some(pb::interaction_update::Message::TextDelta(delta)) = &update.message {
                    text.push_str(&delta.text);
                }
                ended |= matches!(
                    update.message,
                    Some(pb::interaction_update::Message::TurnEnded(_))
                );
            }
            _ => {}
        }
    }
    assert!(ended);
    assert_eq!(text, "child completed");
    assert_eq!(provider.requests().len(), 1);
    assert_eq!(provider.requests()[0].model.model_id, model_id);
    let result: (String, i64) = sqlx::query_as(
        "SELECT status, provider_call_index FROM runs WHERE conversation_id = 'startup-child'",
    )
    .fetch_one(registry.store().pool())
    .await
    .unwrap();
    assert_eq!(result.0, "completed");
    assert_eq!(result.1, 0);
    registry.shutdown().await;
}

#[tokio::test]
async fn initial_append_still_requires_a_model() {
    let (_directory, registry, _provider, router, _model) = setup().await;
    let response = tokio::time::timeout(
        Duration::from_secs(1),
        router.oneshot(post(
            APPEND,
            append_body(
                0,
                pb::agent_client_message::Message::ClientHeartbeat(Default::default()),
            ),
        )),
    )
    .await
    .unwrap()
    .unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert!(registry.local(REQUEST_ID).await.is_none());
}

#[tokio::test]
async fn early_append_uses_the_eventual_upstream_route() {
    let (_directory, registry, _provider, router, _model) = setup().await;
    let mut request = post(
        APPEND,
        append_body(
            1,
            pb::agent_client_message::Message::ClientHeartbeat(Default::default()),
        ),
    );
    // Reject at the upstream URL validator to exercise forwarding without an
    // external service, credentials, or a network request.
    request.headers_mut().insert(
        "x-server-upstream-url",
        "http://invalid.example".parse().unwrap(),
    );
    let mut response = Box::pin(router.oneshot(request));
    assert!(
        tokio::time::timeout(Duration::from_millis(50), &mut response)
            .await
            .is_err()
    );
    registry.mark_upstream(REQUEST_ID).await;
    let response = tokio::time::timeout(Duration::from_secs(2), response)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    let body = axum::body::to_bytes(response.into_body(), 4096)
        .await
        .unwrap();
    assert!(std::str::from_utf8(&body)
        .unwrap()
        .contains("upstream URL must target a Cursor HTTPS host"));
    assert!(registry.local(REQUEST_ID).await.is_none());
    registry.shutdown().await;
}

#[tokio::test]
async fn orphan_followup_times_out_without_creating_a_transport() {
    let (_directory, registry, _provider, router, _model) = setup().await;
    let response = tokio::time::timeout(
        Duration::from_secs(35),
        router.oneshot(post(
            APPEND,
            append_body(
                1,
                pb::agent_client_message::Message::ClientHeartbeat(Default::default()),
            ),
        )),
    )
    .await
    .unwrap()
    .unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    let body = axum::body::to_bytes(response.into_body(), 4096)
        .await
        .unwrap();
    assert!(std::str::from_utf8(&body)
        .unwrap()
        .contains("timed out waiting for the initial BidiAppend model selection"));
    assert!(registry.local(REQUEST_ID).await.is_none());
    assert!(!registry.upstream(REQUEST_ID).await);
}

#[tokio::test]
async fn alias_settings_reject_missing_targets_and_reserved_ids() {
    let (_directory, registry, _provider, _router, model_id) = setup().await;
    let store = registry.store();
    assert!(store.cursor_model_aliases().await.unwrap().is_empty());
    for (alias, target) in [
        ("cursor-grok", "missing"),
        ("", model_id.as_str()),
        ("default", model_id.as_str()),
        ("plugin:test", model_id.as_str()),
        (model_id.as_str(), model_id.as_str()),
    ] {
        assert!(store
            .set_cursor_model_aliases(std::collections::BTreeMap::from([(
                alias.into(),
                target.into()
            ),]))
            .await
            .is_err());
    }
    assert!(store.cursor_model_aliases().await.unwrap().is_empty());
}

#[tokio::test]
async fn deleted_alias_target_is_rejected_instead_of_forwarded() {
    let (_directory, registry, _provider, router, model_id) = setup().await;
    registry
        .store()
        .set_cursor_model_aliases(std::collections::BTreeMap::from([(
            "cursor-grok".into(),
            model_id.clone(),
        )]))
        .await
        .unwrap();
    registry.store().delete_model(&model_id).await.unwrap();
    let response = router
        .oneshot(post(
            APPEND,
            append_body(0, run_request("cursor-grok".into())),
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert!(registry.local(REQUEST_ID).await.is_none());
    assert!(!registry.upstream(REQUEST_ID).await);
}
