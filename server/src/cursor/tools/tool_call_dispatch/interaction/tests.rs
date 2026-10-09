//! Cache reads use exactly the same Cursor approval boundary as URL fetches.
use super::*;
use crate::{search::WebCache, store::Store};
use serde_json::json;

fn cache_call(content_id: &str) -> ToolCall {
    let arguments = json!({"url": "https://example.com", "content_id": content_id, "offset": 16384, "limit": 16384});
    ToolCall {
        index: 0,
        call_id: "cache-read".into(),
        model_call_id: "model-call".into(),
        name: "WebFetch".into(),
        arguments_text: arguments.to_string(),
        arguments,
        argument_error: None,
    }
}

fn response(id: u32, approved: bool) -> pb::InteractionResponse {
    pb::InteractionResponse {
        id,
        result: Some(pb::interaction_response::Result::WebFetchRequestResponse(
            pb::WebFetchRequestResponse {
                result: Some(if approved {
                    pb::web_fetch_request_response::Result::Approved(Default::default())
                } else {
                    pb::web_fetch_request_response::Result::Rejected(
                        pb::web_fetch_request_response::Rejected {
                            reason: "User denied cached content access".into(),
                        },
                    )
                }),
            },
        )),
    }
}

async fn reserve(runtime: &CursorToolRuntime, call: &ToolCall, owner: &str) -> u32 {
    let context = ExecContext {
        conversation_id: owner.into(),
        ..Default::default()
    };
    let started = start(runtime, call, &context).await.unwrap();
    assert!(started.completion.is_none());
    let Some(pb::agent_server_message::Message::InteractionQuery(query)) =
        started.messages[0].message.as_ref()
    else {
        panic!("expected approval query")
    };
    let Some(pb::interaction_query::Query::WebFetchRequestQuery(fetch)) = query.query.as_ref()
    else {
        panic!("expected WebFetch approval query")
    };
    assert!(!fetch.skip_approval);
    assert_eq!(fetch.args.as_ref().unwrap().url, "https://example.com");
    query.id
}

#[tokio::test]
async fn web_fetch_cached_pages_require_approval_and_preserve_conversation_ownership() {
    let cache = WebCache::default();
    let original = format!("{}TAIL", "é".repeat(10_000));
    let (_, entry) = cache
        .store("owner", "https://example.com", original.clone())
        .await
        .unwrap();
    let store = Store::connect("sqlite::memory:").await.unwrap();
    let fetch = WebFetch::managed(store, cache);
    let search = WebSearch::built_in();
    let runtime = CursorToolRuntime::default();
    let call = cache_call(&entry.content_id);
    let (results, mut receiver) = crate::cursor::tools::tool_call_result::tool_result_channel();
    let id = reserve(&runtime, &call, "owner").await;
    // No result is available before the remote approval response.
    assert!(
        tokio::time::timeout(std::time::Duration::from_millis(10), receiver.recv())
            .await
            .is_err()
    );
    let pending = runtime.take_interaction(id).await.unwrap();
    assert!(matches!(
        resume(&results, &search, &fetch, pending, &response(id, true))
            .await
            .unwrap(),
        InteractionContinuation::Pending
    ));
    let completion = receiver.recv().await.unwrap().unwrap();
    assert!(!completion.result().is_error);
    assert!(completion.result().content.ends_with(&original[16384..]));
    assert!(completion
        .result()
        .content
        .contains("End of cached content"));
    assert!(
        runtime.take_interaction(id).await.is_none(),
        "approval can only be consumed once"
    );

    let id = reserve(&runtime, &call, "other-conversation").await;
    let pending = runtime.take_interaction(id).await.unwrap();
    resume(&results, &search, &fetch, pending, &response(id, true))
        .await
        .unwrap();
    let completion = receiver.recv().await.unwrap().unwrap();
    assert!(completion.result().is_error);
    assert!(!completion.result().content.contains("TAIL"));
}

#[tokio::test]
async fn web_fetch_denial_and_preapproval_cancellation_never_read_cache() {
    let runtime = CursorToolRuntime::default();
    let call = cache_call("550e8400-e29b-41d4-a716-446655440000");
    let (results, mut receiver) = crate::cursor::tools::tool_call_result::tool_result_channel();
    let id = reserve(&runtime, &call, "owner").await;
    let pending = runtime.take_interaction(id).await.unwrap();
    let result = resume(
        &results,
        &WebSearch::built_in(),
        &WebFetch::built_in(),
        pending,
        &response(id, false),
    )
    .await
    .unwrap();
    let InteractionContinuation::Completed(completion) = result else {
        panic!("denial must be terminal")
    };
    assert!(completion.result().is_error);
    assert_eq!(
        completion.result().content,
        "User denied cached content access"
    );
    assert!(
        tokio::time::timeout(std::time::Duration::from_millis(10), receiver.recv())
            .await
            .is_err()
    );

    let id = reserve(&runtime, &call, "owner").await;
    runtime.interrupt_for_message().await;
    assert!(runtime.is_interrupted(id).await);
    assert!(runtime.take_interaction(id).await.is_none());
    assert!(
        tokio::time::timeout(std::time::Duration::from_millis(10), receiver.recv())
            .await
            .is_err()
    );
}
