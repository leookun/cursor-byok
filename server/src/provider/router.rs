//! Routes model requests to built-in configurations or stable plugin model IDs.
use std::{sync::Arc, time::Duration};

use async_stream::try_stream;
use futures_util::StreamExt;
use tokio_util::sync::CancellationToken;

use crate::{
    config::{ProviderConfig, ProviderKind},
    model::{
        estimate_context_tokens, ModelInvocation, ModelLatency, NewLlmCall, ProviderType, Usage,
    },
    plugin::{PluginRegistry, ADAPTER_ID_PREFIX},
    store::Store,
    Error, Result,
};

use super::{
    normalize::NormalizedProvider, recorder::CancelOnDrop, AnthropicProvider, CallRecorder,
    OpenAiChatProvider, OpenAiResponsesProvider, Provider, ProviderStream,
};

pub struct ProviderRouter {
    store: Store,
    plugins: PluginRegistry,
    clients: crate::network::NetworkClients,
    request_timeout: Duration,
    stream_idle_timeout: Duration,
}

impl ProviderRouter {
    pub fn new(
        store: Store,
        plugins: PluginRegistry,
        clients: crate::network::NetworkClients,
        request_timeout: Duration,
        stream_idle_timeout: Duration,
    ) -> Self {
        Self {
            store,
            plugins,
            clients,
            request_timeout,
            stream_idle_timeout,
        }
    }
}

impl Provider for ProviderRouter {
    fn stream(
        &self,
        invocation: ModelInvocation,
        cancellation: CancellationToken,
    ) -> ProviderStream {
        let store = self.store.clone();
        let plugins = self.plugins.clone();
        let clients = self.clients.clone();
        let request_timeout = self.request_timeout;
        let stream_idle_timeout = self.stream_idle_timeout;
        Box::pin(try_stream! {
            let selected = invocation.request.model.model_id.clone();
            // 两条分支只负责装配 Recorder 与 Provider 流;
            // 事件消费(空闲超时看门狗、记录、错误规范化)对两者完全一致。
            let (recorder, _cancel_on_drop, mut stream): (CallRecorder, CancelOnDrop, ProviderStream) =
                if selected.starts_with(ADAPTER_ID_PREFIX) {
                    // 插件模型与内置模型走完全相同的流程:资源选择与将来的
                    // 负载均衡都在插件 Provider 内部。
                    let plan = plugins.plan_model(&selected).await?;
                    let recorder = start_recorder(&store, &invocation, &selected, &plan.model.display_name, ProviderType::Plugin, &plan.request_url, &plan.model.model_id).await?;
                    let guard = recorder.cancel_on_drop();
                    let mut routed = invocation.clone();
                    routed.request.model.display_name = Some(plan.model.display_name.clone());
                    if let Some(tokens) = plan.model.max_output_tokens {
                        routed.request.model.max_output_tokens.get_or_insert(tokens);
                    }
                    let provider: Arc<dyn Provider> = Arc::new(NormalizedProvider::new(Arc::new(PluginModelProvider {
                        registry: plugins.clone(),
                        recorder: recorder.clone(),
                    })));
                    (recorder, guard, provider.stream(routed, cancellation.clone()))
                } else {
                    let mut routed = invocation.clone();
                    let model = store.model(&selected).await?.ok_or_else(|| Error::Provider(format!("unknown model: {selected}")))?;
                    let provider_type = model.provider_type();
                    let request_url = model.request_url()?;
                    model.configure(&mut routed.request.model);
                    routed.request.model.extra_params =
                        super::request_template::render_json_strings(
                            model.extra_params(),
                            &invocation.conversation_id,
                        );
                    routed.request.model.model_id = model.model_id.clone();
                    let recorder = start_recorder(&store, &invocation, &model.model_hash, &model.display_name, provider_type, &request_url, &model.model_id).await?;
                    let guard = recorder.cancel_on_drop();
                    let config = ProviderConfig {
                        kind: provider_kind(provider_type),
                        request_url,
                        api_key: model.api_key.clone(),
                        custom_headers: if model.custom_headers_enabled {
                            custom_headers(
                                &model.custom_headers,
                                &invocation.conversation_id,
                            )?
                        } else {
                            reqwest::header::HeaderMap::new()
                        },
                        max_output_tokens: model.max_output_tokens(),
                        request_timeout,
                        allowed_body_fields: None,
                    };
                    let client = clients.provider_client(request_timeout).await?;
                    let provider = build_observed(&config, recorder.clone(), client)?;
                    (recorder, guard, provider.stream(routed, cancellation.clone()))
                };

            let stream_started = std::time::Instant::now();
            tracing::debug!(
                model = %selected,
                request_timeout_ms = request_timeout.as_millis() as u64,
                stream_idle_timeout_ms = stream_idle_timeout.as_millis() as u64,
                "provider stream created"
            );
            let mut last_event_time = std::time::Instant::now();
            let mut event_count: u64 = 0;
            let mut observed_usage = false;
            loop {
                let event = match next_provider_event(&mut stream, stream_idle_timeout).await {
                    Ok(Some(event)) => event,
                    Ok(None) => break,
                    Err(_) => {
                        let elapsed_ms = stream_started.elapsed().as_millis() as u64;
                        let error = stream_idle_timeout_error(stream_idle_timeout);
                        tracing::warn!(
                            error = %error,
                            elapsed_ms,
                            event_count,
                            idle_timeout_ms = stream_idle_timeout.as_millis() as u64,
                            "provider stream idle timeout"
                        );
                        Err(error)
                    }
                };
                let now = std::time::Instant::now();
                let gap_ms = now.duration_since(last_event_time).as_millis() as u64;
                let elapsed_ms = now.duration_since(stream_started).as_millis() as u64;
                event_count += 1;
                match event {
                    Ok(event) => {
                        if gap_ms > 5000 {
                            tracing::debug!(
                                gap_ms,
                                elapsed_ms,
                                event = event_name(&event),
                                event_count,
                                "slow gap detected between provider events"
                            );
                        }
                        if matches!(&event, super::ModelEvent::Usage(_)) {
                            observed_usage = true;
                        }
                        let synthetic_usage = if matches!(&event, super::ModelEvent::Done(_))
                            && !observed_usage
                        {
                            let usage = estimated_usage(&invocation.request);
                            recorder.usage(usage).await?;
                            observed_usage = true;
                            Some(usage)
                        } else {
                            None
                        };
                        recorder.event(&event).await?;
                        last_event_time = now;
                        if let Some(usage) = synthetic_usage {
                            yield super::ModelEvent::Usage(usage);
                        }
                        yield event;
                    }
                    Err(error) => {
                        let error = normalize_provider_stream_error(error, request_timeout);
                        tracing::debug!(
                            error = %error,
                            elapsed_ms,
                            gap_ms,
                            event_count,
                            "provider stream error"
                        );
                        if !observed_usage {
                            recorder.usage(estimated_usage(&invocation.request)).await?;
                        }
                        recorder.failed(&error).await?;
                        Err(error)?;
                    }
                }
            }
            if !observed_usage {
                recorder.usage(estimated_usage(&invocation.request)).await?;
            }
            finish_stream(&recorder, &cancellation).await?;
        })
    }
}

fn estimated_usage(request: &crate::model::ModelRequest) -> Usage {
    let input_tokens = estimate_context_tokens(&request.prompt, &request.history);
    Usage {
        input_tokens: Some(input_tokens),
        context_input_tokens: Some(input_tokens),
        total_tokens: Some(input_tokens),
        ..Default::default()
    }
}

fn event_name(event: &super::ModelEvent) -> &'static str {
    match event {
        super::ModelEvent::Start { .. } => "Start",
        super::ModelEvent::TextStart => "TextStart",
        super::ModelEvent::TextDelta(_) => "TextDelta",
        super::ModelEvent::TextEnd => "TextEnd",
        super::ModelEvent::ThinkingStart => "ThinkingStart",
        super::ModelEvent::ThinkingDelta(_) => "ThinkingDelta",
        super::ModelEvent::ThinkingEnd => "ThinkingEnd",
        super::ModelEvent::ToolCallStart { .. } => "ToolCallStart",
        super::ModelEvent::ToolCallArgumentsDelta { .. } => "ToolCallArgsDelta",
        super::ModelEvent::ToolCallEnd { .. } => "ToolCallEnd",
        super::ModelEvent::ProviderReplayState(_) => "ReplayState",
        super::ModelEvent::Usage(_) => "Usage",
        super::ModelEvent::Done(_) => "Done",
    }
}

async fn start_recorder(
    store: &Store,
    invocation: &ModelInvocation,
    model_hash: &str,
    display_name: &str,
    provider_type: ProviderType,
    request_url: &str,
    model_id: &str,
) -> Result<CallRecorder> {
    CallRecorder::start(
        store.clone(),
        NewLlmCall {
            call_id: invocation.call_id.clone(),
            run_id: invocation.run_id.clone(),
            conversation_id: invocation.conversation_id.clone(),
            provider_call_index: invocation.provider_call_index.min(i64::MAX as u64) as i64,
            model_hash: model_hash.into(),
            provider_type,
            provider_url: request_url.into(),
            request_type: provider_type,
            request_url: request_url.into(),
            model_id: model_id.into(),
            display_name: display_name.into(),
            reasoning_effort: invocation.request.model.reasoning.effort.clone(),
            fast: invocation.request.model.latency == ModelLatency::Fast,
            message_count: invocation.request.history.len(),
            tool_count: invocation.request.prompt.tools.len(),
            detailed: false,
        },
    )
    .await
}

async fn finish_stream(recorder: &CallRecorder, cancellation: &CancellationToken) -> Result<()> {
    if recorder.is_finished() {
        return Ok(());
    }
    if cancellation.is_cancelled() {
        recorder.cancelled().await
    } else {
        let error = Error::Provider("provider stream ended without Done".into());
        recorder.failed(&error).await?;
        Err(error)
    }
}

/// 插件模型的 Provider 实现;对路由与规范化层完全等同于内置 Provider。
struct PluginModelProvider {
    registry: PluginRegistry,
    recorder: CallRecorder,
}

impl Provider for PluginModelProvider {
    fn stream(
        &self,
        invocation: ModelInvocation,
        cancellation: CancellationToken,
    ) -> ProviderStream {
        self.registry
            .stream_model(invocation, cancellation, self.recorder.clone())
    }
}

fn provider_kind(provider_type: ProviderType) -> ProviderKind {
    match provider_type {
        ProviderType::OpenAiChat => ProviderKind::OpenAiChat,
        ProviderType::OpenAiResponses => ProviderKind::OpenAiResponses,
        ProviderType::Anthropic => ProviderKind::Anthropic,
        // 内置模型的 provider_type 只来自 ModelType,不可能是插件。
        ProviderType::Plugin => unreachable!("plugin models never use built-in provider configs"),
    }
}

async fn next_provider_event(
    stream: &mut ProviderStream,
    idle_timeout: Duration,
) -> std::result::Result<Option<Result<super::ModelEvent>>, tokio::time::error::Elapsed> {
    tokio::time::timeout(idle_timeout, stream.next()).await
}

fn stream_idle_timeout_error(idle_timeout: Duration) -> Error {
    Error::Provider(format!(
        "provider stream idle timeout: no events received for {} seconds ({} minutes)",
        idle_timeout.as_secs(),
        idle_timeout.as_secs() / 60
    ))
}

fn request_timeout_error(request_timeout: Duration) -> Error {
    Error::Provider(format!(
        "provider request timed out after {} seconds ({} minutes)",
        request_timeout.as_secs(),
        request_timeout.as_secs() / 60
    ))
}

fn normalize_provider_stream_error(error: Error, request_timeout: Duration) -> Error {
    match error {
        Error::Http(source) if source.is_timeout() => request_timeout_error(request_timeout),
        Error::Http(source) if source.is_body() => Error::Provider(format!(
            "provider stream transport failed while reading the response body: {}",
            root_error_message(&source)
        )),
        error => error,
    }
}

fn root_error_message(error: &(dyn std::error::Error + 'static)) -> String {
    let mut current = error;
    while let Some(source) = current.source() {
        current = source;
    }
    current.to_string()
}

fn custom_headers(
    value: &serde_json::Value,
    conversation_id: &str,
) -> Result<reqwest::header::HeaderMap> {
    let rendered = super::request_template::render_json_strings(value, conversation_id);
    let object = rendered
        .as_object()
        .ok_or_else(|| Error::Config("custom headers must be an object".into()))?;
    let mut headers = reqwest::header::HeaderMap::new();
    for (name, value) in object {
        let name = reqwest::header::HeaderName::from_bytes(name.as_bytes())
            .map_err(|error| Error::Config(format!("invalid custom header name: {error}")))?;
        let value = value
            .as_str()
            .ok_or_else(|| Error::Config("custom header values must be strings".into()))?;
        let value = reqwest::header::HeaderValue::from_str(value)
            .map_err(|error| Error::Config(format!("invalid custom header value: {error}")))?;
        headers.insert(name, value);
    }
    Ok(headers)
}

pub fn build(config: &ProviderConfig) -> Result<Arc<dyn Provider>> {
    build_inner(config, None, None)
}

fn build_observed(
    config: &ProviderConfig,
    recorder: CallRecorder,
    client: reqwest::Client,
) -> Result<Arc<dyn Provider>> {
    build_inner(config, Some(recorder), Some(client))
}

fn build_inner(
    config: &ProviderConfig,
    recorder: Option<CallRecorder>,
    client: Option<reqwest::Client>,
) -> Result<Arc<dyn Provider>> {
    let client = match client {
        Some(client) => client,
        None => reqwest::Client::builder()
            .timeout(config.request_timeout)
            .build()?,
    };
    let provider: Arc<dyn Provider> = match config.kind {
        ProviderKind::OpenAiChat => {
            Arc::new(OpenAiChatProvider::new(client, config.clone()).with_recorder(recorder))
        }
        ProviderKind::OpenAiResponses => {
            Arc::new(OpenAiResponsesProvider::new(client, config.clone()).with_recorder(recorder))
        }
        ProviderKind::Anthropic => {
            Arc::new(AnthropicProvider::new(client, config.clone()).with_recorder(recorder))
        }
    };
    Ok(Arc::new(NormalizedProvider::new(provider)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{
        ContentPart, ModelConfigInput, ModelRequest, ModelSpec, ModelType, ProjectedContent,
        ProjectedMessage, PromptSpec, Role, OPENAI_CHAT_ENDPOINT,
    };
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    async fn openai_chat_server(
        usage: Option<(u64, u64, u64)>,
    ) -> (String, tokio::task::JoinHandle<()>) {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let usage_event = usage.map_or_else(String::new, |(input, output, total)| {
            format!(
                "data: {{\"id\":\"chatcmpl-test\",\"choices\":[],\"usage\":{{\"prompt_tokens\":{input},\"completion_tokens\":{output},\"total_tokens\":{total}}}}}\n\n"
            )
        });
        let body = [
            "data: {\"id\":\"chatcmpl-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n",
            usage_event.as_str(),
            "data: {\"id\":\"chatcmpl-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
            "data: [DONE]\n\n",
        ]
        .concat();
        let response = format!(
            "HTTP/1.1 200 OK\r\ncontent-type: text/event-stream\r\ncontent-length: {}\r\nconnection: close\r\n\r\n{body}",
            body.len()
        );
        let task = tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = [0_u8; 16 * 1024];
            let _ = socket.read(&mut request).await.unwrap();
            socket.write_all(response.as_bytes()).await.unwrap();
        });
        (format!("http://{address}/v1/chat/completions"), task)
    }

    fn test_request(model_id: impl Into<String>) -> ModelRequest {
        ModelRequest {
            prompt: PromptSpec {
                instructions: "system instructions".into(),
                tools: Vec::new(),
            },
            model: ModelSpec::new(model_id),
            history: vec![ProjectedMessage {
                message_id: "user-1".into(),
                role: Role::User,
                content: ProjectedContent::Parts(vec![ContentPart::Text {
                    text: "hello world".into(),
                }]),
            }],
        }
    }

    async fn routed_test_setup(
        upstream_usage: Option<(u64, u64, u64)>,
    ) -> (
        tempfile::TempDir,
        Store,
        ProviderRouter,
        String,
        tokio::task::JoinHandle<()>,
    ) {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        let (request_url, server) = openai_chat_server(upstream_usage).await;
        let model = store
            .create_model(&ModelConfigInput {
                sort_order: 0,
                display_name: "Test Model".into(),
                group_name: None,
                model_type: ModelType::OpenAi,
                base_url: request_url,
                use_full_url: true,
                api_key: "test-key".into(),
                tooltip_data: "Test Model".into(),
                model_id: "upstream-model".into(),
                reasoning_effort: None,
                openai_endpoint: OPENAI_CHAT_ENDPOINT.into(),
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
            .unwrap();
        let plugins = PluginRegistry::for_test(store.clone(), directory.path()).unwrap();
        let router = ProviderRouter::new(
            store.clone(),
            plugins,
            crate::network::NetworkClients::new(store.clone()),
            Duration::from_secs(5),
            Duration::from_secs(5),
        );
        (directory, store, router, model.model_hash, server)
    }

    #[tokio::test]
    async fn routed_call_emits_and_persists_estimated_usage_when_provider_omits_it() {
        let (_directory, store, router, model_hash, server) = routed_test_setup(None).await;
        let mut stream = router.stream(
            ModelInvocation {
                call_id: "call-estimated-usage".into(),
                run_id: "run-estimated-usage".into(),
                conversation_id: "conversation-estimated-usage".into(),
                provider_call_index: 0,
                request: test_request(model_hash),
            },
            CancellationToken::new(),
        );

        let mut events = Vec::new();
        while let Some(event) = stream.next().await {
            let event = event.unwrap();
            if matches!(event, super::super::ModelEvent::Usage(_)) {
                let call = store
                    .llm_call("call-estimated-usage")
                    .await
                    .unwrap()
                    .unwrap();
                assert_eq!(
                    call.status, "completed",
                    "provider completion must be persisted before synthetic usage is emitted"
                );
            }
            events.push(event);
        }
        server.await.unwrap();

        let expected = Usage {
            input_tokens: Some(16),
            context_input_tokens: Some(16),
            total_tokens: Some(16),
            ..Default::default()
        };
        assert!(events.iter().any(
            |event| matches!(event, super::super::ModelEvent::Usage(usage) if *usage == expected)
        ));
        let call = store
            .llm_call("call-estimated-usage")
            .await
            .unwrap()
            .unwrap();
        assert_eq!(call.status, "completed");
        assert_eq!(call.usage, Some(serde_json::to_value(expected).unwrap()));
        assert_eq!(call.input_tokens, Some(16));
        assert_eq!(call.output_tokens, None);
        assert_eq!(call.total_tokens, Some(16));
    }

    #[tokio::test]
    async fn routed_call_preserves_provider_usage_without_an_estimate() {
        let reported = Usage {
            input_tokens: Some(31),
            context_input_tokens: Some(31),
            output_tokens: Some(7),
            total_tokens: Some(38),
            ..Default::default()
        };
        let (_directory, store, router, model_hash, server) =
            routed_test_setup(Some((31, 7, 38))).await;
        let mut stream = router.stream(
            ModelInvocation {
                call_id: "call-reported-usage".into(),
                run_id: "run-reported-usage".into(),
                conversation_id: "conversation-reported-usage".into(),
                provider_call_index: 0,
                request: test_request(model_hash),
            },
            CancellationToken::new(),
        );

        let mut usages = Vec::new();
        while let Some(event) = stream.next().await {
            if let super::super::ModelEvent::Usage(usage) = event.unwrap() {
                usages.push(usage);
            }
        }
        server.await.unwrap();

        assert_eq!(usages, vec![reported]);
        let call = store
            .llm_call("call-reported-usage")
            .await
            .unwrap()
            .unwrap();
        assert_eq!(call.status, "completed");
        assert_eq!(call.usage, Some(serde_json::to_value(reported).unwrap()));
        assert_eq!(call.input_tokens, Some(31));
        assert_eq!(call.output_tokens, Some(7));
        assert_eq!(call.total_tokens, Some(38));
    }

    #[test]
    fn estimates_context_usage_when_provider_omits_usage() {
        let usage = estimated_usage(&test_request("test-model"));

        assert_eq!(usage.input_tokens, Some(16));
        assert_eq!(usage.context_input_tokens, Some(16));
        assert_eq!(usage.output_tokens, None);
        assert_eq!(usage.total_tokens, Some(16));
    }

    #[test]
    fn renders_cursor_conversation_id_in_custom_header_values() {
        let template = serde_json::json!({
            "x-opencode-session-id": "{{SessionId}}",
            "x-label": "cursor/{{SessionId}}"
        });

        let headers = custom_headers(&template, "cursor-conversation-id").unwrap();

        assert_eq!(
            headers.get("x-opencode-session-id").unwrap(),
            "cursor-conversation-id"
        );
        assert_eq!(
            headers.get("x-label").unwrap(),
            "cursor/cursor-conversation-id"
        );
        assert_eq!(template["x-opencode-session-id"], "{{SessionId}}");
    }

    #[tokio::test]
    async fn pending_provider_event_hits_the_idle_timeout() {
        let mut stream: ProviderStream = Box::pin(futures_util::stream::pending());

        let result = next_provider_event(&mut stream, Duration::from_millis(1)).await;

        assert!(result.is_err());
    }

    #[test]
    fn timeout_errors_state_the_boundary_and_duration() {
        let Error::Provider(idle) = stream_idle_timeout_error(Duration::from_secs(30 * 60)) else {
            panic!("idle timeout must be a provider error");
        };
        assert_eq!(
            idle,
            "provider stream idle timeout: no events received for 1800 seconds (30 minutes)"
        );

        let Error::Provider(request) = request_timeout_error(Duration::from_secs(60 * 60)) else {
            panic!("request timeout must be a provider error");
        };
        assert_eq!(
            request,
            "provider request timed out after 3600 seconds (60 minutes)"
        );
    }
}
