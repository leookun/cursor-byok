//! Publishes the desktop management routes for a local agent.
use axum::{
    body::Body,
    extract::State,
    http::{header, HeaderMap, Request, StatusCode, Uri},
    response::{IntoResponse, Response},
    routing::any,
    Json, Router,
};
use serde_json::json;
use tower::ServiceExt;

use crate::store::AppApiAuthMethod;

use super::ControlService;

#[derive(Clone)]
struct Gate {
    service: ControlService,
    api: Router,
}

pub fn attach(service: ControlService, api: Router) -> Router {
    let gate = Gate {
        service,
        api: api.clone(),
    };
    api.merge(
        Router::new()
            .route("/byok/app/v1", any(forward))
            .route("/byok/app/v1/{*path}", any(forward))
            .with_state(gate),
    )
}

async fn forward(State(gate): State<Gate>, request: Request<Body>) -> Response {
    if let Err(response) = authorize(&gate.service, request.headers()).await {
        return response;
    }
    let (mut parts, body) = request.into_parts();
    let Ok(uri) = rewrite(&parts.uri) else {
        return api_error(StatusCode::BAD_REQUEST, "invalid application API path");
    };
    parts.uri = uri;
    match gate
        .api
        .clone()
        .oneshot(Request::from_parts(parts, body))
        .await
    {
        Ok(response) => response,
        Err(error) => match error {},
    }
}

async fn authorize(service: &ControlService, headers: &HeaderMap) -> Result<(), Response> {
    let settings = service
        .app_api_settings()
        .await
        .map_err(|error| api_error(StatusCode::INTERNAL_SERVER_ERROR, error))?;
    if !settings.enabled {
        return Err(api_error(
            StatusCode::FORBIDDEN,
            "application API is disabled",
        ));
    }
    if !settings.auth_required {
        return Ok(());
    }
    let supplied = credential(settings.auth_method, headers);
    if supplied.is_empty() || !constant_time_eq(supplied.as_bytes(), settings.api_key.as_bytes()) {
        return Err(api_error(StatusCode::UNAUTHORIZED, "invalid API key"));
    }
    Ok(())
}

fn credential(method: AppApiAuthMethod, headers: &HeaderMap) -> &str {
    match method {
        AppApiAuthMethod::Bearer => headers
            .get(header::AUTHORIZATION)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "))
            .unwrap_or(""),
        AppApiAuthMethod::ApiKey => headers
            .get("x-api-key")
            .and_then(|value| value.to_str().ok())
            .unwrap_or(""),
    }
}

fn rewrite(uri: &Uri) -> Result<Uri, axum::http::uri::InvalidUri> {
    let rest = uri
        .path()
        .trim_start_matches("/byok/app/v1")
        .trim_start_matches('/');
    let mut path = if rest.is_empty() {
        "/__byok-api__/api".to_owned()
    } else {
        format!("/__byok-api__/api/{rest}")
    };
    if let Some(query) = uri.query() {
        path.push('?');
        path.push_str(query);
    }
    path.parse()
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

fn api_error(status: StatusCode, message: impl std::fmt::Display) -> Response {
    let code = if status == StatusCode::UNAUTHORIZED {
        "unauthenticated"
    } else if status == StatusCode::FORBIDDEN {
        "permission_denied"
    } else if status.is_client_error() {
        "invalid_argument"
    } else {
        "internal"
    };
    (
        status,
        Json(json!({"code": code, "message": message.to_string()})),
    )
        .into_response()
}
