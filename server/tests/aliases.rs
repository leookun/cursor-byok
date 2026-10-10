//! End-to-end routing against a local HTTP provider. No real credentials or traffic interception.
use axum::{
    body::Body,
    extract::State,
    http::{header, StatusCode},
    response::Response,
    routing::post,
    Json, Router,
};
use cursor_server::{
    alias::{Alias, AliasInput, AliasTarget, ReturnMode, SourceType},
    model::{
        ContentPart, ModelConfigInput, ModelInvocation, ModelRequest, ModelSpec, ProjectedContent,
        ProjectedMessage, PromptSpec, Role,
    },
    network::NetworkClients,
    plugin::{PluginRegistry, PluginRuntime},
    provider::{ModelEvent, Provider, ProviderRouter},
    store::Store,
};
use futures_util::StreamExt;
use serde_json::{json, Value};
use std::sync::{
    atomic::{AtomicBool, AtomicU16, AtomicU64, Ordering},
    Arc, Mutex,
};
use tokio_util::sync::CancellationToken;

#[derive(Default)]
struct Fake {
    status: AtomicU16,
    delay_ms: AtomicU64,
    partial: AtomicBool,
    calls: Mutex<Vec<Value>>,
}

async fn answer(State(fake): State<Arc<Fake>>, Json(body): Json<Value>) -> Response {
    fake.calls.lock().unwrap().push(body.clone());
    let primary = body["model"] == "primary";
    if primary {
        tokio::time::sleep(std::time::Duration::from_millis(
            fake.delay_ms.load(Ordering::SeqCst),
        ))
        .await;
        let status = fake.status.load(Ordering::SeqCst);
        if status >= 400 {
            return Response::builder()
                .status(StatusCode::from_u16(status).unwrap())
                .header(header::RETRY_AFTER, "120")
                .body(Body::from("provider rejected request"))
                .unwrap();
        }
    }
    if body
        .get("tools")
        .and_then(Value::as_array)
        .is_some_and(|tools| !tools.is_empty())
    {
        let start = json!({"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tool-1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":null}]});
        let end = json!({"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]});
        return Response::builder()
            .header(header::CONTENT_TYPE, "text/event-stream")
            .body(Body::from(format!(
                "data: {start}\n\ndata: {end}\n\ndata: [DONE]\n\n"
            )))
            .unwrap();
    }
    let delta = json!({"choices":[{"index":0,"delta":{"content":if primary {"primary answer"} else {"backup answer"}},"finish_reason":null}]});
    let ending = if primary && fake.partial.load(Ordering::SeqCst) {
        "data: {\"error\":{\"type\":\"server_error\",\"message\":\"stream broke\"}}\n\n".to_owned()
    } else {
        "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n".to_owned()
    };
    Response::builder()
        .header(header::CONTENT_TYPE, "text/event-stream")
        .body(Body::from(format!("data: {delta}\n\n{ending}")))
        .unwrap()
}

struct Fixture {
    _directory: tempfile::TempDir,
    store: Store,
    router: ProviderRouter,
    alias: Alias,
    fake: Arc<Fake>,
    server: tokio::task::JoinHandle<()>,
}
impl Drop for Fixture {
    fn drop(&mut self) {
        self.server.abort();
    }
}

async fn fixture() -> Fixture {
    let directory = tempfile::tempdir().unwrap();
    let store = Store::connect(&format!(
        "sqlite://{}",
        directory.path().join("test.db").display()
    ))
    .await
    .unwrap();
    let fake = Arc::new(Fake::default());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let app = Router::new()
        .route("/v1/chat/completions", post(answer))
        .with_state(fake.clone());
    let server = tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    let mut targets = Vec::new();
    for (name, context, output, effort) in [
        ("primary", 200_000, 32_000, "high"),
        ("backup", 128_000, 8_000, "low"),
    ] {
        let input:ModelConfigInput = serde_json::from_value(json!({
            "display_name":name,"type":"openai","base_url":url,"api_key":"fake-key","tooltip_data":name,"model_id":name,
            "openai_endpoint":"/v1/chat/completions","context_window_tokens":context,"max_completion_tokens":output,
            "reasoning_effort":effort,"supports_images":true,"supports_tools":true,
            "custom_headers_enabled":true,"custom_headers":{"x-test":"target-config"},
            "openai_extra_params_enabled":true,"openai_extra_params":{"temperature":0.2}
        })).unwrap();
        let model = store.create_model(&input).await.unwrap();
        targets.push(AliasTarget {
            source_type: SourceType::Api,
            source_id: model.source_id,
            model_id: String::new(),
            enabled: true,
        });
    }
    let alias = store
        .save_alias(
            None,
            &AliasInput {
                name: "my-smart".into(),
                description: String::new(),
                enabled: true,
                targets,
                sticky: true,
                return_mode: ReturnMode::NewSessions,
            },
        )
        .await
        .unwrap();
    let plugins = PluginRegistry::managed(
        store.clone(),
        PluginRuntime::managed().unwrap(),
        "test".into(),
    )
    .unwrap();
    let router = ProviderRouter::new(
        store.clone(),
        plugins,
        NetworkClients::new(store.clone()),
        std::time::Duration::from_secs(10),
        std::time::Duration::from_secs(5),
    );
    Fixture {
        _directory: directory,
        store,
        router,
        alias,
        fake,
        server,
    }
}

fn invocation(conversation: &str, run: &str) -> ModelInvocation {
    ModelInvocation {
        call_id: format!("call-{run}"),
        run_id: run.into(),
        conversation_id: conversation.into(),
        provider_call_index: 1,
        request: ModelRequest {
            model: ModelSpec::new("my-smart"),
            prompt: PromptSpec {
                instructions: "Stable instructions".into(),
                tools: Vec::new(),
            },
            history: vec![ProjectedMessage {
                message_id: "user-1".into(),
                role: Role::User,
                content: ProjectedContent::Parts(vec![ContentPart::Text {
                    text: "hello".into(),
                }]),
            }],
        },
    }
}

async fn execute(f: &Fixture, conversation: &str, run: &str) -> (String, Option<String>) {
    let mut stream = f
        .router
        .stream(invocation(conversation, run), CancellationToken::new());
    let mut text = String::new();
    while let Some(event) = stream.next().await {
        match event {
            Ok(ModelEvent::TextDelta(delta)) => text.push_str(&delta),
            Err(error) => return (text, Some(error.to_string())),
            _ => {}
        }
    }
    (text, None)
}

#[tokio::test]
async fn priority_and_target_settings_use_existing_adapters() {
    let f = fixture().await;
    assert_eq!(
        execute(&f, "c", "run1").await,
        ("primary answer".into(), None)
    );
    let calls = f.fake.calls.lock().unwrap();
    assert_eq!(calls.len(), 1);
    assert_eq!(calls[0]["model"], "primary");
    assert_eq!(calls[0]["reasoning_effort"], "high");
    assert_eq!(calls[0]["temperature"], 0.2);
    assert_eq!(calls[0]["max_completion_tokens"], 8_000);
}

#[tokio::test]
async fn rate_limit_switches_before_output_and_records_actual_target() {
    let f = fixture().await;
    f.fake.status.store(429, Ordering::SeqCst);
    assert_eq!(
        execute(&f, "c", "run1").await,
        ("backup answer".into(), None)
    );
    let calls = f.store.llm_calls(10).await.unwrap();
    assert_eq!(calls.len(), 2);
    let success = calls
        .iter()
        .find(|call| call.status == "success")
        .or_else(|| calls.iter().find(|call| call.alias_switch_count == 1))
        .unwrap();
    assert_eq!(success.alias_id.as_deref(), Some(f.alias.id.as_str()));
    assert_eq!(success.alias_switch_count, 1);
    assert_eq!(success.model_id, "backup");
    let resolver = f.router.alias_resolver().unwrap();
    let health = resolver
        .state
        .health(&f.alias.config.targets[0].key(), i64::MIN)
        .unwrap();
    assert_eq!(health.status, "cooldown");
    assert!(health.retry_at_ms.unwrap() > chrono::Utc::now().timestamp_millis() + 110_000);
    let calls = f.fake.calls.lock().unwrap();
    assert_eq!(calls[1]["reasoning_effort"], "low");
}

#[tokio::test]
async fn server_failure_fails_over_but_bad_request_does_not() {
    for status in [500, 503, 401, 403, 400] {
        let f = fixture().await;
        f.fake.status.store(status, Ordering::SeqCst);
        let (text, error) = execute(&f, "c", "run1").await;
        if status == 400 {
            assert!(text.is_empty());
            assert!(error.unwrap().contains("400"));
            assert_eq!(f.fake.calls.lock().unwrap().len(), 1);
        } else {
            assert_eq!(text, "backup answer");
            assert!(error.is_none());
        }
    }
}

#[tokio::test]
async fn sticky_sessions_and_immediate_return_are_distinct() {
    let mut f = fixture().await;
    f.fake.status.store(429, Ordering::SeqCst);
    assert_eq!(execute(&f, "c", "run1").await.0, "backup answer");
    f.fake.status.store(0, Ordering::SeqCst);
    f.router
        .alias_resolver()
        .unwrap()
        .state
        .tested(&f.alias.config.targets[0].key());
    assert_eq!(execute(&f, "c", "run2").await.0, "backup answer");
    assert_eq!(execute(&f, "new-session", "run3").await.0, "primary answer");
    f.alias.config.return_mode = ReturnMode::Immediate;
    f.store
        .save_alias(Some(&f.alias.id), &f.alias.config)
        .await
        .unwrap();
    assert_eq!(execute(&f, "c", "run4").await.0, "primary answer");
}

#[tokio::test]
async fn partial_output_never_switches_and_next_turn_uses_backup() {
    let f = fixture().await;
    f.fake.partial.store(true, Ordering::SeqCst);
    let (text, error) = execute(&f, "c", "run1").await;
    assert_eq!(text, "primary answer");
    assert!(error.is_some());
    assert_eq!(f.fake.calls.lock().unwrap().len(), 1);
    assert_eq!(execute(&f, "c", "run2").await.0, "backup answer");
}

#[tokio::test]
async fn first_response_timeout_uses_next_target() {
    let f = fixture().await;
    let mut settings = f.store.alias_settings().await.unwrap();
    settings.first_token_timeout_seconds = 1;
    f.store.set_alias_settings(&settings).await.unwrap();
    f.fake.delay_ms.store(1500, Ordering::SeqCst);
    assert_eq!(
        execute(&f, "c", "run1").await,
        ("backup answer".into(), None)
    );
}

#[tokio::test]
async fn disabled_and_deleted_targets_are_skipped_and_exhaustion_explains_each() {
    let mut f = fixture().await;
    f.alias.config.targets[0].enabled = false;
    f.store
        .save_alias(Some(&f.alias.id), &f.alias.config)
        .await
        .unwrap();
    assert_eq!(execute(&f, "c", "run1").await.0, "backup answer");
    let model = f
        .store
        .model_by_source_id(&f.alias.config.targets[1].source_id)
        .await
        .unwrap()
        .unwrap();
    f.store.delete_model(&model.model_hash).await.unwrap();
    let (_, error) = execute(&f, "c", "run2").await;
    let error = error.unwrap();
    assert!(error.contains("my-smart"));
    assert!(error.contains("disabled"));
    assert!(error.contains("source_missing"));
}

#[tokio::test]
async fn tools_survive_alias_routing_and_close_failover_for_the_whole_run() {
    let f = fixture().await;
    let mut first = invocation("c", "run1");
    first
        .request
        .prompt
        .tools
        .push(cursor_server::model::ToolDefinition {
            name: "read_file".into(),
            description: "Read a file".into(),
            parameters: json!({"type":"object","properties":{}}),
        });
    let events = f
        .router
        .stream(first, CancellationToken::new())
        .collect::<Vec<_>>()
        .await;
    assert!(events
        .iter()
        .any(|event| matches!(event,Ok(ModelEvent::ToolCallStart {name,..}) if name=="read_file")));
    assert!(events.iter().any(|event| matches!(
        event,
        Ok(ModelEvent::Done(
            cursor_server::provider::FinishReason::ToolUse
        ))
    )));
    f.fake.status.store(503, Ordering::SeqCst);
    let mut next = invocation("c", "run1");
    next.call_id.push_str("-tool-round");
    next.provider_call_index = 2;
    let events = f
        .router
        .stream(next, CancellationToken::new())
        .collect::<Vec<_>>()
        .await;
    assert!(events.iter().any(Result::is_err));
    assert_eq!(
        f.fake.calls.lock().unwrap().len(),
        2,
        "later tool round cannot switch after previous model output"
    );
    assert_eq!(execute(&f, "c", "run2").await.0, "backup answer");
}

#[tokio::test]
async fn cancellation_does_not_poison_health_or_start_backup() {
    let f = fixture().await;
    f.fake.delay_ms.store(5000, Ordering::SeqCst);
    let cancellation = CancellationToken::new();
    let mut stream = f
        .router
        .stream(invocation("c", "cancelled"), cancellation.clone());
    let cancel = tokio::spawn(async move {
        tokio::time::sleep(std::time::Duration::from_millis(100)).await;
        cancellation.cancel();
    });
    let mut failed = false;
    while let Some(event) = stream.next().await {
        if event.is_err() {
            failed = true;
            break;
        }
    }
    cancel.await.unwrap();
    assert!(failed);
    assert!(f
        .router
        .alias_resolver()
        .unwrap()
        .state
        .health(&f.alias.config.targets[0].key(), 0)
        .is_none());
    assert!(f.fake.calls.lock().unwrap().len() <= 1);
}

#[tokio::test]
async fn renamed_alias_rejects_old_name_without_using_a_provider() {
    let mut f = fixture().await;
    f.alias.config.name = "my-renamed".into();
    f.store
        .save_alias(Some(&f.alias.id), &f.alias.config)
        .await
        .unwrap();
    let (_, error) = execute(&f, "c", "run1").await;
    assert!(error.unwrap().contains("Model not found"));
    assert!(f.fake.calls.lock().unwrap().is_empty());
}

#[tokio::test]
async fn alias_parameters_are_applied_before_runtime_compaction() {
    let f = fixture().await;
    let mut model = ModelSpec::new("MY-SMART");
    model.context_window_tokens = Some(1_000_000);
    assert!(f
        .router
        .alias_resolver()
        .unwrap()
        .configure(&mut model)
        .await
        .unwrap());
    assert_eq!(model.context_window_tokens, Some(128_000));
    assert_eq!(model.max_output_tokens, Some(8_000));
    assert_eq!(model.display_name.as_deref(), Some("my-smart"));
    assert!(model.reasoning.effort.is_none());
}
