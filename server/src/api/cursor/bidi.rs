//! Accepts ordered Cursor Bidi append requests and routes them by request_id.
use prost::Message;

use crate::{
    cursor::{
        conversation::TransportCommand,
        protocol::{
            events,
            proto::{agent::v1 as agent, aiserver::v1 as ai},
        },
        transport::{TransportParent, TransportRegistry},
    },
    Error, Result,
};

pub struct DecodedAppend {
    pub request_id: String,
    pub seqno: i64,
    pub message: agent::AgentClientMessage,
}

impl DecodedAppend {
    pub async fn resolve_model_aliases(&mut self, store: &crate::store::Store) -> Result<()> {
        let Some(agent::agent_client_message::Message::RunRequest(request)) =
            self.message.message.as_mut()
        else {
            return Ok(());
        };
        let aliases = store.cursor_model_aliases().await?;
        let mut selections = Vec::new();
        if let Some(model) = request.requested_model.as_mut() {
            selections.push(&mut model.model_id);
        }
        if let Some(model) = request.model_details.as_mut() {
            selections.push(&mut model.model_id);
        }
        for selection in &mut request.subagent_model_overrides {
            if let Some(agent::subagent_model_override::Selection::Model(model)) =
                selection.selection.as_mut()
            {
                selections.push(&mut model.model_id);
            }
        }
        for selection in selections {
            if let Some(target) = aliases.get(selection) {
                // A deleted/reconfigured target must not silently send the request
                // (and its context) to the original hosted model instead.
                if store.model(target).await?.is_none() {
                    return Err(Error::Config(format!(
                        "Cursor model alias {selection} targets a missing BYOK model"
                    )));
                }
                selection.clone_from(target);
            }
        }
        Ok(())
    }

    pub fn model_id(&self) -> Option<&str> {
        let agent::agent_client_message::Message::RunRequest(request) =
            self.message.message.as_ref()?
        else {
            return None;
        };
        request
            .requested_model
            .as_ref()
            .map(|model| model.model_id.as_str())
            .filter(|model| !model.is_empty())
            .or_else(|| {
                request
                    .model_details
                    .as_ref()
                    .map(|model| model.model_id.as_str())
                    .filter(|model| !model.is_empty())
            })
    }

    pub fn conversation_id(&self) -> Option<&str> {
        let agent::agent_client_message::Message::RunRequest(request) =
            self.message.message.as_ref()?
        else {
            return None;
        };
        request.conversation_id.as_deref()
    }

    pub fn is_background_task_completion(&self) -> bool {
        let Some(agent::agent_client_message::Message::RunRequest(request)) =
            self.message.message.as_ref()
        else {
            return false;
        };
        matches!(
            request
                .action
                .as_ref()
                .and_then(|action| action.action.as_ref()),
            Some(agent::conversation_action::Action::BackgroundTaskCompletionAction(_))
        )
    }

    pub fn trace_metadata(&self) -> serde_json::Value {
        let Some(message) = self.message.message.as_ref() else {
            return serde_json::json!({
                "append_seqno": self.seqno,
                "message_type": "empty",
            });
        };
        let agent::agent_client_message::Message::RunRequest(request) = message else {
            return serde_json::json!({
                "append_seqno": self.seqno,
                "message_type": client_message_type(message),
            });
        };
        let (action_type, history_messages, history_images) = request
            .action
            .as_ref()
            .and_then(|action| action.action.as_ref())
            .map(|action| match action {
                agent::conversation_action::Action::UserMessageAction(action) => {
                    let history = action.conversation_history.as_ref();
                    (
                        "user_message",
                        history.map_or(0, |history| history.messages.len()),
                        history.map_or(0, history_image_count),
                    )
                }
                agent::conversation_action::Action::BackgroundTaskCompletionAction(_) => {
                    ("background_task_completion", 0, 0)
                }
                agent::conversation_action::Action::ExecutePlanAction(_) => ("execute_plan", 0, 0),
                agent::conversation_action::Action::SummarizeAction(_) => ("summarize", 0, 0),
                _ => ("other", 0, 0),
            })
            .unwrap_or(("none", 0, 0));
        let state = request.conversation_state.as_ref();
        serde_json::json!({
            "append_seqno": self.seqno,
            "message_type": "run_request",
            "conversation_id": request.conversation_id,
            "model_id": self.model_id(),
            "action_type": action_type,
            "conversation_history_messages": history_messages,
            "conversation_history_images": history_images,
            "root_message_count": state.map_or(0, |state| state.root_prompt_messages_json.len()),
            "turn_count": state.map_or(0, |state| state.turns.len()),
            "prefetched_blob_count": request.pre_fetched_blobs.len(),
        })
    }
}

fn client_message_type(message: &agent::agent_client_message::Message) -> &'static str {
    use agent::agent_client_message::Message;
    match message {
        Message::RunRequest(_) => "run_request",
        Message::ExecClientMessage(_) => "exec_client_message",
        Message::ExecClientControlMessage(_) => "exec_client_control_message",
        Message::KvClientMessage(_) => "kv_client_message",
        Message::ConversationAction(_) => "conversation_action",
        Message::InteractionResponse(_) => "interaction_response",
        Message::ClientHeartbeat(_) => "client_heartbeat",
        Message::PrewarmRequest(_) => "prewarm_request",
    }
}

fn history_image_count(history: &agent::ConversationHistory) -> usize {
    use agent::{
        conversation_history_message::Message,
        conversation_history_tool_result_content::Content as ToolContent,
        conversation_history_user_content::Content as UserContent,
    };
    history
        .messages
        .iter()
        .map(|message| match message.message.as_ref() {
            Some(Message::User(user)) => user
                .content
                .iter()
                .filter(|content| matches!(content.content, Some(UserContent::Image(_))))
                .count(),
            Some(Message::Tool(tool)) => tool
                .content
                .iter()
                .filter(|content| matches!(content.content, Some(ToolContent::Image(_))))
                .count(),
            _ => 0,
        })
        .sum()
}

pub fn decode(request: &ai::BidiAppendRequest) -> Result<DecodedAppend> {
    let request_id = request
        .request_id
        .as_ref()
        .map(|id| id.request_id.as_str())
        .filter(|id| !id.is_empty())
        .ok_or_else(|| Error::Protocol("BidiAppend request_id is required".into()))?;
    if !request.data_binary.is_empty() {
        return Err(Error::Protocol(
            "BidiAppend data_binary is not part of the captured protocol".into(),
        ));
    }
    if request.data.is_empty() {
        return Err(Error::Protocol(
            "BidiAppend contains no AgentClientMessage".into(),
        ));
    }
    let payload = hex::decode(&request.data)
        .map_err(|error| Error::Protocol(format!("invalid BidiAppend hex: {error}")))?;
    Ok(DecodedAppend {
        request_id: request_id.into(),
        seqno: request.append_seqno,
        message: agent::AgentClientMessage::decode(payload.as_slice())?,
    })
}

pub async fn append(
    registry: &TransportRegistry,
    request: DecodedAppend,
    parent: Option<TransportParent>,
) -> Result<ai::BidiAppendResponse> {
    let replace_closing = request.model_id().is_some();
    let handle = registry
        .get_or_create_for_append(&request.request_id, replace_closing)
        .await?;
    let _admission = handle.admit()?;
    if let Some(conversation_id) = request.conversation_id() {
        handle.set_conversation_id(conversation_id)?;
    }
    if let Some(parent) = parent {
        handle.set_parent(parent)?;
    }
    if matches!(
        request.message.message.as_ref(),
        Some(agent::agent_client_message::Message::ClientHeartbeat(_))
    ) {
        handle.emit(&events::heartbeat())?;
    }
    handle
        .command(TransportCommand::Append {
            seqno: request.seqno,
            message: Box::new(request.message),
        })
        .await?;
    Ok(ai::BidiAppendResponse {})
}

#[cfg(test)]
mod alias_tests {
    use super::*;

    #[tokio::test]
    async fn resolves_all_explicit_selections_but_preserves_inheritance_and_parameters() {
        let directory = tempfile::tempdir().unwrap();
        let store = crate::store::Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        let model = store.create_model(&serde_json::from_value(serde_json::json!({
            "display_name": "BYOK model", "type": "openai", "base_url": "https://example.invalid",
            "api_key": "test", "tooltip_data": "test", "model_id": "provider-model"
        })).unwrap()).await.unwrap();
        store
            .set_cursor_model_aliases(std::collections::BTreeMap::from([(
                "hosted-model".into(),
                model.model_hash.clone(),
            )]))
            .await
            .unwrap();
        let selection = agent::RequestedModel {
            model_id: "hosted-model".into(),
            parameters: vec![agent::requested_model::ModelParameterValue {
                id: "effort".into(),
                value: "high".into(),
            }],
            ..Default::default()
        };
        let mut decoded = DecodedAppend {
            request_id: "aliases".into(),
            seqno: 0,
            message: agent::AgentClientMessage {
                message: Some(agent::agent_client_message::Message::RunRequest(
                    agent::AgentRunRequest {
                        requested_model: Some(selection.clone()),
                        model_details: Some(agent::ModelDetails {
                            model_id: "hosted-model".into(),
                            ..Default::default()
                        }),
                        subagent_model_overrides: vec![
                            agent::SubagentModelOverride {
                                subagent_type: "generalPurpose".into(),
                                selection: Some(agent::subagent_model_override::Selection::Model(
                                    selection.clone(),
                                )),
                            },
                            agent::SubagentModelOverride {
                                subagent_type: "explore".into(),
                                selection: Some(
                                    agent::subagent_model_override::Selection::Inherit(true),
                                ),
                            },
                        ],
                        ..Default::default()
                    },
                )),
            },
        };
        decoded.resolve_model_aliases(&store).await.unwrap();
        let Some(agent::agent_client_message::Message::RunRequest(run)) = decoded.message.message
        else {
            unreachable!()
        };
        let requested = run.requested_model.unwrap();
        assert_eq!(requested.model_id, model.model_hash);
        assert_eq!(requested.parameters, selection.parameters);
        assert_eq!(run.model_details.unwrap().model_id, model.model_hash);
        let Some(agent::subagent_model_override::Selection::Model(child)) =
            &run.subagent_model_overrides[0].selection
        else {
            unreachable!()
        };
        assert_eq!(child.model_id, model.model_hash);
        assert_eq!(child.parameters, selection.parameters);
        assert_eq!(
            run.subagent_model_overrides[1].selection,
            Some(agent::subagent_model_override::Selection::Inherit(true))
        );
    }
}
