//! Encodes Tool execution requests sent to Cursor.
use serde_json::{Map, Value};

use crate::{
    cursor::{
        protocol::proto::agent::v1 as pb,
        tools::{
            edit::{self, EditWrite},
            runtime::{ExecContext, McpRoute},
        },
    },
    model::ToolCall,
    Error, Result,
};

pub fn request(id: u32, call: &ToolCall, context: &ExecContext) -> Result<pb::AgentServerMessage> {
    use pb::exec_server_message::Message;
    let string = |name: &str| {
        call.arguments
            .get(name)
            .and_then(Value::as_str)
            .map(str::to_string)
            .ok_or_else(|| Error::Protocol(format!("{} is missing {name}", call.name)))
    };
    // Claude 系模型常按 Claude Code 习惯输出别名参数(如 file_path),逐个回退兼容。
    let string_aliased = |names: &[&str]| -> Result<String> {
        for name in names {
            if let Some(value) = call.arguments.get(name).and_then(Value::as_str) {
                return Ok(value.to_string());
            }
        }
        Err(Error::Protocol(format!(
            "{} is missing {}",
            call.name, names[0]
        )))
    };
    let optional_string = |name: &str| {
        call.arguments
            .get(name)
            .and_then(Value::as_str)
            .map(str::to_string)
    };
    let int = |name: &str| {
        call.arguments
            .get(name)
            .and_then(Value::as_i64)
            .map(|v| v as i32)
    };
    let message = match normalize(&call.name).as_str() {
        "shell" | "bash" => {
            let command = string("command")?;
            let (simple_commands, parsing_result) = shell_command_metadata(&command);
            Message::ShellStreamArgs(pb::ShellArgs {
                command,
                working_directory: optional_string("working_directory").unwrap_or_default(),
                timeout: shell_timeout(call)?,
                tool_call_id: call.call_id.clone(),
                simple_commands,
                parsing_result,
                file_output_threshold_bytes: Some(40_000),
                timeout_behavior: pb::TimeoutBehavior::Background as i32,
                hard_timeout: Some(86_400_000),
                description: optional_string("description"),
                output_notification: shell_notification(call)?,
                smart_mode_approval: smart_mode_approval(
                    call,
                    "request_smart_mode_approval",
                    "smart_mode_block_reason",
                )?,
                requested_sandbox_policy: shell_sandbox_policy(call),
                close_stdin: true,
                conversation_id: Some(context.conversation_id.clone()),
                admin_command_denylist: context.admin_command_denylist.clone(),
                ..Default::default()
            })
        }
        "read" => Message::ReadArgs(pb::ReadArgs {
            path: string_aliased(&["path", "file_path", "filePath"])?,
            tool_call_id: call.call_id.clone(),
            offset: int("offset"),
            limit: call
                .arguments
                .get("limit")
                .and_then(Value::as_u64)
                .map(|v| v as u32),
            encoding_hint: optional_string("encoding_hint"),
        }),
        "delete" => Message::DeleteArgs(pb::DeleteArgs {
            path: string_aliased(&["path", "file_path", "filePath"])?,
            tool_call_id: call.call_id.clone(),
        }),
        "grep" => Message::GrepArgs(pb::GrepArgs {
            pattern: string("pattern")?,
            path: optional_string("path"),
            glob: optional_string("glob"),
            output_mode: optional_string("output_mode"),
            context_before: int("-B"),
            context_after: int("-A"),
            context: int("-C"),
            case_insensitive: call.arguments.get("-i").and_then(Value::as_bool),
            r#type: optional_string("type"),
            head_limit: int("head_limit"),
            multiline: call.arguments.get("multiline").and_then(Value::as_bool),
            sort: optional_string("sort"),
            sort_ascending: call
                .arguments
                .get("sort_ascending")
                .and_then(Value::as_bool),
            tool_call_id: call.call_id.clone(),
            sandbox_policy: None,
            offset: int("offset"),
        }),
        "glob" => Message::GrepArgs(pb::GrepArgs {
            pattern: String::new(),
            path: optional_string("target_directory"),
            glob: optional_string("glob_pattern"),
            output_mode: Some("files_with_matches".into()),
            tool_call_id: call.call_id.clone(),
            ..Default::default()
        }),
        "readlints" => Message::DiagnosticsArgs(pb::DiagnosticsArgs {
            path: call
                .arguments
                .get("paths")
                .and_then(Value::as_array)
                .and_then(|paths| paths.first())
                .and_then(Value::as_str)
                .unwrap_or_default()
                .into(),
            tool_call_id: call.call_id.clone(),
        }),
        "task" => Message::SubagentArgs(pb::SubagentArgs {
            tool_call_id: call.call_id.clone(),
            subagent_type: optional_string("subagent_type").unwrap_or_default(),
            model_id: string("model")?,
            prompt: string("prompt")?,
            readonly: false,
            resume_agent_id: optional_string("resume"),
            run_in_background: call
                .arguments
                .get("run_in_background")
                .and_then(Value::as_bool),
            continuation_config: None,
            parent_conversation_id: Some(context.conversation_id.clone()),
            interrupt: call.arguments.get("interrupt").and_then(Value::as_bool),
            mode: 0,
            fork_agent_id: None,
            root_parent_conversation_id: Some(context.root_conversation_id.clone()),
            selected_context: task_attachments(call),
            direct_meta_parent_child_subagent: None,
            environment: match optional_string("environment").as_deref() {
                Some("cloud") => pb::SubagentExecutionEnvironment::Cloud as i32,
                Some("local") | None => pb::SubagentExecutionEnvironment::Local as i32,
                Some(value) => {
                    return Err(Error::Protocol(format!(
                        "unknown Task environment: {value}"
                    )))
                }
            },
            cloud_base_branch: optional_string("cloud_base_branch"),
            credentials: None,
        }),
        "fetchmcpresource" => Message::ReadMcpResourceExecArgs(pb::ReadMcpResourceExecArgs {
            server: string("server")?,
            uri: string("uri")?,
            download_path: optional_string("downloadPath"),
            tool_call_id: call.call_id.clone(),
            smart_mode_approval: smart_mode_approval(
                call,
                "requestSmartModeApproval",
                "smartModeBlockReason",
            )?,
        }),
        other => {
            return Err(Error::Protocol(format!(
                "tool {other} is not executed through ExecServerMessage"
            )))
        }
    };
    let accept_hook_additional_contexts =
        if matches!(&message, pb::exec_server_message::Message::SubagentArgs(_)) {
            Some(false)
        } else {
            Some(true)
        };
    Ok(server_message(
        id,
        call,
        message,
        accept_hook_additional_contexts,
    ))
}

pub(crate) fn edit_read_request(id: u32, call: &ToolCall) -> Result<pb::AgentServerMessage> {
    Ok(server_message(
        id,
        call,
        pb::exec_server_message::Message::ReadArgs(pb::ReadArgs {
            path: edit::path(call)?,
            tool_call_id: call.call_id.clone(),
            ..Default::default()
        }),
        Some(true),
    ))
}

pub(super) fn edit_write_request(
    id: u32,
    call: &ToolCall,
    write: &EditWrite,
) -> Result<pb::AgentServerMessage> {
    Ok(server_message(
        id,
        call,
        pb::exec_server_message::Message::WriteArgs(pb::WriteArgs {
            path: edit::path(call)?,
            file_text: write.after.clone(),
            tool_call_id: call.call_id.clone(),
            return_file_content_after_write: false,
            file_bytes: Vec::new(),
            encoding_hint: None,
        }),
        Some(true),
    ))
}

fn server_message(
    id: u32,
    call: &ToolCall,
    message: pb::exec_server_message::Message,
    accept_hook_additional_contexts: Option<bool>,
) -> pb::AgentServerMessage {
    pb::AgentServerMessage {
        ttft_breakdown: None,
        message: Some(pb::agent_server_message::Message::ExecServerMessage(
            pb::ExecServerMessage {
                id,
                exec_id: call.call_id.clone(),
                span_context: None,
                accept_hook_additional_contexts,
                message: Some(message),
            },
        )),
    }
}

pub fn mcp_request(
    id: u32,
    call: &ToolCall,
    definition: &pb::McpToolDefinition,
) -> Result<pb::AgentServerMessage> {
    let args = call
        .arguments
        .as_object()
        .map(json_object_to_prost)
        .unwrap_or_default();
    Ok(pb::AgentServerMessage {
        ttft_breakdown: None,
        message: Some(pb::agent_server_message::Message::ExecServerMessage(
            pb::ExecServerMessage {
                id,
                exec_id: call.call_id.clone(),
                span_context: None,
                accept_hook_additional_contexts: None,
                message: Some(pb::exec_server_message::Message::McpArgs(pb::McpArgs {
                    name: definition.name.clone(),
                    args,
                    tool_call_id: call.call_id.clone(),
                    provider_identifier: definition.provider_identifier.clone(),
                    tool_name: definition.tool_name.clone(),
                    smart_mode_approval: None,
                    smart_mode_approval_only: false,
                    skip_approval: false,
                    server_identifier: String::new(),
                })),
            },
        )),
    })
}

pub(crate) fn mcp_meta_request(
    id: u32,
    call: &ToolCall,
    server_identifier: &str,
    route: &McpRoute,
) -> Result<pb::AgentServerMessage> {
    if route.name.is_empty() || route.provider_identifier.is_empty() || route.tool_name.is_empty() {
        return Err(Error::Protocol(format!(
            "MCP definition for {server_identifier} is incomplete"
        )));
    }
    let requested_tool = call
        .arguments
        .get("toolName")
        .and_then(Value::as_str)
        .ok_or_else(|| Error::Protocol("CallMcpTool is missing toolName".into()))?;
    if requested_tool != route.tool_name {
        return Err(Error::Protocol(format!(
            "MCP definition mismatch: requested {requested_tool}, resolved {}",
            route.tool_name
        )));
    }
    let args = call
        .arguments
        .get("arguments")
        .and_then(Value::as_object)
        .map(json_object_to_prost)
        .unwrap_or_default();
    Ok(server_message(
        id,
        call,
        pb::exec_server_message::Message::McpArgs(pb::McpArgs {
            name: route.name.clone(),
            args,
            tool_call_id: call.call_id.clone(),
            provider_identifier: route.provider_identifier.clone(),
            tool_name: route.tool_name.clone(),
            smart_mode_approval: smart_mode_approval(
                call,
                "requestSmartModeApproval",
                "smartModeBlockReason",
            )?,
            smart_mode_approval_only: false,
            skip_approval: false,
            server_identifier: server_identifier.into(),
        }),
        Some(true),
    ))
}

pub fn mcp_state_request(id: u32, call: &ToolCall) -> pb::AgentServerMessage {
    let server_identifiers = call
        .arguments
        .get("server")
        .and_then(Value::as_str)
        .map(|server| vec![server.into()])
        .unwrap_or_default();
    server_message(
        id,
        call,
        pb::exec_server_message::Message::McpStateExecArgs(pb::McpStateExecArgs {
            server_identifiers,
            kick_only: false,
        }),
        Some(false),
    )
}

pub fn abort(id: u32) -> pb::AgentServerMessage {
    pb::AgentServerMessage {
        ttft_breakdown: None,
        message: Some(pb::agent_server_message::Message::ExecServerControlMessage(
            pb::ExecServerControlMessage {
                message: Some(pb::exec_server_control_message::Message::Abort(
                    pb::ExecServerAbort { id },
                )),
            },
        )),
    }
}

fn shell_sandbox_policy(call: &ToolCall) -> Option<pb::SandboxPolicy> {
    let permissions = call.arguments.get("required_permissions")?.as_array()?;
    let perms: Vec<&str> = permissions.iter().filter_map(Value::as_str).collect();
    if perms.contains(&"all") {
        Some(pb::SandboxPolicy {
            r#type: pb::sandbox_policy::Type::InsecureNone as i32,
            network_access: Some(true),
            ..Default::default()
        })
    } else if perms.contains(&"full_network") {
        Some(pb::SandboxPolicy {
            r#type: pb::sandbox_policy::Type::WorkspaceReadwrite as i32,
            network_access: Some(true),
            ..Default::default()
        })
    } else {
        None
    }
}

fn shell_command_metadata(command: &str) -> (Vec<String>, Option<pb::ShellCommandParsingResult>) {
    let command = command.trim();
    let mut parts = command.split_whitespace();
    let Some(name) = parts.next() else {
        return (Vec::new(), None);
    };
    let args = parts
        .map(
            |value| pb::shell_command_parsing_result::ExecutableCommandArg {
                r#type: "word".into(),
                value: value.into(),
            },
        )
        .collect();
    (
        vec![command.into()],
        Some(pb::ShellCommandParsingResult {
            executable_commands: vec![pb::shell_command_parsing_result::ExecutableCommand {
                name: name.into(),
                args,
                full_text: command.into(),
            }],
            ..Default::default()
        }),
    )
}

fn shell_timeout(call: &ToolCall) -> Result<i32> {
    let value = call
        .arguments
        .get("block_until_ms")
        .map(|value| {
            value
                .as_i64()
                .ok_or_else(|| Error::Protocol("Shell block_until_ms must be an integer".into()))
        })
        .transpose()?
        .unwrap_or(30_000);
    i32::try_from(value)
        .ok()
        .filter(|value| *value >= 0)
        .ok_or_else(|| Error::Protocol("Shell block_until_ms is out of range".into()))
}

fn smart_mode_approval(
    call: &ToolCall,
    request_field: &str,
    reason_field: &str,
) -> Result<Option<pb::SmartModeApproval>> {
    if !call
        .arguments
        .get(request_field)
        .and_then(Value::as_bool)
        .unwrap_or(false)
    {
        return Ok(None);
    }
    let reason = call
        .arguments
        .get(reason_field)
        .and_then(Value::as_str)
        .ok_or_else(|| Error::Protocol(format!("{} requires {reason_field}", call.name)))?;
    Ok(Some(pb::SmartModeApproval {
        request_id: call.call_id.clone(),
        reason: reason.to_string(),
    }))
}

fn shell_notification(call: &ToolCall) -> Result<Option<pb::ShellOutputNotificationConfig>> {
    let Some(value) = call.arguments.get("notify_on_output") else {
        return Ok(None);
    };
    let object = value
        .as_object()
        .ok_or_else(|| Error::Protocol("Shell notify_on_output must be an object".into()))?;
    let required = |field: &str| {
        object
            .get(field)
            .and_then(Value::as_str)
            .map(str::to_string)
            .ok_or_else(|| Error::Protocol(format!("Shell notify_on_output is missing {field}")))
    };
    Ok(Some(pb::ShellOutputNotificationConfig {
        pattern: required("pattern")?,
        reason: required("reason")?,
        debounce: object.get("debounce_ms").and_then(Value::as_f64),
        notification_limit: None,
    }))
}

fn task_attachments(call: &ToolCall) -> Option<pb::SelectedContext> {
    let paths = call.arguments.get("file_attachments")?.as_array()?;
    let mut context = pb::SelectedContext::default();
    for path in paths.iter().filter_map(Value::as_str) {
        let extension = std::path::Path::new(path)
            .extension()
            .and_then(std::ffi::OsStr::to_str)
            .unwrap_or_default()
            .to_ascii_lowercase();
        if matches!(extension.as_str(), "mp4" | "mov" | "webm" | "mkv") {
            context.selected_videos.push(pb::SelectedVideo {
                path: path.into(),
                filename: std::path::Path::new(path)
                    .file_name()
                    .and_then(std::ffi::OsStr::to_str)
                    .unwrap_or_default()
                    .into(),
                materialize_to_filesystem: true,
                ..Default::default()
            });
        } else {
            context.selected_images.push(pb::SelectedImage {
                path: path.into(),
                ..Default::default()
            });
        }
    }
    Some(context)
}

fn normalize(value: &str) -> String {
    value
        .chars()
        .filter(|c| c.is_ascii_alphanumeric())
        .flat_map(char::to_lowercase)
        .collect()
}

pub(crate) fn json_object_to_prost(
    value: &Map<String, Value>,
) -> std::collections::HashMap<String, prost_types::Value> {
    value
        .iter()
        .map(|(key, value)| (key.clone(), prost_value(value)))
        .collect()
}

fn prost_value(value: &Value) -> prost_types::Value {
    use prost_types::{value::Kind, ListValue, Struct, Value as ProstValue};
    let kind = match value {
        Value::Null => Kind::NullValue(0),
        Value::Bool(v) => Kind::BoolValue(*v),
        Value::Number(v) => Kind::NumberValue(v.as_f64().unwrap_or_default()),
        Value::String(v) => Kind::StringValue(v.clone()),
        Value::Array(v) => Kind::ListValue(ListValue {
            values: v.iter().map(prost_value).collect(),
        }),
        Value::Object(v) => Kind::StructValue(Struct {
            fields: json_object_to_prost(v).into_iter().collect(),
        }),
    };
    ProstValue { kind: Some(kind) }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::request;
    use crate::cursor::protocol::proto::agent::v1 as pb;
    use crate::cursor::tools::runtime::ExecContext;
    use crate::model::ToolCall;

    #[test]
    fn bash_is_encoded_as_a_shell_exec_request() {
        // The dispatcher routes `bash`/`Bash` to the shell executor, so the
        // request codec must encode it as a Shell stream instead of erroring
        // with `tool bash is not executed through ExecServerMessage`.
        let call = ToolCall {
            index: 0,
            call_id: "call-1".into(),
            model_call_id: "model-1".into(),
            name: "Bash".into(),
            arguments_text: String::new(),
            arguments: json!({ "command": "ls -la" }),
            argument_error: None,
        };
        let message = request(1, &call, &ExecContext::default()).unwrap();
        let Some(pb::agent_server_message::Message::ExecServerMessage(exec)) = message.message
        else {
            panic!("expected an ExecServerMessage");
        };
        let Some(pb::exec_server_message::Message::ShellStreamArgs(args)) = exec.message else {
            panic!("expected ShellStreamArgs");
        };
        assert_eq!(args.command, "ls -la");
    }
}
