use axum::{
    body::{Body, Bytes},
    extract::{Extension, State},
    http::{header, HeaderValue, Request, Response, StatusCode},
};
use bytes::{BufMut, BytesMut};
use prost::Message;

use crate::{
    cursor::{
        proto::agent::v1 as agent,
        proxy::{self, CursorProxy},
        CursorSessionRegistry,
    },
    model::{
        context_label, is_gpt56_long_context_model, is_grok_model,
        parse_token_count, GPT56_DEFAULT_CONTEXT_TOKENS, GPT56_LONG_CONTEXT_TOKENS, ModelConfig,
        ModelType,
    },
    Error, Result,
};

#[derive(Clone, PartialEq, Message)]
struct AvailableModelsAddition {
    #[prost(string, repeated, tag = "1")]
    model_names: Vec<String>,
    #[prost(message, repeated, tag = "2")]
    models: Vec<AvailableModel>,
}

#[derive(Clone, PartialEq, Message)]
struct AvailableModel {
    #[prost(string, tag = "1")]
    name: String,
    #[prost(bool, tag = "2")]
    default_on: bool,
    #[prost(bool, optional, tag = "5")]
    supports_agent: Option<bool>,
    #[prost(int32, optional, tag = "6")]
    degradation_status: Option<i32>,
    #[prost(message, optional, tag = "8")]
    tooltip_data: Option<TooltipData>,
    #[prost(bool, optional, tag = "9")]
    supports_thinking: Option<bool>,
    #[prost(bool, optional, tag = "10")]
    supports_images: Option<bool>,
    #[prost(bool, optional, tag = "14")]
    supports_max_mode: Option<bool>,
    #[prost(string, optional, tag = "17")]
    client_display_name: Option<String>,
    #[prost(string, optional, tag = "18")]
    server_model_name: Option<String>,
    #[prost(bool, optional, tag = "19")]
    supports_non_max_mode: Option<bool>,
    #[prost(message, optional, tag = "20")]
    tooltip_data_for_max_mode: Option<TooltipData>,
    #[prost(bool, optional, tag = "21")]
    is_recommended_for_background_composer: Option<bool>,
    #[prost(bool, optional, tag = "22")]
    supports_plan_mode: Option<bool>,
    #[prost(string, optional, tag = "24")]
    inputbox_short_model_name: Option<String>,
    #[prost(bool, optional, tag = "25")]
    supports_sandboxing: Option<bool>,
    #[prost(bool, optional, tag = "26")]
    supports_cmd_k: Option<bool>,
    #[prost(message, repeated, tag = "29")]
    parameter_definitions: Vec<ModelParameterDefinition>,
    #[prost(message, repeated, tag = "30")]
    variants: Vec<ModelVariant>,
    #[prost(string, repeated, tag = "36")]
    legacy_slugs: Vec<String>,
    #[prost(int32, optional, tag = "38")]
    named_model_section_index: Option<i32>,
    #[prost(string, optional, tag = "41")]
    vendor_name: Option<String>,
    #[prost(message, optional, tag = "42")]
    vendor: Option<AvailableModelVendor>,
    #[prost(message, repeated, tag = "48")]
    model_picker_badges: Vec<ModelPickerBadge>,
}

#[derive(Clone, PartialEq, Message)]
struct TooltipData {
    #[prost(string, optional, tag = "7")]
    markdown_content: Option<String>,
}

#[derive(Clone, PartialEq, Message)]
struct ModelParameterDefinition {
    #[prost(string, tag = "1")]
    id: String,
    #[prost(string, tag = "2")]
    name: String,
    #[prost(string, optional, tag = "3")]
    markdown_tooltip: Option<String>,
    #[prost(message, optional, tag = "4")]
    parameter_type: Option<ModelParameterType>,
    #[prost(bool, optional, tag = "5")]
    is_cycleable_by_hotkey: Option<bool>,
}

#[derive(Clone, PartialEq, Message)]
struct ModelParameterType {
    #[prost(message, optional, tag = "1")]
    boolean_parameter: Option<BooleanParameter>,
    #[prost(message, optional, tag = "2")]
    enum_parameter: Option<EnumParameter>,
}

#[derive(Clone, PartialEq, Message)]
struct BooleanParameter {
    #[prost(message, repeated, tag = "1")]
    values: Vec<BooleanParameterValue>,
}

#[derive(Clone, PartialEq, Message)]
struct BooleanParameterValue {
    #[prost(string, tag = "1")]
    value: String,
    #[prost(string, optional, tag = "2")]
    display_name: Option<String>,
    #[prost(bool, optional, tag = "3")]
    increases_model_cost: Option<bool>,
}

#[derive(Clone, PartialEq, Message)]
struct EnumParameter {
    #[prost(message, repeated, tag = "1")]
    values: Vec<EnumParameterValue>,
}

#[derive(Clone, PartialEq, Message)]
struct EnumParameterValue {
    #[prost(string, tag = "1")]
    value: String,
    #[prost(string, optional, tag = "2")]
    display_name: Option<String>,
}

#[derive(Clone, PartialEq, Message)]
struct ModelVariant {
    #[prost(message, repeated, tag = "1")]
    parameter_values: Vec<ModelParameterValue>,
    #[prost(string, tag = "2")]
    display_name: String,
    #[prost(bool, tag = "3")]
    is_max_mode: bool,
    #[prost(bool, optional, tag = "4")]
    is_default_max_config: Option<bool>,
    #[prost(bool, optional, tag = "5")]
    is_default_non_max_config: Option<bool>,
    #[prost(message, optional, tag = "6")]
    tooltip_data: Option<TooltipData>,
    #[prost(string, optional, tag = "8")]
    display_name_outside_picker: Option<String>,
    #[prost(string, optional, tag = "9")]
    variant_string_representation: Option<String>,
    #[prost(string, optional, tag = "11")]
    legacy_slug: Option<String>,
}

#[derive(Clone, PartialEq, Message)]
struct ModelParameterValue {
    #[prost(string, tag = "1")]
    id: String,
    #[prost(string, tag = "2")]
    value: String,
}

#[derive(Clone, PartialEq, Message)]
struct ModelPickerBadge {
    #[prost(string, tag = "1")]
    label: String,
    #[prost(int32, tag = "2")]
    variant: i32,
    #[prost(bool, tag = "3")]
    dismiss_on_selection: bool,
}

#[derive(Clone, PartialEq, Message)]
struct AvailableModelVendor {
    #[prost(int32, tag = "1")]
    id: i32,
    #[prost(string, tag = "2")]
    display_name: String,
}

#[derive(Clone, PartialEq, Message)]
struct UsableModelsAddition {
    #[prost(message, repeated, tag = "1")]
    models: Vec<agent::ModelDetails>,
}

const CONTEXTS: [(&str, &str); 4] = [
    ("200k", "200K"),
    ("356k", "356K"),
    ("800k", "800K"),
    ("1m", "1M"),
];
const EFFORTS: [(&str, &str); 5] = [
    ("low", "Low"),
    ("medium", "Medium"),
    ("high", "High"),
    ("xhigh", "Extra High"),
    ("max", "Max"),
];
const DEFAULT_CONTEXT: &str = "200k";

fn context_options(model: &ModelConfig) -> Vec<(String, String)> {
    if is_gpt56_long_context_model(&model.model_id) {
        return vec![
            context_label(GPT56_DEFAULT_CONTEXT_TOKENS),
            context_label(GPT56_LONG_CONTEXT_TOKENS),
        ];
    }
    if is_grok_model(&model.model_id, &model.base_url, &model.tooltip_data) {
        let mut contexts = Vec::new();
        if let Some(tokens) = model.context_window_tokens {
            contexts.push(context_label(tokens));
        }
        for (value, display_name) in CONTEXTS {
            let tokens = parse_token_count(value).unwrap_or(0);
            let allowed = model
                .context_window_tokens
                .map_or(true, |max| tokens > 0 && tokens <= max);
            if allowed
                && !contexts
                    .iter()
                    .any(|(existing, _)| parse_token_count(existing) == Some(tokens))
            {
                contexts.push((value.to_owned(), display_name.to_owned()));
            }
        }
        if contexts.is_empty() {
            contexts.extend(
                CONTEXTS
                    .into_iter()
                    .map(|(value, display_name)| (value.to_owned(), display_name.to_owned())),
            );
        }
        return contexts;
    }
    let mut contexts = CONTEXTS
        .into_iter()
        .map(|(value, display_name)| (value.to_owned(), display_name.to_owned()))
        .collect::<Vec<_>>();
    if let Some(tokens) = model.context_window_tokens {
        let duplicate = contexts
            .iter()
            .any(|(existing, _)| parse_token_count(existing) == Some(tokens));
        if !duplicate {
            let (value, display) = context_label(tokens);
            contexts.push((value, format!("{display} (Custom)")));
        }
    }
    contexts
}

fn default_context_value(model: &ModelConfig, contexts: &[(String, String)]) -> String {
    if is_gpt56_long_context_model(&model.model_id) {
        let tokens = model
            .context_window_tokens
            .filter(|tokens| *tokens >= GPT56_LONG_CONTEXT_TOKENS)
            .unwrap_or(GPT56_DEFAULT_CONTEXT_TOKENS);
        return matching_context_value(contexts, tokens)
            .unwrap_or_else(|| context_label(GPT56_DEFAULT_CONTEXT_TOKENS).0);
    }
    if let Some(tokens) = model.context_window_tokens {
        if let Some(value) = matching_context_value(contexts, tokens) {
            return value;
        }
    }
    contexts
        .first()
        .map(|(value, _)| value.clone())
        .unwrap_or_else(|| DEFAULT_CONTEXT.to_owned())
}

fn matching_context_value(contexts: &[(String, String)], tokens: u64) -> Option<String> {
    contexts
        .iter()
        .find(|(value, _)| parse_token_count(value) == Some(tokens))
        .map(|(value, _)| value.clone())
}

pub async fn available_models(
    State(registry): State<CursorSessionRegistry>,
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let models = registry.store().models().await?;
    tracing::info!(
        model_count = models.len(),
        "appending BYOK models to Cursor AvailableModels"
    );
    let available_models = models.iter().map(available_model).collect::<Vec<_>>();
    let local = AvailableModelsAddition {
        model_names: models
            .iter()
            .map(|model| model.model_hash.clone())
            .collect(),
        models: available_models,
    }
    .encode_to_vec();
    match proxy::forward_buffered(&proxy, request).await {
        Ok(upstream) => merge_response(upstream, local),
        Err(error) => {
            tracing::warn!(%error, "Cursor AvailableModels upstream unavailable; using local catalog");
            Ok(local_response(local))
        }
    }
}

pub async fn usable_models(
    State(registry): State<CursorSessionRegistry>,
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let models = registry.store().models().await?;
    tracing::info!(
        model_count = models.len(),
        "appending BYOK models to Cursor GetUsableModels"
    );
    let local = UsableModelsAddition {
        models: models.iter().map(usable_model).collect(),
    }
    .encode_to_vec();
    match proxy::forward_buffered(&proxy, request).await {
        Ok(upstream) => merge_response(upstream, local),
        Err(error) => {
            tracing::warn!(%error, "Cursor GetUsableModels upstream unavailable; using local catalog");
            Ok(local_response(local))
        }
    }
}

fn merge_response(upstream: proxy::BufferedResponse, extra: Vec<u8>) -> Result<Response<Body>> {
    if !upstream.status.is_success() {
        tracing::warn!(status = %upstream.status, "Cursor model catalog upstream rejected request; using local catalog");
        return Ok(local_response(extra));
    }
    let (framed, payload) = unary_payload(&upstream.body)?;
    let body = if framed {
        let mut merged = BytesMut::with_capacity(5 + payload.len() + extra.len());
        merged.put_u8(0);
        merged.put_u32((payload.len() + extra.len()) as u32);
        merged.extend_from_slice(payload);
        merged.extend_from_slice(&extra);
        merged.freeze()
    } else {
        let mut merged = BytesMut::with_capacity(payload.len() + extra.len());
        merged.extend_from_slice(payload);
        merged.extend_from_slice(&extra);
        merged.freeze()
    };
    Ok(upstream.with_body(body))
}

fn local_response(body: Vec<u8>) -> Response<Body> {
    let mut response = Response::new(Body::from(body));
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/proto"),
    );
    response
}

fn unary_payload(body: &Bytes) -> Result<(bool, &[u8])> {
    if body.len() < 5 {
        return Ok((false, body));
    }
    let flags = body[0];
    let length = u32::from_be_bytes([body[1], body[2], body[3], body[4]]) as usize;
    if length != body.len() - 5 {
        return Ok((false, body));
    }
    if flags != 0 {
        return Err(Error::Protocol(format!(
            "cannot merge compressed or terminal model catalog frame: flags={flags}"
        )));
    }
    Ok((true, &body[5..]))
}

fn available_model(model: &ModelConfig) -> AvailableModel {
    let contexts = context_options(model);
    let default_context = default_context_value(model, &contexts);
    let variants = model_variants(model, &contexts, &default_context);
    let legacy_slugs = variants
        .iter()
        .filter_map(|variant| variant.legacy_slug.clone())
        .collect();
    let tooltip = model_tooltip(model);
    AvailableModel {
        name: model.model_hash.clone(),
        default_on: true,
        supports_agent: Some(true),
        degradation_status: Some(0),
        tooltip_data: Some(tooltip.clone()),
        supports_thinking: Some(true),
        supports_images: Some(true),
        supports_max_mode: Some(true),
        client_display_name: Some(model.display_name.clone()),
        server_model_name: Some(model.model_hash.clone()),
        supports_non_max_mode: Some(true),
        tooltip_data_for_max_mode: Some(tooltip),
        is_recommended_for_background_composer: Some(false),
        supports_plan_mode: Some(true),
        inputbox_short_model_name: Some(model.display_name.clone()),
        supports_sandboxing: Some(true),
        supports_cmd_k: Some(false),
        parameter_definitions: model_parameters(&contexts),
        variants,
        legacy_slugs,
        named_model_section_index: Some(1),
        vendor_name: Some("cursor".into()),
        vendor: Some(AvailableModelVendor {
            id: 6,
            display_name: "Cursor".into(),
        }),
        model_picker_badges: vec![ModelPickerBadge {
            label: match model.model_type {
                ModelType::OpenAi => "OpenAI".into(),
                ModelType::Anthropic => "Anthropic".into(),
            },
            variant: 1,
            dismiss_on_selection: false,
        }],
    }
}

fn model_parameters(contexts: &[(String, String)]) -> Vec<ModelParameterDefinition> {
    vec![
        ModelParameterDefinition {
            id: "context".into(),
            name: "Context".into(),
            markdown_tooltip: Some("Context size used to trigger conversation compaction.".into()),
            parameter_type: Some(ModelParameterType {
                boolean_parameter: None,
                enum_parameter: Some(EnumParameter {
                    values: contexts
                        .iter()
                        .map(|(value, display_name)| EnumParameterValue {
                            value: value.clone(),
                            display_name: Some(display_name.clone()),
                        })
                        .collect(),
                }),
            }),
            is_cycleable_by_hotkey: Some(false),
        },
        ModelParameterDefinition {
            id: "effort".into(),
            name: "Effort".into(),
            markdown_tooltip: Some("Effort the model uses to generate its response.".into()),
            parameter_type: Some(ModelParameterType {
                boolean_parameter: None,
                enum_parameter: Some(EnumParameter {
                    values: EFFORTS
                        .into_iter()
                        .map(|(value, display_name)| EnumParameterValue {
                            value: value.into(),
                            display_name: Some(display_name.into()),
                        })
                        .collect(),
                }),
            }),
            is_cycleable_by_hotkey: Some(true),
        },
        ModelParameterDefinition {
            id: "fast".into(),
            name: "Fast".into(),
            markdown_tooltip: Some("Significantly faster but consumes more usage".into()),
            parameter_type: Some(ModelParameterType {
                boolean_parameter: Some(BooleanParameter {
                    values: vec![
                        BooleanParameterValue {
                            value: "false".into(),
                            display_name: None,
                            increases_model_cost: None,
                        },
                        BooleanParameterValue {
                            value: "true".into(),
                            display_name: Some("Fast".into()),
                            increases_model_cost: Some(true),
                        },
                    ],
                }),
                enum_parameter: None,
            }),
            is_cycleable_by_hotkey: Some(false),
        },
    ]
}

fn model_variants(
    model: &ModelConfig,
    contexts: &[(String, String)],
    default_context: &str,
) -> Vec<ModelVariant> {
    let mut variants = Vec::with_capacity(contexts.len() * EFFORTS.len() * 2);
    for (context, context_name) in contexts {
        for (effort, effort_name) in EFFORTS {
            for fast in [false, true] {
                variants.push(model_variant(
                    model,
                    context,
                    context_name,
                    default_context,
                    effort,
                    effort_name,
                    fast,
                ));
            }
        }
    }
    variants
}

fn model_variant(
    model: &ModelConfig,
    context: &str,
    context_name: &str,
    default_context: &str,
    effort: &str,
    effort_name: &str,
    fast: bool,
) -> ModelVariant {
    let mut suffix = Vec::with_capacity(3);
    if context != default_context {
        suffix.push(context_name);
    }
    suffix.push(effort_name);
    if fast {
        suffix.push("Fast");
    }
    let suffix = suffix.join(" ");
    let display_name = format!(
        "{} <span style=\"color: var(--cursor-text-tertiary);\">{suffix}</span>",
        model.display_name
    );
    let is_default = context == default_context && effort == "high" && !fast;
    ModelVariant {
        parameter_values: vec![
            ModelParameterValue {
                id: "context".into(),
                value: context.into(),
            },
            ModelParameterValue {
                id: "effort".into(),
                value: effort.into(),
            },
            ModelParameterValue {
                id: "fast".into(),
                value: fast.to_string(),
            },
        ],
        display_name: display_name.clone(),
        is_max_mode: false,
        is_default_max_config: is_default.then_some(true),
        is_default_non_max_config: is_default.then_some(true),
        tooltip_data: Some(model_tooltip(model)),
        display_name_outside_picker: Some(display_name),
        variant_string_representation: Some(format!(
            "{}[context={context},effort={effort},fast={fast}]",
            model.model_hash
        )),
        legacy_slug: Some(format!(
            "{}-{context}-{effort}{}",
            model.model_hash,
            if fast { "-fast" } else { "" }
        )),
    }
}

fn model_tooltip(model: &ModelConfig) -> TooltipData {
    TooltipData {
        markdown_content: Some(model.tooltip_data.clone()),
    }
}

fn usable_model(model: &ModelConfig) -> agent::ModelDetails {
    agent::ModelDetails {
        model_id: model.model_hash.clone(),
        display_model_id: model.model_hash.clone(),
        display_name: model.display_name.clone(),
        display_name_short: model.display_name.clone(),
        thinking_details: Some(agent::ThinkingDetails::default()),
        ..Default::default()
    }
}

#[cfg(test)]
mod tests {
    use axum::body::{to_bytes, Bytes};

    use super::*;

    #[test]
    fn maps_byok_model_to_cursor_catalog_fields() {
        let model = ModelConfig {
            model_hash: "33ceed20".into(),
            sort_order: 0,
            display_name: "DeepSeek V4 Flash".into(),
            model_type: ModelType::OpenAi,
            base_url: "https://example.com/v1/responses".into(),
            use_full_url: true,
            api_key: "secret".into(),
            tooltip_data: "DeepSeek V4 Flash".into(),
            model_id: "deepseek-v4-flash".into(),
            reasoning_effort: None,
            openai_endpoint: "/v1/responses".into(),
            openai_extra_params_enabled: false,
            openai_extra_params: serde_json::json!({}),
            custom_headers_enabled: false,
            custom_headers: serde_json::json!({}),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: serde_json::json!({}),
            context_window_tokens: Some(272_000),
            max_completion_tokens: None,
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
            created_at_ms: 0,
            updated_at_ms: 0,
        };

        let mapped = available_model(&model);
        assert_eq!(mapped.name, "33ceed20");
        assert!(mapped.default_on);
        assert_eq!(mapped.supports_agent, Some(true));
        assert_eq!(mapped.degradation_status, Some(0));
        assert_eq!(mapped.supports_thinking, Some(true));
        assert_eq!(mapped.supports_images, Some(true));
        assert_eq!(mapped.supports_max_mode, Some(true));
        assert_eq!(mapped.supports_non_max_mode, Some(true));
        assert_eq!(mapped.supports_plan_mode, Some(true));
        assert_eq!(mapped.supports_sandboxing, Some(true));
        assert_eq!(mapped.supports_cmd_k, Some(false));
        assert_eq!(
            mapped.client_display_name.as_deref(),
            Some("DeepSeek V4 Flash")
        );
        assert_eq!(mapped.server_model_name.as_deref(), Some("33ceed20"));
        assert_eq!(mapped.named_model_section_index, Some(1));
        assert_eq!(
            mapped
                .tooltip_data
                .as_ref()
                .and_then(|tooltip| tooltip.markdown_content.as_deref()),
            Some("DeepSeek V4 Flash")
        );
        assert_eq!(mapped.vendor_name.as_deref(), Some("cursor"));
        assert_eq!(mapped.parameter_definitions.len(), 3);
        let context = mapped
            .parameter_definitions
            .iter()
            .find(|parameter| parameter.id == "context")
            .unwrap();
        let context_values = context
            .parameter_type
            .as_ref()
            .unwrap()
            .enum_parameter
            .as_ref()
            .unwrap()
            .values
            .iter()
            .map(|value| value.value.as_str())
            .collect::<Vec<_>>();
        assert_eq!(context_values, ["200k", "356k", "800k", "1m", "272k"]);
        let custom_context = context
            .parameter_type
            .as_ref()
            .unwrap()
            .enum_parameter
            .as_ref()
            .unwrap()
            .values
            .iter()
            .find(|value| value.value == "272k")
            .unwrap();
        assert_eq!(
            custom_context.display_name.as_deref(),
            Some("272K (Custom)")
        );
        let effort = mapped
            .parameter_definitions
            .iter()
            .find(|parameter| parameter.id == "effort")
            .unwrap();
        assert!(effort
            .parameter_type
            .as_ref()
            .unwrap()
            .enum_parameter
            .as_ref()
            .unwrap()
            .values
            .iter()
            .any(|value| value.value == "max"));
        assert_eq!(mapped.variants.len(), 50);
        assert_eq!(mapped.legacy_slugs.len(), 50);
        assert_eq!(mapped.model_picker_badges.len(), 1);
        assert_eq!(mapped.model_picker_badges[0].label, "OpenAI");
        assert!(!mapped.model_picker_badges[0].dismiss_on_selection);
        let default = mapped
            .variants
            .iter()
            .find(|variant| variant.is_default_non_max_config == Some(true))
            .unwrap();
        assert_eq!(
            default.variant_string_representation.as_deref(),
            Some("33ceed20[context=272k,effort=high,fast=false]")
        );
        assert_eq!(mapped.vendor.unwrap().display_name, "Cursor");
        assert!(usable_model(&model).thinking_details.is_some());
    }

    #[test]
    fn gpt56_sol_defaults_to_272k_and_can_select_1m() {
        let model = ModelConfig {
            model_hash: "solhash".into(),
            sort_order: 0,
            display_name: "GPT-5.6 Sol".into(),
            model_type: ModelType::OpenAi,
            base_url: "https://chatgpt.com/backend-api/codex/responses".into(),
            use_full_url: true,
            api_key: "secret".into(),
            tooltip_data: "ChatGPT / OpenAI Codex".into(),
            model_id: "gpt-5.6-sol".into(),
            reasoning_effort: None,
            openai_endpoint: "/v1/responses".into(),
            openai_extra_params_enabled: false,
            openai_extra_params: serde_json::json!({}),
            custom_headers_enabled: false,
            custom_headers: serde_json::json!({}),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: serde_json::json!({}),
            context_window_tokens: Some(272_000),
            max_completion_tokens: Some(128_000),
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
            created_at_ms: 0,
            updated_at_ms: 0,
        };
        let mapped = available_model(&model);
        let context = mapped
            .parameter_definitions
            .iter()
            .find(|parameter| parameter.id == "context")
            .unwrap();
        let context_values = context
            .parameter_type
            .as_ref()
            .unwrap()
            .enum_parameter
            .as_ref()
            .unwrap()
            .values
            .iter()
            .map(|value| value.value.as_str())
            .collect::<Vec<_>>();
        assert_eq!(context_values, ["272k", "1m"]);
        let default = mapped
            .variants
            .iter()
            .find(|variant| variant.is_default_non_max_config == Some(true))
            .unwrap();
        assert_eq!(
            default.variant_string_representation.as_deref(),
            Some("solhash[context=272k,effort=high,fast=false]")
        );

        let mut enabled = model.clone();
        enabled.context_window_tokens = Some(1_000_000);
        let enabled_default = available_model(&enabled)
            .variants
            .into_iter()
            .find(|variant| variant.is_default_non_max_config == Some(true))
            .unwrap();
        assert_eq!(
            enabled_default.variant_string_representation.as_deref(),
            Some("solhash[context=1m,effort=high,fast=false]")
        );
    }

    #[test]
    fn grok_catalog_uses_discovered_context_window() {
        let model = ModelConfig {
            model_hash: "grokhash".into(),
            sort_order: 0,
            display_name: "Grok 4.6".into(),
            model_type: ModelType::OpenAi,
            base_url: "https://api.x.ai/v1".into(),
            use_full_url: false,
            api_key: "secret".into(),
            tooltip_data: "xAI Grok".into(),
            model_id: "grok-4.6".into(),
            reasoning_effort: None,
            openai_endpoint: "/v1/chat/completions".into(),
            openai_extra_params_enabled: false,
            openai_extra_params: serde_json::json!({}),
            custom_headers_enabled: false,
            custom_headers: serde_json::json!({}),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: serde_json::json!({}),
            context_window_tokens: Some(256_000),
            max_completion_tokens: Some(16_384),
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
            created_at_ms: 0,
            updated_at_ms: 0,
        };
        let mapped = available_model(&model);
        let context = mapped
            .parameter_definitions
            .iter()
            .find(|parameter| parameter.id == "context")
            .unwrap();
        let context_values = context
            .parameter_type
            .as_ref()
            .unwrap()
            .enum_parameter
            .as_ref()
            .unwrap()
            .values
            .iter()
            .map(|value| value.value.as_str())
            .collect::<Vec<_>>();
        assert_eq!(context_values[0], "256k");
        assert!(context_values.contains(&"200k"));
        assert!(!context_values.contains(&"1m"));
        let default = mapped
            .variants
            .iter()
            .find(|variant| variant.is_default_non_max_config == Some(true))
            .unwrap();
        assert_eq!(
            default.variant_string_representation.as_deref(),
            Some("grokhash[context=256k,effort=high,fast=false]")
        );
    }

    #[tokio::test]
    async fn appends_models_without_reencoding_official_fields() {
        // Unknown field 99 = 7 stands in for every official field this service does not know.
        let official = Bytes::from_static(&[0x98, 0x06, 0x07]);
        let addition = AvailableModelsAddition {
            model_names: vec!["f246010a".into()],
            models: Vec::new(),
        }
        .encode_to_vec();
        let response = merge_response(
            proxy::BufferedResponse {
                status: axum::http::StatusCode::OK,
                headers: Default::default(),
                body: official.clone(),
            },
            addition.clone(),
        )
        .unwrap();
        let merged = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        assert_eq!(&merged[..official.len()], official.as_ref());
        assert_eq!(&merged[official.len()..], addition);
    }

    #[tokio::test]
    async fn updates_connect_length_when_catalog_is_framed() {
        let official = [0x98, 0x06, 0x07];
        let mut framed = BytesMut::new();
        framed.put_u8(0);
        framed.put_u32(official.len() as u32);
        framed.extend_from_slice(&official);
        let mut headers = axum::http::HeaderMap::new();
        headers.insert(axum::http::header::CONTENT_LENGTH, framed.len().into());
        let response = merge_response(
            proxy::BufferedResponse {
                status: axum::http::StatusCode::OK,
                headers,
                body: framed.freeze(),
            },
            vec![0x0a, 0x01, b'x'],
        )
        .unwrap();
        assert_eq!(response.headers()[axum::http::header::CONTENT_LENGTH], "11");
        let merged = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        assert_eq!(u32::from_be_bytes(merged[1..5].try_into().unwrap()), 6);
        assert_eq!(&merged[5..8], &official);
    }

    #[tokio::test]
    async fn returns_local_catalog_when_upstream_rejects_request() {
        let local = AvailableModelsAddition {
            model_names: vec!["f246010a".into()],
            models: Vec::new(),
        }
        .encode_to_vec();
        let response = merge_response(
            proxy::BufferedResponse {
                status: axum::http::StatusCode::UNAUTHORIZED,
                headers: Default::default(),
                body: Bytes::from_static(b"not logged in"),
            },
            local.clone(),
        )
        .unwrap();
        assert_eq!(response.status(), axum::http::StatusCode::OK);
        assert_eq!(
            response.headers()[axum::http::header::CONTENT_TYPE],
            "application/proto"
        );
        assert_eq!(
            to_bytes(response.into_body(), usize::MAX).await.unwrap(),
            local
        );
    }
}
