use axum::{extract::State, Json};
use serde::{Deserialize, Serialize};

use crate::{Error, Result};
use super::ControlService;

pub const DEFAULT_XAI_CLIENT_ID: &str = "b1a00492-073a-47ea-816f-4c329264a828";
pub const XAI_DEVICE_CODE_URL: &str = "https://auth.x.ai/oauth2/device/code";
pub const XAI_TOKEN_URL: &str = "https://auth.x.ai/oauth2/token";
pub const XAI_DEFAULT_SCOPE: &str = "openid profile email offline_access grok-cli:access api:access";

#[derive(Debug, Deserialize, Serialize)]
pub struct GrokDeviceCodeInput {
    pub client_id: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct GrokDeviceCodeResponse {
    pub device_code: String,
    pub user_code: String,
    pub verification_uri: String,
    pub verification_uri_complete: Option<String>,
    pub expires_in: u64,
    pub interval: u64,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct GrokTokenPollInput {
    pub device_code: String,
    pub client_id: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct GrokTokenPollResponse {
    pub status: String, // "success", "pending", "slow_down", "expired", "error"
    pub access_token: Option<String>,
    pub refresh_token: Option<String>,
    pub token_type: Option<String>,
    pub expires_in: Option<u64>,
    pub error_message: Option<String>,
}

pub async fn grok_device_code(
    State(service): State<ControlService>,
    Json(input): Json<GrokDeviceCodeInput>,
) -> Result<Json<GrokDeviceCodeResponse>> {
    let client = crate::network::client(service.store()).await?;
    let client_id = input
        .client_id
        .filter(|s| !s.trim().is_empty())
        .unwrap_or_else(|| DEFAULT_XAI_CLIENT_ID.to_string());

    let params = [
        ("client_id", client_id.as_str()),
        ("scope", XAI_DEFAULT_SCOPE),
    ];

    let response = client
        .post(XAI_DEVICE_CODE_URL)
        .form(&params)
        .send()
        .await?;

    if !response.status().is_success() {
        let status = response.status();
        let body = response.text().await.unwrap_or_default();
        return Err(Error::Provider(format!(
            "Failed to request xAI device code (HTTP {status}): {body}"
        )));
    }

    let parsed: GrokDeviceCodeResponse = response.json().await?;
    Ok(Json(parsed))
}

pub async fn grok_token_poll(
    State(service): State<ControlService>,
    Json(input): Json<GrokTokenPollInput>,
) -> Result<Json<GrokTokenPollResponse>> {
    let client = crate::network::client(service.store()).await?;
    let client_id = input
        .client_id
        .filter(|s| !s.trim().is_empty())
        .unwrap_or_else(|| DEFAULT_XAI_CLIENT_ID.to_string());

    let params = [
        ("grant_type", "urn:ietf:params:oauth:grant-type:device_code"),
        ("client_id", client_id.as_str()),
        ("device_code", input.device_code.as_str()),
    ];

    let response = client
        .post(XAI_TOKEN_URL)
        .form(&params)
        .send()
        .await?;

    let status = response.status();
    let body: serde_json::Value = response.json().await.unwrap_or_default();

    if status.is_success() {
        let access_token = body.get("access_token").and_then(|v| v.as_str()).map(ToString::to_string);
        let refresh_token = body.get("refresh_token").and_then(|v| v.as_str()).map(ToString::to_string);
        let token_type = body.get("token_type").and_then(|v| v.as_str()).map(ToString::to_string);
        let expires_in = body.get("expires_in").and_then(|v| v.as_u64());

        return Ok(Json(GrokTokenPollResponse {
            status: "success".into(),
            access_token,
            refresh_token,
            token_type,
            expires_in,
            error_message: None,
        }));
    }

    let error_code = body.get("error").and_then(|v| v.as_str()).unwrap_or("");
    let error_desc = body
        .get("error_description")
        .and_then(|v| v.as_str())
        .map(ToString::to_string);

    match error_code {
        "authorization_pending" => Ok(Json(GrokTokenPollResponse {
            status: "pending".into(),
            access_token: None,
            refresh_token: None,
            token_type: None,
            expires_in: None,
            error_message: None,
        })),
        "slow_down" => Ok(Json(GrokTokenPollResponse {
            status: "slow_down".into(),
            access_token: None,
            refresh_token: None,
            token_type: None,
            expires_in: None,
            error_message: None,
        })),
        "expired_token" => Ok(Json(GrokTokenPollResponse {
            status: "expired".into(),
            access_token: None,
            refresh_token: None,
            token_type: None,
            expires_in: None,
            error_message: error_desc.or_else(|| Some("Device authorization code expired".into())),
        })),
        "access_denied" => Ok(Json(GrokTokenPollResponse {
            status: "access_denied".into(),
            access_token: None,
            refresh_token: None,
            token_type: None,
            expires_in: None,
            error_message: error_desc.or_else(|| Some("User denied authorization".into())),
        })),
        _ => Ok(Json(GrokTokenPollResponse {
            status: "error".into(),
            access_token: None,
            refresh_token: None,
            token_type: None,
            expires_in: None,
            error_message: error_desc.or_else(|| Some(format!("OAuth error: {error_code}"))),
        })),
    }
}

// OpenAI / ChatGPT Codex OAuth Device Code Flow
pub const DEFAULT_CODEX_CLIENT_ID: &str = "app_EMoamEEZ73f0CkXaXp7hrann";
pub const CODEX_DEVICE_CODE_URL: &str = "https://auth.openai.com/api/accounts/deviceauth/usercode";
pub const CODEX_TOKEN_URL: &str = "https://auth.openai.com/api/accounts/deviceauth/token";
pub const CODEX_OAUTH_TOKEN_URL: &str = "https://auth.openai.com/oauth/token";
pub const CODEX_DEVICE_CALLBACK_URL: &str = "https://auth.openai.com/deviceauth/callback";
pub const CODEX_VERIFICATION_URI: &str = "https://auth.openai.com/codex/device";

#[derive(Debug, Deserialize, Serialize)]
pub struct CodexDeviceCodeInput {
    pub client_id: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct CodexDeviceCodeResponse {
    pub device_code: String,
    pub user_code: String,
    pub verification_uri: String,
    pub verification_uri_complete: Option<String>,
    pub expires_in: u64,
    pub interval: u64,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct CodexTokenPollInput {
    pub device_code: String,
    pub user_code: Option<String>,
    pub client_id: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct CodexTokenPollResponse {
    pub status: String, // "success", "pending", "slow_down", "expired", "error"
    pub access_token: Option<String>,
    pub refresh_token: Option<String>,
    pub token_type: Option<String>,
    pub expires_in: Option<u64>,
    pub error_message: Option<String>,
}

pub async fn codex_device_code(
    State(service): State<ControlService>,
    Json(input): Json<CodexDeviceCodeInput>,
) -> Result<Json<CodexDeviceCodeResponse>> {
    let client = crate::network::client(service.store()).await?;
    let client_id = input
        .client_id
        .filter(|s| !s.trim().is_empty())
        .unwrap_or_else(|| DEFAULT_CODEX_CLIENT_ID.to_string());

    let payload = serde_json::json!({
        "client_id": client_id,
    });

    let response = client
        .post(CODEX_DEVICE_CODE_URL)
        .header(reqwest::header::ACCEPT, "application/json")
        .header(reqwest::header::CONTENT_TYPE, "application/json")
        .json(&payload)
        .send()
        .await?;

    if !response.status().is_success() {
        let status = response.status();
        let body = response.text().await.unwrap_or_default();
        return Err(Error::Provider(format!(
            "Failed to request OpenAI Codex device code (HTTP {status}): {body}"
        )));
    }

    let body: serde_json::Value = response.json().await.unwrap_or_default();
    let user_code = json_string(&body, "user_code")
        .or_else(|| json_string(&body, "usercode"))
        .unwrap_or_default()
        .to_string();
    let device_code = json_string(&body, "device_auth_id")
        .or_else(|| json_string(&body, "device_code"))
        .unwrap_or_default()
        .to_string();
    let expires_in = json_u64(&body, "expires_in").unwrap_or(900);
    let interval = json_u64(&body, "interval").unwrap_or(5);

    Ok(Json(CodexDeviceCodeResponse {
        device_code,
        user_code,
        verification_uri: CODEX_VERIFICATION_URI.into(),
        verification_uri_complete: Some(CODEX_VERIFICATION_URI.into()),
        expires_in,
        interval,
    }))
}

pub async fn codex_token_poll(
    State(service): State<ControlService>,
    Json(input): Json<CodexTokenPollInput>,
) -> Result<Json<CodexTokenPollResponse>> {
    let client = crate::network::client(service.store()).await?;
    let client_id = input
        .client_id
        .filter(|s| !s.trim().is_empty())
        .unwrap_or_else(|| DEFAULT_CODEX_CLIENT_ID.to_string());

    let payload = serde_json::json!({
        "device_auth_id": input.device_code,
        "user_code": input.user_code.unwrap_or_default(),
    });

    let response = client
        .post(CODEX_TOKEN_URL)
        .header(reqwest::header::ACCEPT, "application/json")
        .header(reqwest::header::CONTENT_TYPE, "application/json")
        .json(&payload)
        .send()
        .await?;

    let status = response.status();
    let body: serde_json::Value = response.json().await.unwrap_or_default();

    match classify_codex_device_poll(status, &body) {
        CodexPollKind::Tokens {
            access_token,
            refresh_token,
            token_type,
            expires_in,
        } => Ok(Json(codex_success(
            access_token,
            refresh_token,
            token_type,
            expires_in,
        ))),
        CodexPollKind::AuthorizationCode {
            authorization_code,
            code_verifier,
        } => Ok(Json(
            exchange_codex_authorization_code(
                &client,
                &client_id,
                &authorization_code,
                &code_verifier,
            )
            .await?,
        )),
        CodexPollKind::Pending => Ok(Json(codex_status("pending", None))),
        CodexPollKind::SlowDown => Ok(Json(codex_status("slow_down", None))),
        CodexPollKind::Expired { message } => Ok(Json(codex_status(
            "expired",
            Some(message.unwrap_or_else(|| "Device authorization code expired".into())),
        ))),
        CodexPollKind::Denied { message } => Ok(Json(codex_status(
            "access_denied",
            Some(message.unwrap_or_else(|| "User denied authorization".into())),
        ))),
        CodexPollKind::Error { message } => Ok(Json(codex_status("error", Some(message)))),
    }
}

fn json_string<'a>(body: &'a serde_json::Value, key: &str) -> Option<&'a str> {
    body.get(key)
        .and_then(|value| value.as_str())
        .map(str::trim)
        .filter(|value| !value.is_empty())
}

fn json_u64(body: &serde_json::Value, key: &str) -> Option<u64> {
    body.get(key).and_then(|value| match value {
        serde_json::Value::Number(number) => number.as_u64(),
        serde_json::Value::String(text) => text.trim().parse().ok(),
        _ => None,
    })
}

fn nested_error_code(body: &serde_json::Value) -> &str {
    body.get("error")
        .and_then(|value| {
            if let Some(code) = value.as_str() {
                Some(code)
            } else {
                value
                    .get("code")
                    .or_else(|| value.get("type"))
                    .and_then(|code| code.as_str())
            }
        })
        .or_else(|| body.get("status").and_then(|value| value.as_str()))
        .or_else(|| body.get("state").and_then(|value| value.as_str()))
        .unwrap_or("")
}

fn nested_error_message(body: &serde_json::Value) -> Option<String> {
    json_string(body, "error_description")
        .or_else(|| json_string(body, "message"))
        .or_else(|| {
            body.get("error")
                .and_then(|error| json_string(error, "message"))
        })
        .map(ToString::to_string)
}

fn is_codex_pending_code(code: &str) -> bool {
    matches!(
        code,
        "authorization_pending"
            | "pending"
            | "waiting"
            | "in_progress"
            | "device_authorization_pending"
    )
}

fn is_codex_pending_message(message: &str) -> bool {
    let lower = message.to_ascii_lowercase();
    lower.contains("authorization is pending")
        || lower.contains("authorization_pending")
        || lower.contains("device authorization is pending")
}

enum CodexPollKind {
    Tokens {
        access_token: String,
        refresh_token: Option<String>,
        token_type: Option<String>,
        expires_in: Option<u64>,
    },
    AuthorizationCode {
        authorization_code: String,
        code_verifier: String,
    },
    Pending,
    SlowDown,
    Expired {
        message: Option<String>,
    },
    Denied {
        message: Option<String>,
    },
    Error {
        message: String,
    },
}

fn classify_codex_device_poll(
    status: reqwest::StatusCode,
    body: &serde_json::Value,
) -> CodexPollKind {
    // Codex device auth uses HTTP 403/404 as "still waiting", not a terminal failure.
    if status == reqwest::StatusCode::FORBIDDEN || status == reqwest::StatusCode::NOT_FOUND {
        return CodexPollKind::Pending;
    }

    let error_code = nested_error_code(body);
    let error_message = nested_error_message(body);

    if is_codex_pending_code(error_code)
        || error_message
            .as_deref()
            .is_some_and(is_codex_pending_message)
    {
        return CodexPollKind::Pending;
    }

    if status.is_success() {
        if let Some(access_token) = json_string(body, "access_token") {
            return CodexPollKind::Tokens {
                access_token: access_token.to_string(),
                refresh_token: json_string(body, "refresh_token").map(ToString::to_string),
                token_type: json_string(body, "token_type").map(ToString::to_string),
                expires_in: json_u64(body, "expires_in"),
            };
        }
        if let (Some(authorization_code), Some(code_verifier)) = (
            json_string(body, "authorization_code"),
            json_string(body, "code_verifier"),
        ) {
            return CodexPollKind::AuthorizationCode {
                authorization_code: authorization_code.to_string(),
                code_verifier: code_verifier.to_string(),
            };
        }
    }

    match error_code {
        "slow_down" => CodexPollKind::SlowDown,
        "expired_token" | "expired" => CodexPollKind::Expired {
            message: error_message,
        },
        "access_denied" | "denied" => CodexPollKind::Denied {
            message: error_message,
        },
        "" if body.get("error").is_none() && !status.is_success() => CodexPollKind::Pending,
        _ => CodexPollKind::Error {
            message: error_message.unwrap_or_else(|| {
                if !error_code.is_empty() {
                    format!("OAuth error: {error_code}")
                } else {
                    "授权等待中或需在 ChatGPT 安全设置中启用 Codex 设备代码授权".into()
                }
            }),
        },
    }
}

fn codex_success(
    access_token: String,
    refresh_token: Option<String>,
    token_type: Option<String>,
    expires_in: Option<u64>,
) -> CodexTokenPollResponse {
    CodexTokenPollResponse {
        status: "success".into(),
        access_token: Some(access_token),
        refresh_token,
        token_type,
        expires_in,
        error_message: None,
    }
}

fn codex_status(status: &str, error_message: Option<String>) -> CodexTokenPollResponse {
    CodexTokenPollResponse {
        status: status.into(),
        access_token: None,
        refresh_token: None,
        token_type: None,
        expires_in: None,
        error_message,
    }
}

async fn exchange_codex_authorization_code(
    client: &reqwest::Client,
    client_id: &str,
    authorization_code: &str,
    code_verifier: &str,
) -> Result<CodexTokenPollResponse> {
    let params = [
        ("grant_type", "authorization_code"),
        ("code", authorization_code),
        ("redirect_uri", CODEX_DEVICE_CALLBACK_URL),
        ("client_id", client_id),
        ("code_verifier", code_verifier),
    ];

    let response = client
        .post(CODEX_OAUTH_TOKEN_URL)
        .header(reqwest::header::ACCEPT, "application/json")
        .form(&params)
        .send()
        .await?;

    let status = response.status();
    let body: serde_json::Value = response.json().await.unwrap_or_default();
    if let Some(access_token) = json_string(&body, "access_token") {
        return Ok(codex_success(
            access_token.to_string(),
            json_string(&body, "refresh_token").map(ToString::to_string),
            json_string(&body, "token_type").map(ToString::to_string),
            json_u64(&body, "expires_in"),
        ));
    }

    let message = nested_error_message(&body).unwrap_or_else(|| {
        format!("Failed to exchange Codex authorization code (HTTP {status})")
    });
    Ok(codex_status("error", Some(message)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use reqwest::StatusCode;
    use serde_json::json;

    fn kind_status(kind: CodexPollKind) -> &'static str {
        match kind {
            CodexPollKind::Tokens { .. } => "tokens",
            CodexPollKind::AuthorizationCode { .. } => "authorization_code",
            CodexPollKind::Pending => "pending",
            CodexPollKind::SlowDown => "slow_down",
            CodexPollKind::Expired { .. } => "expired",
            CodexPollKind::Denied { .. } => "denied",
            CodexPollKind::Error { .. } => "error",
        }
    }

    #[test]
    fn screenshot_pending_message_on_forbidden_is_pending() {
        let body = json!({
            "error": {
                "message": "Device authorization is pending. Please try again."
            }
        });
        assert_eq!(
            kind_status(classify_codex_device_poll(StatusCode::FORBIDDEN, &body)),
            "pending"
        );
    }

    #[test]
    fn pending_message_on_bad_request_is_pending() {
        let body = json!({
            "error": "authorization_pending",
            "error_description": "Device authorization is pending. Please try again."
        });
        assert_eq!(
            kind_status(classify_codex_device_poll(StatusCode::BAD_REQUEST, &body)),
            "pending"
        );
    }

    #[test]
    fn not_found_without_body_is_pending() {
        assert_eq!(
            kind_status(classify_codex_device_poll(StatusCode::NOT_FOUND, &json!({}))),
            "pending"
        );
    }

    #[test]
    fn success_returns_authorization_code_for_token_exchange() {
        let body = json!({
            "authorization_code": "auth-code",
            "code_verifier": "pkce-verifier",
            "code_challenge": "pkce-challenge"
        });
        match classify_codex_device_poll(StatusCode::OK, &body) {
            CodexPollKind::AuthorizationCode {
                authorization_code,
                code_verifier,
            } => {
                assert_eq!(authorization_code, "auth-code");
                assert_eq!(code_verifier, "pkce-verifier");
            }
            other => panic!("expected authorization code, got {}", kind_status(other)),
        }
    }

    #[test]
    fn success_with_access_token_is_complete() {
        let body = json!({
            "access_token": "tok",
            "refresh_token": "ref",
            "token_type": "Bearer",
            "expires_in": 3600
        });
        match classify_codex_device_poll(StatusCode::OK, &body) {
            CodexPollKind::Tokens {
                access_token,
                refresh_token,
                ..
            } => {
                assert_eq!(access_token, "tok");
                assert_eq!(refresh_token.as_deref(), Some("ref"));
            }
            other => panic!("expected tokens, got {}", kind_status(other)),
        }
    }
}
