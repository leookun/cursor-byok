use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex},
    time::Instant,
};


use base64::{engine::general_purpose::STANDARD, Engine};
use futures_util::StreamExt;
use reqwest::header::{HeaderName, HeaderValue};
use serde::{Deserialize, Serialize};
use tokio_util::sync::CancellationToken;
use url::Url;

use super::ads::{
    AdDismissalInput, AdRuntime, ADS_ENDPOINT, APP_VERSION_HEADER, DEVICE_ID_HEADER,
    DISABLED_AD_IDS_HEADER, LANGUAGE_HEADER, OS_HEADER,
};

use crate::{
    harness::CursorHarness,
    model::{
        ContentPart, CursorRunTraceArtifact, CursorRunTraceSummary, LlmCallRequest,
        LlmCallResponseChunk, LlmCallSummary, ModelConfig, ModelConfigInput, ModelInvocation,
        ModelRequest, ModelSpec, ModelType, Overview, ProjectedContent, ProjectedMessage,
        PromptSpec, ProviderType, Role,
    },
    provider::{ModelEvent, Provider},
    store::{
        DesktopSettings, PortSettings, ProxySettings, ProxySettingsInput, StatisticsStorage, Store,
        TabSettings,
    },
    Error, Result,
};

#[derive(Clone)]
pub struct ControlService {
    store: Store,
    cursor_harness: CursorHarness,
    provider: Arc<dyn Provider>,
    model_tests: Arc<Mutex<BTreeMap<String, CancellationToken>>>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct DiscoveredModels {
    pub models: Vec<DiscoveredModel>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct DiscoveredModel {
    pub id: String,
    pub context_window_tokens: Option<u64>,
}

#[derive(Clone, Debug, Serialize)]
pub struct LegacyModelImportResult {
    pub imported: usize,
    pub skipped: usize,
    pub total: usize,
}

#[derive(Clone, Debug, Serialize)]
pub struct LegacyModelImportPreview {
    pub source: String,
    pub total: usize,
    pub new_models: usize,
    pub existing_models: usize,
    pub models: Vec<LegacyModelImportPreviewItem>,
}

#[derive(Clone, Debug, Serialize)]
pub struct LegacyModelImportPreviewItem {
    pub model_hash: String,
    pub display_name: String,
    pub model_id: String,
    #[serde(rename = "type")]
    pub model_type: ModelType,
    pub existing: bool,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ModelDiscoveryInput {
    #[serde(rename = "type")]
    pub model_type: ModelType,
    pub base_url: String,
    pub api_key: String,
    #[serde(default)]
    pub custom_headers_enabled: bool,
    #[serde(default = "empty_json_object")]
    pub custom_headers: serde_json::Value,
}

fn empty_json_object() -> serde_json::Value {
    serde_json::json!({})
}

fn empty_json_object_ref() -> &'static serde_json::Value {
    static EMPTY: std::sync::OnceLock<serde_json::Value> = std::sync::OnceLock::new();
    EMPTY.get_or_init(empty_json_object)
}

#[derive(Clone, Debug, Serialize)]
pub struct ModelConnectivityResult {
    pub duration_ms: u64,
    pub first_text_ms: Option<u64>,
    pub output_tokens: u64,
    pub tokens_per_second: f64,
    pub tokens_estimated: bool,
    pub output: String,
}

#[derive(Clone, Debug, Serialize)]
pub struct CallDetail {
    pub call: CallSummary,
    pub request: Option<LlmCallRequest>,
    pub response_chunks: Vec<LlmCallResponseChunk>,
    pub cursor_trace: Option<CursorTraceDetail>,
}

#[derive(Clone, Debug, Serialize)]
pub struct CallSummary {
    #[serde(flatten)]
    pub call: LlmCallSummary,
    pub call_kind: &'static str,
    pub route: &'static str,
}

#[derive(Clone, Debug, Serialize)]
pub struct CursorTraceDetail {
    pub trace: CursorRunTraceSummary,
    pub artifacts: Vec<CursorTraceArtifactDetail>,
}

#[derive(Clone, Debug, Serialize)]
pub struct CursorTraceArtifactDetail {
    pub seq: i64,
    pub artifact_type: String,
    pub source: String,
    pub metadata: serde_json::Value,
    pub created_at_ms: i64,
    pub byte_count: usize,
    pub encoding: &'static str,
    pub data: String,
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize)]
pub struct ObservabilitySettings {
    pub detailed: bool,
}

impl ControlService {
    pub fn new(store: Store, provider: Arc<dyn Provider>) -> Result<Self> {
        Ok(Self {
            cursor_harness: CursorHarness::new(store.clone())?,
            store,
            provider,
            model_tests: Arc::new(Mutex::new(BTreeMap::new())),
        })
    }

    pub fn cursor_harness(&self) -> &CursorHarness {
        &self.cursor_harness
    }

    pub fn store(&self) -> &Store {
        &self.store
    }

    pub(super) async fn ads(
        &self,
        disabled_ad_ids: Option<&str>,
        language: &str,
    ) -> Result<AdRuntime> {
        let client = crate::network::client(&self.store).await?;
        let installation_id = self.store.installation_id().await?;
        let mut request = client
            .get(ADS_ENDPOINT)
            .header(DEVICE_ID_HEADER, installation_id)
            .header(OS_HEADER, std::env::consts::OS)
            .header(APP_VERSION_HEADER, env!("CARGO_PKG_VERSION"))
            .header(LANGUAGE_HEADER, language)
            .timeout(std::time::Duration::from_secs(5));
        if let Some(disabled_ad_ids) = disabled_ad_ids.filter(|value| !value.is_empty()) {
            request = request.header(DISABLED_AD_IDS_HEADER, disabled_ad_ids);
        }
        let response = request.send().await?;
        let status = response.status();
        if !status.is_success() {
            let message = response.text().await.unwrap_or_default();
            return Err(Error::Provider(format!(
                "advertisement service failed ({status}): {}",
                message.chars().take(200).collect::<String>()
            )));
        }
        response.json::<AdRuntime>().await?.into_menu_slots()
    }

    pub(super) async fn dismiss_ad(&self, ad_id: &str, input: &AdDismissalInput) -> Result<()> {
        let client = crate::network::client(&self.store).await?;
        let installation_id = self.store.installation_id().await?;
        let mut endpoint = Url::parse(ADS_ENDPOINT).map_err(|error| {
            Error::Config(format!("advertisement endpoint is invalid: {error}"))
        })?;
        endpoint.set_query(None);
        endpoint
            .path_segments_mut()
            .map_err(|_| Error::Config("advertisement endpoint cannot contain an ad id".into()))?
            .push(ad_id)
            .push("dismissals");
        let response = client
            .post(endpoint)
            .header(DEVICE_ID_HEADER, installation_id)
            .header(OS_HEADER, std::env::consts::OS)
            .header(APP_VERSION_HEADER, env!("CARGO_PKG_VERSION"))
            .json(input)
            .timeout(std::time::Duration::from_secs(5))
            .send()
            .await?;
        let status = response.status();
        if !status.is_success() {
            let message = response.text().await.unwrap_or_default();
            return Err(Error::Provider(format!(
                "advertisement dismissal failed ({status}): {}",
                message.chars().take(200).collect::<String>()
            )));
        }
        Ok(())
    }

    pub async fn models(&self) -> Result<Vec<ModelConfig>> {
        self.store.models().await
    }

    pub async fn overview(
        &self,
        start_ms: Option<i64>,
        end_ms: Option<i64>,
        model_hashes: Option<&str>,
    ) -> Result<Overview> {
        self.store.overview(start_ms, end_ms, model_hashes).await
    }

    pub async fn create_models(&self, models: &[ModelConfigInput]) -> Result<Vec<ModelConfig>> {
        self.store.create_models(models).await
    }

    pub async fn reorder_models(&self, model_hashes: &[String]) -> Result<Vec<ModelConfig>> {
        self.store.reorder_models(model_hashes).await
    }

    pub async fn delete_model(&self, model_hash: &str) -> Result<()> {
        self.store.delete_model(model_hash).await
    }

    pub async fn update_model(
        &self,
        model_hash: &str,
        input: &ModelConfigInput,
    ) -> Result<ModelConfig> {
        self.store.update_model(model_hash, input).await
    }

    pub async fn test_model(
        &self,
        model_hash: &str,
        test_id: &str,
    ) -> Result<ModelConnectivityResult> {
        let cancellation = CancellationToken::new();
        let cancellation = {
            let mut tests = self
                .model_tests
                .lock()
                .expect("model test registry mutex poisoned");
            tests
                .entry(test_id.to_owned())
                .or_insert_with(|| cancellation.clone())
                .clone()
        };
        let result = self.run_model_test(model_hash, cancellation).await;
        self.model_tests
            .lock()
            .expect("model test registry mutex poisoned")
            .remove(test_id);
        result
    }

    pub fn cancel_model_test(&self, test_id: &str) {
        let cancellation = {
            let mut tests = self
                .model_tests
                .lock()
                .expect("model test registry mutex poisoned");
            tests.entry(test_id.to_owned()).or_default().clone()
        };
        cancellation.cancel();
    }

    async fn run_model_test(
        &self,
        model_hash: &str,
        cancellation: CancellationToken,
    ) -> Result<ModelConnectivityResult> {
        const TEST_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(45);
        const TEST_PROMPT: &str = "Output the numbers 1 through 120 separated by a single space. No commas, no newlines, no explanation.";

        let configured = self
            .store
            .model(model_hash)
            .await?
            .ok_or_else(|| Error::RunNotFound(format!("model {model_hash}")))?;
        let mut model = ModelSpec::new(model_hash);
        configured.configure(&mut model);
        model.max_output_tokens = Some(configured.max_output_tokens().unwrap_or(65_536));
        let call_id = format!("model-test-{}", uuid::Uuid::new_v4());
        let invocation = ModelInvocation {
            call_id: call_id.clone(),
            run_id: call_id.clone(),
            conversation_id: call_id.clone(),
            provider_call_index: 0,
            request: ModelRequest {
                prompt: PromptSpec {
                    instructions: String::new(),
                    tools: Vec::new(),
                },
                model,
                history: vec![ProjectedMessage {
                    message_id: "connectivity-test".into(),
                    role: Role::User,
                    content: ProjectedContent::Parts(vec![ContentPart::Text {
                        text: TEST_PROMPT.into(),
                    }]),
                }],
            },
        };
        let started = Instant::now();
        let mut first_text_at = None;
        let mut output_tokens = None;
        let mut output = String::new();
        let stream = self.provider.stream(invocation, cancellation.clone());
        let completed = tokio::time::timeout(TEST_TIMEOUT, async {
            futures_util::pin_mut!(stream);
            let mut finished = false;
            while let Some(event) = stream.next().await {
                match event? {
                    ModelEvent::TextDelta(delta) => {
                        if first_text_at.is_none() && !delta.trim().is_empty() {
                            first_text_at = Some(Instant::now());
                        }
                        output.push_str(&delta);
                    }
                    ModelEvent::Usage(usage) => {
                        if let Some(tokens) = usage.output_tokens.filter(|tokens| *tokens > 0) {
                            output_tokens = Some(
                                output_tokens.map_or(tokens, |current: u64| current.max(tokens)),
                            );
                        }
                    }
                    ModelEvent::Done(_) => finished = true,
                    _ => {}
                }
            }
            if cancellation.is_cancelled() {
                return Err(Error::Cancelled);
            }
            if !finished {
                return Err(Error::Protocol(
                    "provider stream ended without Done during connectivity test".into(),
                ));
            }
            Ok(())
        })
        .await;
        match completed {
            Ok(result) => result?,
            Err(_) => {
                cancellation.cancel();
                self.store
                    .finish_llm_call(
                        &call_id,
                        "error",
                        None,
                        started.elapsed().as_millis().min(i64::MAX as u128) as i64,
                        Some("timeout"),
                        Some("model connectivity test timed out after 45 seconds"),
                    )
                    .await?;
                return Err(Error::Provider(
                    "model connectivity test timed out after 45 seconds".into(),
                ));
            }
        }
        let elapsed = started.elapsed();
        let output = output.trim().to_string();
        if first_text_at.is_none() {
            return Err(Error::Provider(
                "model connectivity test received no text output".into(),
            ));
        }
        let tokens_estimated = output_tokens.is_none();
        let output_tokens = output_tokens.unwrap_or_else(|| estimate_output_tokens(&output));
        Ok(ModelConnectivityResult {
            duration_ms: elapsed.as_millis().min(u128::from(u64::MAX)) as u64,
            first_text_ms: first_text_at.map(|first| {
                first
                    .duration_since(started)
                    .as_millis()
                    .min(u128::from(u64::MAX)) as u64
            }),
            output_tokens,
            tokens_per_second: if elapsed.is_zero() {
                0.0
            } else {
                output_tokens as f64 / elapsed.as_secs_f64()
            },
            tokens_estimated,
            output,
        })
    }

    pub async fn discover_models(&self, input: &ModelDiscoveryInput) -> Result<DiscoveredModels> {
        let client = crate::network::client(&self.store).await?;
        let base_url = crate::model::normalize_request_url(&input.base_url)?;
        discover_models_from_endpoint(
            &client,
            match input.model_type {
                ModelType::OpenAi => ProviderType::OpenAiResponses,
                ModelType::Anthropic => ProviderType::Anthropic,
            },
            &base_url,
            &input.api_key,
            if input.custom_headers_enabled {
                &input.custom_headers
            } else {
                empty_json_object_ref()
            },
        )
        .await
    }

    pub async fn import_v0049_models(&self) -> Result<LegacyModelImportResult> {
        let path = crate::config::v0049_config_path()?;
        let outcome = self.store.import_v0049_model_config(&path).await?;
        Ok(LegacyModelImportResult {
            imported: outcome.imported,
            skipped: outcome.skipped,
            total: outcome.total,
        })
    }

    pub async fn preview_v0049_models(&self) -> Result<LegacyModelImportPreview> {
        let path = crate::config::v0049_config_path()?;
        let plan = self.store.preview_v0049_model_config(&path).await?;
        let total = plan.models.len();
        let existing_models = plan.models.iter().filter(|model| model.existing).count();
        Ok(LegacyModelImportPreview {
            source: path.display().to_string(),
            total,
            new_models: total - existing_models,
            existing_models,
            models: plan
                .models
                .into_iter()
                .map(|model| LegacyModelImportPreviewItem {
                    model_hash: model.model_hash,
                    display_name: model.input.display_name,
                    model_id: model.input.model_id,
                    model_type: model.input.model_type,
                    existing: model.existing,
                })
                .collect(),
        })
    }

    pub async fn calls(&self, limit: i64) -> Result<Vec<CallSummary>> {
        let mut calls = self
            .store
            .llm_calls(limit)
            .await?
            .into_iter()
            .map(|call| CallSummary {
                call,
                call_kind: "provider_llm",
                route: "local_byok",
            })
            .collect::<Vec<_>>();
        calls.extend(
            self.store
                .official_cursor_traces(limit)
                .await?
                .into_iter()
                .map(official_call),
        );
        calls.sort_by_key(|call| std::cmp::Reverse(call.call.created_at_ms));
        calls.truncate(limit.clamp(1, 500) as usize);
        Ok(calls)
    }

    pub async fn call(&self, call_id: &str) -> Result<CallDetail> {
        if let Some(call) = self.store.llm_call(call_id).await? {
            let cursor_trace = self.cursor_trace_detail(&call.run_id).await?;
            return Ok(CallDetail {
                request: self.store.llm_call_request(call_id).await?,
                response_chunks: self.store.llm_call_chunks(call_id).await?,
                call: CallSummary {
                    call,
                    call_kind: "provider_llm",
                    route: "local_byok",
                },
                cursor_trace,
            });
        }
        let request_id = call_id.strip_prefix("cursor:").unwrap_or(call_id);
        let trace = self
            .store
            .cursor_trace(request_id)
            .await?
            .filter(|trace| trace.route == "cursor_official")
            .ok_or_else(|| Error::RunNotFound(format!("call {call_id}")))?;
        Ok(CallDetail {
            call: official_call(trace.clone()),
            request: None,
            response_chunks: Vec::new(),
            cursor_trace: Some(self.cursor_trace_detail_from(trace).await?),
        })
    }

    async fn cursor_trace_detail(&self, request_id: &str) -> Result<Option<CursorTraceDetail>> {
        let Some(trace) = self.store.cursor_trace(request_id).await? else {
            return Ok(None);
        };
        Ok(Some(self.cursor_trace_detail_from(trace).await?))
    }

    async fn cursor_trace_detail_from(
        &self,
        trace: CursorRunTraceSummary,
    ) -> Result<CursorTraceDetail> {
        let artifacts = self
            .store
            .cursor_trace_artifacts(&trace.request_id)
            .await?
            .into_iter()
            .map(cursor_artifact)
            .collect();
        Ok(CursorTraceDetail { trace, artifacts })
    }

    pub async fn observability(&self) -> Result<ObservabilitySettings> {
        Ok(ObservabilitySettings {
            detailed: self.store.detailed_logging().await?,
        })
    }

    pub async fn set_observability(
        &self,
        settings: ObservabilitySettings,
    ) -> Result<ObservabilitySettings> {
        self.store.set_detailed_logging(settings.detailed).await?;
        Ok(settings)
    }

    pub async fn ports(&self) -> Result<PortSettings> {
        self.store.port_settings().await
    }

    pub async fn set_ports(&self, settings: PortSettings) -> Result<PortSettings> {
        self.store.set_port_settings(settings).await?;
        Ok(settings)
    }

    pub async fn statistics_storage(&self) -> Result<StatisticsStorage> {
        self.store.statistics_storage().await
    }

    pub async fn clear_statistics_storage(&self) -> Result<StatisticsStorage> {
        self.store.clear_statistics_storage().await
    }

    pub async fn proxy_settings(&self) -> Result<ProxySettings> {
        self.store.proxy_settings().await
    }

    pub async fn set_proxy_settings(&self, settings: ProxySettingsInput) -> Result<ProxySettings> {
        self.store.set_proxy_settings(settings).await
    }

    pub async fn tab_settings(&self) -> Result<TabSettings> {
        self.store.tab_settings().await
    }

    pub async fn set_tab_settings(&self, settings: TabSettings) -> Result<TabSettings> {
        self.cursor_harness.set_tab_settings(settings).await
    }

    pub async fn desktop_settings(&self) -> Result<DesktopSettings> {
        self.store.desktop_settings().await
    }

    pub async fn set_desktop_settings(&self, settings: DesktopSettings) -> Result<()> {
        self.store.set_desktop_settings(settings).await
    }
}

fn official_call(trace: CursorRunTraceSummary) -> CallSummary {
    let model_id = trace.model_id.clone().unwrap_or_else(|| "Cursor".into());
    let ttfb = trace
        .first_response_at_ms
        .map(|value| (value - trace.received_at_ms).max(0));
    let duration = trace
        .finished_at_ms
        .map(|value| (value - trace.received_at_ms).max(0));
    let error = trace.error_message.clone();
    CallSummary {
        call: LlmCallSummary {
            call_id: format!("cursor:{}", trace.request_id),
            run_id: trace.request_id.clone(),
            conversation_id: trace
                .conversation_id
                .clone()
                .unwrap_or_else(|| trace.request_id.clone()),
            provider_call_index: 0,
            model_hash: None,
            provider_type: "cursor-official".into(),
            provider_url: "https://api2.cursor.sh".into(),
            request_type: "cursor-run-sse".into(),
            request_url: "https://api2.cursor.sh/agent.v1.AgentService/RunSSE".into(),
            model_id: model_id.clone(),
            display_name: model_id,
            reasoning_effort: None,
            fast: None,
            status: trace.status.clone(),
            finish_reason: None,
            created_at_ms: trace.received_at_ms,
            request_started_at_ms: Some(trace.received_at_ms),
            response_headers_at_ms: trace.first_response_at_ms,
            first_event_at_ms: trace.first_response_at_ms,
            first_text_at_ms: None,
            finished_at_ms: trace.finished_at_ms,
            queue_ms: None,
            ttfb_ms: ttfb,
            ttft_ms: None,
            duration_ms: duration,
            input_tokens: None,
            output_tokens: None,
            total_tokens: None,
            cache_read_tokens: None,
            cache_write_tokens: None,
            reasoning_tokens: None,
            usage: None,
            message_count: 0,
            tool_count: 0,
            request_bytes: Some(trace.request_bytes),
            response_bytes: trace.response_bytes,
            stream_event_count: trace.response_event_count,
            http_status: trace.http_status,
            error_kind: error.as_ref().map(|_| "cursor_official".into()),
            error_message: error,
            detailed: true,
        },
        call_kind: "cursor_official",
        route: "cursor_official",
    }
}

fn cursor_artifact(artifact: CursorRunTraceArtifact) -> CursorTraceArtifactDetail {
    let byte_count = artifact.data.len();
    let (encoding, data) = match readable_utf8(&artifact.data) {
        Some(value) => ("utf8", value.into()),
        None => ("base64", STANDARD.encode(&artifact.data)),
    };
    CursorTraceArtifactDetail {
        seq: artifact.seq,
        artifact_type: artifact.artifact_type,
        source: artifact.source,
        metadata: artifact.metadata,
        created_at_ms: artifact.created_at_ms,
        byte_count,
        encoding,
        data,
    }
}

fn readable_utf8(data: &[u8]) -> Option<&str> {
    let value = std::str::from_utf8(data).ok()?;
    value
        .chars()
        .all(|character| !character.is_control() || matches!(character, '\n' | '\r' | '\t'))
        .then_some(value)
}

async fn discover_models_from_endpoint(
    client: &reqwest::Client,
    provider_type: ProviderType,
    base_url: &str,
    api_key: &str,
    custom_headers: &serde_json::Value,
) -> Result<DiscoveredModels> {
    let mut models = match provider_type {
        ProviderType::OpenAiChat | ProviderType::OpenAiResponses => {
            openai_models(client, base_url, api_key, custom_headers).await?
        }
        ProviderType::Anthropic => {
            anthropic_models(client, base_url, api_key, custom_headers).await?
        }
    };
    models.sort_by(|left, right| left.id.cmp(&right.id));
    models.dedup_by(|left, right| left.id == right.id);
    Ok(DiscoveredModels { models })
}

fn model_discovery_url(base_url: &str) -> Result<Url> {
    let mut url = Url::parse(base_url)
        .map_err(|error| Error::Config(format!("invalid model request URL: {error}")))?;
    if url.host_str().is_none() {
        return Err(Error::Config(
            "model request URL must contain a host".into(),
        ));
    }
    // 在现有路径上追加，而不是整段替换：多数编程套餐的 API 挂在子路径下
    // （/api/anthropic、/coding、/api/paas/v4 等），直接 set_path("/v1/models")
    // 会把这些前缀吃掉，发现请求必然 404
    let path = url.path().trim_end_matches('/');
    let last = path.rsplit('/').next().unwrap_or("");
    let versioned = last.len() > 1
        && last.starts_with('v')
        && last[1..].bytes().all(|byte| byte.is_ascii_digit());
    let new_path = if let Some(parent) = path.strip_suffix("/chat/completions") {
        // 完整请求 URL：剥掉端点段（chat/completions 是两段），换成 models
        format!("{parent}/models")
    } else if let Some(parent) = path
        .strip_suffix("/responses")
        .or_else(|| path.strip_suffix("/messages"))
        .or_else(|| path.strip_suffix("/completions"))
    {
        format!("{parent}/models")
    } else if path.is_empty() {
        "/v1/models".to_string()
    } else if versioned {
        // 已带版本段（/v1、/api/v3、/api/paas/v4）：只补 models
        format!("{path}/models")
    } else {
        format!("{path}/v1/models")
    };
    url.set_path(&new_path);
    url.set_query(None);
    url.set_fragment(None);
    Ok(url)
}

fn model_discovery_urls(base_url: &str) -> Result<Vec<Url>> {
    let mut configured = Url::parse(base_url)
        .map_err(|error| Error::Config(format!("invalid model request URL: {error}")))?;
    if configured
        .host_str()
        .is_some_and(|host| host.eq_ignore_ascii_case("chatgpt.com"))
    {
        return Ok(vec![Url::parse("https://chatgpt.com/backend-api/codex/models?client_version=1.0.0")
            .map_err(|error| Error::Config(format!("invalid Codex models URL: {error}")))?]);
    }
    let path = configured.path().trim_end_matches('/');
    let tail = path.rsplit('/').next().unwrap_or_default();
    if matches!(tail.to_ascii_lowercase().as_str(), "model" | "models") {
        configured.set_query(None);
        configured.set_fragment(None);
        return Ok(vec![configured]);
    }

    let primary = model_discovery_url(base_url)?;
    let versioned = tail.len() > 1
        && tail.starts_with('v')
        && tail[1..].bytes().all(|byte| byte.is_ascii_digit());
    let complete_request_url = [
        "/chat/completions",
        "/responses",
        "/messages",
        "/completions",
    ]
    .iter()
    .any(|suffix| path.to_ascii_lowercase().ends_with(suffix));
    if versioned || complete_request_url {
        return Ok(vec![primary]);
    }

    let Some(prefix) = primary.path().strip_suffix("/v1/models") else {
        return Ok(vec![primary]);
    };
    let mut fallback = primary.clone();
    fallback.set_path(&format!("{prefix}/models"));
    Ok(vec![primary, fallback])
}

async fn openai_models(
    client: &reqwest::Client,
    base_url: &str,
    api_key: &str,
    custom_headers: &serde_json::Value,
) -> Result<Vec<DiscoveredModel>> {
    let mut last_error = None;
    for url in model_discovery_urls(base_url)? {
        match openai_models_at(client, url, api_key, custom_headers).await {
            Ok(models) => return Ok(models),
            Err(error) => last_error = Some(error),
        }
    }
    Err(last_error.unwrap_or_else(|| Error::Provider("no model discovery URL available".into())))
}

async fn openai_models_at(
    client: &reqwest::Client,
    url: Url,
    api_key: &str,
    custom_headers: &serde_json::Value,
) -> Result<Vec<DiscoveredModel>> {
    let mut request = client.get(url.clone());
    if !api_key.is_empty() {
        request = request.bearer_auth(api_key);
    }
    request = crate::provider::apply_chatgpt_codex_headers(request, url.as_str(), api_key);
    let response = apply_discovery_headers(request, custom_headers)?
        .send()
        .await?;
    let status = response.status();
    let body: serde_json::Value = response.json().await?;
    if !status.is_success() {
        return Err(Error::Provider(format!(
            "model discovery failed ({status}): {body}"
        )));
    }
    Ok(discovered_models(
        body.get("data")
            .or_else(|| body.get("models"))
            .unwrap_or(&body),
    ))
}

async fn anthropic_models(
    client: &reqwest::Client,
    base_url: &str,
    api_key: &str,
    custom_headers: &serde_json::Value,
) -> Result<Vec<DiscoveredModel>> {
    let mut last_error = None;
    for url in model_discovery_urls(base_url)? {
        match anthropic_models_at(client, url, api_key, custom_headers).await {
            Ok(models) => return Ok(models),
            Err(error) => last_error = Some(error),
        }
    }
    Err(last_error.unwrap_or_else(|| Error::Provider("no model discovery URL available".into())))
}

async fn anthropic_models_at(
    client: &reqwest::Client,
    url: Url,
    api_key: &str,
    custom_headers: &serde_json::Value,
) -> Result<Vec<DiscoveredModel>> {
    let mut after_id = None::<String>;
    let mut found = Vec::new();
    loop {
        let mut request = client
            .get(url.clone())
            .query(&[("limit", "100")])
            .header("anthropic-version", "2023-06-01");
        if !api_key.is_empty() {
            request = request.header("x-api-key", api_key);
        }
        if let Some(after_id) = &after_id {
            request = request.query(&[("after_id", after_id)]);
        }
        let response = apply_discovery_headers(request, custom_headers)?
            .send()
            .await?;
        let status = response.status();
        let body: serde_json::Value = response.json().await?;
        if !status.is_success() {
            return Err(Error::Provider(format!(
                "model discovery failed ({status}): {body}"
            )));
        }
        found.extend(discovered_models(body.get("data").unwrap_or(&body)));
        if body.get("has_more").and_then(serde_json::Value::as_bool) != Some(true) {
            break;
        }
        after_id = body
            .get("last_id")
            .and_then(serde_json::Value::as_str)
            .map(str::to_owned);
        if after_id.is_none() {
            return Err(Error::Provider(
                "Anthropic model response has_more without last_id".into(),
            ));
        }
    }
    Ok(found.into_iter().collect())
}

fn discovered_models(value: &serde_json::Value) -> Vec<DiscoveredModel> {
    value
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|item| match item {
            serde_json::Value::String(id) => {
                let id = id.trim();
                (!id.is_empty()).then(|| DiscoveredModel {
                    id: id.to_string(),
                    context_window_tokens: None,
                })
            }
            serde_json::Value::Object(object) => {
                if object
                    .get("supported_in_api")
                    .and_then(serde_json::Value::as_bool)
                    == Some(false)
                {
                    return None;
                }
                if object
                    .get("visibility")
                    .and_then(serde_json::Value::as_str)
                    .is_some_and(|visibility| visibility != "list")
                {
                    return None;
                }
                let id = object
                    .get("id")
                    .or_else(|| object.get("name"))
                    .or_else(|| object.get("slug"))
                    .and_then(serde_json::Value::as_str)
                    .map(str::trim)
                    .filter(|id| !id.is_empty())?;
                Some(DiscoveredModel {
                    id: id.to_string(),
                    context_window_tokens: json_token_count(object.get("context_window"))
                        .or_else(|| json_token_count(object.get("context_length")))
                        .or_else(|| json_token_count(object.get("max_prompt_tokens")))
                        .or_else(|| json_token_count(object.get("input_token_limit"))),
                })
            }
            _ => None,
        })
        .collect()
}

fn json_token_count(value: Option<&serde_json::Value>) -> Option<u64> {
    let value = value?;
    value
        .as_u64()
        .or_else(|| value.as_i64().and_then(|tokens| u64::try_from(tokens).ok()))
        .or_else(|| value.as_f64().and_then(|tokens| (tokens > 0.0).then_some(tokens as u64)))
        .or_else(|| value.as_str().and_then(crate::model::parse_token_count))
        .filter(|tokens| *tokens > 0)
}

fn estimate_output_tokens(output: &str) -> u64 {
    let words = output.split_whitespace().count() as u64;
    if words > 0 {
        words
    } else if output.is_empty() {
        0
    } else {
        (output.chars().count() as u64).div_ceil(4)
    }
}

fn apply_discovery_headers(
    mut request: reqwest::RequestBuilder,
    headers: &serde_json::Value,
) -> Result<reqwest::RequestBuilder> {
    let object = headers
        .as_object()
        .ok_or_else(|| Error::Config("custom headers must be an object".into()))?;
    for (name, value) in object {
        if name.eq_ignore_ascii_case("user-agent") {
            continue;
        }
        let value = value
            .as_str()
            .ok_or_else(|| Error::Config(format!("custom header {name} must be a string")))?;
        let name = HeaderName::try_from(name)
            .map_err(|error| Error::Config(format!("invalid header name: {error}")))?;
        let value = HeaderValue::try_from(value)
            .map_err(|error| Error::Config(format!("invalid header value: {error}")))?;
        request = request.header(name, value);
    }
    Ok(request)
}

#[cfg(test)]
mod tests {
    use std::sync::{Arc, Mutex};

    use tokio_util::sync::CancellationToken;

    use crate::{
        model::{ModelConfig, ModelConfigInput, ModelInvocation, ModelType, ProjectedContent},
        provider::{FinishReason, ModelEvent, Provider, ProviderStream},
        store::Store,
    };

    use super::{model_discovery_url, model_discovery_urls, ControlService};

    #[test]
    fn model_discovery_url_appends_to_path() {
        let cases = [
            (
                "https://api.deepseek.com",
                "https://api.deepseek.com/v1/models",
            ),
            (
                "https://open.bigmodel.cn/api/anthropic",
                "https://open.bigmodel.cn/api/anthropic/v1/models",
            ),
            (
                "https://api.kimi.com/coding",
                "https://api.kimi.com/coding/v1/models",
            ),
            (
                "https://api.moonshot.cn/v1",
                "https://api.moonshot.cn/v1/models",
            ),
            (
                "https://ark.cn-beijing.volces.com/api/v3",
                "https://ark.cn-beijing.volces.com/api/v3/models",
            ),
            (
                "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions",
                "https://open.bigmodel.cn/api/coding/paas/v4/models",
            ),
        ];
        for (base, expected) in cases {
            assert_eq!(
                model_discovery_url(base).unwrap().as_str(),
                expected,
                "base: {base}"
            );
        }
    }

    #[test]
    fn model_discovery_urls_fall_back_without_a_version() {
        let cases = [
            (
                "https://opencode.ai/zen/go/v1",
                vec!["https://opencode.ai/zen/go/v1/models"],
            ),
            (
                "https://opencode.ai/zen/go",
                vec![
                    "https://opencode.ai/zen/go/v1/models",
                    "https://opencode.ai/zen/go/models",
                ],
            ),
            (
                "https://api.example.com/openai/v1/models",
                vec!["https://api.example.com/openai/v1/models"],
            ),
        ];
        for (base, expected) in cases {
            let actual = model_discovery_urls(base)
                .unwrap()
                .into_iter()
                .map(|url| url.to_string())
                .collect::<Vec<_>>();
            assert_eq!(actual, expected, "base: {base}");
        }
    }

    #[tokio::test]
    async fn openai_model_discovery_uses_the_unversioned_fallback() {
        let app = axum::Router::new().route(
            "/proxy/models",
            axum::routing::get(|| async {
                axum::Json(serde_json::json!({ "data": [{ "id": "model-a" }] }))
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });

        let models = super::openai_models(
            &reqwest::Client::new(),
            &format!("http://{address}/proxy"),
            "secret",
            &serde_json::json!({}),
        )
        .await
        .unwrap();

        assert_eq!(
            models.into_iter().map(|m| m.id).collect::<Vec<_>>(),
            vec!["model-a"]
        );
        server.abort();
    }

    struct TestProvider {
        invocation: Arc<Mutex<Option<ModelInvocation>>>,
    }

    struct CancellationProvider {
        started: Arc<tokio::sync::Notify>,
    }

    impl Provider for TestProvider {
        fn stream(
            &self,
            invocation: ModelInvocation,
            _cancellation: CancellationToken,
        ) -> ProviderStream {
            *self.invocation.lock().unwrap() = Some(invocation);
            Box::pin(futures_util::stream::iter([
                Ok(ModelEvent::Start {
                    model_call_id: "test-call".into(),
                }),
                Ok(ModelEvent::TextStart),
                Ok(ModelEvent::TextDelta("OK".into())),
                Ok(ModelEvent::TextEnd),
                Ok(ModelEvent::Usage(crate::model::Usage {
                    output_tokens: Some(2),
                    ..Default::default()
                })),
                Ok(ModelEvent::Done(FinishReason::Stop)),
            ]))
        }
    }

    impl Provider for CancellationProvider {
        fn stream(
            &self,
            _invocation: ModelInvocation,
            cancellation: CancellationToken,
        ) -> ProviderStream {
            let started = self.started.clone();
            Box::pin(async_stream::try_stream! {
                started.notify_one();
                cancellation.cancelled().await;
                if false { yield ModelEvent::TextStart; }
            })
        }
    }

    async fn create_test_model(store: &Store) -> ModelConfig {
        store
            .create_model(&ModelConfigInput {
                model_id: "reasoning-model".into(),
                display_name: "Reasoning Model".into(),
                model_type: ModelType::OpenAi,
                base_url: "https://example.com/v1/responses".into(),
                use_full_url: true,
                api_key: "secret".into(),
                tooltip_data: "Reasoning Model".into(),
                sort_order: 0,
                reasoning_effort: Some("medium".into()),
                openai_endpoint: "/v1/responses".into(),
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
            })
            .await
            .unwrap()
    }

    #[tokio::test]
    async fn connectivity_test_uses_the_configured_llm_provider() {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("control.db").display()
        ))
        .await
        .unwrap();
        let invocation = Arc::new(Mutex::new(None));
        let model = create_test_model(&store).await;
        let service = ControlService::new(
            store,
            Arc::new(TestProvider {
                invocation: invocation.clone(),
            }),
        )
        .unwrap();

        let result = service
            .test_model(&model.model_hash, "test-id")
            .await
            .unwrap();

        assert_eq!(result.output, "OK");
        assert_eq!(result.output_tokens, 2);
        assert!(!result.tokens_estimated);
        assert!(result.tokens_per_second > 0.0);
        let invocation = invocation.lock().unwrap().clone().unwrap();
        assert_eq!(invocation.request.model.model_id, model.model_hash);
        assert!(invocation.request.model.reasoning.enabled);
        assert_eq!(
            invocation.request.model.reasoning.effort.as_deref(),
            Some("medium")
        );
        assert!(invocation.request.prompt.tools.is_empty());
        assert_eq!(invocation.request.history.len(), 1);
        assert!(matches!(
            &invocation.request.history[0].content,
            ProjectedContent::Parts(parts)
                if matches!(&parts[..], [crate::model::ContentPart::Text { text }] if text == "Output the numbers 1 through 120 separated by a single space. No commas, no newlines, no explanation.")
        ));
    }

    #[tokio::test]
    async fn connectivity_test_can_be_cancelled() {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("cancel.db").display()
        ))
        .await
        .unwrap();
        let model = create_test_model(&store).await;
        let started = Arc::new(tokio::sync::Notify::new());
        let service = ControlService::new(
            store,
            Arc::new(CancellationProvider {
                started: started.clone(),
            }),
        )
        .unwrap();
        let running_service = service.clone();
        let model_hash = model.model_hash.clone();
        let task =
            tokio::spawn(
                async move { running_service.test_model(&model_hash, "cancel-test").await },
            );

        started.notified().await;
        service.cancel_model_test("cancel-test");

        assert!(matches!(task.await.unwrap(), Err(crate::Error::Cancelled)));
        assert!(!service
            .model_tests
            .lock()
            .unwrap()
            .contains_key("cancel-test"));
    }

    #[test]
    fn connectivity_output_token_estimate_handles_words_and_empty_text() {
        assert_eq!(super::estimate_output_tokens("1 2 3"), 3);
        assert_eq!(super::estimate_output_tokens(""), 0);
    }

    #[test]
    fn model_discovery_url_keeps_provider_path_prefix() {
        assert_eq!(
            super::model_discovery_url("https://example.com:8443/arbitrary/v1/chat/completions")
                .unwrap()
                .as_str(),
            "https://example.com:8443/arbitrary/v1/models"
        );
    }

    #[tokio::test]
    async fn model_discovery_does_not_inherit_user_agent_or_request_body_settings() {
        type CapturedRequest = (
            axum::http::Method,
            axum::http::Uri,
            axum::http::HeaderMap,
            bytes::Bytes,
        );

        async fn models(
            axum::extract::State(sender): axum::extract::State<
                tokio::sync::mpsc::UnboundedSender<CapturedRequest>,
            >,
            request: axum::extract::Request,
        ) -> axum::Json<serde_json::Value> {
            let (parts, body) = request.into_parts();
            let body = axum::body::to_bytes(body, usize::MAX).await.unwrap();
            sender
                .send((parts.method, parts.uri, parts.headers, body))
                .unwrap();
            axum::Json(serde_json::json!({ "data": [{ "id": "model-a" }] }))
        }

        let (sender, mut requests) = tokio::sync::mpsc::unbounded_channel();
        let app = axum::Router::new()
            .route("/custom/models", axum::routing::get(models))
            .with_state(sender);
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });

        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("discovery.db").display()
        ))
        .await
        .unwrap();
        let service = ControlService::new(
            store,
            Arc::new(TestProvider {
                invocation: Arc::new(Mutex::new(None)),
            }),
        )
        .unwrap();
        let result = service
            .discover_models(&super::ModelDiscoveryInput {
                model_type: ModelType::OpenAi,
                base_url: format!("http://{address}/custom/responses"),
                api_key: "secret".into(),
                custom_headers_enabled: true,
                custom_headers: serde_json::json!({
                    "uSeR-aGeNt": "inherited-user-agent",
                    "x-tenant": "tenant-a"
                }),
            })
            .await
            .unwrap();

        assert_eq!(result.models[0].id, "model-a");
        let (method, uri, headers, body) = requests.recv().await.unwrap();
        assert_eq!(method, axum::http::Method::GET);
        // /custom/responses 剥掉端点段后是 /custom，发现地址为 /custom/models
        assert_eq!(uri.path(), "/custom/models");
        assert!(body.is_empty());
        assert!(headers.get(axum::http::header::USER_AGENT).is_none());
        assert_eq!(headers.get("x-tenant").unwrap(), "tenant-a");
        assert_eq!(
            headers.get(axum::http::header::AUTHORIZATION).unwrap(),
            "Bearer secret"
        );
        server.abort();
    }

    #[test]
    fn chatgpt_codex_discovery_uses_backend_models_endpoint() {
        assert_eq!(
            super::model_discovery_urls("https://chatgpt.com/backend-api/codex")
                .unwrap()[0]
                .as_str(),
            "https://chatgpt.com/backend-api/codex/models?client_version=1.0.0"
        );
    }

    #[test]
    fn model_ids_read_openai_and_codex_listings() {
        let openai = serde_json::json!([
            { "id": "grok-4", "context_window": 256000 },
            { "name": "grok-4.20" }
        ]);
        let models = super::discovered_models(&openai);
        assert_eq!(models[0].id, "grok-4");
        assert_eq!(models[0].context_window_tokens, Some(256_000));
        assert_eq!(models[1].id, "grok-4.20");
        assert_eq!(models[1].context_window_tokens, None);

        let codex = serde_json::json!([
            { "slug": "gpt-5.4", "supported_in_api": true, "visibility": "list" },
            { "slug": "hidden-model", "supported_in_api": true, "visibility": "hidden" },
            { "slug": "internal-model", "supported_in_api": false, "visibility": "list" }
        ]);
        assert_eq!(
            super::discovered_models(&codex)
                .into_iter()
                .map(|model| model.id)
                .collect::<Vec<_>>(),
            vec!["gpt-5.4"]
        );
    }
}
