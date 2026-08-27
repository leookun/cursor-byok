use base64::Engine;
use sha2::{Digest, Sha256};

use crate::{
    model::{ModelConfig, ProviderType},
    provider::chatgpt_account_id,
    Error, Result,
};

pub const CODEX_REQUEST_URL: &str = "https://chatgpt.com/backend-api/codex/responses";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SubscriptionKind {
    Grok,
    Codex,
}

impl SubscriptionKind {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Grok => "grok",
            Self::Codex => "codex",
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::Grok => "Grok",
            Self::Codex => "Codex",
        }
    }

    pub fn parse(value: &str) -> Result<Self> {
        match value {
            "grok" => Ok(Self::Grok),
            "codex" => Ok(Self::Codex),
            _ => Err(Error::Config(format!("unsupported subscription provider: {value}"))),
        }
    }

    pub fn from_model(model: &ModelConfig) -> Option<Self> {
        if model.base_url.contains("chatgpt.com")
            || model.tooltip_data.contains("Codex")
            || model.tooltip_data.contains("ChatGPT")
            || chatgpt_account_id(&model.api_key).is_some()
        {
            return Some(Self::Codex);
        }
        if model.base_url.contains("api.x.ai")
            || model.model_id.to_ascii_lowercase().contains("grok")
            || model.tooltip_data.contains("xAI")
            || model.tooltip_data.contains("Grok")
        {
            return Some(Self::Grok);
        }
        None
    }
}

/// Codex ChatGPT tokens are not Platform API keys. Always send them to the
/// ChatGPT Codex Responses endpoint, even if the stored model still points at
/// `api.openai.com` Chat Completions.
pub fn apply_subscription_route(
    model: &ModelConfig,
    request_url: &mut String,
    provider_type: &mut ProviderType,
) {
    if SubscriptionKind::from_model(model) != Some(SubscriptionKind::Codex) {
        return;
    }
    *request_url = CODEX_REQUEST_URL.into();
    *provider_type = ProviderType::OpenAiResponses;
}

pub fn account_identity(kind: SubscriptionKind, access_token: &str) -> (String, String) {
    let payload = jwt_payload(access_token);
    let subject = match kind {
        SubscriptionKind::Codex => chatgpt_account_id(access_token)
            .or_else(|| jwt_string(&payload, "sub"))
            .or_else(|| jwt_string(&payload, "email")),
        SubscriptionKind::Grok => jwt_string(&payload, "sub")
            .or_else(|| jwt_string(&payload, "email"))
            .or_else(|| jwt_string(&payload, "preferred_username")),
    };
    let display = jwt_string(&payload, "email")
        .or_else(|| jwt_string(&payload, "preferred_username"))
        .or_else(|| jwt_string(&payload, "name"))
        .or_else(|| subject.clone())
        .unwrap_or_else(|| kind.label().into());
    let key = subject.unwrap_or_else(|| token_fingerprint(access_token));
    (format!("{}:{key}", kind.as_str()), display)
}

pub const CODEX_SESSION_WINDOW_MS: i64 = 5 * 60 * 60 * 1000;

pub fn window_exhausted(remaining_percent: Option<f64>, reset_at_ms: Option<i64>, now_ms: i64) -> bool {
    if reset_at_ms.is_some_and(|reset| reset <= now_ms) {
        return false;
    }
    remaining_percent.is_some_and(|value| value <= 0.0)
}

pub fn account_exhausted(
    remaining_percent: Option<f64>,
    reset_at_ms: Option<i64>,
    session_remaining_percent: Option<f64>,
    session_reset_at_ms: Option<i64>,
    now_ms: i64,
) -> bool {
    window_exhausted(remaining_percent, reset_at_ms, now_ms)
        || window_exhausted(session_remaining_percent, session_reset_at_ms, now_ms)
}

pub fn is_quota_error(error: &Error) -> bool {
    let message = error.to_string().to_ascii_lowercase();
    message.contains("insufficient_quota")
        || message.contains("usage_limit_reached")
        || message.contains("exceeded your current quota")
        || message.contains("quota_exceeded")
        || message.contains("5-hour")
        || message.contains("5 hour")
        || (message.contains("429")
            && (message.contains("quota")
                || message.contains("usage_limit")
                || message.contains("insufficient")))
}

fn jwt_payload(token: &str) -> Option<serde_json::Value> {
    let payload = token.split('.').nth(1)?;
    let decoded = base64::engine::general_purpose::URL_SAFE_NO_PAD
        .decode(payload)
        .or_else(|_| base64::engine::general_purpose::STANDARD.decode(payload))
        .ok()?;
    serde_json::from_slice(&decoded).ok()
}

fn jwt_string(payload: &Option<serde_json::Value>, key: &str) -> Option<String> {
    payload
        .as_ref()?
        .get(key)
        .and_then(serde_json::Value::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToString::to_string)
}

fn token_fingerprint(token: &str) -> String {
    hex::encode(&Sha256::digest(token.as_bytes())[..8])
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ImportedCredential {
    pub kind: SubscriptionKind,
    pub access_token: String,
    pub refresh_token: Option<String>,
    pub display_name: Option<String>,
}

pub fn parse_imported_credentials(
    filename: &str,
    value: &serde_json::Value,
) -> Result<Vec<ImportedCredential>> {
    let mut credentials = Vec::new();
    let mut skipped_disabled = false;
    collect_imported_credentials(filename, value, &mut credentials, &mut skipped_disabled)?;
    if credentials.is_empty() && !skipped_disabled {
        return Err(Error::Config(format!(
            "{filename} does not contain a Grok or Codex access token"
        )));
    }
    Ok(credentials)
}

fn collect_imported_credentials(
    filename: &str,
    value: &serde_json::Value,
    credentials: &mut Vec<ImportedCredential>,
    skipped_disabled: &mut bool,
) -> Result<()> {
    if let Some(items) = value.as_array() {
        for item in items {
            collect_imported_credentials(filename, item, credentials, skipped_disabled)?;
        }
        return Ok(());
    }
    let Some(object) = value.as_object() else {
        return Ok(());
    };
    for key in ["accounts", "credentials", "items"] {
        if let Some(items) = object.get(key).and_then(|item| item.as_array()) {
            for item in items {
                collect_imported_credentials(filename, item, credentials, skipped_disabled)?;
            }
            return Ok(());
        }
    }
    if object.get("disabled").and_then(serde_json::Value::as_bool) == Some(true) {
        *skipped_disabled = true;
        return Ok(());
    }
    let token_source = object
        .get("tokens")
        .and_then(|item| item.as_object())
        .unwrap_or(object);
    let access_token = json_text(token_source.get("access_token"))
        .or_else(|| json_text(token_source.get("token")))
        .or_else(|| json_text(token_source.get("key")))
        .or_else(|| json_text(object.get("access_token")))
        .or_else(|| json_text(object.get("key")));
    let Some(access_token) = access_token else {
        return Ok(());
    };
    let Some(kind) = detect_credential_kind(filename, object, &access_token) else {
        return Err(Error::Config(format!(
            "{filename} is not a Grok or Codex credential"
        )));
    };
    let refresh_token = json_text(token_source.get("refresh_token"))
        .or_else(|| json_text(object.get("refresh_token")));
    let display_name = json_text(object.get("email"))
        .or_else(|| json_text(token_source.get("email")))
        .or_else(|| email_from_filename(filename))
        .or_else(|| {
            json_text(object.get("id_token"))
                .and_then(|token| jwt_string(&jwt_payload(&token), "email"))
        });
    credentials.push(ImportedCredential {
        kind,
        access_token,
        refresh_token,
        display_name,
    });
    Ok(())
}

fn detect_credential_kind(
    filename: &str,
    object: &serde_json::Map<String, serde_json::Value>,
    access_token: &str,
) -> Option<SubscriptionKind> {
    if let Some(kind) = kind_from_label(json_text(object.get("type")).as_deref())
        .or_else(|| kind_from_label(json_text(object.get("auth_kind")).as_deref()))
        .or_else(|| kind_from_label(json_text(object.get("provider")).as_deref()))
    {
        return Some(kind);
    }
    let haystack = [
        json_text(object.get("base_url")),
        json_text(object.get("token_endpoint")),
        json_text(object.get("issuer")),
        Some(filename.replace('\\', "/")),
        jwt_string(&jwt_payload(access_token), "iss"),
    ]
    .into_iter()
    .flatten()
    .collect::<Vec<_>>()
    .join(" ")
    .to_ascii_lowercase();
    if haystack.contains("chatgpt")
        || haystack.contains("openai")
        || haystack.contains("codex")
    {
        return Some(SubscriptionKind::Codex);
    }
    if haystack.contains("x.ai")
        || haystack.contains("xai")
        || haystack.contains("grok")
        || haystack.contains("cli-chat-proxy")
    {
        return Some(SubscriptionKind::Grok);
    }
    object.get("tokens").and(Some(SubscriptionKind::Codex))
}

fn kind_from_label(label: Option<&str>) -> Option<SubscriptionKind> {
    match label?.trim().to_ascii_lowercase().as_str() {
        "xai" | "grok" | "x-ai" | "super grok" | "supergrok" => Some(SubscriptionKind::Grok),
        "openai" | "chatgpt" | "codex" | "chatgpt-codex" | "openai-codex" => {
            Some(SubscriptionKind::Codex)
        }
        _ => None,
    }
}

fn email_from_filename(filename: &str) -> Option<String> {
    let name = filename.replace('\\', "/");
    let name = name.rsplit('/').next().unwrap_or(&name);
    let name = name.strip_suffix(".json").unwrap_or(name);
    let name = name
        .strip_prefix("xai-")
        .or_else(|| name.strip_prefix("grok-"))
        .or_else(|| name.strip_prefix("openai-"))
        .or_else(|| name.strip_prefix("chatgpt-"))
        .or_else(|| name.strip_prefix("codex-"))
        .unwrap_or(name);
    name.contains('@').then(|| name.to_string())
}

fn json_text(value: Option<&serde_json::Value>) -> Option<String> {
    value
        .and_then(serde_json::Value::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToString::to_string)
}

#[cfg(test)]
mod tests {
    use super::{
        account_exhausted, account_identity, apply_subscription_route, is_quota_error,
        parse_imported_credentials, window_exhausted, CODEX_REQUEST_URL, SubscriptionKind,
    };
    use crate::{
        model::{ModelConfig, ModelType, ProviderType},
        Error,
    };
    use base64::Engine;

    fn model_config(base_url: &str, tooltip: &str, api_key: &str, openai_endpoint: &str) -> ModelConfig {
        ModelConfig {
            model_hash: "hash".into(),
            sort_order: 0,
            display_name: "model".into(),
            model_type: ModelType::OpenAi,
            base_url: base_url.into(),
            use_full_url: false,
            api_key: api_key.into(),
            tooltip_data: tooltip.into(),
            model_id: "gpt-5.4".into(),
            reasoning_effort: None,
            openai_endpoint: openai_endpoint.into(),
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
            created_at_ms: 0,
            updated_at_ms: 0,
        }
    }

    fn chatgpt_jwt() -> String {
        let payload = base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(
            r#"{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}"#,
        );
        format!("header.{payload}.sig")
    }

    #[test]
    fn grok_identity_uses_email_and_subject() {
        let payload = base64::engine::general_purpose::URL_SAFE_NO_PAD
            .encode(r#"{"sub":"user-1","email":"a@x.ai"}"#);
        let token = format!("h.{payload}.s");
        let (id, name) = account_identity(SubscriptionKind::Grok, &token);
        assert_eq!(id, "grok:user-1");
        assert_eq!(name, "a@x.ai");
    }

    #[test]
    fn exhausted_quota_clears_after_reset_time() {
        assert!(window_exhausted(Some(0.0), Some(50), 40));
        assert!(!window_exhausted(Some(0.0), Some(50), 60));
        assert!(!window_exhausted(Some(12.0), None, 1));
    }

    #[test]
    fn codex_switches_when_five_hour_window_is_empty() {
        assert!(account_exhausted(Some(80.0), Some(9_999_999), Some(0.0), Some(50), 40));
        assert!(!account_exhausted(Some(80.0), Some(9_999_999), Some(0.0), Some(50), 60));
        assert!(!account_exhausted(Some(80.0), Some(9_999_999), Some(12.0), None, 40));
    }

    #[test]
    fn stored_codex_chat_completions_models_route_to_chatgpt_responses() {
        let model = model_config(
            "https://api.openai.com/v1",
            "ChatGPT / OpenAI Codex",
            "sk-not-a-platform-key",
            "/v1/chat/completions",
        );
        assert_eq!(SubscriptionKind::from_model(&model), Some(SubscriptionKind::Codex));
        let mut request_url = "https://api.openai.com/v1/chat/completions".into();
        let mut provider_type = ProviderType::OpenAiChat;
        apply_subscription_route(&model, &mut request_url, &mut provider_type);
        assert_eq!(request_url, CODEX_REQUEST_URL);
        assert_eq!(provider_type, ProviderType::OpenAiResponses);
    }

    #[test]
    fn chatgpt_jwt_on_platform_url_is_treated_as_codex() {
        let model = model_config(
            "https://api.openai.com",
            "备注",
            &chatgpt_jwt(),
            "/v1/chat/completions",
        );
        assert_eq!(SubscriptionKind::from_model(&model), Some(SubscriptionKind::Codex));
        let mut request_url = "https://api.openai.com/v1/chat/completions".into();
        let mut provider_type = ProviderType::OpenAiChat;
        apply_subscription_route(&model, &mut request_url, &mut provider_type);
        assert_eq!(request_url, CODEX_REQUEST_URL);
        assert_eq!(provider_type, ProviderType::OpenAiResponses);
    }

    #[test]
    fn platform_openai_models_keep_their_stored_route() {
        let model = model_config(
            "https://api.openai.com/v1",
            "备注",
            "sk-live",
            "/v1/chat/completions",
        );
        assert_eq!(SubscriptionKind::from_model(&model), None);
        let mut request_url = "https://api.openai.com/v1/chat/completions".into();
        let mut provider_type = ProviderType::OpenAiChat;
        apply_subscription_route(&model, &mut request_url, &mut provider_type);
        assert_eq!(request_url, "https://api.openai.com/v1/chat/completions");
        assert_eq!(provider_type, ProviderType::OpenAiChat);
    }

    #[test]
    fn rate_limit_alone_is_not_quota_exhaustion() {
        assert!(!is_quota_error(&Error::Provider(
            "OpenAI Responses 429 Too Many Requests: rate_limit_reached".into(),
        )));
        assert!(is_quota_error(&Error::Provider(
            "usage_limit_reached: 5-hour limit".into(),
        )));
    }

    #[test]
    fn xai_export_file_imports_as_grok() {
        let value = serde_json::json!({
            "access_token": "tok-a",
            "refresh_token": "ref-a",
            "auth_kind": "oauth",
            "base_url": "https://cli-chat-proxy.grok.com/v1",
            "email": "user@example.com",
            "type": "xai"
        });
        let parsed = parse_imported_credentials("xai-user@example.com.json", &value).unwrap();
        assert_eq!(parsed.len(), 1);
        assert_eq!(parsed[0].kind, SubscriptionKind::Grok);
        assert_eq!(parsed[0].access_token, "tok-a");
        assert_eq!(parsed[0].refresh_token.as_deref(), Some("ref-a"));
        assert_eq!(parsed[0].display_name.as_deref(), Some("user@example.com"));
    }

    #[test]
    fn disabled_xai_export_is_skipped() {
        let value = serde_json::json!({
            "access_token": "tok-a",
            "disabled": true,
            "type": "xai"
        });
        assert!(parse_imported_credentials("xai-disabled.json", &value)
            .unwrap()
            .is_empty());
    }

    #[test]
    fn grok_pager_export_with_headers_is_one_grok_account() {
        let value = serde_json::json!({
            "access_token": "tok-a",
            "refresh_token": "ref-a",
            "auth_kind": "oauth",
            "base_url": "https://cli-chat-proxy.grok.com/v1",
            "disabled": false,
            "email": "user@example.com",
            "expired": "2026-01-01T00:00:00Z",
            "expires_in": 21600,
            "headers": {
                "User-Agent": "grok-pager/0.2.93 grok-shell/0.2.93 (linux; x86_64)",
                "x-authenticateresponse": "authenticate-response",
                "x-grok-client-identifier": "grok-pager",
                "x-grok-client-version": "0.2.93",
                "x-xai-token-auth": "xai-grok-cli"
            },
            "id_token": "header.payload.sig",
            "last_refresh": "2026-01-01T00:00:00Z",
            "redirect_uri": "http://127.0.0.1:56121/callback",
            "sub": "00000000-0000-4000-8000-000000000001",
            "token_endpoint": "https://auth.x.ai/oauth2/token",
            "token_type": "Bearer",
            "type": "xai"
        });
        let parsed = parse_imported_credentials("xai-user@example.com.json", &value).unwrap();
        assert_eq!(parsed.len(), 1);
        assert_eq!(parsed[0].kind, SubscriptionKind::Grok);
        assert_eq!(parsed[0].access_token, "tok-a");
        assert_eq!(parsed[0].refresh_token.as_deref(), Some("ref-a"));
        assert_eq!(parsed[0].display_name.as_deref(), Some("user@example.com"));
    }

    #[test]
    fn enabled_and_disabled_exports_keep_the_usable_account() {
        let value = serde_json::json!([
            {
                "access_token": "tok-a",
                "disabled": false,
                "email": "ok@x.ai",
                "type": "xai"
            },
            {
                "access_token": "tok-b",
                "disabled": true,
                "email": "off@x.ai",
                "type": "xai"
            }
        ]);
        let parsed = parse_imported_credentials("accounts.json", &value).unwrap();
        assert_eq!(parsed.len(), 1);
        assert_eq!(parsed[0].display_name.as_deref(), Some("ok@x.ai"));
    }

    #[test]
    fn codex_auth_json_imports_from_nested_tokens() {
        let value = serde_json::json!({
            "tokens": {
                "access_token": "codex-tok",
                "refresh_token": "codex-ref"
            },
            "last_refresh": "2026-08-26T00:00:00Z"
        });
        let parsed = parse_imported_credentials("auth.json", &value).unwrap();
        assert_eq!(parsed[0].kind, SubscriptionKind::Codex);
        assert_eq!(parsed[0].access_token, "codex-tok");
    }
}
