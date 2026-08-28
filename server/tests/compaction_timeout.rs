#[path = "support/fake_provider.rs"]
mod fake_provider;
#[path = "support/fixtures.rs"]
mod fixtures;

use std::{collections::HashMap, sync::Arc, time::Duration};

use cursor_server::{
    cursor::prompting::{PromptAssets, PromptCompiler},
    cursor::{connect, proto::agent::v1 as pb, CursorCommand, CursorSessionRegistry},
    model::{
        ContentPart, ModelConfigInput, ModelType, ProjectedContent, ProjectedMessage, Role, Usage,
        OPENAI_CHAT_ENDPOINT,
    },
    provider::{FinishReason, ModelEvent},
};
use prost::Message;

#[tokio::test]
async fn stalled_auto_compaction_times_out_falls_back_and_continues() {
    let (_directory, store) = fixtures::temp_store().await;
    let model = store
        .create_model(&ModelConfigInput {
            sort_order: 0,
            display_name: "Compaction Timeout Model".into(),
            model_type: ModelType::OpenAi,
            base_url: "https://example.com/v1/chat/completions".into(),
            use_full_url: true,
            api_key: "test-key".into(),
            tooltip_data: "Compaction Timeout Model".into(),
            model_id: "compaction-timeout-model".into(),
            reasoning_effort: None,
            openai_endpoint: OPENAI_CHAT_ENDPOINT.into(),
            openai_extra_params_enabled: false,
            openai_extra_params: serde_json::json!({}),
            custom_headers_enabled: false,
            custom_headers: serde_json::json!({}),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: serde_json::json!({}),
            context_window_tokens: Some(30_000),
            max_completion_tokens: Some(4_000),
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
        })
        .await
        .unwrap();

    let provider = fake_provider::FakeProvider::default();
    provider.push(text_response("x".repeat(100_000), 1_000, 25_000));
    // The automatic summary request never produces an event. The engine must stop
    // waiting at its own deadline instead of relying on provider cancellation.
    provider.push_pending();
    provider.push(text_response("continued after timeout fallback", 2_000, 20));

    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = CursorSessionRegistry::new(
        store,
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
        Default::default(),
    );

    let first = run(
        &registry,
        "timeout-first-request",
        user_request(
            "timeout-conversation",
            "timeout-user",
            "start a long-running task",
            &model.model_hash,
            None,
        ),
    )
    .await;
    assert!(first.end_error.is_none());
    let state = first.checkpoints.last().unwrap().clone();

    let started = std::time::Instant::now();
    let resumed = run(
        &registry,
        "timeout-resume-request",
        resume_request("timeout-conversation", &model.model_hash, state),
    )
    .await;
    let elapsed = started.elapsed();

    assert!(resumed.end_error.is_none());
    assert_eq!(resumed.summary_started, 1);
    assert_eq!(resumed.summary_completed, 1);
    assert!(
        elapsed >= Duration::from_secs(29),
        "test must actually exercise the 30s automatic-compaction deadline: {elapsed:?}"
    );
    assert!(
        elapsed < Duration::from_secs(40),
        "stalled compaction must be bounded rather than hanging: {elapsed:?}"
    );

    let compacted = resumed
        .checkpoints
        .iter()
        .rev()
        .find(|state| state.summary.is_some())
        .expect("timeout fallback must publish a compacted checkpoint");
    let summary_id = compacted.summary.as_ref().unwrap();
    let summary = pb::ConversationSummary::decode(
        resumed
            .blobs
            .get(summary_id)
            .expect("fallback summary blob must be published")
            .as_slice(),
    )
    .unwrap();
    assert!(summary
        .summary
        .contains("Durable recent conversation state"));

    let requests = provider.requests();
    assert_eq!(requests.len(), 3);
    assert!(requests[1].prompt.tools.is_empty());
    assert_eq!(requests[1].model.max_output_tokens, Some(2_048));
    let continued_history = history_text(&requests[2].history);
    assert!(continued_history.contains("Durable recent conversation state"));
    assert_eq!(requests[2].history.last().unwrap().role, Role::User);
}

#[derive(Default)]
struct Output {
    checkpoints: Vec<pb::ConversationStateStructure>,
    blobs: HashMap<Vec<u8>, Vec<u8>>,
    summary_started: usize,
    summary_completed: usize,
    end_error: Option<String>,
}

async fn run(
    registry: &CursorSessionRegistry,
    request_id: &str,
    request: pb::AgentClientMessage,
) -> Output {
    let handle = registry.get_or_create(request_id).await.unwrap();
    let mut receiver = handle.subscribe();
    handle
        .command(CursorCommand::Append {
            seqno: 0,
            message: Box::new(request),
        })
        .await
        .unwrap();

    let mut append_seqno = 1;
    let mut output = Output::default();
    loop {
        let frame = tokio::time::timeout(Duration::from_secs(45), receiver.recv())
            .await
            .expect("server must make progress within the timeout-test window")
            .unwrap();
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            let text = String::from_utf8_lossy(&payload).into_owned();
            if text.contains("error") {
                output.end_error = Some(text);
            }
            return output;
        }

        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                if let Some(pb::kv_server_message::Message::SetBlobArgs(set)) = kv.message {
                    output.blobs.insert(set.blob_id, set.blob_data);
                }
                handle
                    .command(CursorCommand::Append {
                        seqno: append_seqno,
                        message: Box::new(kv_ack(kv.id)),
                    })
                    .await
                    .unwrap();
                append_seqno += 1;
            }
            Some(pb::agent_server_message::Message::ConversationCheckpointUpdate(state)) => {
                output.checkpoints.push(state)
            }
            Some(pb::agent_server_message::Message::InteractionUpdate(update)) => {
                match update.message {
                    Some(pb::interaction_update::Message::SummaryStarted(_)) => {
                        output.summary_started += 1
                    }
                    Some(pb::interaction_update::Message::SummaryCompleted(_)) => {
                        output.summary_completed += 1
                    }
                    _ => {}
                }
            }
            _ => {}
        }
    }
}

fn text_response(text: impl Into<String>, input: u64, output: u64) -> Vec<ModelEvent> {
    let text = text.into();
    let model_call_id = format!("call-{}", text.len());
    vec![
        ModelEvent::Start { model_call_id },
        ModelEvent::TextStart,
        ModelEvent::TextDelta(text),
        ModelEvent::TextEnd,
        ModelEvent::Usage(Usage {
            input_tokens: Some(input),
            output_tokens: Some(output),
            total_tokens: Some(input + output),
            ..Default::default()
        }),
        ModelEvent::Done(FinishReason::Stop),
    ]
}

fn history_text(history: &[ProjectedMessage]) -> String {
    let mut output = String::new();
    for message in history {
        match &message.content {
            ProjectedContent::Parts(parts) => {
                for part in parts {
                    if let ContentPart::Text { text } = part {
                        output.push_str(text);
                        output.push('\n');
                    }
                }
            }
            ProjectedContent::Assistant { text, thinking, .. } => {
                output.push_str(text);
                output.push('\n');
                output.push_str(thinking);
                output.push('\n');
            }
            ProjectedContent::ToolResult(result) => {
                output.push_str(&result.content);
                output.push('\n');
            }
        }
    }
    output
}

fn user_request(
    conversation_id: &str,
    message_id: &str,
    text: &str,
    model_id: &str,
    state: Option<pb::ConversationStateStructure>,
) -> pb::AgentClientMessage {
    request(
        conversation_id,
        model_id,
        state,
        pb::conversation_action::Action::UserMessageAction(pb::UserMessageAction {
            user_message: Some(pb::UserMessage {
                text: text.into(),
                message_id: message_id.into(),
                mode: pb::AgentMode::Agent as i32,
                ..Default::default()
            }),
            request_context: Some(pb::RequestContext::default()),
            ..Default::default()
        }),
    )
}

fn resume_request(
    conversation_id: &str,
    model_id: &str,
    state: pb::ConversationStateStructure,
) -> pb::AgentClientMessage {
    request(
        conversation_id,
        model_id,
        Some(state),
        pb::conversation_action::Action::ResumeAction(pb::ResumeAction {
            request_context: Some(pb::RequestContext::default()),
            ..Default::default()
        }),
    )
}

fn request(
    conversation_id: &str,
    model_id: &str,
    state: Option<pb::ConversationStateStructure>,
    action: pb::conversation_action::Action,
) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::RunRequest(
            pb::AgentRunRequest {
                requested_model: Some(pb::RequestedModel {
                    model_id: model_id.into(),
                    ..Default::default()
                }),
                action: Some(pb::ConversationAction {
                    action: Some(action),
                    ..Default::default()
                }),
                conversation_id: Some(conversation_id.into()),
                conversation_state: state,
                run_id: Some("reusable-wire-run-id".into()),
                ..Default::default()
            },
        )),
    }
}

fn kv_ack(id: u32) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::KvClientMessage(
            pb::KvClientMessage {
                id,
                message: Some(pb::kv_client_message::Message::SetBlobResult(
                    pb::SetBlobResult { error: None },
                )),
            },
        )),
    }
}
