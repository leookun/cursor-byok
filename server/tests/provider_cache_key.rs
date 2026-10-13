//! Verifies conversation cache keys on actual serialized provider requests.
use std::time::Duration;

use axum::{routing::post, Json, Router};
use cursor_server::{
    config::{ProviderConfig, ProviderKind},
    model::{
        ContentPart, ModelInvocation, ModelRequest, ModelSpec, ProjectedContent, ProjectedMessage,
        PromptSpec, Role,
    },
    provider::build_provider,
};
use futures_util::StreamExt;
use serde_json::Value;
use tokio_util::sync::CancellationToken;

#[tokio::test]
async fn openai_cache_keys_follow_conversations_across_turns_retries_and_model_tests() {
    for kind in [ProviderKind::OpenAiChat, ProviderKind::OpenAiResponses] {
        let (requests, mut captured) = tokio::sync::mpsc::unbounded_channel();
        let response = match kind {
            ProviderKind::OpenAiChat => "data: [DONE]\n\n",
            ProviderKind::OpenAiResponses => {
                "data: {\"type\":\"response.completed\",\"response\":{}}\n\n"
            }
            ProviderKind::Anthropic => unreachable!(),
        };
        let router = Router::new().route(
            "/",
            post(move |Json(body): Json<Value>| {
                let requests = requests.clone();
                async move {
                    requests.send(body).unwrap();
                    ([("content-type", "text/event-stream")], response)
                }
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move { axum::serve(listener, router).await.unwrap() });
        let provider = build_provider(&ProviderConfig {
            kind,
            request_url: format!("http://{address}/"),
            api_key: "test".into(),
            custom_headers: Default::default(),
            max_output_tokens: None,
            request_timeout: Duration::from_secs(5),
            allowed_body_fields: None,
        })
        .unwrap();

        for (conversation, run, call, model, prompt_cache_enabled, expected) in [
            (
                "conversation-a",
                "run-1",
                "run-1:0",
                "gpt-test",
                true,
                Some("cursor:conversation-a"),
            ),
            (
                "conversation-a",
                "run-1",
                "run-1:0:retry-1",
                "gpt-test",
                true,
                Some("cursor:conversation-a"),
            ),
            (
                "conversation-a",
                "run-2",
                "run-2:0",
                "gpt-test",
                true,
                Some("cursor:conversation-a"),
            ),
            (
                "conversation-b",
                "run-3",
                "run-3:0",
                "gpt-test",
                true,
                Some("cursor:conversation-b"),
            ),
            (
                "model-test-a",
                "model-test-a",
                "model-test-a",
                "gpt-test",
                false,
                None,
            ),
            (
                "model-test-b",
                "model-test-b",
                "model-test-b",
                "gpt-test",
                false,
                None,
            ),
            (
                "conversation-a",
                "run-4",
                "run-4:0",
                "qwen-test",
                true,
                None,
            ),
        ] {
            let invocation = ModelInvocation {
                call_id: call.into(),
                run_id: run.into(),
                conversation_id: conversation.into(),
                prompt_cache_enabled,
                provider_call_index: 0,
                request: ModelRequest {
                    prompt: PromptSpec {
                        instructions: String::new(),
                        tools: Vec::new(),
                    },
                    model: ModelSpec::new(model),
                    history: vec![ProjectedMessage {
                        message_id: call.into(),
                        role: Role::User,
                        content: ProjectedContent::Parts(vec![ContentPart::Text {
                            text: "hello".into(),
                        }]),
                    }],
                },
            };
            let mut stream = provider.stream(invocation, CancellationToken::new());
            while let Some(event) = stream.next().await {
                event.unwrap();
            }
            let body = captured.recv().await.unwrap();
            assert_eq!(
                body.get("prompt_cache_key"),
                expected.map(Value::from).as_ref(),
                "call {call}"
            );
        }
        server.abort();
    }
}
