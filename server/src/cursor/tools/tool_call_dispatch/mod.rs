//! Dispatches Tool calls to their execution adapters.
mod edit;
mod exec;
mod interaction;
mod local;
mod search;

use std::collections::BTreeMap;

use crate::{
    cursor::protocol::proto::agent::v1 as pb,
    model::ToolCall,
    search::{WebFetch, WebSearch},
    store::Store,
    Error, Result,
};

use super::{
    compat,
    runtime::{CursorToolRuntime, ExecContext, PendingInteraction},
    tool_call_result::{ToolCompletion, ToolResultSender},
};

pub(super) struct ToolStart {
    pub messages: Vec<pb::AgentServerMessage>,
    pub completion: Option<ToolCompletion>,
}

pub(super) enum InteractionContinuation {
    Completed(Box<ToolCompletion>),
    Pending,
}

pub(super) async fn start(
    runtime: &CursorToolRuntime,
    results: &ToolResultSender,
    call: &ToolCall,
    message_index: usize,
    dynamic_mcp: &BTreeMap<String, pb::McpToolDefinition>,
    context: &ExecContext,
    store: Option<&Store>,
) -> Result<ToolStart> {
    if let Some(definition) = dynamic_mcp.get(&call.name) {
        return exec::start_dynamic(runtime, call, definition, context).await;
    }

    if is_mcp_auth(call) {
        return interaction::start(runtime, call, context).await;
    }

    if context.task_disabled(call) {
        return local::subagents_disabled(call);
    }

    let normalized_call = normalize_block_until_ms(call)?;
    let call = normalized_call.as_ref().unwrap_or(call);

    match normalized(&call.name).as_str() {
        "shell" | "bash" | "read" | "delete" | "grep" | "glob" | "readlints" | "task"
        | "callmcptool" | "fetchmcpresource" | "getmcptools" => {
            exec::start(runtime, call, context).await
        }
        "write" | "strreplace" | "editnotebook" => edit::start(runtime, call, context).await,
        "askquestion" | "websearch" | "webfetch" | "switchmode" | "createplan"
        | "generateimage" => interaction::start(runtime, call, context).await,
        "todowrite" | "updatecurrentstep" => local::start(call, message_index),
        "semblesearch" | "semblefindrelated" => {
            search::start(runtime, results, call, context, store.cloned()).await
        }
        _ => Ok(unavailable_tool(call)),
    }
}

fn unavailable_tool(call: &ToolCall) -> ToolStart {
    ToolStart {
        messages: Vec::new(),
        completion: Some(compat::failure(call)),
    }
}

fn normalize_block_until_ms(call: &ToolCall) -> Result<Option<ToolCall>> {
    if !is_shell_tool(&call.name) {
        return Ok(None);
    }
    let Some(value) = call.arguments.get("block_until_ms") else {
        return Ok(None);
    };

    let integer = if let Some(value) = value.as_i64() {
        value
    } else {
        let value = value.as_f64().ok_or_else(|| {
            Error::Protocol(format!("{} block_until_ms must be an integer", call.name))
        })?;
        if !value.is_finite() || value.fract() != 0.0 {
            return Err(Error::Protocol(format!(
                "{} block_until_ms must be an integer",
                call.name
            )));
        }
        if value < i64::MIN as f64 || value > i64::MAX as f64 {
            return Err(Error::Protocol(format!(
                "{} block_until_ms is out of range",
                call.name
            )));
        }
        value as i64
    };

    if integer < 0 {
        return Err(Error::Protocol(format!(
            "{} block_until_ms is out of range",
            call.name
        )));
    }

    if value.as_i64().is_some() {
        return Ok(None);
    }

    let mut normalized_call = call.clone();
    normalized_call
        .arguments
        .as_object_mut()
        .ok_or_else(|| Error::Protocol(format!("{} arguments must be a JSON object", call.name)))?
        .insert("block_until_ms".into(), serde_json::Value::from(integer));
    Ok(Some(normalized_call))
}

fn is_mcp_auth(call: &ToolCall) -> bool {
    normalized(&call.name) == "callmcptool"
        && call
            .arguments
            .get("toolName")
            .and_then(serde_json::Value::as_str)
            .is_some_and(|tool| normalized(tool) == "mcpauth")
}

pub(super) async fn resume_interaction(
    results: &ToolResultSender,
    search: &WebSearch,
    fetch: &WebFetch,
    pending: PendingInteraction,
    response: &pb::InteractionResponse,
) -> Result<InteractionContinuation> {
    interaction::resume(results, search, fetch, pending, response).await
}

fn is_shell_tool(name: &str) -> bool {
    matches!(normalized(name).as_str(), "shell" | "bash")
}

pub(super) fn normalized(name: &str) -> String {
    name.chars()
        .filter(|character| character.is_ascii_alphanumeric())
        .flat_map(char::to_lowercase)
        .collect()
}
