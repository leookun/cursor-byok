//! Configures the local application proxy.
use std::{net::SocketAddr, sync::Arc};

use hudsucker::{
    certificate_authority::RcgenAuthority,
    hyper::{header, http::StatusCode, Method, Request, Response, Uri},
    rustls::crypto::aws_lc_rs,
    Body, HttpContext, HttpHandler, Proxy, RequestOrResponse,
};
use tokio::{net::TcpListener, sync::oneshot, task::JoinHandle};

use parking_lot::RwLock;

use crate::{
    api::cursor::proxy::UPSTREAM_URL_HEADER, cursor::services::tab::is_tab_path, store::TabMode,
    Error, Result,
};

use super::ca::LoadedCa;

#[derive(Default)]
pub struct ProxyRuntime {
    url: Option<String>,
    port: Option<u16>,
    stop: Option<oneshot::Sender<()>>,
    task: Option<JoinHandle<()>>,
}

impl ProxyRuntime {
    pub fn running(&self) -> bool {
        self.task.as_ref().is_some_and(|task| !task.is_finished())
    }
    pub fn url(&self) -> Option<String> {
        self.running().then(|| self.url.clone()).flatten()
    }
    pub fn port(&self) -> Option<u16> {
        if self.running() {
            self.port
        } else {
            None
        }
    }

    pub async fn start(
        &mut self,
        backend: SocketAddr,
        ca: LoadedCa,
        requested_port: u16,
        tab_mode: Arc<RwLock<TabMode>>,
    ) -> Result<(String, u16)> {
        if let Some(url) = self.url() {
            return Ok((url, self.port.unwrap_or_default()));
        }
        let listener = bind_proxy_listener(requested_port).await?;
        let address = listener.local_addr()?;
        let (stop, done) = oneshot::channel();
        let authority = RcgenAuthority::new(ca.issuer, 1_000, aws_lc_rs::default_provider());
        let proxy = Proxy::builder()
            .with_listener(listener)
            .with_ca(authority)
            .with_rustls_connector(aws_lc_rs::default_provider())
            .with_http_handler(CursorRelay { backend, tab_mode })
            .with_graceful_shutdown(async move {
                let _ = done.await;
            })
            .build()
            .map_err(|error| Error::Store(format!("build Cursor proxy: {error}")))?;
        self.stop = Some(stop);
        self.url = Some(format!("http://{address}"));
        self.port = Some(address.port());
        self.task = Some(tokio::spawn(async move {
            if let Err(error) = proxy.start().await {
                tracing::error!(%error, "Cursor proxy stopped unexpectedly");
            }
        }));
        Ok((self.url.clone().unwrap(), address.port()))
    }

    pub async fn stop(&mut self) {
        if let Some(stop) = self.stop.take() {
            let _ = stop.send(());
        }
        if let Some(task) = self.task.take() {
            let _ = tokio::time::timeout(std::time::Duration::from_secs(5), task).await;
        }
        self.url = None;
        self.port = None;
    }
}

async fn bind_proxy_listener(requested_port: u16) -> Result<TcpListener> {
    let requested = SocketAddr::from(([127, 0, 0, 1], requested_port));
    match TcpListener::bind(requested).await {
        Ok(listener) => Ok(listener),
        Err(error) if requested_port != 0 => {
            tracing::warn!(%requested, %error, "configured proxy port unavailable; selecting a random port");
            Ok(TcpListener::bind("127.0.0.1:0").await?)
        }
        Err(error) => Err(error.into()),
    }
}

#[derive(Clone)]
struct CursorRelay {
    backend: SocketAddr,
    tab_mode: Arc<RwLock<TabMode>>,
}

impl HttpHandler for CursorRelay {
    async fn handle_request(
        &mut self,
        _ctx: &HttpContext,
        mut request: Request<Body>,
    ) -> RequestOrResponse {
        let original = request.uri().clone();
        // Never rewrite CONNECT tunnels: hudsucker inspects them only to decide
        // whether to MITM (should_intercept_connect), and rewriting the
        // authority to the local backend would disable TLS interception for
        // every Cursor-host connection. Only inner (post-MITM) requests are
        // routed locally.
        if request.method() == Method::CONNECT {
            return request.into();
        }
        // External-only endpoints that the official upstream deterministically
        // rejects (404/400) for accounts without the corresponding cloud
        // features (e.g. legacy `/agent/v1/run` probe, agent-store /
        // background-composer mints, tab file sync). Forwarding them upstream
        // only re-produces the rejection over the network, which on
        // proxied-only egress networks used to hang for ~75s. Answer them
        // locally with the same status Cursor already tolerates so the request
        // never leaves the machine: Cursor keeps its existing fallbacks
        // (gRPC `RunSSE` after the `/agent/v1/run` 404, local agent stores
        // after `MintAgentStoreToken` 400).
        if let Some(status) = local_stub_status(original.path()) {
            let mut response = Response::new(Body::empty());
            *response.status_mut() = status;
            response.headers_mut().insert(
                header::CONTENT_LENGTH,
                header::HeaderValue::from_static("0"),
            );
            if original.path().starts_with("/aiserver.v1.")
                || original.path().starts_with("/agent.v1.")
            {
                response.headers_mut().insert(
                    header::CONTENT_TYPE,
                    header::HeaderValue::from_static("application/grpc"),
                );
            }
            tracing::info!(
                path = %original.path(),
                status = status.as_u16(),
                "local stub: external-only path answered locally, upstream blocked"
            );
            return RequestOrResponse::Response(response);
        }
        // Route every Cursor-host request through the local backend instead of
        // letting hudsucker dial the official upstream directly: direct dials
        // hang (~75s) on networks that only allow proxied egress. Paths outside
        // the explicit route table fall through to the router fallback
        // (proxy::forward), which uses the configured outbound proxy. Tab paths
        // in explicit Direct mode keep their original upstream pass-through.
        let route_locally = is_cursor_host(original.host().unwrap_or_default())
            && (should_route_locally(original.path(), *self.tab_mode.read())
                || !is_tab_path(original.path()));
        if route_locally {
            if let Ok(value) = original.to_string().parse() {
                request.headers_mut().insert(UPSTREAM_URL_HEADER, value);
            }
            let path = original
                .path_and_query()
                .map(|value| value.as_str())
                .unwrap_or("/");
            if let Ok(uri) = format!("http://{}{}", self.backend, path).parse::<Uri>() {
                *request.uri_mut() = uri;
            }
        }
        request.into()
    }

    async fn should_intercept_connect(
        &mut self,
        _ctx: &HttpContext,
        request: &Request<Body>,
    ) -> bool {
        request
            .uri()
            .authority()
            .is_some_and(|authority| is_cursor_host(authority.host()))
    }

    async fn should_intercept_tls(
        &mut self,
        _ctx: &HttpContext,
        hello: hudsucker::rustls::server::ClientHello<'_>,
    ) -> bool {
        hello.server_name().is_some_and(is_cursor_host)
    }
}

pub fn is_cursor_host(host: &str) -> bool {
    let host = host.trim_end_matches('.').to_ascii_lowercase();
    matches!(host.as_str(), "api2.cursor.sh" | "api3.cursor.sh") || host.ends_with(".cursor.sh")
}

/// Paths the official upstream deterministically rejects (404/400) for
/// accounts without the corresponding cloud features. Kept in sync with the
/// observed upstream answers; answered locally by `CursorRelay` so these
/// requests never leave the machine.
fn local_stub_status(path: &str) -> Option<StatusCode> {
    let stub = match path {
        "/agent/v1/run" => StatusCode::NOT_FOUND,
        "/aiserver.v1.BackgroundComposerService/MintAgentStoreToken"
        | "/aiserver.v1.BackgroundComposerService/ListPrivateWorkers"
        | "/aiserver.v1.BackgroundComposerService/ListEnvironments"
        | "/aiserver.v1.BackgroundComposerService/ListBackgroundComposers" => {
            StatusCode::BAD_REQUEST
        }
        "/aiserver.v1.FileSyncService/FSSyncFile" => StatusCode::NOT_FOUND,
        "/ws-reachability-probe" => StatusCode::NOT_FOUND,
        _ => return None,
    };
    Some(stub)
}

fn is_local_path(path: &str) -> bool {
    matches!(
        path,
        "/agent.v1.AgentService/RunSSE"
            | "/aiserver.v1.BidiService/BidiAppend"
            | "/aiserver.v1.AiService/AvailableDocs"
            | "/aiserver.v1.DashboardService/GetEffectiveUserPlugins"
            | "/aiserver.v1.DashboardService/GetUserPrivacyMode"
            | "/agent.v1.AgentService/UpdateConversationMetadata"
            | "/aiserver.v1.AiService/GetServerConfig"
            | "/aiserver.v1.ServerConfigService/GetServerConfig"
            | "/aiserver.v1.AiService/AvailableModels"
            | "/agent.v1.AgentService/GetUsableModels"
            | "/aiserver.v1.AiService/GetUsableModels"
            | "/agent.v1.AgentService/GetDefaultModelForCli"
            | "/aiserver.v1.AiService/GetDefaultModelForCli"
            | "/aiserver.v1.AiService/GetDefaultModel"
            | "/aiserver.v1.AiService/GetDefaultModelNudgeData"
            | "/aiserver.v1.AuthService/GetEmail"
            | "/aiserver.v1.AuthService/GetUserMeta"
            | "/aiserver.v1.DashboardService/GetMe"
            | "/aiserver.v1.DashboardService/GetTeams"
            | "/aiserver.v1.DashboardService/GetUserProfile"
            | "/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
            | "/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants"
            | "/aiserver.v1.AiService/KnowledgeBaseAdd"
            | "/aiserver.v1.AiService/KnowledgeBaseList"
            | "/aiserver.v1.AiService/KnowledgeBaseUpdate"
            | "/aiserver.v1.AiService/KnowledgeBaseRemove"
            | "/aiserver.v1.AiService/WriteGitCommitMessage"
            | "/aiserver.v1.NetworkService/IsConnected"
            | "/aiserver.v1.AnalyticsService/BootstrapStatsig"
            | "/auth/full_stripe_profile"
            | "/auth/stripe_profile"
    )
}

fn should_route_locally(path: &str, tab_mode: TabMode) -> bool {
    is_local_path(path) || (is_tab_path(path) && tab_mode != TabMode::Direct)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cursor_cli_transport_and_model_metadata_routes_stay_local() {
        for path in [
            "/aiserver.v1.AiService/GetServerConfig",
            "/aiserver.v1.ServerConfigService/GetServerConfig",
            "/agent.v1.AgentService/GetDefaultModelForCli",
            "/aiserver.v1.AiService/GetDefaultModelForCli",
            "/aiserver.v1.AiService/GetDefaultModel",
            "/aiserver.v1.AiService/GetDefaultModelNudgeData",
            "/aiserver.v1.AiService/AvailableDocs",
            "/aiserver.v1.DashboardService/GetEffectiveUserPlugins",
            "/aiserver.v1.DashboardService/GetUserPrivacyMode",
            "/aiserver.v1.AuthService/GetUserMeta",
            "/agent.v1.AgentService/UpdateConversationMetadata",
            "/auth/full_stripe_profile",
            "/auth/stripe_profile",
        ] {
            assert!(is_local_path(path), "{path} must not reach Cursor upstream");
        }
    }
}
