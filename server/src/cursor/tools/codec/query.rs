//! Encodes Tool calls as Cursor InteractionQuery messages.
use serde_json::Value;

use crate::{cursor::protocol::proto::agent::v1 as pb, model::ToolCall, Error, Result};

pub fn tool_query(id: u32, call: &ToolCall) -> Result<pb::AgentServerMessage> {
    use pb::interaction_query::Query;
    let string = |name: &str| {
        call.arguments
            .get(name)
            .and_then(Value::as_str)
            .map(str::to_string)
            .ok_or_else(|| Error::Protocol(format!("{} is missing {name}", call.name)))
    };
    // Claude 系模型常按 Claude Code 习惯输出别名参数(如 query),逐个回退兼容。
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
    let query = match normalized(&call.name).as_str() {
        "askquestion" => {
            let questions = call
                .arguments
                .get("questions")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .map(|question| -> Result<_> {
                    let required = |name: &str| {
                        question
                            .get(name)
                            .and_then(Value::as_str)
                            .map(str::to_string)
                            .ok_or_else(|| Error::Protocol(format!("question is missing {name}")))
                    };
                    let options = question
                        .get("options")
                        .and_then(Value::as_array)
                        .into_iter()
                        .flatten()
                        .map(|option| -> Result<_> {
                            let value = |name: &str| {
                                option
                                    .get(name)
                                    .and_then(Value::as_str)
                                    .map(str::to_string)
                                    .ok_or_else(|| {
                                        Error::Protocol(format!(
                                            "question option is missing {name}"
                                        ))
                                    })
                            };
                            Ok(pb::ask_question_args::Option {
                                id: value("id")?,
                                label: value("label")?,
                            })
                        })
                        .collect::<Result<Vec<_>>>()?;
                    Ok(pb::ask_question_args::Question {
                        id: required("id")?,
                        prompt: required("prompt")?,
                        options,
                        allow_multiple: question
                            .get("allow_multiple")
                            .and_then(Value::as_bool)
                            .unwrap_or(false),
                    })
                })
                .collect::<Result<Vec<_>>>()?;
            Query::AskQuestionInteractionQuery(pb::AskQuestionInteractionQuery {
                args: Some(pb::AskQuestionArgs {
                    title: optional_string("title").unwrap_or_default(),
                    questions,
                    run_async: false,
                    async_original_tool_call_id: String::new(),
                }),
                tool_call_id: call.call_id.clone(),
            })
        }
        "websearch" => Query::WebSearchRequestQuery(pb::WebSearchRequestQuery {
            args: Some(pb::WebSearchArgs {
                search_term: string_aliased(&["search_term", "query"])?,
                tool_call_id: call.call_id.clone(),
            }),
        }),
        "webfetch" => Query::WebFetchRequestQuery(pb::WebFetchRequestQuery {
            args: Some(pb::WebFetchArgs {
                url: string("url")?,
                tool_call_id: call.call_id.clone(),
            }),
            skip_approval: false,
            smart_mode_approval: smart_mode_approval(
                call,
                "requestSmartModeApproval",
                "smartModeBlockReason",
            )?,
        }),
        "switchmode" => Query::SwitchModeRequestQuery(pb::SwitchModeRequestQuery {
            args: Some(pb::SwitchModeArgs {
                target_mode_id: string("target_mode_id")?,
                explanation: optional_string("explanation"),
                tool_call_id: call.call_id.clone(),
            }),
        }),
        "createplan" => {
            let todos = call
                .arguments
                .get("todos")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .map(|todo| pb::TodoItem {
                    id: todo
                        .get("id")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .into(),
                    content: todo
                        .get("content")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .into(),
                    status: pb::TodoStatus::Pending as i32,
                    created_at: 0,
                    updated_at: 0,
                    dependencies: Vec::new(),
                })
                .collect();
            Query::CreatePlanRequestQuery(pb::CreatePlanRequestQuery {
                args: Some(pb::CreatePlanArgs {
                    plan: string("plan")?,
                    todos,
                    overview: string("overview")?,
                    name: optional_string("name").unwrap_or_default(),
                    is_project: false,
                    phases: Vec::new(),
                }),
                tool_call_id: call.call_id.clone(),
            })
        }
        "generateimage" => Query::GenerateImageRequestQuery(pb::GenerateImageRequestQuery {
            args: Some(pb::GenerateImageArgs {
                description: string("description")?,
                file_path: optional_string("filename"),
                reference_image_paths: call
                    .arguments
                    .get("reference_image_paths")
                    .and_then(Value::as_array)
                    .into_iter()
                    .flatten()
                    .filter_map(Value::as_str)
                    .map(str::to_string)
                    .collect(),
                aspect_ratio: optional_string("aspect_ratio"),
            }),
            tool_call_id: call.call_id.clone(),
        }),
        "callmcptool"
            if optional_string("toolName").is_some_and(|tool| normalized(&tool) == "mcpauth") =>
        {
            Query::McpAuthRequestQuery(pb::McpAuthRequestQuery {
                args: Some(pb::McpAuthArgs {
                    server_identifier: string("server")?,
                    tool_call_id: call.call_id.clone(),
                }),
            })
        }
        other => {
            return Err(Error::Protocol(format!(
                "tool {other} is not an InteractionQuery"
            )))
        }
    };
    Ok(pb::AgentServerMessage {
        ttft_breakdown: None,
        message: Some(pb::agent_server_message::Message::InteractionQuery(
            pb::InteractionQuery {
                id,
                query: Some(query),
            },
        )),
    })
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

fn normalized(value: &str) -> String {
    value
        .chars()
        .filter(|character| character.is_ascii_alphanumeric())
        .flat_map(char::to_lowercase)
        .collect()
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::tool_query;
    use crate::cursor::protocol::proto::agent::v1 as pb;
    use crate::model::ToolCall;

    #[test]
    fn web_search_accepts_query_as_search_term_alias() {
        // Claude 系模型常按 Claude Code 习惯发送 query 而非 search_term,
        // 交互查询编码必须接受别名,而不是报 `WebSearch is missing search_term`。
        let call = ToolCall {
            index: 0,
            call_id: "call-1".into(),
            model_call_id: "model-1".into(),
            name: "WebSearch".into(),
            arguments_text: String::new(),
            arguments: json!({ "query": "lmarena leaderboard" }),
            argument_error: None,
        };
        let message = tool_query(1, &call).unwrap();
        let Some(pb::agent_server_message::Message::InteractionQuery(query)) = message.message
        else {
            panic!("expected an InteractionQuery");
        };
        let Some(pb::interaction_query::Query::WebSearchRequestQuery(request)) = query.query else {
            panic!("expected a WebSearchRequestQuery");
        };
        assert_eq!(request.args.unwrap().search_term, "lmarena leaderboard");
    }
}
