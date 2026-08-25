use std::{fmt, str::FromStr};

use reqwest::Url;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::{Error, Result};

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub enum ProviderType {
    #[serde(rename = "openai-chat")]
    OpenAiChat,
    #[serde(rename = "openai-responses")]
    OpenAiResponses,
    #[serde(rename = "anthropic")]
    Anthropic,
}

impl ProviderType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::OpenAiChat => "openai-chat",
            Self::OpenAiResponses => "openai-responses",
            Self::Anthropic => "anthropic",
        }
    }
}

impl fmt::Display for ProviderType {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

impl FromStr for ProviderType {
    type Err = Error;

    fn from_str(value: &str) -> Result<Self> {
        match value {
            "openai-chat" => Ok(Self::OpenAiChat),
            "openai-responses" => Ok(Self::OpenAiResponses),
            "anthropic" => Ok(Self::Anthropic),
            _ => Err(Error::Config(format!("unsupported provider type: {value}"))),
        }
    }
}

#[derive(Clone, Debug, Serialize)]
pub struct ProviderEndpoint {
    pub provider_id: i64,
    pub name: String,
    pub provider_type: ProviderType,
    pub base_url: String,
    pub api_key: Option<String>,
    pub has_api_key: bool,
    pub custom_headers: serde_json::Value,
    pub extra_params: serde_json::Value,
    pub created_at_ms: i64,
    pub updated_at_ms: i64,
}

#[derive(Clone, Debug)]
pub struct ProviderEndpointSecret {
    pub endpoint: ProviderEndpoint,
    pub custom_headers: serde_json::Value,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ProviderEndpointInput {
    pub name: String,
    pub provider_type: ProviderType,
    pub base_url: String,
    #[serde(default)]
    pub api_key: Option<String>,
    #[serde(default = "empty_object")]
    pub custom_headers: serde_json::Value,
    #[serde(default = "empty_object")]
    pub extra_params: serde_json::Value,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ProviderModelInput {
    pub model_id: String,
    pub display_name: String,
    pub endpoint_type: ProviderType,
    #[serde(default)]
    pub request_url: String,
    #[serde(default = "enabled")]
    pub enabled: bool,
    #[serde(default)]
    pub sort_order: i64,
    pub context_window_tokens: Option<u64>,
    pub max_output_tokens: Option<u64>,
    #[serde(default)]
    pub reasoning_enabled: bool,
    pub reasoning_effort: Option<String>,
    #[serde(default)]
    pub supports_image_generation: bool,
}

#[derive(Clone, Debug, Serialize)]
pub struct ProviderModel {
    pub model_hash: String,
    pub provider_id: i64,
    pub model_id: String,
    pub display_name: String,
    pub endpoint_type: ProviderType,
    pub request_url: String,
    pub enabled: bool,
    pub sort_order: i64,
    pub context_window_tokens: Option<u64>,
    pub max_output_tokens: Option<u64>,
    pub reasoning_enabled: bool,
    pub reasoning_effort: Option<String>,
    pub supports_image_generation: bool,
    pub created_at_ms: i64,
    pub updated_at_ms: i64,
}

impl ProviderModel {
    pub fn configure(&self, model: &mut super::ModelSpec) {
        model.display_name = Some(self.display_name.clone());
        model.context_window_tokens = self.context_window_tokens.or(model.context_window_tokens);
        model.supports_image_generation = self.supports_image_generation;
        model.reasoning.enabled |= self.reasoning_enabled;
    }
}

pub fn normalize_base_url(value: &str) -> Result<String> {
    let mut url = Url::parse(value.trim())
        .map_err(|error| Error::Config(format!("invalid provider base URL: {error}")))?;
    if url.query().is_some() || url.fragment().is_some() {
        return Err(Error::Config(
            "provider base URL cannot contain query or fragment".into(),
        ));
    }
    let path = url.path().trim_end_matches('/').to_string();
    url.set_path(if path.is_empty() { "/" } else { &path });
    Ok(url.as_str().trim_end_matches('/').to_string())
}

pub fn model_hash(
    base_url: &str,
    api_key: &str,
    provider_type: ProviderType,
    model_id: &str,
) -> Result<String> {
    let base_url = normalize_base_url(base_url)?;
    let model_id = model_id.trim();
    if model_id.is_empty() {
        return Err(Error::Config("model id cannot be empty".into()));
    }
    let mut digest = Sha256::new();
    digest.update(base_url.as_bytes());
    digest.update([0]);
    digest.update(api_key.as_bytes());
    digest.update([0]);
    digest.update(provider_type.as_str().as_bytes());
    digest.update([0]);
    digest.update(model_id.as_bytes());
    Ok(hex::encode(&digest.finalize()[..4]))
}

pub fn resolve_request_url(
    base_url: &str,
    endpoint_type: ProviderType,
    request_url: &str,
) -> Result<String> {
    let base_url = normalize_base_url(base_url)?;
    let request_url = request_url.trim();
    let combined = if request_url.starts_with("http://") || request_url.starts_with("https://") {
        let url = Url::parse(request_url)
            .map_err(|error| Error::Config(format!("invalid model request URL: {error}")))?;
        if url.host_str().is_none() {
            return Err(Error::Config(
                "model request URL must contain a host".into(),
            ));
        }
        url.to_string()
    } else {
        let path = if request_url.is_empty() {
            match endpoint_type {
                ProviderType::OpenAiChat => "/v1/chat/completions",
                ProviderType::OpenAiResponses => "/v1/responses",
                ProviderType::Anthropic => "/v1/messages",
            }
        } else if request_url.starts_with('/') {
            request_url
        } else {
            return Err(Error::Config(
                "model request URL must be an HTTP(S) URL or start with /".into(),
            ));
        };
        format!("{}{}", base_url.trim_end_matches('/'), path)
    };
    let mut normalized = combined;
    while normalized.contains("/v1/v1") {
        normalized = normalized.replace("/v1/v1", "/v1");
    }
    Ok(normalized)
}

pub fn is_sensitive_header(name: &str) -> bool {
    matches!(
        name.to_ascii_lowercase().as_str(),
        "authorization" | "proxy-authorization" | "x-api-key" | "api-key" | "cookie" | "set-cookie"
    )
}

fn empty_object() -> serde_json::Value {
    serde_json::json!({})
}

fn enabled() -> bool {
    true
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hash_uses_normalized_url_key_type_and_model() {
        let first = model_hash(
            "HTTPS://Example.COM/v1/",
            "secret",
            ProviderType::OpenAiChat,
            "model-a",
        )
        .unwrap();
        let second = model_hash(
            "https://example.com/v1",
            "secret",
            ProviderType::OpenAiChat,
            "model-a",
        )
        .unwrap();
        assert_eq!(first, second);
        assert_ne!(
            first,
            model_hash(
                "https://example.com/v1",
                "different-secret",
                ProviderType::OpenAiChat,
                "model-a",
            )
            .unwrap()
        );
        assert_ne!(
            first,
            model_hash(
                "https://example.com/v1",
                "secret",
                ProviderType::Anthropic,
                "model-a",
            )
            .unwrap()
        );
    }

    #[test]
    fn provider_type_json_uses_public_identifiers() {
        for (value, provider_type) in [
            ("openai-chat", ProviderType::OpenAiChat),
            ("openai-responses", ProviderType::OpenAiResponses),
            ("anthropic", ProviderType::Anthropic),
        ] {
            assert_eq!(
                serde_json::from_str::<ProviderType>(&format!("\"{value}\"")).unwrap(),
                provider_type
            );
            assert_eq!(
                serde_json::to_string(&provider_type).unwrap(),
                format!("\"{value}\"")
            );
        }
    }

    #[test]
    fn resolves_default_relative_and_absolute_model_urls() {
        assert_eq!(
            resolve_request_url("https://example.com/v1", ProviderType::OpenAiChat, "").unwrap(),
            "https://example.com/v1/chat/completions"
        );
        assert_eq!(
            resolve_request_url("https://example.com/v1", ProviderType::OpenAiResponses, "")
                .unwrap(),
            "https://example.com/v1/responses"
        );
        assert_eq!(
            resolve_request_url("https://example.com", ProviderType::Anthropic, "").unwrap(),
            "https://example.com/v1/messages"
        );
        assert_eq!(
            resolve_request_url(
                "https://example.com",
                ProviderType::OpenAiChat,
                "/v2/chat/completions"
            )
            .unwrap(),
            "https://example.com/v2/chat/completions"
        );
        assert_eq!(
            resolve_request_url(
                "https://example.com/v1",
                ProviderType::OpenAiChat,
                "https://gateway.example/v1/v1/custom"
            )
            .unwrap(),
            "https://gateway.example/v1/custom"
        );
    }

    #[test]
    fn configured_context_window_has_priority_over_cursor_selection() {
        let provider = ProviderModel {
            model_hash: "12345678".into(),
            provider_id: 1,
            model_id: "model".into(),
            display_name: "Model".into(),
            endpoint_type: ProviderType::OpenAiResponses,
            request_url: String::new(),
            enabled: true,
            sort_order: 0,
            context_window_tokens: Some(200_000),
            max_output_tokens: None,
            reasoning_enabled: false,
            reasoning_effort: None,
            supports_image_generation: false,
            created_at_ms: 0,
            updated_at_ms: 0,
        };
        let mut selected = super::super::ModelSpec::new("12345678");
        selected.context_window_tokens = Some(800_000);
        provider.configure(&mut selected);
        assert_eq!(selected.context_window_tokens, Some(200_000));

        let mut defaulted = super::super::ModelSpec::new("12345678");
        provider.configure(&mut defaulted);
        assert_eq!(defaulted.context_window_tokens, Some(200_000));
    }
}
