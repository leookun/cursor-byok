#[path = "support/fake_provider.rs"]
mod fake_provider;
#[path = "support/fixtures.rs"]
mod fixtures;

use std::{collections::HashMap, sync::Arc, time::Duration};

use cursor_server::{
    cursor::prompting::{PromptAssets, PromptCompiler},
    cursor::{connect, proto::agent::v1 as pb, CursorCommand, CursorSessionRegistry},
    model::{
        ContentPart, ModelConfigInput, ModelType, NewLlmCall, ProjectedContent, ProjectedMessage,
        Role, Usage, OPENAI_CHAT_ENDPOINT,
    },
    provider::{FinishReason, ModelEvent},
    Error,
};
use prost::Message;

#[tokio::test]
async fn compacted_checkpoint_survives_followup_error_and_resume_continues() {
    let (_directory, store) = fixtures::temp_store().await;
    let model = store
        .create_model(&ModelConfigInput {
            sort_order: 0,
            display_name: "Threshold Model".into(),
            model_type: ModelType::OpenAi,
            base_url: "https://example.com/v1/chat/completions".into(),
            use_full_url: true,
            api_key: "test-key".into(),
            tooltip_data: "Threshold Model".into(),
            model_id: "threshold-model".into(),
            reasoning_effort: None,
            openai_endpoint: OPENAI_CHAT_ENDPOINT.into(),
            openai_extra_params_enabled: false,
            openai_extra_params: serde_json::json!({}),
            custom_headers_enabled: false,
            custom_headers: serde_json::json!({}),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: serde_json::json!({}),
            context_window_tokens: Some(150_000),
            max_completion_tokens: Some(16_000),
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
        })
        .await
        .unwrap();
    let provider = fake_provider::FakeProvider::default();
    provider.push(text_response("first answer marker", 132_000, 100));
    provider.push(text_response(
        "Durable summary: current task remains unfinished.",
        132_100,
        20,
    ));
    provider.push_error(Error::Provider("post-compaction failure".into()));
    provider.push(text_response("recovered answer", 2_000, 10));

    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = CursorSessionRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
        Default::default(),
    );

    let first = run(
        &registry,
        "first-request",
        user_request(
            "compaction-recovery-conversation",
            "first-user",
            "remember the first answer marker",
            &model.model_hash,
            None,
        ),
    )
    .await;
    assert!(first.end_error.is_none());
    let first_state = first.checkpoints.last().unwrap().clone();
    let first_used = first_state.token_details.as_ref().unwrap().used_tokens;
    assert_eq!(first_used, 132_100);
    let first_request = provider.requests().into_iter().next().unwrap();
    record_threshold_anchor(&store, &model, 0, first_request.prompt.tools.len()).await;

    let failed = run(
        &registry,
        "failing-request",
        user_request(
            "compaction-recovery-conversation",
            "current-user",
            "continue-current-work-marker",
            &model.model_hash,
            Some(first_state),
        ),
    )
    .await;

    assert_eq!(failed.summary_started, 1);
    assert_eq!(failed.summary_completed, 1);
    assert!(failed
        .end_error
        .as_deref()
        .is_some_and(|error| error.contains("post-compaction failure")));

    let compacted_state = failed
        .checkpoints
        .iter()
        .rev()
        .find(|state| state.summary.is_some())
        .cloned()
        .expect("automatic compaction must publish its checkpoint before continuing");
    let compacted_details = compacted_state.token_details.as_ref().unwrap();
    assert_eq!(compacted_details.max_tokens, 150_000);
    assert!(
        compacted_details.used_tokens < first_used,
        "the compacted checkpoint must reset the stale pre-compaction token count"
    );
    let summary_id = compacted_state.summary.as_ref().unwrap();
    let summary = pb::ConversationSummary::decode(
        failed
            .blobs
            .get(summary_id)
            .expect("published summary Blob")
            .as_slice(),
    )
    .unwrap();
    assert_eq!(
        summary.summary,
        "Durable summary: current task remains unfinished."
    );

    let recovered = run(
        &registry,
        "resume-request",
        resume_request(
            "compaction-recovery-conversation",
            &model.model_hash,
            compacted_state,
        ),
    )
    .await;
    assert!(recovered.end_error.is_none());
    assert!(!recovered.checkpoints.is_empty());

    let requests = provider.requests();
    assert_eq!(requests.len(), 4);
    assert!(requests[1].prompt.tools.is_empty());
    let failed_history = history_text(&requests[2].history);
    assert!(failed_history.contains("Durable summary"));
    assert!(failed_history.contains("continue-current-work-marker"));
    assert!(!failed_history.contains("first answer marker"));
    let resumed_history = history_text(&requests[3].history);
    assert!(resumed_history.contains("Durable summary"));
    assert!(resumed_history.contains("continue-current-work-marker"));
    assert!(!resumed_history.contains("first answer marker"));
    assert_eq!(requests[3].history.last().unwrap().role, Role::User);
}

#[tokio::test]
async fn resume_action_auto_compacts_large_recovered_state_without_new_user_message() {
    let (_directory, store) = fixtures::temp_store().await;
    let model = store
        .create_model(&ModelConfigInput {
            sort_order: 0,
            display_name: "Resume Threshold".into(),
            model_type: ModelType::OpenAi,
            base_url: "https://example.com/v1/chat/completions".into(),
            use_full_url: true,
            api_key: "test-key".into(),
            tooltip_data: "Resume Threshold".into(),
            model_id: "resume-threshold".into(),
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
    provider.push(text_response("resume summary marker", 26_000, 100));
    provider.push(text_response(
        "continued after resume compaction",
        2_000,
        20,
    ));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = CursorSessionRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
        Default::default(),
    );
    let first = run(
        &registry,
        "resume-large-first",
        user_request(
            "resume-large-conversation",
            "resume-user",
            "start long work",
            &model.model_hash,
            None,
        ),
    )
    .await;
    assert!(first.end_error.is_none());
    let state = first.checkpoints.last().unwrap().clone();
    let resumed = run(
        &registry,
        "resume-large-second",
        resume_request("resume-large-conversation", &model.model_hash, state),
    )
    .await;
    assert!(resumed.end_error.is_none());
    assert_eq!(resumed.summary_started, 1);
    assert_eq!(resumed.summary_completed, 1);
    let requests = provider.requests();
    assert_eq!(requests.len(), 3);
    assert!(requests[1].prompt.tools.is_empty());
    assert!(history_text(&requests[2].history).contains("resume summary marker"));
}

async fn record_threshold_anchor(
    store: &cursor_server::store::Store,
    model: &cursor_server::model::ModelConfig,
    message_count: usize,
    tool_count: usize,
) {
    let call_id = "synthetic-threshold-anchor";
    let provider_type = model.provider_type();
    store
        .start_llm_call(&NewLlmCall {
            call_id: call_id.into(),
            run_id: "synthetic-anchor-run".into(),
            conversation_id: "compaction-recovery-conversation".into(),
            provider_call_index: 0,
            model_hash: model.model_hash.clone(),
            provider_type,
            provider_url: model.base_url.clone(),
            request_type: provider_type,
            request_url: model.request_url().unwrap(),
            model_id: model.model_id.clone(),
            display_name: model.display_name.clone(),
            reasoning_effort: None,
            fast: false,
            message_count,
            tool_count,
            detailed: false,
        })
        .await
        .unwrap();
    store
        .record_llm_usage(
            call_id,
            Usage {
                input_tokens: Some(132_000),
                output_tokens: Some(100),
                total_tokens: Some(132_100),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    store
        .finish_llm_call(call_id, "completed", Some("stop"), 1, None, None)
        .await
        .unwrap();
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
        let frame = tokio::time::timeout(Duration::from_secs(30), receiver.recv())
            .await
            .unwrap()
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
