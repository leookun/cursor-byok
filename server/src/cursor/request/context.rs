use std::{
    collections::{BTreeMap, HashMap, HashSet},
    path::Path,
};

use prost::Message;
use serde_json::Value;

use crate::{
    cursor::{
        context_sync::RequestContextSynchronizer, proto::agent::v1 as pb, tools::runtime::McpRoute,
    },
    model::ToolDefinition,
    store::BlobId,
    Error, Result,
};

pub async fn hydrate(
    request: &pb::AgentRunRequest,
    context_sync: &RequestContextSynchronizer,
) -> Result<pb::RequestContext> {
    let mut context = request_context(request).cloned().unwrap_or_default();
    let Some(parts) = request
        .action
        .as_ref()
        .and_then(|action| action.request_context_parts.as_ref())
    else {
        if is_background_completion(request) {
            return context_sync
                .load(request.conversation_id.as_deref().unwrap_or_default())
                .await;
        }
        return Ok(context);
    };

    if let Some(current) = context_sync
        .refresh_if_missing(
            parts,
            request.conversation_id.as_deref().unwrap_or_default(),
        )
        .await?
    {
        context.rules = current.rules;
        context.non_file_rules = current.non_file_rules;
        context.cloud_rule = current.cloud_rule;
        context.agent_skills = current.agent_skills;
        context.skill_options = current.skill_options;
        context.custom_subagents = current.custom_subagents;
        context.tools = current.tools;
        context.mcp_instructions = current.mcp_instructions;
        context.mcp_file_system_options = current.mcp_file_system_options;
        context.mcp_meta_tool_options = current.mcp_meta_tool_options;
        return Ok(context);
    }

    if let Some(part) = decode_part::<pb::RequestContextRulesPart>(
        "rules",
        &parts.rules_blob_id,
        parts.rules_byte_length,
        context_sync,
    )
    .await?
    {
        context.rules = part.rules;
        context.non_file_rules = part.non_file_rules;
        context.cloud_rule = part.cloud_rule;
    }
    if let Some(part) = decode_part::<pb::RequestContextSkillsPart>(
        "skills",
        &parts.skills_blob_id,
        parts.skills_byte_length,
        context_sync,
    )
    .await?
    {
        context.agent_skills = part.agent_skills;
        context.skill_options = part.skill_options;
    }
    if let Some(part) = decode_part::<pb::RequestContextSubagentsPart>(
        "subagents",
        &parts.subagents_blob_id,
        parts.subagents_byte_length,
        context_sync,
    )
    .await?
    {
        context.custom_subagents = part.custom_subagents;
    }
    if let Some(part) = decode_part::<pb::RequestContextMcpsPart>(
        "MCP",
        &parts.mcps_blob_id,
        parts.mcps_byte_length,
        context_sync,
    )
    .await?
    {
        context.tools = part.tools;
        context.mcp_instructions = part.mcp_instructions;
        context.mcp_file_system_options = part.mcp_file_system_options;
        context.mcp_meta_tool_options = part.mcp_meta_tool_options;
    }
    Ok(context)
}

fn is_background_completion(request: &pb::AgentRunRequest) -> bool {
    matches!(
        request
            .action
            .as_ref()
            .and_then(|action| action.action.as_ref()),
        Some(pb::conversation_action::Action::BackgroundTaskCompletionAction(_))
    )
}

async fn decode_part<T: Message + Default>(
    name: &str,
    raw_id: &[u8],
    expected_length: u32,
    context_sync: &RequestContextSynchronizer,
) -> Result<Option<T>> {
    if raw_id.is_empty() {
        if expected_length != 0 {
            return Err(Error::Protocol(format!(
                "{name} context has a byte length but no BlobID"
            )));
        }
        return Ok(None);
    }
    let id = BlobId::from_bytes(raw_id)?;
    let data = context_sync.get(&id).await?.ok_or_else(|| {
        Error::Protocol(format!(
            "{name} context Blob is missing: {}",
            id.to_base64()
        ))
    })?;
    if data.len() != expected_length as usize {
        return Err(Error::Protocol(format!(
            "{name} context Blob length mismatch: expected {expected_length}, got {}",
            data.len()
        )));
    }
    T::decode(data.as_slice())
        .map(Some)
        .map_err(|error| Error::Protocol(format!("invalid {name} context Blob: {error}")))
}

pub fn request_context(request: &pb::AgentRunRequest) -> Option<&pb::RequestContext> {
    let action = request.action.as_ref()?;
    action
        .request_context_parts
        .as_ref()
        .and_then(|parts| parts.dynamic_context.as_ref())
        .or_else(|| match action.action.as_ref()? {
            pb::conversation_action::Action::UserMessageAction(action) => {
                action.request_context.as_ref()
            }
            pb::conversation_action::Action::ExecutePlanAction(action) => {
                action.request_context.as_ref()
            }
            pb::conversation_action::Action::ResumeAction(action) => {
                action.request_context.as_ref()
            }
            _ => None,
        })
}

pub fn compile_context(context: &pb::RequestContext, today: &str) -> String {
    let mut sections = Vec::new();
    let mut transcripts = None;
    if let Some(env) = &context.env {
        let workspace = env
            .workspace_paths
            .first()
            .map(String::as_str)
            .unwrap_or("");
        let repo = context.git_repos.iter().find(|repo| repo.path == workspace);
        sections.push(format!(
            "<user_info>\nOS Version: {}\n\nShell: {}\n\nWorkspace Path: {}\n\nIs directory a git repo: {}\n\nTerminals folder: {}\n\nToday's date: {}\n\nNote: Prefer using absolute paths over relative paths as tool call args when possible.\n</user_info>",
            env.os_version,
            env.shell,
            workspace,
            repo.map(|repo| format!("Yes, at {}", repo.path)).unwrap_or_else(|| "No".into()),
            env.terminals_folder,
            today,
        ));
        if !env.agent_transcripts_folder.is_empty() {
            transcripts = Some(format!(
                "<agent_transcripts>\nAgent transcripts (past chats) live in {}. They have names like <uuid>.jsonl, cite parent chat transcripts to the user as [<title for chat <=6 words>\n](<uuid excluding .jsonl>). Don't discuss the folder structure.\n</agent_transcripts>",
                env.agent_transcripts_folder
            ));
        }
    }
    sections.extend(context.git_repos.iter().map(|repo| {
        format!(
            "<git_status>\nThis is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.\n\n\nGit repo: {}\n\n```\n{}\n```\n</git_status>",
            repo.path, repo.status
        )
    }));
    sections.extend(transcripts);
    let skill_contents = context
        .agent_skills
        .iter()
        .map(|skill| skill.content.as_str())
        .filter(|content| !content.is_empty())
        .collect::<HashSet<_>>();
    let mut rules = context
        .rules
        .iter()
        .chain(context.non_file_rules.iter())
        .filter(|rule| {
            !rule.content.trim().is_empty()
                && !is_skill_rule(rule)
                && !skill_contents.contains(rule.content.as_str())
        })
        .map(|rule| format!("<user_rule>\n{}\n</user_rule>", rule.content))
        .collect::<Vec<_>>();
    rules.extend(
        context
            .cloud_rule
            .iter()
            .map(|rule| format!("<user_rule>\n{rule}\n</user_rule>")),
    );
    if !rules.is_empty() {
        sections.push(format!("<rules>\n{}\n</rules>", rules.join("\n")));
    }
    let skills = context
        .agent_skills
        .iter()
        .filter(|skill| !skill.disable_model_invocation)
        .map(|skill| {
            format!(
                "<agent_skill fullPath=\"{}\">{}</agent_skill>",
                xml(&skill.full_path),
                xml(&skill.description),
            )
        })
        .collect::<Vec<_>>();
    if !skills.is_empty() {
        sections.push(format!(
            "<agent_skills>\n<available_skills>\n{}\n</available_skills>\n</agent_skills>",
            skills.join("\n")
        ));
    }
    let subagents = context
        .custom_subagents
        .iter()
        .map(|agent| {
            format!(
                "<subagent name=\"{}\">{}</subagent>",
                xml(&agent.name),
                agent.description
            )
        })
        .collect::<Vec<_>>();
    if !subagents.is_empty() {
        sections.push(format!(
            "<subagents>\n{}\n</subagents>",
            subagents.join("\n")
        ));
    }
    {
        let servers = context
            .mcp_meta_tool_options
            .as_ref()
            .into_iter()
            .flat_map(|options| &options.mcp_descriptors)
            .filter_map(compile_mcp_descriptor)
            .collect::<Vec<_>>();
        if !servers.is_empty() {
            sections.push(format!(
                "<mcp_meta_tools>\nThe following MCP tools are available. Call a listed tool directly with CallMcpTool without calling GetMcpTools first. If a call returns an error, use it to correct the arguments or authentication and retry when appropriate.\n<mcp_meta_tool_servers>\n{}\n</mcp_meta_tool_servers>\n</mcp_meta_tools>",
                servers.join("\n")
            ));
        }
    }
    sections.join("\n\n")
}

fn compile_mcp_descriptor(server: &pb::McpDescriptor) -> Option<String> {
    if server.server_identifier.trim().is_empty() {
        return None;
    }
    let tools = server
        .tools
        .iter()
        .filter(|tool| !tool.tool_name.trim().is_empty())
        .map(|tool| {
            let mut lines = vec![format!("<mcp_tool name=\"{}\">", xml(&tool.tool_name))];
            if let Some(path) = tool
                .definition_path
                .as_deref()
                .filter(|value| !value.trim().is_empty())
            {
                lines.push(format!("<definition_path>{}</definition_path>", xml(path)));
            }
            if let Some(description) = tool
                .description
                .as_deref()
                .filter(|value| !value.trim().is_empty())
            {
                lines.push(format!("<description>{}</description>", xml(description)));
            }
            if let Some(schema) = mcp_input_schema(tool) {
                lines.push(format!("<input_schema>{}</input_schema>", xml(&schema)));
            }
            lines.push("</mcp_tool>".into());
            lines.join("\n")
        })
        .collect::<Vec<_>>();
    if tools.is_empty() {
        return None;
    }
    Some(format!(
        "<mcp_meta_tool_server name=\"{}\" identifier=\"{}\">\n<tools>\n{}\n</tools>\n</mcp_meta_tool_server>",
        xml(if server.server_name.trim().is_empty() {
            &server.server_identifier
        } else {
            &server.server_name
        }),
        xml(&server.server_identifier),
        tools.join("\n"),
    ))
}

fn mcp_input_schema(tool: &pb::McpToolDescriptor) -> Option<String> {
    tool.input_schema_json
        .as_deref()
        .filter(|value| !value.trim().is_empty())
        .map(|value| {
            serde_json::from_str::<Value>(value)
                .map(|value| value.to_string())
                .unwrap_or_else(|_| value.to_string())
        })
        .or_else(|| {
            tool.input_schema
                .as_ref()
                .map(prost_value)
                .map(|value| value.to_string())
        })
}

pub fn meta_mcp_routes(context: &pb::RequestContext) -> HashMap<(String, String), McpRoute> {
    context
        .mcp_meta_tool_options
        .as_ref()
        .into_iter()
        .flat_map(|options| &options.mcp_descriptors)
        .filter(|server| !server.server_identifier.trim().is_empty())
        .flat_map(|server| {
            server.tools.iter().filter_map(move |tool| {
                if tool.tool_name.trim().is_empty() {
                    return None;
                }
                let provider_identifier = if server.server_name.trim().is_empty() {
                    server.server_identifier.clone()
                } else {
                    server.server_name.clone()
                };
                Some((
                    (server.server_identifier.clone(), tool.tool_name.clone()),
                    McpRoute {
                        name: format!("{}-{}", server.server_identifier, tool.tool_name),
                        provider_identifier,
                        tool_name: tool.tool_name.clone(),
                        description: tool.description.clone().unwrap_or_default(),
                    },
                ))
            })
        })
        .collect()
}

fn is_skill_rule(rule: &pb::CursorRule) -> bool {
    Path::new(&rule.full_path)
        .file_name()
        .and_then(|name| name.to_str())
        .is_some_and(|name| name.eq_ignore_ascii_case("SKILL.md"))
}

pub fn selected_context(user: &pb::UserMessage) -> Option<String> {
    let selected = user.selected_context.as_ref()?;
    let mut sections = selected.extra_context.clone();
    sections.extend(
        selected
            .files
            .iter()
            .map(|file| format!("<file path=\"{}\">\n{}\n</file>", file.path, file.content)),
    );
    sections.extend(
        selected
            .code_selections
            .iter()
            .map(|value| format!("<code path=\"{}\">\n{}\n</code>", value.path, value.content)),
    );
    sections.extend(selected.terminals.iter().map(|value| {
        format!(
            "<terminal title=\"{}\">\n{}\n</terminal>",
            value.title.as_deref().unwrap_or_default(),
            value.content
        )
    }));
    sections.extend(selected.terminal_selections.iter().map(|value| {
        format!(
            "<terminal_selection title=\"{}\">\n{}\n</terminal_selection>",
            value.title.as_deref().unwrap_or_default(),
            value.content
        )
    }));
    sections.extend(selected.cursor_rules.iter().filter_map(|value| {
        value.rule.as_ref().map(|rule| {
            format!(
                "<rule path=\"{}\">\n{}\n</rule>",
                rule.full_path, rule.content
            )
        })
    }));
    sections.extend(selected.cursor_commands.iter().map(|value| {
        format!(
            "<command name=\"{}\">\n{}\n</command>",
            value.name, value.content
        )
    }));
    sections.extend(selected.selected_skills.iter().map(|value| {
        format!(
            "<skill path=\"{}\">\n{}\n{}\n</skill>",
            value.full_path, value.description, value.content
        )
    }));
    sections.extend(selected.external_links.iter().map(|value| {
        format!(
            "External link: {}{}",
            value.url,
            value
                .pdf_content
                .as_deref()
                .map(|content| format!("\n{content}"))
                .unwrap_or_default()
        )
    }));
    Some(sections.join("\n\n"))
}

pub fn dynamic_mcp(
    request: &pb::AgentRunRequest,
    context: &pb::RequestContext,
) -> Result<BTreeMap<String, (pb::McpToolDefinition, ToolDefinition)>> {
    let direct = request
        .mcp_tools
        .iter()
        .flat_map(|tools| tools.mcp_tools.iter());
    let contextual = context.tools.iter();
    let mut output = BTreeMap::new();
    for wire in direct.chain(contextual) {
        if wire.name.is_empty() {
            return Err(Error::Protocol(
                "MCP tool definition is missing name".into(),
            ));
        }
        let parameters = match wire.input_schema_json.as_deref() {
            Some(json) if !json.trim().is_empty() => serde_json::from_str(json)?,
            _ => prost_value(wire.input_schema.as_ref().ok_or_else(|| {
                Error::Protocol(format!("MCP tool {} is missing input schema", wire.name))
            })?),
        };
        let parameters = normalize_mcp_parameters(&wire.name, parameters)?;
        let name = model_tool_name(&wire.name);
        let definition = ToolDefinition {
            name: name.clone(),
            description: wire.description.clone(),
            parameters,
        };
        if output
            .insert(name.clone(), (wire.clone(), definition))
            .is_some()
        {
            return Err(Error::Protocol(format!(
                "duplicate MCP tool name after normalization: {name}"
            )));
        }
    }
    Ok(output)
}

fn normalize_mcp_parameters(tool_name: &str, mut parameters: Value) -> Result<Value> {
    let schema = parameters
        .as_object_mut()
        .ok_or_else(|| invalid_mcp_parameters(tool_name))?;
    match schema.get("type") {
        Some(Value::String(schema_type)) if schema_type == "object" => return Ok(parameters),
        Some(_) => return Err(invalid_mcp_parameters(tool_name)),
        None => {}
    }
    let object_only_union = ["anyOf", "oneOf"].into_iter().any(|keyword| {
        schema
            .get(keyword)
            .and_then(Value::as_array)
            .is_some_and(|branches| {
                !branches.is_empty()
                    && branches.iter().all(|branch| {
                        branch
                            .as_object()
                            .and_then(|branch| branch.get("type"))
                            .and_then(Value::as_str)
                            == Some("object")
                    })
            })
    });
    if !object_only_union {
        return Err(invalid_mcp_parameters(tool_name));
    }
    // OpenAI-compatible function schemas (and the corresponding schema
    // validators used by other providers) require the root schema to declare
    // an object type. Cursor's app-control MCP sometimes sends an object-only
    // `anyOf`/`oneOf` schema without that root annotation. Preserve the union
    // while adding the annotation to the model-facing copy.
    schema.insert("type".into(), Value::String("object".into()));
    Ok(parameters)
}

fn invalid_mcp_parameters(tool_name: &str) -> Error {
    Error::Protocol(format!(
        "MCP tool {tool_name} input schema must describe an object"
    ))
}

fn model_tool_name(name: &str) -> String {
    name.chars()
        .map(|character| {
            if character.is_ascii_alphanumeric() || matches!(character, '_' | '-') {
                character
            } else {
                '_'
            }
        })
        .collect()
}

fn prost_value(value: &prost_types::Value) -> Value {
    use prost_types::value::Kind;
    match value.kind.as_ref() {
        None | Some(Kind::NullValue(_)) => Value::Null,
        Some(Kind::NumberValue(value)) => serde_json::Number::from_f64(*value)
            .map(Value::Number)
            .unwrap_or(Value::Null),
        Some(Kind::StringValue(value)) => Value::String(value.clone()),
        Some(Kind::BoolValue(value)) => Value::Bool(*value),
        Some(Kind::StructValue(value)) => Value::Object(
            value
                .fields
                .iter()
                .map(|(key, value)| (key.clone(), prost_value(value)))
                .collect(),
        ),
        Some(Kind::ListValue(value)) => {
            Value::Array(value.values.iter().map(prost_value).collect())
        }
    }
}

fn xml(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('"', "&quot;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn direct_mcp_tool(name: &str) -> pb::McpToolDefinition {
        pb::McpToolDefinition {
            name: name.into(),
            provider_identifier: "extension-GitKraken".into(),
            tool_name: "git_status".into(),
            description: "Get repository status".into(),
            input_schema_json: Some(r#"{"type":"object"}"#.into()),
            ..Default::default()
        }
    }

    #[test]
    fn dynamic_mcp_normalizes_extension_identifier_for_model_tool_names() {
        let original = "user-eamodio.gitlens-extension-GitKraken-git_status";
        let request = pb::AgentRunRequest {
            mcp_tools: Some(pb::McpTools {
                mcp_tools: vec![direct_mcp_tool(original)],
            }),
            ..Default::default()
        };

        let tools = dynamic_mcp(&request, &pb::RequestContext::default()).unwrap();
        let normalized = "user-eamodio_gitlens-extension-GitKraken-git_status";
        let (wire, definition) = tools.get(normalized).unwrap();

        assert_eq!(definition.name, normalized);
        assert_eq!(wire.name, original);
        assert_eq!(wire.provider_identifier, "extension-GitKraken");
        assert_eq!(wire.tool_name, "git_status");
        assert!(normalized
            .chars()
            .all(|character| character.is_ascii_alphanumeric() || matches!(character, '_' | '-')));
    }

    #[test]
    fn dynamic_mcp_rejects_names_that_collide_after_normalization() {
        let request = pb::AgentRunRequest {
            mcp_tools: Some(pb::McpTools {
                mcp_tools: vec![
                    direct_mcp_tool("server.name-tool"),
                    direct_mcp_tool("server_name-tool"),
                ],
            }),
            ..Default::default()
        };

        let error = dynamic_mcp(&request, &pb::RequestContext::default()).unwrap_err();
        assert!(error
            .to_string()
            .contains("duplicate MCP tool name after normalization: server_name-tool"));
    }

    #[test]
    fn dynamic_mcp_normalizes_cursor_object_union_without_mutating_wire_schema() {
        let original_schema = serde_json::json!({
            "$schema": "https://json-schema.org/draft/2020-12/schema",
            "anyOf": [
                {
                    "type": "object",
                    "properties": {
                        "rootPath": { "type": "string", "minLength": 1 }
                    },
                    "required": ["rootPath"],
                    "additionalProperties": false
                },
                {
                    "type": "object",
                    "properties": {
                        "rootPaths": {
                            "type": "array",
                            "items": { "type": "string", "minLength": 1 },
                            "minItems": 1
                        }
                    },
                    "required": ["rootPaths"],
                    "additionalProperties": false
                }
            ]
        });
        let original_json = original_schema.to_string();
        let mut tool = direct_mcp_tool("cursor-app-control-move_agent_to_cloned_root");
        tool.input_schema_json = Some(original_json.clone());
        let request = pb::AgentRunRequest {
            mcp_tools: Some(pb::McpTools {
                mcp_tools: vec![tool],
            }),
            ..Default::default()
        };

        let tools = dynamic_mcp(&request, &pb::RequestContext::default()).unwrap();
        let (wire, definition) = tools
            .get("cursor-app-control-move_agent_to_cloned_root")
            .unwrap();

        assert_eq!(definition.parameters["type"], "object");
        assert_eq!(definition.parameters["anyOf"], original_schema["anyOf"]);
        assert_eq!(
            wire.input_schema_json.as_deref(),
            Some(original_json.as_str())
        );
    }

    #[test]
    fn dynamic_mcp_preserves_valid_object_schema() {
        let original_schema = serde_json::json!({
            "type": "object",
            "properties": {
                "query": { "type": "string" }
            },
            "required": ["query"],
            "additionalProperties": false
        });
        let mut tool = direct_mcp_tool("search");
        tool.input_schema_json = Some(original_schema.to_string());
        let request = pb::AgentRunRequest {
            mcp_tools: Some(pb::McpTools {
                mcp_tools: vec![tool],
            }),
            ..Default::default()
        };

        let tools = dynamic_mcp(&request, &pb::RequestContext::default()).unwrap();
        let (_, definition) = tools.get("search").unwrap();

        assert_eq!(definition.parameters, original_schema);
    }

    #[test]
    fn dynamic_mcp_rejects_schemas_that_are_not_provably_objects() {
        let invalid_schemas = [
            serde_json::Value::Null,
            serde_json::json!({ "type": "string" }),
            serde_json::json!({ "properties": { "query": { "type": "string" } } }),
            serde_json::json!({
                "anyOf": [
                    { "type": "object" },
                    { "type": "string" }
                ]
            }),
        ];

        for schema in invalid_schemas {
            let mut tool = direct_mcp_tool("unsafe_schema");
            tool.input_schema_json = Some(schema.to_string());
            let request = pb::AgentRunRequest {
                mcp_tools: Some(pb::McpTools {
                    mcp_tools: vec![tool],
                }),
                ..Default::default()
            };

            let error = dynamic_mcp(&request, &pb::RequestContext::default()).unwrap_err();
            assert!(
                error
                    .to_string()
                    .contains("MCP tool unsafe_schema input schema must describe an object"),
                "unexpected error for {schema}: {error}"
            );
        }
    }

    #[test]
    fn meta_mcp_routes_projects_descriptor_routing_without_runtime_discovery() {
        let context = pb::RequestContext {
            mcp_meta_tool_options: Some(pb::McpMetaToolOptions {
                enabled: true,
                mcp_descriptors: vec![pb::McpDescriptor {
                    server_name: "fast-context".into(),
                    server_identifier: "fast-context".into(),
                    tools: vec![pb::McpToolDescriptor {
                        tool_name: "fast_context_search".into(),
                        description: Some("search code".into()),
                        input_schema_json: Some(r#"{"type":"object"}"#.into()),
                        ..Default::default()
                    }],
                    ..Default::default()
                }],
            }),
            ..Default::default()
        };

        let routes = meta_mcp_routes(&context);
        let route = routes
            .get(&("fast-context".into(), "fast_context_search".into()))
            .unwrap();
        assert_eq!(route.name, "fast-context-fast_context_search");
        assert_eq!(route.provider_identifier, "fast-context");
        assert_eq!(route.tool_name, "fast_context_search");
    }
}
