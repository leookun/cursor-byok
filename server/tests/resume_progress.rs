#[path = "support/fake_provider.rs"]
mod fake_provider;
#[path = "support/fixtures.rs"]
mod fixtures;

use std::sync::Arc;

use cursor_server::{
    cursor::{
        connect,
        prompting::{PromptAssets, PromptCompiler},
        proto::agent::v1 as pb,
        CursorCommand, CursorSessionHandle, CursorSessionRegistry,
    },
    model::{ContentPart, ProjectedContent, Role},
    provider::{FinishReason, ModelEvent},
};
use prost::Message;

#[tokio::test]
async fn resume_action_context_and_continuation_reach_the_next_model_call() {
    let (_directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(text_response("model-initial", "initial response"));
    provider.push(text_response("model-resumed", "continued response"));
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

    let first = registry.get_or_create("initial-request").await.unwrap();
    let mut first_output = first.subscribe();
    first
        .command(CursorCommand::Append {
            seqno: 0,
            message: Box::new(start_request()),
        })
        .await
        .unwrap();
    let state = drive_to_end(&first, &mut first_output, 1).await;
    assert!(state.pending_tool_calls.is_empty());

    let resumed = registry.get_or_create("resume-request").await.unwrap();
    let mut resumed_output = resumed.subscribe();
    resumed
        .command(CursorCommand::Append {
            seqno: 0,
            message: Box::new(resume_request(state)),
        })
        .await
        .unwrap();
    let _ = drive_to_end(&resumed, &mut resumed_output, 1).await;

    let requests = provider.requests();
    assert_eq!(requests.len(), 2);
    let history = &requests[1].history;
    assert_eq!(
        history
            .iter()
            .filter(|message| message.message_id.starts_with("request-context:"))
            .count(),
        2,
        "the changed ResumeAction context must be appended to history"
    );
    let texts = history
        .iter()
        .filter_map(projected_text)
        .collect::<Vec<_>>();
    assert!(texts.iter().any(|text| text.contains("Shell: pwsh")));
    let last = history.last().expect("resume history must not be empty");
    assert_eq!(last.role, Role::User);
    assert_eq!(last.message_id, "resume-prompt:resume-request");
    assert!(projected_text(last).as_deref().is_some_and(|text| {
        text.contains("<resume>") && text.contains("make concrete progress")
    }));
}

fn text_response(model_call_id: &str, text: &str) -> Vec<ModelEvent> {
    vec![
        ModelEvent::Start {
            model_call_id: model_call_id.into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta(text.into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]
}

fn start_request() -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::RunRequest(
            pb::AgentRunRequest {
                action: Some(pb::ConversationAction {
                    action: Some(pb::conversation_action::Action::UserMessageAction(
                        pb::UserMessageAction {
                            user_message: Some(pb::UserMessage {
                                text: "perform the task".into(),
                                message_id: "user-1".into(),
                                mode: pb::AgentMode::Agent as i32,
                                ..Default::default()
                            }),
                            request_context: Some(request_context("bash")),
                            ..Default::default()
                        },
                    )),
                    ..Default::default()
                }),
                conversation_id: Some("resume-progress-conversation".into()),
                run_id: Some("initial-wire-run".into()),
                requested_model: Some(pb::RequestedModel {
                    model_id: "test-model".into(),
                    ..Default::default()
                }),
                ..Default::default()
            },
        )),
    }
}

fn resume_request(state: pb::ConversationStateStructure) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::RunRequest(
            pb::AgentRunRequest {
                action: Some(pb::ConversationAction {
                    action: Some(pb::conversation_action::Action::ResumeAction(
                        pb::ResumeAction {
                            request_context: Some(request_context("pwsh")),
                            ..Default::default()
                        },
                    )),
                    ..Default::default()
                }),
                conversation_state: Some(state),
                conversation_id: Some("resume-progress-conversation".into()),
                run_id: Some("resume-wire-run".into()),
                requested_model: Some(pb::RequestedModel {
                    model_id: "test-model".into(),
                    ..Default::default()
                }),
                ..Default::default()
            },
        )),
    }
}

fn request_context(shell: &str) -> pb::RequestContext {
    pb::RequestContext {
        env: Some(pb::RequestContextEnv {
            os_version: "windows".into(),
            workspace_paths: vec!["C:/workspace".into()],
            shell: shell.into(),
            terminals_folder: "C:/terminals".into(),
            agent_transcripts_folder: "C:/transcripts".into(),
            ..Default::default()
        }),
        ..Default::default()
    }
}

fn projected_text(message: &cursor_server::model::ProjectedMessage) -> Option<String> {
    let ProjectedContent::Parts(parts) = &message.content else {
        return None;
    };
    Some(
        parts
            .iter()
            .filter_map(|part| match part {
                ContentPart::Text { text } => Some(text.as_str()),
                _ => None,
            })
            .collect::<Vec<_>>()
            .join("\n"),
    )
}

async fn drive_to_end(
    handle: &CursorSessionHandle,
    output: &mut tokio::sync::mpsc::UnboundedReceiver<bytes::Bytes>,
    mut seqno: i64,
) -> pb::ConversationStateStructure {
    let mut latest = None;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(10), output.recv())
            .await
            .unwrap()
            .unwrap();
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                assert!(matches!(
                    kv.message,
                    Some(pb::kv_server_message::Message::SetBlobArgs(_))
                ));
                handle
                    .command(CursorCommand::Append {
                        seqno,
                        message: Box::new(pb::AgentClientMessage {
                            message: Some(pb::agent_client_message::Message::KvClientMessage(
                                pb::KvClientMessage {
                                    id: kv.id,
                                    message: Some(pb::kv_client_message::Message::SetBlobResult(
                                        pb::SetBlobResult { error: None },
                                    )),
                                },
                            )),
                        }),
                    })
                    .await
                    .unwrap();
                seqno += 1;
            }
            Some(pb::agent_server_message::Message::ConversationCheckpointUpdate(state)) => {
                latest = Some(state)
            }
            _ => {}
        }
    }
    latest.expect("run must publish a checkpoint")
}
