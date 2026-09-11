//! Defines model and provider configuration.
use std::{fmt, str::FromStr};

use reqwest::Url;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::{Error, Result};

pub const OPENAI_RESPONSES_ENDPOINT: &str = "/v1/responses";
pub const OPENAI_CHAT_ENDPOINT: &str = "/v1/chat/completions";

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub enum ProviderType {
    #[serde(rename = "openai-chat")]
    OpenAiChat,
    #[serde(rename = "openai-responses")]
    OpenAiResponses,
    #[serde(rename = "anthropic")]
    Anthropic,
    /// 插件执行的调用;协议细节在插件内部,核心只按统一事件流记录。
    #[serde(rename = "plugin")]
    Plugin,
}

impl ProviderType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::OpenAiChat => "openai-chat",
            Self::OpenAiResponses => "openai-responses",
            Self::Anthropic => "anthropic",
            Self::Plugin => "plugin",
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
            "plugin" => Ok(Self::Plugin),
            _ => Err(Error::Config(format!("unsupported provider type: {value}"))),
        }
    }
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum ModelType {
    OpenAi,
    Anthropic,
}

impl ModelType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::OpenAi => "openai",
            Self::Anthropic => "anthropic",
        }
    }
}

impl FromStr for ModelType {
    type Err = Error;

    fn from_str(value: &str) -> Result<Self> {
        match value {
            "openai" => Ok(Self::OpenAi),
            "anthropic" => Ok(Self::Anthropic),
            _ => Err(Error::Config(format!("unsupported model type: {value}"))),
        }
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ModelConfigInput {
    #[serde(default)]
    pub sort_order: i64,
    pub display_name: String,
    /// 供应商分组的自定义显示名;同一 base_url 主机下的模型共享。
    #[serde(default)]
    pub group_name: Option<String>,
    #[serde(rename = "type")]
    pub model_type: ModelType,
    pub base_url: String,
    #[serde(default)]
    pub use_full_url: bool,
    pub api_key: String,
    pub tooltip_data: String,
    pub model_id: String,
    #[serde(default)]
    pub reasoning_effort: Option<String>,
    #[serde(default)]
    pub openai_endpoint: String,
    #[serde(default)]
    pub openai_extra_params_enabled: bool,
    #[serde(default = "empty_object")]
    pub openai_extra_params: serde_json::Value,
    #[serde(default)]
    pub custom_headers_enabled: bool,
    #[serde(default = "empty_object")]
    pub custom_headers: serde_json::Value,
    #[serde(default)]
    pub anthropic_extra_params_enabled: bool,
    #[serde(default = "empty_object")]
    pub anthropic_extra_params: serde_json::Value,
    pub context_window_tokens: Option<u64>,
    pub max_completion_tokens: Option<u64>,
    pub anthropic_max_tokens: Option<u64>,
    #[serde(default)]
    pub anthropic_thinking_effort: Option<String>,
    pub thinking_budget_tokens: Option<u64>,
}

#[derive(Clone, Debug, Serialize)]
pub struct ModelConfig {
    pub model_hash: String,
    pub sort_order: i64,
    pub display_name: String,
    pub group_name: Option<String>,
    /// 是否发布到 Cursor 的模型目录;分组开关批量切换该标记,不参与模型身份哈希。
    pub enabled: bool,
    #[serde(rename = "type")]
    pub model_type: ModelType,
    pub base_url: String,
    pub use_full_url: bool,
    pub api_key: String,
    pub tooltip_data: String,
    pub model_id: String,
    pub reasoning_effort: Option<String>,
    pub openai_endpoint: String,
    pub openai_extra_params_enabled: bool,
    pub openai_extra_params: serde_json::Value,
    pub custom_headers_enabled: bool,
    pub custom_headers: serde_json::Value,
    pub anthropic_extra_params_enabled: bool,
    pub anthropic_extra_params: serde_json::Value,
    pub context_window_tokens: Option<u64>,
    pub max_completion_tokens: Option<u64>,
    pub anthropic_max_tokens: Option<u64>,
    pub anthropic_thinking_effort: Option<String>,
    pub thinking_budget_tokens: Option<u64>,
    pub created_at_ms: i64,
    pub updated_at_ms: i64,
}

impl ModelConfig {
    pub fn provider_type(&self) -> ProviderType {
        match self.model_type {
            ModelType::Anthropic => ProviderType::Anthropic,
            ModelType::OpenAi if self.openai_endpoint == OPENAI_RESPONSES_ENDPOINT => {
                ProviderType::OpenAiResponses
            }
            ModelType::OpenAi => ProviderType::OpenAiChat,
        }
    }

    pub fn request_url(&self) -> Result<String> {
        resolve_request_url(
            self.model_type,
            &self.base_url,
            &self.openai_endpoint,
            self.use_full_url,
        )
    }

    pub fn max_output_tokens(&self) -> Option<u64> {
        match self.model_type {
            ModelType::OpenAi => self.max_completion_tokens,
            ModelType::Anthropic => self.anthropic_max_tokens.or(self.max_completion_tokens),
        }
    }

    pub fn extra_params(&self) -> &serde_json::Value {
        match self.model_type {
            ModelType::OpenAi if self.openai_extra_params_enabled => &self.openai_extra_params,
            ModelType::Anthropic if self.anthropic_extra_params_enabled => {
                &self.anthropic_extra_params
            }
            _ => empty_object_ref(),
        }
    }

    pub fn configure(&self, model: &mut super::ModelSpec) {
        model.display_name = Some(self.display_name.clone());
        // A request-selected context is authoritative.  Use the saved model
        // value only when Cursor did not send a context parameter.
        if model.context_window_tokens.is_none() {
            model.context_window_tokens = self.context_window_tokens;
        }
        if model.reasoning.effort.is_none() {
            model.reasoning.effort = match self.model_type {
                ModelType::OpenAi => self.reasoning_effort.clone(),
                ModelType::Anthropic => self.anthropic_thinking_effort.clone(),
            };
        }
        model.reasoning.enabled |= model.reasoning.effort.is_some();
    }
}

pub fn normalize_model_input(input: &ModelConfigInput) -> Result<ModelConfigInput> {
    let display_name = required(&input.display_name, "model display name")?;
    let group_name = input
        .group_name
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(String::from);
    let base_url = normalize_request_url(&input.base_url)?;
    let api_key = required(&input.api_key, "model API key")?;
    let tooltip_data = required(&input.tooltip_data, "model tooltip")?;
    let model_id = required(&input.model_id, "model id")?;
    let reasoning_effort = normalize_effort(input.reasoning_effort.as_deref(), true)?;
    let anthropic_thinking_effort = match input.model_type {
        ModelType::Anthropic => Some(
            normalize_effort(
                input.anthropic_thinking_effort.as_deref().or(Some("xhigh")),
                false,
            )?
            .expect("Anthropic effort has a default"),
        ),
        ModelType::OpenAi => None,
    };
    let openai_endpoint = match input.model_type {
        ModelType::OpenAi => normalize_openai_endpoint(&input.openai_endpoint)?,
        ModelType::Anthropic => String::new(),
    };
    validate_object(&input.openai_extra_params, "OpenAI extra params")?;
    validate_object(&input.anthropic_extra_params, "Anthropic extra params")?;
    validate_headers(&input.custom_headers)?;

    let normalized = ModelConfigInput {
        sort_order: input.sort_order.max(0),
        display_name,
        group_name,
        model_type: input.model_type,
        base_url,
        use_full_url: input.use_full_url,
        api_key,
        tooltip_data,
        model_id,
        reasoning_effort: (input.model_type == ModelType::OpenAi)
            .then_some(reasoning_effort)
            .flatten(),
        openai_endpoint,
        openai_extra_params_enabled: input.model_type == ModelType::OpenAi
            && input.openai_extra_params_enabled,
        openai_extra_params: if input.model_type == ModelType::OpenAi {
            input.openai_extra_params.clone()
        } else {
            empty_object()
        },
        custom_headers_enabled: input.custom_headers_enabled,
        custom_headers: input.custom_headers.clone(),
        anthropic_extra_params_enabled: input.model_type == ModelType::Anthropic
            && input.anthropic_extra_params_enabled,
        anthropic_extra_params: if input.model_type == ModelType::Anthropic {
            input.anthropic_extra_params.clone()
        } else {
            empty_object()
        },
        context_window_tokens: positive(input.context_window_tokens, "context window")?,
        max_completion_tokens: positive(input.max_completion_tokens, "max completion tokens")?,
        anthropic_max_tokens: positive(input.anthropic_max_tokens, "Anthropic max tokens")?,
        anthropic_thinking_effort,
        thinking_budget_tokens: positive(input.thinking_budget_tokens, "thinking budget")?,
    };
    resolve_request_url(
        normalized.model_type,
        &normalized.base_url,
        &normalized.openai_endpoint,
        normalized.use_full_url,
    )?;
    Ok(normalized)
}

pub fn model_hash(input: &ModelConfigInput) -> Result<String> {
    let normalized = normalize_model_input(input)?;
    let request_url = resolve_request_url(
        normalized.model_type,
        &normalized.base_url,
        &normalized.openai_endpoint,
        normalized.use_full_url,
    )?;
    let mut parts = vec![
        request_url,
        normalized.model_id,
        normalized.api_key,
        normalized.display_name,
    ];
    if normalized.model_type == ModelType::OpenAi {
        parts.push(normalized.openai_endpoint);
    }
    let digest = Sha256::digest(parts.join("\n").as_bytes());
    Ok(hex::encode(&digest[..8]))
}

pub fn normalize_request_url(value: &str) -> Result<String> {
    let value = value.trim();
    let url = Url::parse(value)
        .map_err(|error| Error::Config(format!("invalid model request URL: {error}")))?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
        return Err(Error::Config(
            "model request URL must be an HTTP(S) URL with a host".into(),
        ));
    }
    if url.fragment().is_some() {
        return Err(Error::Config(
            "model request URL cannot contain a fragment".into(),
        ));
    }
    Ok(value.into())
}

pub fn resolve_request_url(
    model_type: ModelType,
    base_url: &str,
    openai_endpoint: &str,
    use_full_url: bool,
) -> Result<String> {
    let base_url = normalize_request_url(base_url)?;
    let endpoint = match model_type {
        ModelType::OpenAi => normalize_openai_endpoint(openai_endpoint)?,
        ModelType::Anthropic => "/v1/messages".into(),
    };
    if use_full_url {
        return Ok(base_url);
    }
    append_standard_endpoint(&base_url, &endpoint)
}

fn append_standard_endpoint(base_url: &str, endpoint: &str) -> Result<String> {
    let mut url = Url::parse(base_url)
        .map_err(|error| Error::Config(format!("invalid model server URL: {error}")))?;
    let base_path = url.path().trim_end_matches('/').to_string();
    let endpoint = if has_trailing_version(&base_path) {
        endpoint.strip_prefix("/v1").unwrap_or(endpoint)
    } else {
        endpoint
    };
    url.set_path(&format!("{base_path}{endpoint}"));
    normalize_request_url(url.as_str())
}

fn has_trailing_version(path: &str) -> bool {
    let Some(segment) = path.rsplit('/').next() else {
        return false;
    };
    segment.strip_prefix('v').is_some_and(|digits| {
        !digits.is_empty() && digits.bytes().all(|byte| byte.is_ascii_digit())
    })
}

pub fn is_sensitive_header(name: &str) -> bool {
    matches!(
        name.to_ascii_lowercase().as_str(),
        "authorization" | "proxy-authorization" | "x-api-key" | "api-key" | "cookie" | "set-cookie"
    )
}

fn normalize_openai_endpoint(value: &str) -> Result<String> {
    match value.trim() {
        "" | OPENAI_RESPONSES_ENDPOINT => Ok(OPENAI_RESPONSES_ENDPOINT.into()),
        OPENAI_CHAT_ENDPOINT => Ok(OPENAI_CHAT_ENDPOINT.into()),
        value => Err(Error::Config(format!(
            "unsupported OpenAI endpoint: {value}"
        ))),
    }
}

fn normalize_effort(value: Option<&str>, allow_empty: bool) -> Result<Option<String>> {
    let value = value.unwrap_or_default().trim().to_ascii_lowercase();
    if value.is_empty() && allow_empty {
        return Ok(None);
    }
    if matches!(value.as_str(), "low" | "medium" | "high" | "xhigh" | "max") {
        Ok(Some(value))
    } else {
        Err(Error::Config(format!(
            "unsupported reasoning effort: {value}"
        )))
    }
}

fn positive(value: Option<u64>, label: &str) -> Result<Option<u64>> {
    match value {
        Some(0) => Err(Error::Config(format!("{label} must be greater than zero"))),
        value => Ok(value),
    }
}

fn required(value: &str, label: &str) -> Result<String> {
    let value = value.trim();
    if value.is_empty() {
        Err(Error::Config(format!("{label} cannot be empty")))
    } else {
        Ok(value.into())
    }
}

fn validate_object(value: &serde_json::Value, label: &str) -> Result<()> {
    if value.is_object() {
        Ok(())
    } else {
        Err(Error::Config(format!("{label} must be a JSON object")))
    }
}

fn validate_headers(value: &serde_json::Value) -> Result<()> {
    validate_object(value, "custom headers")?;
    for (name, value) in value.as_object().expect("validated object") {
        if name.trim().is_empty() || !value.is_string() {
            return Err(Error::Config(
                "custom headers must have non-empty names and string values".into(),
            ));
        }
    }
    Ok(())
}

fn empty_object() -> serde_json::Value {
    serde_json::json!({})
}

fn empty_object_ref() -> &'static serde_json::Value {
    static EMPTY: std::sync::OnceLock<serde_json::Value> = std::sync::OnceLock::new();
    EMPTY.get_or_init(empty_object)
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct ReasoningSpec {
    pub enabled: bool,
    pub effort: Option<String>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum ModelLatency {
    #[default]
    Standard,
    Fast,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct ModelSpec {
    pub model_id: String,
    pub display_name: Option<String>,
    pub reasoning: ReasoningSpec,
    pub latency: ModelLatency,
    pub max_output_tokens: Option<u64>,
    pub context_window_tokens: Option<u64>,
    #[serde(default)]
    pub supports_image_generation: bool,
    #[serde(default)]
    pub extra_params: serde_json::Value,
}

impl ModelSpec {
    pub fn new(model_id: impl Into<String>) -> Self {
        Self {
            model_id: model_id.into(),
            display_name: None,
            reasoning: ReasoningSpec::default(),
            latency: ModelLatency::Standard,
            max_output_tokens: None,
            context_window_tokens: None,
            supports_image_generation: false,
            extra_params: serde_json::json!({}),
        }
    }
}
