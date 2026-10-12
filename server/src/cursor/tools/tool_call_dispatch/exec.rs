//! Dispatches command execution Tool calls.
//! Direct Exec and dynamic MCP dispatch.

use crate::{cursor::protocol::proto::agent::v1 as pb, model::ToolCall, Error, Result};

use super::{normalized, ToolReview, ToolStart};
use crate::cursor::tools::{
    auto_review, codec,
    runtime::{CursorToolRuntime, ExecContext},
    tool_call_result as result,
};

pub(super) async fn start(
    runtime: &CursorToolRuntime,
    call: &ToolCall,
    context: &ExecContext,
    review: Option<ToolReview<'_>>,
) -> Result<ToolStart> {
    let message = match normalized(&call.name).as_str() {
        "getmcptools" => {
            let id = runtime.reserve_exec(call, context).await?;
            codec::mcp_state_request(id, call)
        }
        "callmcptool" => {
            let server = required(call, "server")?;
            let tool = required(call, "toolName")?;
            let Some(route) = context
                .mcp_routes
                .get(&(server.to_string(), tool.to_string()))
            else {
                return Ok(ToolStart {
                    messages: Vec::new(),
                    completion: Some(result::mcp_failure(
                        call,
                        format!("MCP descriptor not found for {server}/{tool}"),
                    )?),
                });
            };
            let id = runtime.reserve_exec(call, context).await?;
            let message = codec::mcp_meta_request(id, call, server, route)?;
            let action = auto_review::Action::Mcp {
                server: server.into(),
                tool: route.tool_name.clone(),
                description: route.description.clone(),
                arguments: call.arguments.get("arguments").cloned().unwrap_or_default(),
            };
            if let Some(started) = review.and_then(|review| {
                review.hold(
                    runtime,
                    auto_review::Held::Exec(id),
                    action,
                    call,
                    context,
                    &message,
                )
            }) {
                return Ok(started);
            }
            message
        }
        _ => {
            let id = runtime.reserve_exec(call, context).await?;
            let message = codec::request(id, call, context)?;
            if let Some(started) =
                auto_review::Action::builtin(call)
                    .zip(review)
                    .and_then(|(action, review)| {
                        review.hold(
                            runtime,
                            auto_review::Held::Exec(id),
                            action,
                            call,
                            context,
                            &message,
                        )
                    })
            {
                return Ok(started);
            }
            message
        }
    };
    Ok(ToolStart {
        messages: vec![message],
        completion: None,
    })
}

fn required<'a>(call: &'a ToolCall, name: &str) -> Result<&'a str> {
    call.arguments
        .get(name)
        .and_then(serde_json::Value::as_str)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| Error::Protocol(format!("{} is missing {name}", call.name)))
}

pub(super) async fn start_dynamic(
    runtime: &CursorToolRuntime,
    call: &ToolCall,
    definition: &pb::McpToolDefinition,
    context: &ExecContext,
    review: Option<ToolReview<'_>>,
) -> Result<ToolStart> {
    let id = runtime
        .reserve_dynamic_mcp(call, context, definition)
        .await?;
    let message = codec::mcp_request(id, call, definition)?;
    let action = auto_review::Action::Mcp {
        server: definition.provider_identifier.clone(),
        tool: definition.tool_name.clone(),
        description: definition.description.clone(),
        arguments: call.arguments.clone(),
    };
    if let Some(started) = review.and_then(|review| {
        review.hold(
            runtime,
            auto_review::Held::Exec(id),
            action,
            call,
            context,
            &message,
        )
    }) {
        return Ok(started);
    }
    Ok(ToolStart {
        messages: vec![message],
        completion: None,
    })
}
