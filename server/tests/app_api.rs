#[path = "support/fixtures.rs"]
mod fixtures;

use std::{sync::Arc, time::Duration};

use axum::{
    body::{to_bytes, Body},
    http::{header, HeaderMap, Request, StatusCode},
    Router,
};
use cursor_server::{
    control,
    model::{ModelConfigInput, ModelType, OPENAI_CHAT_ENDPOINT},
    network::NetworkClients,
    plugin::{PluginRegistry, PluginRuntime},
    provider::ProviderRouter,
    store::{AppApiAuthMethod, AppApiSettings},
};
use serde_json::{json, Value};
use tower::ServiceExt;

async fn setup() -> (Router, cursor_server::store::Store) {
    let (_directory, store) = fixtures::temp_store().await;
    let runtime = PluginRuntime::managed().unwrap();
    let plugins = PluginRegistry::managed(store.clone(), runtime.clone(), "0.1.0".into()).unwrap();
    let provider = Arc::new(ProviderRouter::new(
        store.clone(),
        plugins.clone(),
        NetworkClients::new(store.clone()),
        Duration::from_secs(5),
        Duration::from_secs(5),
    ));
    let control = control::ControlService::new(
        store.clone(),
        provider,
        runtime,
        plugins,
        NetworkClients::new(store.clone()),
        "0.1.0".into(),
    )
    .unwrap();
    (control::api_router(control), store)
}

fn model_input() -> ModelConfigInput {
    ModelConfigInput {
        sort_order: 0,
        display_name: "Agent model".into(),
        group_name: None,
        model_type: ModelType::OpenAi,
        base_url: "https://example.com/v1".into(),
        use_full_url: false,
        api_key: "upstream".into(),
        tooltip_data: "added by the application API".into(),
        model_id: "agent-model".into(),
        reasoning_effort: None,
        openai_endpoint: OPENAI_CHAT_ENDPOINT.into(),
        openai_extra_params_enabled: false,
        openai_extra_params: json!({}),
        custom_headers_enabled: false,
        custom_headers: json!({}),
        anthropic_extra_params_enabled: false,
        anthropic_extra_params: json!({}),
        context_window_tokens: None,
        max_completion_tokens: None,
        anthropic_max_tokens: None,
        anthropic_thinking_effort: None,
        thinking_budget_tokens: None,
    }
}

async fn send(
    router: Router,
    method: &str,
    path: &str,
    headers: HeaderMap,
    body: Value,
) -> (StatusCode, String) {
    let mut request = Request::builder()
        .method(method)
        .uri(path)
        .header(header::CONTENT_TYPE, "application/json");
    for (name, value) in headers.iter() {
        request = request.header(name, value);
    }
    let response = router
        .oneshot(request.body(Body::from(body.to_string())).unwrap())
        .await
        .unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), 1024 * 1024).await.unwrap();
    (status, String::from_utf8(bytes.to_vec()).unwrap())
}

fn bearer(key: &str) -> HeaderMap {
    let mut headers = HeaderMap::new();
    headers.insert(
        header::AUTHORIZATION,
        format!("Bearer {key}").parse().unwrap(),
    );
    headers
}

fn api_key(key: &str) -> HeaderMap {
    let mut headers = HeaderMap::new();
    headers.insert("x-api-key", key.parse().unwrap());
    headers
}

#[tokio::test]
async fn disabled_application_api_rejects_agents_without_blocking_the_desktop() {
    let (router, _store) = setup().await;
    let (status, body) = send(
        router.clone(),
        "GET",
        "/byok/app/v1/models",
        HeaderMap::new(),
        json!({}),
    )
    .await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    assert_eq!(
        serde_json::from_str::<Value>(&body).unwrap()["message"],
        "application API is disabled"
    );
    assert_eq!(
        send(
            router,
            "GET",
            "/__byok-api__/api/models",
            HeaderMap::new(),
            json!({}),
        )
        .await
        .0,
        StatusCode::OK
    );
}

#[tokio::test]
async fn enabled_application_api_can_add_a_model_and_change_settings() {
    let (router, store) = setup().await;
    store
        .set_app_api_settings(AppApiSettings {
            enabled: true,
            auth_required: false,
            ..AppApiSettings::default()
        })
        .await
        .unwrap();
    let (status, _) = send(
        router.clone(),
        "POST",
        "/byok/app/v1/models",
        HeaderMap::new(),
        json!({ "models": [model_input()] }),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED);
    let (status, body) = send(
        router.clone(),
        "GET",
        "/byok/app/v1/models",
        HeaderMap::new(),
        json!({}),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    let models = serde_json::from_str::<Value>(&body).unwrap();
    assert_eq!(models[0]["display_name"], "Agent model");

    let (status, body) = send(
        router,
        "PUT",
        "/byok/app/v1/settings/observability",
        HeaderMap::new(),
        json!({ "detailed": false }),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(
        serde_json::from_str::<Value>(&body).unwrap()["detailed"],
        false
    );
}

#[tokio::test]
async fn application_api_accepts_only_the_selected_authorization_method() {
    let (router, store) = setup().await;
    store
        .set_app_api_settings(AppApiSettings {
            enabled: true,
            auth_required: true,
            auth_method: AppApiAuthMethod::Bearer,
            api_key: "control-key".into(),
        })
        .await
        .unwrap();
    assert_eq!(
        send(
            router.clone(),
            "GET",
            "/byok/app/v1/plugins",
            HeaderMap::new(),
            json!({}),
        )
        .await
        .0,
        StatusCode::UNAUTHORIZED
    );
    assert_eq!(
        send(
            router.clone(),
            "GET",
            "/byok/app/v1/plugins",
            api_key("control-key"),
            json!({}),
        )
        .await
        .0,
        StatusCode::UNAUTHORIZED
    );
    assert_eq!(
        send(
            router.clone(),
            "GET",
            "/byok/app/v1/plugins",
            bearer("control-key"),
            json!({}),
        )
        .await
        .0,
        StatusCode::OK
    );

    store
        .set_app_api_settings(AppApiSettings {
            enabled: true,
            auth_required: true,
            auth_method: AppApiAuthMethod::ApiKey,
            api_key: "control-key".into(),
        })
        .await
        .unwrap();
    assert_eq!(
        send(
            router.clone(),
            "GET",
            "/byok/app/v1/plugins",
            bearer("control-key"),
            json!({}),
        )
        .await
        .0,
        StatusCode::UNAUTHORIZED
    );
    assert_eq!(
        send(
            router,
            "GET",
            "/byok/app/v1/plugins",
            api_key("control-key"),
            json!({}),
        )
        .await
        .0,
        StatusCode::OK
    );
}
