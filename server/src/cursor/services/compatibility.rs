//! Routes Cursor metadata calls according to the supplied authentication token.
use axum::{
    body::{to_bytes, Body},
    extract::{Extension, Request},
    http::{header, HeaderValue, Response},
};
use prost::Message;

use crate::{
    api::cursor::proxy::{self, CursorProxy},
    cursor::protocol::proto::agent::v1 as agent,
    local_app, Result,
};

#[derive(Clone, Copy, PartialEq, Message)]
struct EmptyResponse {}

pub async fn available_docs(
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    route(&proxy, request, EmptyResponse {}).await
}

pub async fn effective_user_plugins(
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    route(&proxy, request, EmptyResponse {}).await
}

pub async fn user_privacy_mode(
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    route(&proxy, request, EmptyResponse {}).await
}

// The built-in local BYOK identity has no Cursor organization. Real account
// tokens must still fetch their policies from Cursor rather than losing them.
pub async fn team_configuration(
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    route(&proxy, request, EmptyResponse {}).await
}

pub async fn update_conversation_metadata(
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    route(
        &proxy,
        request,
        agent::UpdateConversationMetadataResponse {},
    )
    .await
}

async fn route<M: Message>(
    proxy: &CursorProxy,
    request: Request<Body>,
    mock: M,
) -> Result<Response<Body>> {
    let local = local_app::request_uses_local_cursor_token(request.headers());
    if local {
        consume_body(request).await?;
        return Ok(proto(mock));
    }
    proxy::forward(Extension(proxy.clone()), request).await
}

async fn consume_body(request: Request<Body>) -> Result<()> {
    to_bytes(request.into_body(), usize::MAX)
        .await
        .map_err(|error| crate::Error::Protocol(format!("cannot read request body: {error}")))?;
    Ok(())
}

fn proto(message: impl Message) -> Response<Body> {
    let body = message.encode_to_vec();
    let length = body.len();
    let mut response = Response::new(Body::from(body));
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/proto"),
    );
    response.headers_mut().insert(
        header::CONTENT_LENGTH,
        HeaderValue::from_str(&length.to_string()).expect("body length is valid"),
    );
    response
}

#[cfg(test)]
mod tests {
    use std::sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    };

    use axum::body::{to_bytes, Bytes};
    use futures_util::stream;

    use super::*;

    #[tokio::test]
    async fn team_configuration_is_local_only_for_the_builtin_byok_identity() {
        let directory = tempfile::tempdir().unwrap();
        let store = crate::store::Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        let proxy = CursorProxy::cursor(crate::network::NetworkClients::new(store));
        for path in [
            "/aiserver.v1.DashboardService/GetTeamReposOrEmptyIfNotInTeam",
            "/aiserver.v1.DashboardService/GetTeamAdminSettingsOrEmptyIfNotInTeam",
        ] {
            let request = |authorization: &str| {
                Request::builder()
                    .method("POST")
                    .uri(path)
                    .header(header::AUTHORIZATION, authorization)
                    // An invalid upstream target makes unintended forwarding fail
                    // deterministically without contacting a real Cursor account.
                    .header(
                        crate::api::cursor::proxy::UPSTREAM_URL_HEADER,
                        "http://127.0.0.1/invalid",
                    )
                    .body(Body::empty())
                    .unwrap()
            };
            let response = team_configuration(
                Extension(proxy.clone()),
                request(&crate::local_app::local_cursor_authorization()),
            )
            .await
            .unwrap();
            assert_eq!(response.status(), axum::http::StatusCode::OK);
            assert!(to_bytes(response.into_body(), usize::MAX)
                .await
                .unwrap()
                .is_empty());
            let error = team_configuration(
                Extension(proxy.clone()),
                request("Bearer real-account-placeholder"),
            )
            .await
            .unwrap_err();
            assert!(error
                .to_string()
                .contains("upstream URL must target a Cursor HTTPS host"));
        }
    }

    #[tokio::test]
    async fn local_response_consumes_request_body_before_replying() {
        let polled = Arc::new(AtomicBool::new(false));
        let observed = polled.clone();
        let body = Body::from_stream(stream::once(async move {
            observed.store(true, Ordering::SeqCst);
            Ok::<_, std::convert::Infallible>(Bytes::from_static(b"request"))
        }));

        consume_body(Request::new(body)).await.unwrap();

        assert!(polled.load(Ordering::SeqCst));
    }

    #[tokio::test]
    async fn empty_unary_mock_is_bare_protobuf_without_connect_frame() {
        let response = proto(EmptyResponse {});
        assert_eq!(response.headers()[header::CONTENT_LENGTH], "0");
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        assert!(body.is_empty());
    }
}
