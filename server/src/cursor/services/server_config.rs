//! Serves the server-config snapshot Cursor reads at startup.
//!
//! Speech-to-text and agent runs share one HTTP/2 transport. That transport
//! does not use `http.proxy`, so while the local agent listener is up its
//! origin is advertised as `agentn_url` and HTTP/2 is left to the client
//! setting. Without a listener, HTTP/2 is forced off: an agent run that
//! reached Cursor with a BYOK model id fails as an unknown model, and HTTP/1.1
//! keeps those runs on the proxy.
use std::sync::Mutex;

use axum::{
    body::Body,
    http::{header, HeaderValue, Response, StatusCode},
};
use prost::Message;

use crate::Result;

const HTTP2_CONFIG_UNSPECIFIED: i32 = 0;
const HTTP2_CONFIG_FORCE_ALL_DISABLED: i32 = 1;

static AGENT_ORIGIN: Mutex<Option<String>> = Mutex::new(None);

/// Publishes the loopback origin Cursor should use for HTTP/2 agent calls.
pub fn set_agent_origin(origin: Option<String>) {
    *AGENT_ORIGIN.lock().expect("agent origin lock") = origin;
}

#[derive(Clone, PartialEq, Message)]
struct AgentUrlConfig {
    #[prost(string, tag = "2")]
    agentn_url: String,
}

#[derive(Clone, PartialEq, Message)]
struct ServerConfigResponse {
    #[prost(string, tag = "6")]
    config_version: String,
    #[prost(int32, tag = "7")]
    http2_config: i32,
    #[prost(message, optional, tag = "27")]
    agent_url_config: Option<AgentUrlConfig>,
    #[prost(bool, optional, tag = "28")]
    cli_sandbox_default_enabled: Option<bool>,
}

pub async fn get() -> Result<Response<Body>> {
    let payload = server_config().encode_to_vec();
    let mut response = Response::new(Body::from(payload));
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/proto"),
    );
    Ok(response)
}

fn server_config() -> ServerConfigResponse {
    let agentn_url = AGENT_ORIGIN.lock().expect("agent origin lock").clone();
    let http2_config = match agentn_url {
        Some(_) => HTTP2_CONFIG_UNSPECIFIED,
        None => HTTP2_CONFIG_FORCE_ALL_DISABLED,
    };
    ServerConfigResponse {
        config_version: "cursor_byok_local_agent_v1".into(),
        http2_config,
        agent_url_config: agentn_url.map(|agentn_url| AgentUrlConfig { agentn_url }),
        cli_sandbox_default_enabled: Some(true),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The origin is process-global, so one test walks both states.
    #[test]
    fn advertises_the_listener_and_falls_back_to_http1_without_one() {
        set_agent_origin(Some("https://127.0.0.1:47321".into()));
        let config = server_config();
        assert_eq!(config.http2_config, HTTP2_CONFIG_UNSPECIFIED);
        assert_eq!(
            config.agent_url_config.map(|config| config.agentn_url),
            Some("https://127.0.0.1:47321".into())
        );
        assert_eq!(config.cli_sandbox_default_enabled, Some(true));

        set_agent_origin(None);
        let config = server_config();
        assert_eq!(config.http2_config, HTTP2_CONFIG_FORCE_ALL_DISABLED);
        assert!(config.agent_url_config.is_none());
    }
}
