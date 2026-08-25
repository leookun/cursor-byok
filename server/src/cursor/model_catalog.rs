use std::collections::HashMap;

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
    model::{format_token_count, parse_token_count, ProviderModel},
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
    #[prost(int32, optional, tag = "15")]
    context_token_limit: Option<i32>,
    #[prost(int32, optional, tag = "16")]
    context_token_limit_for_max_mode: Option<i32>,
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

fn configured_context(model: &ProviderModel) -> Option<(String, String)> {
    let tokens = model.context_window_tokens?;
    CONTEXTS
        .into_iter()
        .find(|(value, _)| parse_token_count(value) == Some(tokens))
        .map(|(value, display_name)| (value.to_owned(), display_name.to_owned()))
        .or_else(|| Some((tokens.to_string(), format_token_count(tokens))))
}

fn context_options(model: &ProviderModel) -> Vec<(String, String)> {
    let configured = configured_context(model);
    let mut contexts =
        Vec::with_capacity(CONTEXTS.len() + if configured.is_some() { 1 } else { 0 });
    if let Some(context) = configured.as_ref() {
        contexts.push(context.clone());
    }
    contexts.extend(
        CONTEXTS
            .into_iter()
            .filter(|(value, _)| {
                configured.as_ref().map_or(true, |(configured_value, _)| {
                    configured_value.as_str() != *value
                })
            })
            .map(|(value, display_name)| (value.to_owned(), display_name.to_owned())),
    );
    contexts
}

pub async fn available_models(
    State(registry): State<CursorSessionRegistry>,
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let models = registry.store().provider_models(true).await?;
    let provider_names = registry
        .store()
        .providers()
        .await?
        .into_iter()
        .map(|provider| (provider.provider_id, provider.name))
        .collect::<HashMap<_, _>>();
    tracing::info!(
        model_count = models.len(),
        "appending BYOK models to Cursor AvailableModels"
    );
    let available_models = models
        .iter()
        .map(|model| {
            let provider_name = provider_names.get(&model.provider_id).ok_or_else(|| {
                Error::Config(format!(
                    "provider {} for model {} does not exist",
                    model.provider_id, model.model_hash
                ))
            })?;
            Ok(available_model(model, provider_name))
        })
        .collect::<Result<Vec<_>>>()?;
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
    let models = registry.store().provider_models(true).await?;
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

fn available_model(model: &ProviderModel, provider_name: &str) -> AvailableModel {
    let contexts = context_options(model);
    let default_context_name = configured_context(model)
        .map(|(_, display_name)| display_name)
        .unwrap_or_else(|| "200K".into());
    let context_token_limit = model
        .context_window_tokens
        .map(|tokens| tokens.min(i32::MAX as u64) as i32);
    let variants = model_variants(model, &contexts);
    let legacy_slugs = variants
        .iter()
        .filter_map(|variant| variant.legacy_slug.clone())
        .collect();
    let tooltip = model_tooltip(model, &default_context_name, "high", false);
    AvailableModel {
        name: model.model_hash.clone(),
        default_on: true,
        supports_agent: Some(true),
        degradation_status: Some(0),
        tooltip_data: Some(tooltip.clone()),
        supports_thinking: Some(true),
        supports_images: Some(true),
        supports_max_mode: Some(true),
        context_token_limit,
        context_token_limit_for_max_mode: context_token_limit,
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
            label: provider_name.into(),
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

fn model_variants(model: &ProviderModel, contexts: &[(String, String)]) -> Vec<ModelVariant> {
    let default_context = configured_context(model)
        .map(|(value, _)| value)
        .unwrap_or_else(|| DEFAULT_CONTEXT.to_owned());
    let mut variants = Vec::with_capacity(contexts.len() * EFFORTS.len() * 2);
    for (context, context_name) in contexts {
        for (effort, effort_name) in EFFORTS {
            for fast in [false, true] {
                variants.push(model_variant(
                    model,
                    context,
                    context_name,
                    &default_context,
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
    model: &ProviderModel,
    context: &str,
    context_name: &str,
    default_context: &str,
    effort: &str,
    effort_name: &str,
    fast: bool,
) -> ModelVariant {
    let mut suffix = Vec::with_capacity(3);
    if context != DEFAULT_CONTEXT {
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
        tooltip_data: Some(model_tooltip(model, context_name, effort, fast)),
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

fn model_tooltip(
    model: &ProviderModel,
    context_name: &str,
    effort: &str,
    fast: bool,
) -> TooltipData {
    let fast_label = if fast { " (Fast)" } else { "" };
    TooltipData {
        markdown_content: Some(format!(
            "**{}{fast_label}**<br /><br />{context_name} context window<br /><br />*Version: {effort} effort*",
            model.display_name
        )),
    }
}

fn usable_model(model: &ProviderModel) -> agent::ModelDetails {
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
        let model = ProviderModel {
            model_hash: "33ceed20".into(),
            provider_id: 1,
            model_id: "deepseek-v4-flash".into(),
            display_name: "DeepSeek V4 Flash".into(),
            endpoint_type: crate::model::ProviderType::OpenAiResponses,
            request_url: String::new(),
            enabled: true,
            sort_order: 0,
            context_window_tokens: Some(272_000),
            max_output_tokens: None,
            reasoning_enabled: false,
            reasoning_effort: None,
            supports_image_generation: false,
            created_at_ms: 0,
            updated_at_ms: 0,
        };

        let mapped = available_model(&model, "OpenRouter");
        assert_eq!(mapped.name, "33ceed20");
        assert!(mapped.default_on);
        assert_eq!(mapped.supports_agent, Some(true));
        assert_eq!(mapped.degradation_status, Some(0));
        assert_eq!(mapped.supports_thinking, Some(true));
        assert_eq!(mapped.supports_images, Some(true));
        assert_eq!(mapped.supports_max_mode, Some(true));
        assert_eq!(mapped.context_token_limit, Some(272_000));
        assert_eq!(mapped.context_token_limit_for_max_mode, Some(272_000));
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
        assert_eq!(context_values, ["272000", "200k", "356k", "800k", "1m"]);
        let custom_context = context
            .parameter_type
            .as_ref()
            .unwrap()
            .enum_parameter
            .as_ref()
            .unwrap()
            .values
            .iter()
            .find(|value| value.value == "272000")
            .unwrap();
        assert_eq!(custom_context.display_name.as_deref(), Some("272K"));
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
        assert_eq!(mapped.model_picker_badges[0].label, "OpenRouter");
        assert!(!mapped.model_picker_badges[0].dismiss_on_selection);
        let default = mapped
            .variants
            .iter()
            .find(|variant| variant.is_default_non_max_config == Some(true))
            .unwrap();
        assert_eq!(
            default.variant_string_representation.as_deref(),
            Some("33ceed20[context=272000,effort=high,fast=false]")
        );
        assert_eq!(mapped.vendor.unwrap().display_name, "Cursor");
        assert!(usable_model(&model).thinking_details.is_some());
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
