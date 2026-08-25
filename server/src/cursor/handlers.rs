use axum::{
    body::{to_bytes, Body, Bytes},
    extract::{DefaultBodyLimit, Extension, State},
    http::{header, HeaderMap, HeaderValue, Request, Response, StatusCode},
    routing::{get, post},
    Router,
};
use tower_http::decompression::RequestDecompressionLayer;

use crate::{
    cursor::{
        account, analytics, bidi_append, connect, model_catalog,
        observability::CursorTraceRecorder,
        proto::{agent::v1 as agent, aiserver::v1 as ai},
        proxy::{self, CursorProxy},
        rules, run_sse, tab,
    },
    cursor::{CursorParent, CursorSessionRegistry},
    Result,
};

pub fn router(registry: CursorSessionRegistry) -> Result<Router> {
    let proxy = CursorProxy::cursor(registry.store().clone())?;
    Ok(router_with_proxy(registry, proxy))
}

fn router_with_proxy(registry: CursorSessionRegistry, proxy: CursorProxy) -> Router {
    Router::new()
        .route("/__byok-api__/healthz", get(health))
        .route("/agent.v1.AgentService/RunSSE", post(run_sse_handler))
        .route(
            "/aiserver.v1.BidiService/BidiAppend",
            post(bidi_append_handler),
        )
        .route(
            "/aiserver.v1.AiService/AvailableModels",
            post(model_catalog::available_models),
        )
        .route(
            "/agent.v1.AgentService/GetUsableModels",
            post(model_catalog::usable_models),
        )
        .route(
            "/aiserver.v1.AiService/GetUsableModels",
            post(model_catalog::usable_models),
        )
        .route(
            "/aiserver.v1.AuthService/GetEmail",
            post(account::get_email),
        )
        .route("/aiserver.v1.DashboardService/GetMe", post(account::get_me))
        .route(
            "/aiserver.v1.DashboardService/GetTeams",
            post(account::get_teams),
        )
        .route(
            "/aiserver.v1.DashboardService/GetUserProfile",
            post(account::get_user_profile),
        )
        .route(
            "/aiserver.v1.DashboardService/GetCurrentPeriodUsage",
            post(account::current_period_usage),
        )
        .route(
            "/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants",
            post(account::usage_limit_status),
        )
        .route("/aiserver.v1.AiService/KnowledgeBaseAdd", post(rules::add))
        .route(
            "/aiserver.v1.AiService/KnowledgeBaseList",
            post(rules::list),
        )
        .route(
            "/aiserver.v1.AiService/KnowledgeBaseUpdate",
            post(rules::update),
        )
        .route(
            "/aiserver.v1.AiService/KnowledgeBaseRemove",
            post(rules::remove),
        )
        .route(
            analytics::BOOTSTRAP_STATSIG_PATH,
            post(analytics::bootstrap_statsig),
        )
        .route("/auth/full_stripe_profile", get(account::stripe_profile))
        .merge(tab::router())
        .route_layer(DefaultBodyLimit::disable())
        .route_layer(RequestDecompressionLayer::new())
        .fallback(proxy::forward)
        .method_not_allowed_fallback(proxy::forward)
        .layer(Extension(proxy))
        .with_state(registry)
}

async fn health() -> StatusCode {
    StatusCode::NO_CONTENT
}

async fn run_sse_handler(
    State(registry): State<CursorSessionRegistry>,
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let (parts, body) = buffered(request).await?;
    let request: agent::BidiRequestId = connect::decode_unary(&body)?;
    let route = registry.wait_route(&request.request_id).await;
    let trace = CursorTraceRecorder::resume(registry.store().clone(), &request.request_id).await;
    if let Some(trace) = &trace {
        trace
            .request(
                "run_sse_request",
                &body,
                serde_json::json!({"request_id": request.request_id}),
            )
            .await;
    }
    match route {
        super::sessions::CursorRoute::Local => {
            run_sse::stream(&registry, &request.request_id).await
        }
        super::sessions::CursorRoute::Upstream(generation) => {
            let response = proxy::forward(
                Extension(proxy),
                Request::from_parts(parts, Body::from(body)),
            )
            .await?;
            Ok(run_sse::upstream(registry, request.request_id, generation, response, trace).await)
        }
    }
}

async fn bidi_append_handler(
    State(registry): State<CursorSessionRegistry>,
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let (parts, body) = buffered(request).await?;
    let request: ai::BidiAppendRequest = connect::decode_unary(&body)?;
    let decoded = bidi_append::decode(&request)?;
    let first_model = decoded.model_id().map(str::to_owned);
    let conversation_id = decoded.conversation_id().map(str::to_owned);
    let trace_metadata = decoded.trace_metadata();
    let local = if let Some(model_id) = decoded.model_id() {
        if registry.store().provider_model(model_id).await?.is_some() {
            tracing::info!(
                request_id = decoded.request_id,
                model_id,
                "routing Cursor Run to BYOK provider"
            );
            true
        } else {
            tracing::info!(
                request_id = decoded.request_id,
                model_id,
                "routing Cursor Run to Cursor upstream"
            );
            false
        }
    } else if registry.local(&decoded.request_id).await.is_some() {
        true
    } else if registry.upstream(&decoded.request_id).await {
        false
    } else {
        return Err(crate::Error::Protocol(
            "first BidiAppend message must select a model".into(),
        ));
    };
    let trace = if first_model.is_some() {
        CursorTraceRecorder::begin(
            registry.store().clone(),
            &decoded.request_id,
            conversation_id.as_deref(),
            if local {
                "local_byok"
            } else {
                "cursor_official"
            },
            first_model.as_deref(),
        )
        .await
    } else {
        CursorTraceRecorder::resume(registry.store().clone(), &decoded.request_id).await
    };
    if let Some(trace) = &trace {
        trace
            .request("bidi_append_request", &body, trace_metadata)
            .await;
    }
    if !local {
        if first_model.is_some() {
            registry.mark_upstream(&decoded.request_id).await;
        }
        return proxy::forward(
            Extension(proxy),
            Request::from_parts(parts, Body::from(body)),
        )
        .await;
    }
    let parent = parent_headers(&parts.headers)?;
    bidi_append::append(&registry, decoded, parent).await?;
    let mut response = Response::new(axum::body::Body::empty());
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/proto"),
    );
    Ok(response)
}

async fn buffered(request: Request<Body>) -> Result<(axum::http::request::Parts, Bytes)> {
    let (parts, body) = request.into_parts();
    let body = to_bytes(body, usize::MAX)
        .await
        .map_err(|error| crate::Error::Protocol(format!("cannot read request body: {error}")))?;
    Ok((parts, body))
}

fn parent_headers(headers: &HeaderMap) -> Result<Option<CursorParent>> {
    let run_id = header_text(headers, "x-parent-request-id")?;
    let tool_call_id = header_text(headers, "x-parent-agent-tool-call-id")?;
    match (run_id, tool_call_id) {
        (None, None) => Ok(None),
        (Some(run_id), Some(tool_call_id)) => Ok(Some(CursorParent {
            run_id: run_id.into(),
            tool_call_id: tool_call_id.into(),
        })),
        _ => Err(crate::Error::Protocol(
            "Cursor subagent request must include both parent headers".into(),
        )),
    }
}

fn header_text<'a>(headers: &'a HeaderMap, name: &str) -> Result<Option<&'a str>> {
    headers
        .get(name)
        .map(|value| value.to_str())
        .transpose()
        .map_err(|error| crate::Error::Protocol(format!("invalid {name} header: {error}")))
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use axum::{body::to_bytes, routing::post};
    use prost::Message;
    use tower::ServiceExt;

    use crate::{
        cursor::prompting::{PromptAssets, PromptCompiler},
        model::ModelInvocation,
        provider::{Provider, ProviderStream},
        store::Store,
    };

    use super::*;

    struct NeverProvider;

    impl Provider for NeverProvider {
        fn stream(
            &self,
            _invocation: ModelInvocation,
            _cancellation: tokio_util::sync::CancellationToken,
        ) -> ProviderStream {
            panic!("official models must not enter the BYOK provider")
        }
    }

    #[test]
    fn subagent_parent_headers_are_an_atomic_pair() {
        let mut headers = HeaderMap::new();
        headers.insert(
            "x-parent-request-id",
            HeaderValue::from_static("parent-run"),
        );
        assert!(parent_headers(&headers).is_err());

        headers.insert(
            "x-parent-agent-tool-call-id",
            HeaderValue::from_static("parent-call"),
        );
        assert_eq!(
            parent_headers(&headers).unwrap(),
            Some(CursorParent {
                run_id: "parent-run".into(),
                tool_call_id: "parent-call".into(),
            })
        );
    }

    #[tokio::test]
    async fn official_model_run_sse_and_bidi_are_forwarded_together() {
        let upstream = Router::new()
            .route(
                "/agent.v1.AgentService/RunSSE",
                post(|| async { "official-stream" }),
            )
            .route(
                "/aiserver.v1.BidiService/BidiAppend",
                post(|| async { StatusCode::OK }),
            );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move { axum::serve(listener, upstream).await.unwrap() });

        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        store.set_detailed_logging(true).await.unwrap();
        let assets = PromptAssets::load(
            std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("prompt/cursor")
                .as_path(),
        )
        .unwrap();
        let registry = CursorSessionRegistry::new(
            store.clone(),
            Arc::new(NeverProvider),
            PromptCompiler::new(assets),
            Default::default(),
        );
        let proxy = CursorProxy::for_upstream(&format!("http://{address}")).unwrap();
        let app = router_with_proxy(registry, proxy);

        let run = tokio::spawn(
            app.clone().oneshot(
                Request::post("/agent.v1.AgentService/RunSSE")
                    .body(Body::from(
                        agent::BidiRequestId {
                            request_id: "official-request".into(),
                        }
                        .encode_to_vec(),
                    ))
                    .unwrap(),
            ),
        );
        let client_message = agent::AgentClientMessage {
            message: Some(agent::agent_client_message::Message::RunRequest(
                agent::AgentRunRequest {
                    requested_model: Some(agent::RequestedModel {
                        model_id: "grok-4.6".into(),
                        ..Default::default()
                    }),
                    ..Default::default()
                },
            )),
        };
        let bidi = ai::BidiAppendRequest {
            data: hex::encode(client_message.encode_to_vec()),
            request_id: Some(ai::BidiRequestId {
                request_id: "official-request".into(),
            }),
            append_seqno: 1,
            data_binary: Vec::new(),
        };
        let response = app
            .oneshot(
                Request::post("/aiserver.v1.BidiService/BidiAppend")
                    .body(Body::from(bidi.encode_to_vec()))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);

        let response = run.await.unwrap().unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(
            to_bytes(response.into_body(), usize::MAX).await.unwrap(),
            "official-stream"
        );
        let trace = tokio::time::timeout(std::time::Duration::from_secs(2), async {
            loop {
                if let Some(trace) = store.cursor_trace("official-request").await.unwrap() {
                    if trace.status == "completed" {
                        break trace;
                    }
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        assert_eq!(trace.route, "cursor_official");
        let artifacts = store
            .cursor_trace_artifacts("official-request")
            .await
            .unwrap();
        let kinds = artifacts
            .iter()
            .map(|artifact| artifact.artifact_type.as_str())
            .collect::<Vec<_>>();
        assert!(kinds.contains(&"bidi_append_request"));
        assert!(kinds.contains(&"run_sse_request"));
        assert!(kinds.contains(&"run_sse_chunk"));
        server.abort();
    }
}
