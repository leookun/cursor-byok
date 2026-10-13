//! External OpenAI and Anthropic compatible API.
mod direct;
mod models;
mod output;
mod protocol;

use std::sync::Arc;

use axum::{
    extract::State,
    http::{header, HeaderMap, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
use serde_json::{json, Value};
use tokio_util::sync::CancellationToken;

use crate::{
    model::ModelInvocation, plugin::PluginRegistry, provider::Provider, store::Store, Error,
};

use self::protocol::Protocol;
pub use direct::NativeForwarder;

#[derive(Clone)]
struct ApiState {
    store: Store,
    plugins: PluginRegistry,
    provider: Arc<dyn Provider>,
    native: Option<NativeForwarder>,
}

pub fn router(
    store: Store,
    plugins: PluginRegistry,
    provider: Arc<dyn Provider>,
    native: Option<NativeForwarder>,
) -> Router {
    Router::new()
        .route("/byok/v1/models", get(list_models))
        .route("/byok/v1/chat/completions", post(chat))
        .route("/byok/v1/responses", post(responses))
        .route("/byok/v1/messages", post(messages))
        .with_state(ApiState {
            store,
            plugins,
            provider,
            native,
        })
}

fn api_error(status: StatusCode, message: impl std::fmt::Display) -> Response {
    (status, Json(json!({"error":{"message":message.to_string(),"type":if status.is_client_error() { "invalid_request_error" } else { "server_error" }}}))).into_response()
}

async fn authorized(
    state: &ApiState,
    headers: &HeaderMap,
) -> std::result::Result<(), Box<Response>> {
    let settings = state
        .store
        .external_api_settings()
        .await
        .map_err(|error| Box::new(api_error(StatusCode::INTERNAL_SERVER_ERROR, error)))?;
    if !settings.enabled {
        return Err(Box::new(api_error(
            StatusCode::FORBIDDEN,
            "external API is disabled",
        )));
    }
    let supplied = headers
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .or_else(|| {
            headers
                .get("x-api-key")
                .and_then(|value| value.to_str().ok())
        })
        .unwrap_or_default();
    if supplied.is_empty() || !constant_time_eq(supplied.as_bytes(), settings.api_key.as_bytes()) {
        return Err(Box::new(api_error(
            StatusCode::UNAUTHORIZED,
            "invalid API key",
        )));
    }
    Ok(())
}

fn constant_time_eq(left: &[u8], right: &[u8]) -> bool {
    if left.len() != right.len() {
        return false;
    }
    let difference = left
        .iter()
        .zip(right)
        .fold(0_u8, |difference, (left, right)| {
            difference | (left ^ right)
        });
    difference == 0
}

async fn list_models(State(state): State<ApiState>, headers: HeaderMap) -> Response {
    if let Err(error) = authorized(&state, &headers).await {
        return *error;
    }
    match models::list(&state.store, &state.plugins).await {
        Ok(models) => Json(models::response(&models)).into_response(),
        Err(error) => api_error(StatusCode::CONFLICT, error),
    }
}

async fn chat(
    State(state): State<ApiState>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response {
    generate(state, headers, body, Protocol::Chat).await
}

async fn responses(
    State(state): State<ApiState>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response {
    generate(state, headers, body, Protocol::Responses).await
}

async fn messages(
    State(state): State<ApiState>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response {
    generate(state, headers, body, Protocol::Messages).await
}

async fn generate(
    state: ApiState,
    headers: HeaderMap,
    body: Value,
    protocol: Protocol,
) -> Response {
    if let Err(error) = authorized(&state, &headers).await {
        return *error;
    }
    let Some(public_model_id) = body
        .get("model")
        .and_then(Value::as_str)
        .filter(|id| !id.is_empty())
    else {
        return api_error(StatusCode::BAD_REQUEST, "model is required");
    };
    let models = match models::list(&state.store, &state.plugins).await {
        Ok(models) => models,
        Err(error) => return api_error(StatusCode::CONFLICT, error),
    };
    let Some(model) = models
        .iter()
        .find(|model| model.public_id == public_model_id)
    else {
        return api_error(StatusCode::NOT_FOUND, "model not found");
    };
    if let Some(native) = &state.native {
        match state.store.model(&model.internal_id).await {
            Ok(Some(config)) if protocol.provider_type() == config.provider_type() => {
                return match native.forward(protocol, &headers, body, &config).await {
                    Ok(response) => response,
                    Err(error) => api_error(StatusCode::BAD_GATEWAY, error),
                };
            }
            Ok(_) => {}
            Err(error) => return api_error(StatusCode::INTERNAL_SERVER_ERROR, error),
        }
    }
    let parsed = match protocol::parse(protocol, &body) {
        Ok(parsed) => parsed,
        Err(error) => return api_error(StatusCode::BAD_REQUEST, error),
    };
    let id = format!("external-api:{}", uuid::Uuid::new_v4());
    let mut request = parsed.request;
    request.model.model_id = model.internal_id.clone();
    let invocation = ModelInvocation {
        call_id: id.clone(),
        run_id: id.clone(),
        conversation_id: id.clone(),
        prompt_cache_enabled: true,
        provider_call_index: 0,
        request,
    };
    let cancellation = CancellationToken::new();
    let stream = state.provider.stream(invocation, cancellation.clone());
    if parsed.stream {
        output::streamed(protocol, stream, cancellation, id, parsed.public_model_id)
    } else {
        match output::complete(protocol, stream, &id, &parsed.public_model_id).await {
            Ok(response) => response.into_response(),
            Err(Error::Protocol(message)) => api_error(StatusCode::BAD_REQUEST, message),
            Err(error) => api_error(StatusCode::BAD_GATEWAY, error),
        }
    }
}
