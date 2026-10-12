//! Verifies the local WriteGitCommitMessage engine end to end on the wire.
#[path = "support/fake_provider.rs"]
mod fake_provider;
#[path = "support/fixtures.rs"]
mod fixtures;

use std::sync::Arc;

use axum::{
    body::{to_bytes, Body},
    http::{header, Request, StatusCode},
};
use cursor_server::{
    api::cursor,
    cursor::{
        prompting::{PromptAssets, PromptCompiler},
        protocol::{connect, proto::aiserver::v1 as ai},
        transport::TransportRegistry,
    },
    model::{ContentPart, ModelConfigInput, ModelType, ProjectedContent, OPENAI_CHAT_ENDPOINT},
    network::NetworkClients,
    provider::{FinishReason, ModelEvent},
    store::{CommitPromptLocale, CommitSettings, DEFAULT_COMMIT_PROMPT_ZH_CN},
};
use tower::ServiceExt;

async fn commit_router(
    store: cursor_server::store::Store,
    provider: fake_provider::FakeProvider,
) -> axum::Router {
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let clients = NetworkClients::new(store.clone());
    let registry = TransportRegistry::new(store, Arc::new(provider), PromptCompiler::new(assets));
    cursor::router(registry, clients).unwrap()
}

fn model_input(model_id: &str) -> ModelConfigInput {
    ModelConfigInput {
        sort_order: 1,
        display_name: "Qwen Flash".into(),
        group_name: None,
        model_type: ModelType::OpenAi,
        base_url: "https://example.com/v1".into(),
        use_full_url: false,
        api_key: "test-key".into(),
        supports_images: None,
        supports_tools: None,
        tooltip_data: "模型介绍".into(),
        model_id: model_id.into(),
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
    }
}

async fn post_commit_message(
    router: axum::Router,
    request: ai::WriteGitCommitMessageRequest,
) -> axum::response::Response {
    let body = connect::encode_message(&request).unwrap();
    router
        .oneshot(
            Request::post("/aiserver.v1.AiService/WriteGitCommitMessage")
                .header(header::CONTENT_TYPE, "application/proto")
                .body(Body::from(body))
                .unwrap(),
        )
        .await
        .unwrap()
}

fn diff_request(diff: &str) -> ai::WriteGitCommitMessageRequest {
    ai::WriteGitCommitMessageRequest {
        diffs: vec![diff.into()],
        previous_commit_messages: vec!["feat: 上一次提交".into()],
        explicit_context: None,
    }
}

#[tokio::test]
async fn commit_message_is_generated_through_configured_model() {
    let (_directory, store) = fixtures::temp_store().await;
    let created = store
        .create_model(&model_input("qwen/qwen3-flash"))
        .await
        .unwrap();
    store
        .set_commit_settings(CommitSettings {
            model_id: created.model_hash.clone(),
            prompt: String::new(),
            prompt_locale: CommitPromptLocale::ZhCn,
        })
        .await
        .unwrap();
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::TextStart,
        ModelEvent::TextDelta("```\nCommit message: feat: 新增提交引擎\n```".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let router = commit_router(store, provider.clone()).await;

    let response = post_commit_message(router, diff_request("diff --git a/engine.rs")).await;

    assert_eq!(response.status(), StatusCode::OK);
    let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
    let decoded: ai::WriteGitCommitMessageResponse = prost::Message::decode(&body[..]).unwrap();
    assert_eq!(decoded.commit_message, "feat: 新增提交引擎");

    let requests = provider.requests();
    assert_eq!(requests.len(), 1);
    assert_eq!(
        requests[0].prompt.instructions,
        DEFAULT_COMMIT_PROMPT_ZH_CN.trim()
    );
    let ProjectedContent::Parts(parts) = &requests[0].history[0].content else {
        panic!("expected user text parts");
    };
    let ContentPart::Text { text } = &parts[0] else {
        panic!("expected text part");
    };
    assert!(text.contains("diff --git a/engine.rs"));
    assert!(text.contains("- feat: 上一次提交"));
}

#[tokio::test]
async fn custom_prompt_and_model_from_commit_settings_are_used() {
    let (_directory, store) = fixtures::temp_store().await;
    let created = store
        .create_model(&model_input("qwen/qwen3-coder"))
        .await
        .unwrap();
    store
        .set_commit_settings(CommitSettings {
            model_id: created.model_hash,
            prompt: "自定义提交提示词".into(),
            prompt_locale: CommitPromptLocale::ZhCn,
        })
        .await
        .unwrap();
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::TextDelta("chore: 清理旧代码".into()),
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let router = commit_router(store, provider.clone()).await;

    let response = post_commit_message(router, diff_request("diff --git a/old.rs")).await;

    assert_eq!(response.status(), StatusCode::OK);
    let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
    let decoded: ai::WriteGitCommitMessageResponse = prost::Message::decode(&body[..]).unwrap();
    assert_eq!(decoded.commit_message, "chore: 清理旧代码");
    assert_eq!(
        provider.requests()[0].prompt.instructions,
        "自定义提交提示词"
    );
}

#[tokio::test]
async fn empty_diffs_are_rejected_when_generating() {
    let (_directory, store) = fixtures::temp_store().await;
    let created = store
        .create_model(&model_input("qwen/qwen3-flash"))
        .await
        .unwrap();
    store
        .set_commit_settings(CommitSettings {
            model_id: created.model_hash,
            prompt: String::new(),
            prompt_locale: CommitPromptLocale::ZhCn,
        })
        .await
        .unwrap();
    let provider = fake_provider::FakeProvider::default();
    let router = commit_router(store, provider).await;

    let response = post_commit_message(router, ai::WriteGitCommitMessageRequest::default()).await;

    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    let body = to_bytes(response.into_body(), 4096).await.unwrap();
    let text = std::str::from_utf8(&body).unwrap();
    assert!(text.contains("diffs are required"));
}

#[tokio::test]
async fn tool_call_events_are_rejected() {
    let (_directory, store) = fixtures::temp_store().await;
    let created = store
        .create_model(&model_input("qwen/qwen3-flash"))
        .await
        .unwrap();
    store
        .set_commit_settings(CommitSettings {
            model_id: created.model_hash,
            prompt: String::new(),
            prompt_locale: CommitPromptLocale::ZhCn,
        })
        .await
        .unwrap();
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![ModelEvent::ToolCallStart {
        index: 0,
        call_id: "call-1".into(),
        name: "shell".into(),
    }]);
    let router = commit_router(store, provider).await;

    let response = post_commit_message(router, diff_request("diff --git a/x.rs")).await;

    assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    let body = to_bytes(response.into_body(), 4096).await.unwrap();
    let text = std::str::from_utf8(&body).unwrap();
    assert!(text.contains("must not invoke tools"));
}

#[tokio::test]
async fn unconfigured_model_is_rejected() {
    let (_directory, store) = fixtures::temp_store().await;
    store
        .set_commit_settings(CommitSettings {
            model_id: "missing-hash".into(),
            prompt: String::new(),
            prompt_locale: CommitPromptLocale::ZhCn,
        })
        .await
        .unwrap();
    let provider = fake_provider::FakeProvider::default();
    let router = commit_router(store, provider).await;

    let response = post_commit_message(router, diff_request("diff --git a/x.rs")).await;

    assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    let body = to_bytes(response.into_body(), 4096).await.unwrap();
    let text = std::str::from_utf8(&body).unwrap();
    assert!(text.contains("missing-hash"));
}
