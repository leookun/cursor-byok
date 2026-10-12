//! Encodes and decodes Cursor Tool wire messages.

mod query;
mod render;
mod request;
mod response;

use crate::{
    cursor::protocol::{events::server_interaction, proto::agent::v1 as pb},
    model::ToolCall,
    Error, Result,
};

pub use query::tool_query;
pub(crate) use render::{create_plan_partial, edit_content_delta, edit_path_partial, task_partial};
pub use render::{dynamic_mcp_placeholder, render_dynamic_mcp, tool_completed};
pub use request::{abort, mcp_request, mcp_state_request, request};
pub(crate) use request::{
    edit_read_request, json_object_to_prost, mcp_meta_request, semble_snapshot_request,
};
pub use response::{client_event, stream_closed, ClientExecEvent};

use render::{
    render_tool_call as render_builtin_tool_call, tool_placeholder as builtin_tool_placeholder,
    tool_started as builtin_tool_started,
};

pub fn tool_placeholder(name: &str, call_id: &str) -> Result<pb::ToolCall> {
    match builtin_tool_placeholder(name, call_id) {
        Ok(tool) => Ok(tool),
        Err(error) if is_unsupported_tool(&error, name) => {
            Ok(super::compat::placeholder(name, call_id))
        }
        Err(error) => Err(error),
    }
}

pub fn render_tool_call(call: &ToolCall, completed: bool) -> Result<pb::ToolCall> {
    match render_builtin_tool_call(call, completed) {
        Ok(tool) => Ok(tool),
        Err(error) if is_unsupported_tool(&error, &call.name) => {
            Ok(super::compat::render(call, completed))
        }
        Err(error) => Err(error),
    }
}

pub fn tool_started(
    call: &ToolCall,
    dynamic_mcp: Option<&pb::McpToolDefinition>,
) -> Result<pb::AgentServerMessage> {
    match builtin_tool_started(call, dynamic_mcp) {
        Ok(message) => Ok(message),
        Err(error) if dynamic_mcp.is_none() && is_unsupported_tool(&error, &call.name) => {
            Ok(server_interaction(
                pb::interaction_update::Message::ToolCallStarted(pb::ToolCallStartedUpdate {
                    call_id: call.call_id.clone(),
                    tool_call: Some(super::compat::render(call, false)),
                    model_call_id: call.model_call_id.clone(),
                }),
            ))
        }
        Err(error) => Err(error),
    }
}

fn is_unsupported_tool(error: &Error, name: &str) -> bool {
    matches!(error, Error::Protocol(message) if message == &format!("unsupported tool: {name}"))
}

pub fn arguments_delta(call: &ToolCall, delta: &str) -> Result<pb::AgentServerMessage> {
    Ok(server_interaction(
        pb::interaction_update::Message::PartialToolCall(pb::PartialToolCallUpdate {
            call_id: call.call_id.clone(),
            tool_call: Some(tool_placeholder(&call.name, &call.call_id)?),
            args_text_delta: delta.into(),
            model_call_id: call.model_call_id.clone(),
        }),
    ))
}

pub fn dynamic_mcp_arguments_delta(
    call: &ToolCall,
    delta: &str,
    definition: &pb::McpToolDefinition,
) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::PartialToolCall(
        pb::PartialToolCallUpdate {
            call_id: call.call_id.clone(),
            tool_call: Some(dynamic_mcp_placeholder(definition, &call.call_id)),
            args_text_delta: delta.into(),
            model_call_id: call.model_call_id.clone(),
        },
    ))
}
