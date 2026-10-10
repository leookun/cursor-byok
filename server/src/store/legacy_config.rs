//! Imports model definitions from the v0.0.49 YAML configuration.
use std::{collections::HashSet, path::Path};

use serde::Deserialize;

use crate::{
    model::{
        model_hash, normalize_model_input, normalize_request_url, ModelConfigInput, ModelType,
        OPENAI_CHAT_ENDPOINT, OPENAI_RESPONSES_ENDPOINT,
    },
    Error, Result,
};

use super::Store;

pub struct LegacyModelImportPlan {
    pub models: Vec<LegacyModelImportEntry>,
}

pub struct LegacyModelImportEntry {
    pub model_hash: String,
    pub input: ModelConfigInput,
    pub existing: bool,
}

pub struct LegacyModelImportOutcome {
    pub imported: usize,
    pub skipped: usize,
    pub total: usize,
}

#[derive(Default, Deserialize)]
struct LegacyConfig {
    #[serde(rename = "modelAdapters", default)]
    model_adapters: Vec<LegacyModel>,
}

#[derive(Default, Deserialize)]
struct LegacyModel {
    #[serde(default)]
    sort: i64,
    #[serde(rename = "displayName", default)]
    display_name: String,
    #[serde(rename = "type", default)]
    model_type: String,
    #[serde(rename = "baseURL", default)]
    base_url: String,
    #[serde(rename = "apiKey", default)]
    api_key: String,
    #[serde(rename = "tooltipData", default)]
    tooltip_data: String,
    #[serde(rename = "modelID", default)]
    model_id: String,
    #[serde(rename = "reasoningEffort", default)]
    reasoning_effort: String,
    #[serde(rename = "openAIEndpoint", default)]
    openai_endpoint: String,
    #[serde(rename = "openAIExtraParamsEnabled", default)]
    openai_extra_params_enabled: bool,
    #[serde(rename = "openAIExtraParamsJSON", default)]
    openai_extra_params_json: String,
    #[serde(rename = "customHeadersEnabled", default)]
    custom_headers_enabled: bool,
    #[serde(rename = "customHeadersJSON", default)]
    custom_headers_json: String,
    #[serde(rename = "anthropicExtraParamsEnabled", default)]
    anthropic_extra_params_enabled: bool,
    #[serde(rename = "anthropicExtraParamsJSON", default)]
    anthropic_extra_params_json: String,
    #[serde(rename = "contextWindowTokens", default)]
    context_window_tokens: u64,
    #[serde(rename = "maxCompletionTokens", default)]
    max_completion_tokens: u64,
    #[serde(rename = "anthropicMaxTokens", default)]
    anthropic_max_tokens: u64,
    #[serde(rename = "anthropicThinkingEffort", default)]
    anthropic_thinking_effort: String,
    #[serde(rename = "thinkingBudgetTokens", default)]
    thinking_budget_tokens: u64,
}

impl Store {
    pub async fn preview_v0049_model_config(&self, path: &Path) -> Result<LegacyModelImportPlan> {
        let inputs = load_v0049_model_config(path)?;
        let existing = self
            .models()
            .await?
            .into_iter()
            .map(|model| model.model_hash)
            .collect::<HashSet<_>>();
        let mut seen = HashSet::with_capacity(inputs.len());
        let mut models = Vec::with_capacity(inputs.len());
        for input in inputs {
            let input = normalize_model_input(&input)?;
            let hash = model_hash(&input)?;
            if seen.insert(hash.clone()) {
                models.push(LegacyModelImportEntry {
                    existing: existing.contains(&hash),
                    model_hash: hash,
                    input,
                });
            }
        }
        Ok(LegacyModelImportPlan { models })
    }

    pub async fn import_v0049_model_config(&self, path: &Path) -> Result<LegacyModelImportOutcome> {
        let plan = self.preview_v0049_model_config(path).await?;
        let total = plan.models.len();
        let missing = plan
            .models
            .into_iter()
            .filter(|model| !model.existing)
            .map(|model| model.input)
            .collect::<Vec<_>>();
        let imported = self.create_models_if_missing(&missing).await?;
        Ok(LegacyModelImportOutcome {
            imported,
            skipped: total - imported,
            total,
        })
    }
}

fn load_v0049_model_config(path: &Path) -> Result<Vec<ModelConfigInput>> {
    let raw = match std::fs::read(path) {
        Ok(raw) => raw,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            return Err(Error::Config(format!(
                "v0.0.49 config not found at {}",
                path.display()
            )))
        }
        Err(error) => return Err(error.into()),
    };
    let legacy: LegacyConfig = serde_yaml::from_slice(&raw)
        .map_err(|error| Error::Config(format!("invalid v0.0.49 config: {error}")))?;
    if legacy.model_adapters.is_empty() {
        return Err(Error::Config(
            "v0.0.49 config contains no model adapters".into(),
        ));
    }
    legacy.model_adapters.into_iter().map(model_input).collect()
}

fn model_input(model: LegacyModel) -> Result<ModelConfigInput> {
    let model_type = match model.model_type.trim().to_ascii_lowercase().as_str() {
        "openai" => ModelType::OpenAi,
        "anthropic" => ModelType::Anthropic,
        value => {
            return Err(Error::Config(format!(
                "unsupported v0.0.49 model type: {value}"
            )))
        }
    };
    let (base_url, openai_endpoint, use_full_url) =
        legacy_request_configuration(model_type, &model.base_url, &model.openai_endpoint)?;
    Ok(ModelConfigInput {
        sort_order: model.sort,
        display_name: model.display_name.clone(),
        group_name: None,
        model_type,
        base_url,
        use_full_url,
        api_key: model.api_key,
        supports_images: None,
        supports_tools: None,
        tooltip_data: if model.tooltip_data.trim().is_empty() {
            model.display_name
        } else {
            model.tooltip_data
        },
        model_id: model.model_id,
        reasoning_effort: optional_string(model.reasoning_effort),
        openai_endpoint,
        openai_extra_params_enabled: model.openai_extra_params_enabled,
        openai_extra_params: enabled_json_object(
            model_type == ModelType::OpenAi && model.openai_extra_params_enabled,
            &model.openai_extra_params_json,
        )?,
        custom_headers_enabled: model.custom_headers_enabled,
        custom_headers: enabled_json_object(
            model.custom_headers_enabled,
            &model.custom_headers_json,
        )?,
        anthropic_extra_params_enabled: model.anthropic_extra_params_enabled,
        anthropic_extra_params: enabled_json_object(
            model_type == ModelType::Anthropic && model.anthropic_extra_params_enabled,
            &model.anthropic_extra_params_json,
        )?,
        context_window_tokens: positive(model.context_window_tokens),
        max_completion_tokens: positive(model.max_completion_tokens),
        anthropic_max_tokens: positive(model.anthropic_max_tokens),
        anthropic_thinking_effort: optional_string(model.anthropic_thinking_effort),
        thinking_budget_tokens: positive(model.thinking_budget_tokens),
    })
}

fn legacy_request_configuration(
    model_type: ModelType,
    base_url: &str,
    openai_endpoint: &str,
) -> Result<(String, String, bool)> {
    let base_url = normalize_request_url(base_url)?;
    match model_type {
        ModelType::Anthropic => {
            let use_full_url = url_path_ends_with(&base_url, "/messages");
            Ok((base_url, String::new(), use_full_url))
        }
        ModelType::OpenAi => {
            let detected = openai_protocol_from_url(&base_url);
            let configured = match openai_endpoint.trim() {
                "" | OPENAI_RESPONSES_ENDPOINT => OPENAI_RESPONSES_ENDPOINT,
                OPENAI_CHAT_ENDPOINT => OPENAI_CHAT_ENDPOINT,
                "/custom" => OPENAI_CHAT_ENDPOINT,
                value => {
                    return Err(Error::Config(format!(
                        "unsupported v0.0.49 OpenAI endpoint: {value}"
                    )))
                }
            };
            let protocol = detected.unwrap_or(configured);
            let use_full_url = detected.is_some() || openai_endpoint.trim() == "/custom";
            Ok((base_url, protocol.into(), use_full_url))
        }
    }
}

fn openai_protocol_from_url(value: &str) -> Option<&'static str> {
    let url = reqwest::Url::parse(value).ok()?;
    let path = url.path().trim_end_matches('/');
    if path.to_ascii_lowercase().ends_with("/responses") {
        Some(OPENAI_RESPONSES_ENDPOINT)
    } else if path.to_ascii_lowercase().ends_with("/chat/completions") {
        Some(OPENAI_CHAT_ENDPOINT)
    } else {
        None
    }
}

fn url_path_ends_with(value: &str, suffix: &str) -> bool {
    reqwest::Url::parse(value).is_ok_and(|url| {
        url.path()
            .trim_end_matches('/')
            .to_ascii_lowercase()
            .ends_with(suffix)
    })
}

fn enabled_json_object(enabled: bool, value: &str) -> Result<serde_json::Value> {
    if enabled {
        json_object(value)
    } else {
        Ok(serde_json::json!({}))
    }
}

fn json_object(value: &str) -> Result<serde_json::Value> {
    if value.trim().is_empty() {
        return Ok(serde_json::json!({}));
    }
    let value: serde_json::Value = serde_json::from_str(value)?;
    if value.is_object() {
        Ok(value)
    } else {
        Err(Error::Config(
            "v0.0.49 model JSON fields must be objects".into(),
        ))
    }
}

fn positive(value: u64) -> Option<u64> {
    (value > 0).then_some(value)
}

fn optional_string(value: String) -> Option<String> {
    (!value.trim().is_empty()).then_some(value)
}
