//! Renders Tool calls and results as Cursor Tool cards.
use serde_json::Value;

use crate::{
    cursor::{
        protocol::proto::agent::v1 as pb,
        tools::{
            codec, edit,
            tool_call_result::{self as tool_result, ToolCompletion},
        },
    },
    model::ToolCall,
    Error, Result,
};

use super::server_interaction;

pub(crate) fn edit_path_partial(call: &ToolCall, path: &str) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::PartialToolCall(
        pb::PartialToolCallUpdate {
            call_id: call.call_id.clone(),
            tool_call: Some(pb::ToolCall {
                hook_additional_contexts: Vec::new(),
                tool_call_id: Some(call.call_id.clone()),
                started_at_ms: None,
                completed_at_ms: None,
                tool: Some(pb::tool_call::Tool::EditToolCall(pb::EditToolCall {
                    args: Some(pb::EditArgs {
                        path: path.into(),
                        stream_content: None,
                    }),
                    result: None,
                })),
            }),
            args_text_delta: String::new(),
            model_call_id: call.model_call_id.clone(),
        },
    ))
}

pub(crate) fn edit_content_delta(call: &ToolCall, content: String) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::ToolCallDelta(Box::new(
        pb::ToolCallDeltaUpdate {
            call_id: call.call_id.clone(),
            tool_call_delta: Some(Box::new(pb::ToolCallDelta {
                delta: Some(pb::tool_call_delta::Delta::EditToolCallDelta(
                    pb::EditToolCallDelta {
                        stream_content_delta: content,
                    },
                )),
            })),
            model_call_id: call.model_call_id.clone(),
        },
    )))
}

pub(crate) fn task_partial(
    call: &ToolCall,
    description: &str,
    prompt: &str,
    subagent: &str,
    model: &str,
    resume: &str,
    environment: &str,
) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::PartialToolCall(
        pb::PartialToolCallUpdate {
            call_id: call.call_id.clone(),
            tool_call: Some(pb::ToolCall {
                hook_additional_contexts: Vec::new(),
                tool_call_id: Some(call.call_id.clone()),
                started_at_ms: None,
                completed_at_ms: None,
                tool: Some(pb::tool_call::Tool::TaskToolCall(pb::TaskToolCall {
                    args: Some(pb::TaskArgs {
                        description: description.into(),
                        prompt: prompt.into(),
                        subagent_type: Some(subagent_type(subagent)),
                        model: (!model.is_empty()).then(|| model.into()),
                        resume: (!resume.is_empty()).then(|| resume.into()),
                        agent_id: None,
                        attachments: Vec::new(),
                        mode: 0,
                        responding_to_message_ids: Vec::new(),
                        environment: execution_environment(
                            (!environment.is_empty()).then_some(environment),
                        ),
                        machine: None,
                    }),
                    result: None,
                    ..Default::default()
                })),
            }),
            args_text_delta: String::new(),
            model_call_id: call.model_call_id.clone(),
        },
    ))
}

pub(crate) fn create_plan_partial(
    call: &ToolCall,
    name: &str,
    plan: &str,
    overview: &str,
) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::PartialToolCall(
        pb::PartialToolCallUpdate {
            call_id: call.call_id.clone(),
            tool_call: Some(pb::ToolCall {
                hook_additional_contexts: Vec::new(),
                tool_call_id: Some(call.call_id.clone()),
                started_at_ms: None,
                completed_at_ms: None,
                tool: Some(pb::tool_call::Tool::CreatePlanToolCall(
                    pb::CreatePlanToolCall {
                        args: Some(pb::CreatePlanArgs {
                            plan: plan.into(),
                            todos: Vec::new(),
                            overview: overview.into(),
                            name: name.into(),
                            is_project: false,
                            phases: Vec::new(),
                        }),
                        result: None,
                    },
                )),
            }),
            args_text_delta: String::new(),
            model_call_id: call.model_call_id.clone(),
        },
    ))
}

pub fn tool_started(
    call: &ToolCall,
    dynamic_mcp: Option<&pb::McpToolDefinition>,
) -> Result<pb::AgentServerMessage> {
    let tool_call = match dynamic_mcp {
        Some(definition) => render_dynamic_mcp(call, definition, false),
        None => render_tool_call(call, false)?,
    };
    Ok(server_interaction(
        pb::interaction_update::Message::ToolCallStarted(pb::ToolCallStartedUpdate {
            call_id: call.call_id.clone(),
            tool_call: Some(tool_call),
            model_call_id: call.model_call_id.clone(),
        }),
    ))
}

pub fn dynamic_mcp_placeholder(definition: &pb::McpToolDefinition, call_id: &str) -> pb::ToolCall {
    dynamic_mcp_tool_call(call_id, None, definition, false, false)
}

pub fn render_dynamic_mcp(
    call: &ToolCall,
    definition: &pb::McpToolDefinition,
    completed: bool,
) -> pb::ToolCall {
    dynamic_mcp_tool_call(
        &call.call_id,
        Some(&call.arguments),
        definition,
        true,
        completed,
    )
}

fn dynamic_mcp_tool_call(
    call_id: &str,
    arguments: Option<&Value>,
    definition: &pb::McpToolDefinition,
    started: bool,
    completed: bool,
) -> pb::ToolCall {
    let timestamp = now_ms();
    pb::ToolCall {
        hook_additional_contexts: Vec::new(),
        tool_call_id: Some(call_id.into()),
        started_at_ms: started.then_some(timestamp),
        completed_at_ms: completed.then_some(timestamp),
        tool: Some(pb::tool_call::Tool::McpToolCall(pb::McpToolCall {
            args: Some(pb::McpArgs {
                name: definition.name.clone(),
                args: arguments
                    .and_then(Value::as_object)
                    .map(codec::json_object_to_prost)
                    .unwrap_or_default(),
                tool_call_id: call_id.into(),
                provider_identifier: definition.provider_identifier.clone(),
                tool_name: definition.tool_name.clone(),
                ..Default::default()
            }),
            result: None,
            description: Some(definition.description.clone()),
        })),
    }
}

pub fn tool_completed(call: &ToolCall, completion: &ToolCompletion) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::ToolCallCompleted(
        pb::ToolCallCompletedUpdate {
            call_id: call.call_id.clone(),
            tool_call: Some(completion.tool_call().clone()),
            model_call_id: call.model_call_id.clone(),
        },
    ))
}

pub fn tool_placeholder(name: &str, call_id: &str) -> Result<pb::ToolCall> {
    use pb::tool_call::Tool;
    let tool = match normalized(name).as_str() {
        "shell" | "bash" => Tool::ShellToolCall(pb::ShellToolCall::default()),
        "delete" => Tool::DeleteToolCall(pb::DeleteToolCall::default()),
        "glob" => Tool::GlobToolCall(pb::GlobToolCall::default()),
        "grep" => Tool::GrepToolCall(pb::GrepToolCall::default()),
        "read" => Tool::ReadToolCall(pb::ReadToolCall::default()),
        "todowrite" => Tool::UpdateTodosToolCall(pb::UpdateTodosToolCall::default()),
        "strreplace" | "editnotebook" | "write" => Tool::EditToolCall(pb::EditToolCall::default()),
        "readlints" => Tool::ReadLintsToolCall(pb::ReadLintsToolCall::default()),
        "callmcptool" | "semblesearch" | "semblefindrelated" => {
            Tool::McpToolCall(pb::McpToolCall::default())
        }
        "createplan" => Tool::CreatePlanToolCall(pb::CreatePlanToolCall::default()),
        "websearch" => Tool::WebSearchToolCall(pb::WebSearchToolCall::default()),
        "task" => Tool::TaskToolCall(pb::TaskToolCall::default()),
        "fetchmcpresource" => Tool::ReadMcpResourceToolCall(pb::ReadMcpResourceToolCall::default()),
        "askquestion" => Tool::AskQuestionToolCall(pb::AskQuestionToolCall::default()),
        "webfetch" => Tool::WebFetchToolCall(pb::WebFetchToolCall::default()),
        "switchmode" => Tool::SwitchModeToolCall(pb::SwitchModeToolCall::default()),
        "generateimage" => Tool::GenerateImageToolCall(pb::GenerateImageToolCall::default()),
        "updatecurrentstep" => {
            Tool::CommunicateUpdateToolCall(pb::CommunicateUpdateToolCall::default())
        }
        "getmcptools" => Tool::GetMcpToolsToolCall(pb::GetMcpToolsToolCall::default()),
        _ => return Err(Error::Protocol(format!("unsupported tool: {name}"))),
    };
    Ok(pb::ToolCall {
        hook_additional_contexts: Vec::new(),
        tool_call_id: Some(call_id.into()),
        started_at_ms: None,
        completed_at_ms: None,
        tool: Some(tool),
    })
}

pub fn render_tool_call(call: &ToolCall, completed: bool) -> Result<pb::ToolCall> {
    if is_mcp_auth(call) {
        let server_identifier = call
            .arguments
            .get("server")
            .and_then(Value::as_str)
            .filter(|server| !server.is_empty())
            .ok_or_else(|| Error::Protocol("CallMcpTool mcp_auth is missing server".into()))?;
        let timestamp = now_ms();
        return Ok(pb::ToolCall {
            hook_additional_contexts: Vec::new(),
            tool_call_id: Some(call.call_id.clone()),
            started_at_ms: Some(timestamp),
            completed_at_ms: completed.then_some(timestamp),
            tool: Some(pb::tool_call::Tool::McpAuthToolCall(pb::McpAuthToolCall {
                args: Some(pb::McpAuthArgs {
                    server_identifier: server_identifier.into(),
                    tool_call_id: call.call_id.clone(),
                }),
                result: None,
            })),
        });
    }
    let mut output = tool_placeholder(&call.name, &call.call_id)?;
    let timestamp = now_ms();
    output.started_at_ms = Some(timestamp);
    if completed {
        output.completed_at_ms = Some(timestamp);
    }
    let string = |name: &str| {
        call.arguments
            .get(name)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    let optional = |name: &str| {
        call.arguments
            .get(name)
            .and_then(Value::as_str)
            .map(str::to_string)
    };
    // 与执行侧一致的参数别名兼容(如 Claude Code 习惯的 file_path),仅影响展示。
    let aliased = |names: &[&str]| -> String {
        names
            .iter()
            .find_map(|name| call.arguments.get(name).and_then(Value::as_str))
            .unwrap_or_default()
            .to_string()
    };
    let optional_aliased = |names: &[&str]| -> Option<String> {
        names
            .iter()
            .find_map(|name| call.arguments.get(name).and_then(Value::as_str))
            .map(str::to_string)
    };
    match output.tool.as_mut() {
        Some(pb::tool_call::Tool::ShellToolCall(tool)) => {
            tool.description = optional("description");
            tool.args = Some(pb::ShellArgs {
                command: string("command"),
                working_directory: optional("working_directory").unwrap_or_default(),
                description: optional("description"),
                tool_call_id: call.call_id.clone(),
                ..Default::default()
            })
        }
        Some(pb::tool_call::Tool::DeleteToolCall(tool)) => {
            tool.args = Some(pb::DeleteArgs {
                path: aliased(&["path", "file_path", "filePath"]),
                tool_call_id: call.call_id.clone(),
            })
        }
        Some(pb::tool_call::Tool::GlobToolCall(tool)) => {
            tool.args = Some(pb::GlobToolArgs {
                target_directory: optional("target_directory"),
                glob_pattern: string("glob_pattern"),
            })
        }
        Some(pb::tool_call::Tool::GrepToolCall(tool)) => {
            tool.args = Some(pb::GrepArgs {
                pattern: string("pattern"),
                path: optional("path"),
                glob: optional("glob"),
                output_mode: optional("output_mode"),
                tool_call_id: call.call_id.clone(),
                ..Default::default()
            })
        }
        Some(pb::tool_call::Tool::ReadToolCall(tool)) => {
            tool.args = Some(pb::ReadToolArgs {
                path: aliased(&["path", "file_path", "filePath"]),
                offset: call
                    .arguments
                    .get("offset")
                    .and_then(Value::as_i64)
                    .map(|value| value as i32),
                limit: call
                    .arguments
                    .get("limit")
                    .and_then(Value::as_i64)
                    .map(|value| value as i32),
                include_line_numbers: call
                    .arguments
                    .get("include_line_numbers")
                    .and_then(Value::as_bool),
            })
        }
        Some(pb::tool_call::Tool::UpdateTodosToolCall(tool)) => {
            tool.args = Some(pb::UpdateTodosArgs {
                todos: tool_result::todo_items(&call.arguments),
                merge: call
                    .arguments
                    .get("merge")
                    .and_then(Value::as_bool)
                    .unwrap_or(false),
            })
        }
        Some(pb::tool_call::Tool::EditToolCall(tool)) => {
            let stream_content = if normalized(&call.name) == "write" {
                optional_aliased(&["contents", "content"]).unwrap_or_default()
            } else {
                optional("new_string").unwrap_or_default()
            };
            tool.args = Some(pb::EditArgs {
                path: if normalized(&call.name) == "editnotebook" {
                    string("target_notebook")
                } else {
                    aliased(&["path", "file_path", "filePath"])
                },
                stream_content: Some(edit::normalize_newlines(&stream_content)),
            })
        }
        Some(pb::tool_call::Tool::ReadLintsToolCall(tool)) => {
            tool.args = Some(pb::ReadLintsToolArgs {
                paths: call
                    .arguments
                    .get("paths")
                    .and_then(Value::as_array)
                    .into_iter()
                    .flatten()
                    .filter_map(Value::as_str)
                    .map(str::to_string)
                    .collect(),
            })
        }
        Some(pb::tool_call::Tool::McpToolCall(tool)) => {
            tool.description = optional("description");
            if let Some(tool_name) = semble_tool_name(&call.name) {
                let mut arguments = call.arguments.as_object().cloned().unwrap_or_default();
                arguments.remove("description");
                tool.args = Some(pb::McpArgs {
                    name: tool_name.into(),
                    args: codec::json_object_to_prost(&arguments),
                    tool_call_id: call.call_id.clone(),
                    provider_identifier: "builtin-semble".into(),
                    tool_name: tool_name.into(),
                    server_identifier: "builtin-semble".into(),
                    ..Default::default()
                });
            } else {
                tool.args = Some(pb::McpArgs {
                    name: optional("toolName").unwrap_or_default(),
                    args: call
                        .arguments
                        .get("arguments")
                        .and_then(Value::as_object)
                        .map(codec::json_object_to_prost)
                        .unwrap_or_default(),
                    tool_call_id: call.call_id.clone(),
                    tool_name: optional("toolName").unwrap_or_default(),
                    server_identifier: string("server"),
                    ..Default::default()
                });
            }
        }
        Some(pb::tool_call::Tool::CreatePlanToolCall(tool)) => {
            tool.args = Some(pb::CreatePlanArgs {
                plan: string("plan"),
                todos: tool_result::todo_items(&call.arguments),
                overview: string("overview"),
                name: string("name"),
                is_project: false,
                phases: Vec::new(),
            })
        }
        Some(pb::tool_call::Tool::WebSearchToolCall(tool)) => {
            tool.args = Some(pb::WebSearchArgs {
                search_term: aliased(&["search_term", "query"]),
                tool_call_id: call.call_id.clone(),
            })
        }
        Some(pb::tool_call::Tool::TaskToolCall(tool)) => {
            tool.args = Some(pb::TaskArgs {
                description: string("description"),
                prompt: string("prompt"),
                subagent_type: Some(subagent_type(&string("subagent_type"))),
                model: optional("model"),
                resume: optional("resume"),
                agent_id: None,
                attachments: call
                    .arguments
                    .get("file_attachments")
                    .and_then(Value::as_array)
                    .into_iter()
                    .flatten()
                    .filter_map(Value::as_str)
                    .map(str::to_string)
                    .collect(),
                mode: 0,
                responding_to_message_ids: Vec::new(),
                environment: execution_environment(optional("environment").as_deref()),
                machine: None,
            })
        }
        Some(pb::tool_call::Tool::ReadMcpResourceToolCall(tool)) => {
            tool.args = Some(pb::ReadMcpResourceExecArgs {
                server: string("server"),
                uri: string("uri"),
                download_path: optional("downloadPath"),
                tool_call_id: call.call_id.clone(),
                smart_mode_approval: None,
            })
        }
        Some(pb::tool_call::Tool::WebFetchToolCall(tool)) => {
            tool.args = Some(pb::WebFetchArgs {
                url: string("url"),
                tool_call_id: call.call_id.clone(),
            })
        }
        Some(pb::tool_call::Tool::SwitchModeToolCall(tool)) => {
            tool.args = Some(pb::SwitchModeArgs {
                target_mode_id: string("target_mode_id"),
                explanation: optional("explanation"),
                tool_call_id: call.call_id.clone(),
            })
        }
        Some(pb::tool_call::Tool::GenerateImageToolCall(tool)) => {
            tool.args = Some(pb::GenerateImageArgs {
                description: string("description"),
                file_path: optional("filename"),
                reference_image_paths: call
                    .arguments
                    .get("reference_image_paths")
                    .and_then(Value::as_array)
                    .into_iter()
                    .flatten()
                    .filter_map(Value::as_str)
                    .map(str::to_string)
                    .collect(),
                aspect_ratio: optional("aspect_ratio"),
            })
        }
        Some(pb::tool_call::Tool::CommunicateUpdateToolCall(tool)) => {
            tool.args = Some(pb::CommunicateUpdateArgs {
                current_step: optional("current_step"),
                final_summary: optional("final_summary"),
                completed_subtitle: optional("completed_subtitle"),
            })
        }
        Some(pb::tool_call::Tool::WriteShellStdinToolCall(tool)) => {
            tool.args = Some(pb::WriteShellStdinArgs {
                shell_id: call
                    .arguments
                    .get("shell_id")
                    .and_then(Value::as_u64)
                    .unwrap_or_default() as u32,
                chars: string("chars"),
            })
        }
        Some(pb::tool_call::Tool::GetMcpToolsToolCall(tool)) => {
            tool.args = Some(pb::GetMcpToolsArgs {
                server: optional("server"),
                tool_name: optional("toolName"),
                pattern: optional("pattern"),
                tool_call_id: call.call_id.clone(),
            })
        }
        _ => {}
    }
    Ok(output)
}

fn is_mcp_auth(call: &ToolCall) -> bool {
    normalized(&call.name) == "callmcptool"
        && call
            .arguments
            .get("toolName")
            .and_then(Value::as_str)
            .is_some_and(|tool| normalized(tool) == "mcpauth")
}

fn subagent_type(name: &str) -> pb::SubagentType {
    use pb::subagent_type::Type;
    let r#type = match name.to_ascii_lowercase().as_str() {
        "" | "generalpurpose" => Type::Unspecified(pb::SubagentTypeUnspecified {}),
        "explore" => Type::Explore(pb::SubagentTypeExplore {}),
        "browser-use" | "browseruse" => Type::BrowserUse(pb::SubagentTypeBrowserUse {}),
        "shell" => Type::Shell(pb::SubagentTypeShell {}),
        "bash" => Type::Bash(pb::SubagentTypeBash {}),
        "debug" => Type::Debug(pb::SubagentTypeDebug {}),
        "cursor-guide" | "cursorguide" => Type::CursorGuide(pb::SubagentTypeCursorGuide {}),
        "computer-use" | "computeruse" => Type::ComputerUse(pb::SubagentTypeComputerUse {}),
        _ => Type::Custom(pb::SubagentTypeCustom { name: name.into() }),
    };
    pb::SubagentType {
        r#type: Some(r#type),
    }
}

fn execution_environment(value: Option<&str>) -> i32 {
    match value {
        Some("cloud") => pb::SubagentExecutionEnvironment::Cloud as i32,
        Some("local") | None => pb::SubagentExecutionEnvironment::Local as i32,
        Some(_) => pb::SubagentExecutionEnvironment::Unspecified as i32,
    }
}

fn normalized(value: &str) -> String {
    value
        .chars()
        .filter(|character| character.is_ascii_alphanumeric())
        .flat_map(char::to_lowercase)
        .collect()
}

fn semble_tool_name(name: &str) -> Option<&'static str> {
    match normalized(name).as_str() {
        "semblesearch" => Some("search"),
        "semblefindrelated" => Some("find_related"),
        _ => None,
    }
}

fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as u64
}

#[cfg(test)]
mod tests {
    use super::tool_placeholder;
    use crate::cursor::protocol::proto::agent::v1 as pb;

    #[test]
    fn bash_renders_as_a_shell_placeholder() {
        // The dispatcher treats `bash`/`Bash` as a Shell alias, so the streaming
        // placeholder must too; otherwise a `Bash` tool call aborts the turn with
        // `unsupported tool: bash` before it ever runs.
        for name in ["shell", "Shell", "bash", "Bash"] {
            let tool = tool_placeholder(name, "call-1").unwrap().tool;
            assert!(
                matches!(tool, Some(pb::tool_call::Tool::ShellToolCall(_))),
                "{name} should render as a Shell tool"
            );
        }
    }
}
