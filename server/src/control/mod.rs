mod accounts;
mod ads;
mod calls;
mod harness;
mod models;
mod oauth;
mod overview;
mod service;
mod settings;
mod usage;

use axum::{
    body::{to_bytes, Body},
    extract::{DefaultBodyLimit, State},
    http::{header, header::CONTENT_TYPE, HeaderValue, Method, Request, Response, StatusCode},
    routing::{any, get, post, put},
    Router,
};
use tower_http::{
    cors::{AllowOrigin, CorsLayer},
    services::ServeDir,
};
use url::{Host, Url};

pub use service::{
    CallDetail, CallSummary, ControlService, DiscoveredModels, LegacyModelImportPreview,
    LegacyModelImportResult, ModelConnectivityResult, ModelDiscoveryInput, ObservabilitySettings,
};

pub fn web_router(service: ControlService, assets: impl AsRef<std::path::Path>) -> Router {
    Router::new()
        .nest_service(
            "/__byok-api__",
            ServeDir::new(assets).append_index_html_on_directories(true),
        )
        .merge(api_router(service))
}

pub fn proxy_web_router(service: ControlService, target: Url) -> Router {
    frontend_proxy_router(target).merge(api_router(service))
}

fn frontend_proxy_router(target: Url) -> Router {
    let state = FrontendProxy {
        client: reqwest::Client::new(),
        target: target.as_str().trim_end_matches('/').to_string(),
    };
    Router::new()
        .route("/__byok-api__/", any(proxy_frontend))
        .route("/__byok-api__/{*path}", any(proxy_frontend))
        .with_state(state)
}

#[derive(Clone)]
struct FrontendProxy {
    client: reqwest::Client,
    target: String,
}

async fn proxy_frontend(
    State(proxy): State<FrontendProxy>,
    request: Request<Body>,
) -> Response<Body> {
    let (parts, body) = request.into_parts();
    let path = parts
        .uri
        .path_and_query()
        .map(|value| value.as_str())
        .unwrap_or("/__byok-api__/");
    let mut upstream = proxy
        .client
        .request(parts.method, format!("{}{path}", proxy.target));
    for (name, value) in &parts.headers {
        if name != header::HOST && name != header::CONNECTION {
            upstream = upstream.header(name, value);
        }
    }
    let body = match to_bytes(body, 64 * 1024 * 1024).await {
        Ok(body) => body,
        Err(error) => return proxy_error(error),
    };
    let upstream = match upstream.body(body).send().await {
        Ok(response) => response,
        Err(error) => return proxy_error(error),
    };
    let status = upstream.status();
    let headers = upstream.headers().clone();
    let body = match upstream.bytes().await {
        Ok(body) => body,
        Err(error) => return proxy_error(error),
    };
    let mut response = Response::new(Body::from(body));
    *response.status_mut() = status;
    for (name, value) in &headers {
        if name != header::CONNECTION
            && name != header::TRANSFER_ENCODING
            && name != header::CONTENT_LENGTH
        {
            response.headers_mut().insert(name, value.clone());
        }
    }
    response
}

fn proxy_error(error: impl std::fmt::Display) -> Response<Body> {
    tracing::warn!(%error, "frontend development proxy failed");
    Response::builder()
        .status(StatusCode::BAD_GATEWAY)
        .body(Body::from("frontend development server is unavailable"))
        .expect("static proxy error response")
}

pub fn api_router(service: ControlService) -> Router {
    Router::new()
        .route("/__byok-api__/api/ads", get(ads::get))
        .route(
            "/__byok-api__/api/ads/{ad_id}/dismissals",
            post(ads::dismiss),
        )
        .route(
            "/__byok-api__/api/auth/grok/device-code",
            post(oauth::grok_device_code),
        )
        .route(
            "/__byok-api__/api/auth/accounts/import",
            post(accounts::import_accounts).layer(DefaultBodyLimit::disable()),
        )
        .route("/__byok-api__/api/auth/grok/poll", post(oauth::grok_token_poll))
        .route("/__byok-api__/api/auth/grok/usage", post(usage::grok_usage))
        .route(
            "/__byok-api__/api/auth/grok/accounts",
            get(accounts::list_grok).post(accounts::upsert_grok),
        )
        .route(
            "/__byok-api__/api/auth/grok/accounts/{account_id}",
            put(accounts::activate_grok).delete(accounts::delete_grok),
        )
        .route(
            "/__byok-api__/api/auth/codex/device-code",
            post(oauth::codex_device_code),
        )
        .route("/__byok-api__/api/auth/codex/poll", post(oauth::codex_token_poll))
        .route("/__byok-api__/api/auth/codex/usage", post(usage::codex_usage))
        .route(
            "/__byok-api__/api/auth/codex/accounts",
            get(accounts::list_codex).post(accounts::upsert_codex),
        )
        .route(
            "/__byok-api__/api/auth/codex/accounts/{account_id}",
            put(accounts::activate_codex).delete(accounts::delete_codex),
        )
        .route(
            "/__byok-api__/api/models",
            get(models::list).post(models::create),
        )
        .route("/__byok-api__/api/models/discover", post(models::discover))
        .route(
            "/__byok-api__/api/models/import-v0049",
            get(models::preview_v0049).post(models::import_v0049),
        )
        .route("/__byok-api__/api/models/order", put(models::reorder))
        .route("/__byok-api__/api/overview", get(overview::get))
        .route(
            "/__byok-api__/api/models/{model_hash}",
            put(models::update).delete(models::remove),
        )
        .route(
            "/__byok-api__/api/models/{model_hash}/test/{test_id}",
            post(models::test).delete(models::cancel),
        )
        .route("/__byok-api__/api/llm-calls", get(calls::list))
        .route("/__byok-api__/api/llm-calls/{call_id}", get(calls::detail))
        .route(
            "/__byok-api__/api/settings/observability",
            get(settings::get).put(settings::update),
        )
        .route(
            "/__byok-api__/api/settings/ports",
            get(settings::get_ports).put(settings::update_ports),
        )
        .route(
            "/__byok-api__/api/settings/storage/statistics",
            get(settings::get_storage).delete(settings::clear_storage),
        )
        .route(
            "/__byok-api__/api/settings/proxy",
            get(settings::get_proxy).put(settings::update_proxy),
        )
        .route(
            "/__byok-api__/api/settings/tab",
            get(settings::get_tab).put(settings::update_tab),
        )
        .route(
            "/__byok-api__/api/settings/desktop",
            get(settings::get_desktop).put(settings::update_desktop),
        )
        .route(
            "/__byok-api__/api/harness/cursor/status",
            get(harness::status),
        )
        .route(
            "/__byok-api__/api/harness/cursor/ca/initialize",
            post(harness::initialize_ca),
        )
        .route(
            "/__byok-api__/api/harness/cursor/enabled",
            put(harness::set_enabled),
        )
        .with_state(service)
        .layer(desktop_cors())
}

fn desktop_cors() -> CorsLayer {
    CorsLayer::new()
        .allow_origin(AllowOrigin::predicate(|origin, _| local_origin(origin)))
        .allow_methods([Method::GET, Method::POST, Method::PUT, Method::DELETE])
        .allow_headers([
            CONTENT_TYPE,
            header::ACCEPT_LANGUAGE,
            header::HeaderName::from_static("disable-ad-ids"),
        ])
}

fn local_origin(origin: &HeaderValue) -> bool {
    let Ok(origin) = origin.to_str() else {
        return false;
    };
    if origin.eq_ignore_ascii_case("tauri://localhost") {
        return true;
    }
    let Ok(origin) = Url::parse(origin) else {
        return false;
    };
    if !matches!(origin.scheme(), "http" | "https")
        || !origin.username().is_empty()
        || origin.password().is_some()
        || origin.path() != "/"
        || origin.query().is_some()
        || origin.fragment().is_some()
    {
        return false;
    }
    match origin.host() {
        Some(Host::Domain(host)) => {
            host.eq_ignore_ascii_case("localhost") || host.eq_ignore_ascii_case("tauri.localhost")
        }
        Some(Host::Ipv4(address)) => {
            address.is_loopback() || address.is_private() || address.is_link_local()
        }
        Some(Host::Ipv6(address)) => {
            address.is_loopback() || address.is_unique_local() || address.is_unicast_link_local()
        }
        None => false,
    }
}

#[cfg(test)]
mod tests {
    use axum::{
        body::Body,
        http::{header, HeaderValue, Request},
    };
    use tower::ServiceExt;

    use super::*;

    #[tokio::test]
    async fn control_routes_only_exist_below_the_reserved_namespace() {
        let directory = tempfile::tempdir().unwrap();
        let store = crate::store::Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("control.db").display()
        ))
        .await
        .unwrap();
        let provider = std::sync::Arc::new(crate::provider::ProviderRouter::new(
            store.clone(),
            std::time::Duration::from_secs(300),
        ));
        let router = api_router(ControlService::new(store, provider).unwrap());

        let response = router
            .clone()
            .oneshot(
                Request::builder()
                    .uri("/__byok-api__/api/models")
                    .header(header::ORIGIN, "tauri://localhost")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), axum::http::StatusCode::OK);
        assert_eq!(
            response.headers().get(header::ACCESS_CONTROL_ALLOW_ORIGIN),
            Some(&HeaderValue::from_static("tauri://localhost"))
        );

        let response = router
            .clone()
            .oneshot(
                Request::builder()
                    .uri("/__byok-api__/api/overview")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), axum::http::StatusCode::OK);

        let response = router
            .oneshot(
                Request::builder()
                    .uri("/api/models")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), axum::http::StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn development_frontend_proxy_preserves_the_reserved_path_and_query() {
        let upstream = Router::new().route(
            "/__byok-api__/{*path}",
            get(|request: Request<Body>| async move {
                request.uri().path_and_query().unwrap().as_str().to_string()
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let task = tokio::spawn(async move { axum::serve(listener, upstream).await.unwrap() });
        let router = frontend_proxy_router(format!("http://{address}").parse().unwrap());

        let response = router
            .oneshot(
                Request::builder()
                    .uri("/__byok-api__/src/index.tsx?direct=1")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        assert_eq!(body, "/__byok-api__/src/index.tsx?direct=1");
        task.abort();
    }

    #[test]
    fn cors_only_allows_tauri_loopback_and_private_network_origins() {
        for origin in [
            "tauri://localhost",
            "http://tauri.localhost",
            "http://localhost:1420",
            "http://127.0.0.1:1420",
            "https://192.168.1.20:8443",
            "http://[::1]:1420",
            "http://[fd00::20]:1420",
        ] {
            assert!(local_origin(&origin.parse().unwrap()), "{origin}");
        }
        for origin in [
            "https://example.com",
            "https://8.8.8.8",
            "https://localhost.example.com",
            "null",
        ] {
            assert!(!local_origin(&origin.parse().unwrap()), "{origin}");
        }
    }
}
