//! Exercises text reconciliation through actual Responses HTTP/SSE and model cycles.
use std::{sync::Arc, time::Duration};

use axum::{http::header, routing::post, Router};
use cursor_server::{
    config::{ProviderConfig, ProviderKind},
    model::{ModelInvocation, ModelRequest, ModelSpec, PromptSpec},
    provider::{FinishReason, OpenAiResponsesProvider, Provider},
    run::{consume_model_cycle, ModelCycleResult},
};
use serde_json::{json, Value};
use tokio_util::sync::CancellationToken;

async fn cycle(mut events: Vec<Value>, completed: bool) -> ModelCycleResult {
    if completed {
        events.push(json!({"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2}}}));
    }
    let sse = events
        .iter()
        .map(|e| format!("event: {}\ndata: {e}\n\n", e["type"].as_str().unwrap()))
        .collect::<String>();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let app = Router::new().route(
        "/v1/responses",
        post(move || {
            let sse = sse.clone();
            async move { ([(header::CONTENT_TYPE, "text/event-stream")], sse) }
        }),
    );
    let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    let provider = Arc::new(OpenAiResponsesProvider::new(
        reqwest::Client::builder().no_proxy().build().unwrap(),
        ProviderConfig {
            kind: ProviderKind::OpenAiResponses,
            request_url: format!("http://{address}/v1/responses"),
            api_key: "test-only".into(),
            custom_headers: Default::default(),
            max_output_tokens: None,
            request_timeout: Duration::from_secs(5),
            allowed_body_fields: None,
        },
    ));
    let invocation = ModelInvocation {
        call_id: "text-call".into(),
        run_id: "text-run".into(),
        conversation_id: "text-conversation".into(),
        provider_call_index: 0,
        request: ModelRequest {
            prompt: PromptSpec {
                instructions: String::new(),
                tools: vec![],
            },
            model: ModelSpec::new("test-model"),
            history: vec![],
        },
    };
    let cancel = CancellationToken::new();
    let (sender, _receiver) = tokio::sync::mpsc::channel(128);
    let result = tokio::time::timeout(
        Duration::from_secs(5),
        consume_model_cycle(
            provider.stream(invocation, cancel.clone()),
            &sender,
            &cancel,
        ),
    )
    .await;
    server.abort();
    result.unwrap().unwrap()
}

fn delta(index: u64, content: u64, text: &str) -> Value {
    json!({"type":"response.output_text.delta","output_index":index,"content_index":content,"delta":text})
}

fn text_done(index: u64, content: u64, text: &str) -> Value {
    json!({"type":"response.output_text.done","output_index":index,"content_index":content,"text":text})
}

fn item_done(index: u64, texts: &[&str]) -> Value {
    json!({"type":"response.output_item.done","output_index":index,"item":{"type":"message","id":format!("msg_{index}"),"role":"assistant","content":texts.iter().map(|text|json!({"type":"output_text","text":text})).collect::<Vec<_>>()}})
}

#[tokio::test]
async fn terminal_only_items_around_a_tool_keep_all_text() {
    for completed in [true, false] {
        let result = cycle(vec![
            item_done(0, &["checking "]),
            json!({"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"Read","arguments":"{\"path\":\"test.txt\"}"}}),
            item_done(2, &["looks fine"]),
        ], completed).await;
        assert_eq!(result.text, "checking looks fine");
        assert_eq!(result.finish_reason, FinishReason::ToolUse);
        assert_eq!(result.calls.len(), 1);
        assert_eq!(result.calls[0].arguments, json!({"path":"test.txt"}));
    }
}

#[tokio::test]
async fn later_item_repairs_missing_unicode_suffix_once() {
    let result = cycle(
        vec![
            item_done(0, &["开始。"]),
            delta(2, 0, "结果"),
            text_done(2, 0, "结果正确。"),
            item_done(2, &["结果正确。"]),
        ],
        true,
    )
    .await;
    assert_eq!(result.text, "开始。结果正确。");
    assert_eq!(result.finish_reason, FinishReason::Stop);
    assert_eq!(result.usage.unwrap().output_tokens, Some(2));
}

#[tokio::test]
async fn repeated_terminal_reports_do_not_replay_text() {
    let result = cycle(
        vec![
            delta(0, 0, "done"),
            text_done(0, 0, "done"),
            text_done(0, 0, "done"),
            item_done(0, &["done"]),
            item_done(0, &["done"]),
        ],
        true,
    )
    .await;
    assert_eq!(result.text, "done");
}

#[tokio::test]
async fn interleaved_items_reconcile_only_their_own_suffixes() {
    let result = cycle(
        vec![
            delta(0, 0, "甲"),
            delta(1, 0, "乙"),
            text_done(0, 0, "甲尾甲"),
            text_done(1, 0, "乙尾乙"),
            item_done(0, &["甲尾甲"]),
            item_done(1, &["乙尾乙"]),
        ],
        true,
    )
    .await;
    assert_eq!(result.text, "甲乙尾甲尾乙");
}

#[tokio::test]
async fn late_terminal_report_for_an_earlier_item_is_not_replayed() {
    let result = cycle(
        vec![
            text_done(0, 0, "first"),
            item_done(1, &["second"]),
            item_done(0, &["first"]),
        ],
        true,
    )
    .await;
    assert_eq!(result.text, "firstsecond");
}

#[tokio::test]
async fn item_snapshot_repairs_multiple_content_parts() {
    let result = cycle(
        vec![
            text_done(0, 0, "part one;"),
            delta(0, 1, "part"),
            text_done(0, 1, "part two"),
            item_done(0, &["part one;", "part two"]),
        ],
        true,
    )
    .await;
    assert_eq!(result.text, "part one;part two");
}

#[tokio::test]
async fn unindexed_events_keep_the_current_scope() {
    let result = cycle(
        vec![
            delta(0, 0, "hello"),
            json!({"type":"response.output_text.done","text":"hello world"}),
            item_done(0, &["hello world"]),
        ],
        true,
    )
    .await;
    assert_eq!(result.text, "hello world");
}

#[tokio::test]
async fn empty_messages_do_not_hide_later_text() {
    let result = cycle(vec![item_done(0, &[""]), item_done(1, &["visible"])], true).await;
    assert_eq!(result.text, "visible");
}

#[tokio::test]
async fn first_explicit_index_adopts_the_unindexed_prefix() {
    let result = cycle(
        vec![
            json!({"type":"response.output_text.delta","delta":"hello"}),
            text_done(2, 0, "hello world"),
            item_done(2, &["hello world"]),
        ],
        true,
    )
    .await;
    assert_eq!(result.text, "hello world");
}
