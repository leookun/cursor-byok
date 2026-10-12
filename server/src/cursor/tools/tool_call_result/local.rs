//! Converts server-local Tool completions into Tool results.
use serde_json::Value;

use crate::{
    cursor::{
        prompting::apply_todo_write, protocol::proto::agent::v1 as pb, tools::codec as interaction,
    },
    model::{ToolCall, ToolResult},
    Error, Result,
};

use super::{now_ms, ToolCompletion};

const SUBAGENTS_DISABLED_REMINDER: &str = "<system_reminder>The user has disabled the subagent model. Please remind the user to enable it in Cursor Settings → Models → Explore Subagent Model.</system_reminder>";

pub(crate) fn local(call: &ToolCall, message_index: usize) -> Result<ToolCompletion> {
    match normalized(&call.name).as_str() {
        "todowrite" => todo_write(call),
        "updatecurrentstep" => update_current_step(call, message_index),
        _ => Err(Error::Protocol(format!("unsupported tool: {}", call.name))),
    }
}

pub(crate) fn subagents_disabled(call: &ToolCall) -> Result<ToolCompletion> {
    let mut rendered = interaction::render_tool_call(call, false)?;
    let Some(pb::tool_call::Tool::TaskToolCall(tool)) = rendered.tool.as_mut() else {
        return Err(Error::Protocol("Task has no Cursor representation".into()));
    };
    tool.result = Some(pb::TaskResult {
        result: Some(pb::task_result::Result::Error(pb::TaskError {
            error: SUBAGENTS_DISABLED_REMINDER.into(),
        })),
    });
    let tool = rendered
        .tool
        .ok_or_else(|| Error::Protocol("Task has no Cursor representation".into()))?;
    Ok(ToolCompletion::new(
        call,
        now_ms(),
        ToolResult {
            call_id: call.call_id.clone(),
            content: SUBAGENTS_DISABLED_REMINDER.into(),
            is_error: true,
            image: None,
        },
        tool,
    ))
}

fn todo_write(call: &ToolCall) -> Result<ToolCompletion> {
    let todos = todo_items(&call.arguments);
    let total_count = todos.len() as i32;
    let was_merge = call
        .arguments
        .get("merge")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    let mut rendered = interaction::render_tool_call(call, false)?;
    let Some(pb::tool_call::Tool::UpdateTodosToolCall(tool)) = rendered.tool.as_mut() else {
        return Err(Error::Protocol(
            "TodoWrite has no Cursor representation".into(),
        ));
    };
    tool.result = Some(pb::UpdateTodosResult {
        result: Some(pb::update_todos_result::Result::Success(
            pb::UpdateTodosSuccess {
                todos,
                total_count,
                was_merge,
            },
        )),
    });
    let tool = rendered
        .tool
        .ok_or_else(|| Error::Protocol("TodoWrite has no Cursor representation".into()))?;
    Ok(ToolCompletion::new(
        call,
        now_ms(),
        ToolResult {
            call_id: call.call_id.clone(),
            content: call.arguments.to_string(),
            is_error: false,
            image: None,
        },
        tool,
    ))
}

pub(crate) fn project_todo_completion(
    call: &ToolCall,
    completion: &mut ToolCompletion,
    state: &mut Value,
) {
    let Some(pb::tool_call::Tool::UpdateTodosToolCall(tool)) = completion.tool_call.tool.as_mut()
    else {
        return;
    };
    let Some(pb::update_todos_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    let next = apply_todo_write(Some(state.clone()), call.arguments.clone());
    // Cursor compares the completed call's args against its result to describe
    // the change. Keep the pre-update list in args and the full merged list in
    // the result; canonical model arguments and ToolResult remain unchanged.
    if let Some(args) = tool.args.as_mut() {
        args.todos = todo_items(state);
    }
    success.todos = todo_items(&next);
    success.total_count = success.todos.len() as i32;
    *state = next;
}

fn update_current_step(call: &ToolCall, message_index: usize) -> Result<ToolCompletion> {
    let current_step = call
        .arguments
        .get("current_step")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    let mut rendered = interaction::render_tool_call(call, false)?;
    let Some(pb::tool_call::Tool::CommunicateUpdateToolCall(tool)) = rendered.tool.as_mut() else {
        return Err(Error::Protocol(
            "UpdateCurrentStep has no Cursor representation".into(),
        ));
    };
    let message_index = u32::try_from(message_index)
        .map_err(|_| Error::Protocol("Cursor message index space exhausted".into()))?;
    tool.result = Some(pb::CommunicateUpdateResult {
        result: Some(pb::communicate_update_result::Result::Success(
            pb::CommunicateUpdateSuccess {
                current_step: current_step.clone(),
                message_index,
            },
        )),
    });
    let tool = rendered
        .tool
        .ok_or_else(|| Error::Protocol("UpdateCurrentStep has no Cursor representation".into()))?;
    Ok(ToolCompletion::new(
        call,
        now_ms(),
        ToolResult {
            call_id: call.call_id.clone(),
            content: serde_json::json!({
                "success": {
                    "current_step": current_step,
                    "message_index": message_index,
                }
            })
            .to_string(),
            is_error: false,
            image: None,
        },
        tool,
    ))
}

pub(crate) fn todo_items(arguments: &Value) -> Vec<pb::TodoItem> {
    arguments
        .get("todos")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .map(|todo| pb::TodoItem {
            id: text(todo, "id"),
            content: text(todo, "content"),
            status: match todo
                .get("status")
                .and_then(Value::as_str)
                .unwrap_or("pending")
            {
                "in_progress" => pb::TodoStatus::InProgress as i32,
                "completed" => pb::TodoStatus::Completed as i32,
                "cancelled" => pb::TodoStatus::Cancelled as i32,
                _ => pb::TodoStatus::Pending as i32,
            },
            created_at: 0,
            updated_at: 0,
            dependencies: todo
                .get("dependencies")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .filter_map(Value::as_str)
                .map(str::to_string)
                .collect(),
        })
        .collect()
}

fn text(value: &Value, name: &str) -> String {
    value
        .get(name)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .into()
}

fn normalized(name: &str) -> String {
    name.chars()
        .filter(|character| character.is_ascii_alphanumeric())
        .flat_map(char::to_lowercase)
        .collect()
}

#[cfg(test)]
mod tests {
    use prost::Message;
    use serde_json::json;

    use super::*;

    fn call(arguments: Value) -> ToolCall {
        ToolCall {
            index: 0,
            call_id: "todo-call".into(),
            model_call_id: "model-call".into(),
            name: "TodoWrite".into(),
            arguments_text: arguments.to_string(),
            arguments,
            argument_error: None,
        }
    }

    fn rendered(completion: &ToolCompletion) -> pb::UpdateTodosToolCall {
        let encoded = completion.tool_call().encode_to_vec();
        let decoded = pb::ToolCall::decode(encoded.as_slice()).unwrap();
        let Some(pb::tool_call::Tool::UpdateTodosToolCall(tool)) = decoded.tool else {
            panic!("expected TodoWrite wire call");
        };
        tool
    }

    fn success(tool: &pb::UpdateTodosToolCall) -> &pb::UpdateTodosSuccess {
        let Some(pb::update_todos_result::Result::Success(result)) = tool
            .result
            .as_ref()
            .and_then(|result| result.result.as_ref())
        else {
            panic!("expected TodoWrite success");
        };
        result
    }

    #[test]
    fn new_todos_render_as_additions_without_changing_model_results() {
        let call = call(json!({"merge": false, "todos": [
            {"id": "read", "content": "Read fixture", "status": "pending"},
            {"id": "verify", "content": "Verify fixture", "status": "pending"},
        ]}));
        let mut completion = todo_write(&call).unwrap();
        let mut state = json!({"merge": false, "todos": []});
        project_todo_completion(&call, &mut completion, &mut state);

        let tool = rendered(&completion);
        assert!(tool.args.unwrap().todos.is_empty());
        let result = success(&rendered(&completion)).clone();
        assert_eq!(result.todos.len(), 2);
        assert_eq!(result.total_count, 2);
        assert!(result
            .todos
            .iter()
            .all(|todo| todo.status == pb::TodoStatus::Pending as i32));
        assert_eq!(
            serde_json::from_str::<Value>(&completion.result().content).unwrap(),
            call.arguments
        );
        assert_eq!(state, call.arguments);
    }

    #[test]
    fn merged_todo_completion_keeps_previous_status_and_all_items() {
        let mut state = json!({"merge": false, "todos": [
            {"id": "read", "content": "Read fixture", "status": "completed"},
            {"id": "verify", "content": "Verify fixture", "status": "in_progress", "dependencies": ["read"]},
        ]});
        let call = call(json!({"merge": true, "todos": [
            {"id": "verify", "status": "completed"},
        ]}));
        let mut completion = todo_write(&call).unwrap();
        project_todo_completion(&call, &mut completion, &mut state);

        let tool = rendered(&completion);
        let previous = &tool.args.as_ref().unwrap().todos;
        let result = success(&tool);
        assert!(tool.args.as_ref().unwrap().merge);
        assert_eq!(previous.len(), 2);
        assert_eq!(previous[1].status, pb::TodoStatus::InProgress as i32);
        assert_eq!(result.total_count, 2);
        assert_eq!(result.todos[0].id, "read");
        assert_eq!(result.todos[1].content, "Verify fixture");
        assert_eq!(result.todos[1].status, pb::TodoStatus::Completed as i32);
        assert_eq!(result.todos[1].dependencies, ["read"]);
        assert_eq!(state["todos"][1]["status"], "completed");
    }

    #[test]
    fn clearing_todos_keeps_the_previous_list_for_cursor_display() {
        let mut state = json!({"merge": false, "todos": [
            {"id": "read", "content": "Read fixture", "status": "pending"},
        ]});
        let call = call(json!({"merge": false, "todos": []}));
        let mut completion = todo_write(&call).unwrap();
        project_todo_completion(&call, &mut completion, &mut state);

        let tool = rendered(&completion);
        assert_eq!(tool.args.as_ref().unwrap().todos.len(), 1);
        assert!(success(&tool).todos.is_empty());
        assert_eq!(success(&tool).total_count, 0);
        assert_eq!(state["todos"], json!([]));
    }
}
