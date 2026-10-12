//! Builds deterministic prompt state derived from Conversation context.
use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::model::{CanonicalMessage, MessageContent};

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct DerivedState {
    pub todos: Option<Value>,
    pub plan: Option<Value>,
}

pub fn fold_derived_state(messages: &[CanonicalMessage]) -> DerivedState {
    fold_derived_state_from(messages, DerivedState::default())
}

pub fn fold_derived_state_from(
    messages: &[CanonicalMessage],
    mut state: DerivedState,
) -> DerivedState {
    let mut calls = std::collections::HashMap::<String, (String, Value)>::new();
    for message in messages {
        match &message.content {
            MessageContent::Assistant { tool_calls, .. } => {
                for call in tool_calls {
                    calls.insert(
                        call.call_id.clone(),
                        (call.name.clone(), call.arguments.clone()),
                    );
                }
            }
            MessageContent::ToolResult(result) if !result.is_error => {
                let Some((name, input)) = calls.get(&result.call_id).cloned() else {
                    continue;
                };
                match normalize(&name).as_str() {
                    "todowrite" | "updatetodos" => {
                        state.todos = Some(apply_todo_write(state.todos.take(), input));
                    }
                    "createplan" | "updateplan" | "writeplan" => state.plan = Some(input),
                    _ => {}
                }
            }
            _ => {}
        }
    }
    state
}

pub(crate) fn apply_todo_write(current: Option<Value>, mut input: Value) -> Value {
    if !input.get("merge").and_then(Value::as_bool).unwrap_or(false) {
        return input;
    }
    let mut todos = current
        .as_ref()
        .and_then(|value| value.get("todos"))
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    let patches = input
        .get("todos")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    for patch in patches {
        let existing = patch.get("id").and_then(Value::as_str).and_then(|id| {
            todos
                .iter_mut()
                .find(|todo| todo.get("id").and_then(Value::as_str) == Some(id))
        });
        match (existing, patch) {
            (Some(Value::Object(todo)), Value::Object(patch)) => todo.extend(patch),
            (_, patch) => todos.push(patch),
        }
    }
    if let Some(object) = input.as_object_mut() {
        object.insert("merge".into(), Value::Bool(false));
        object.insert("todos".into(), Value::Array(todos));
    }
    input
}

fn normalize(value: &str) -> String {
    value
        .chars()
        .filter(|character| character.is_ascii_alphanumeric())
        .flat_map(char::to_lowercase)
        .collect()
}

#[cfg(test)]
mod tests {
    use serde_json::{json, Value};

    use super::{fold_derived_state_from, DerivedState};
    use crate::model::{
        CanonicalMessage, MessageContent, Origin, Role, ToolCallContent, ToolResultContent,
    };

    #[test]
    fn merge_patch_inherits_content_from_checkpoint_todo_state() {
        let messages = todo_write_messages(json!({
            "merge": true,
            "todos": [{"id": "tests", "status": "completed"}],
        }));
        let initial = DerivedState {
            todos: Some(json!({
                "merge": false,
                "todos": [{
                    "id": "tests",
                    "content": "Run focused tests",
                    "status": "in_progress",
                }],
            })),
            plan: None,
        };

        let state = fold_derived_state_from(&messages, initial);
        assert_eq!(
            state.todos,
            Some(json!({
                "merge": false,
                "todos": [{
                    "id": "tests",
                    "content": "Run focused tests",
                    "status": "completed",
                }],
            }))
        );
    }

    fn todo_write_messages(arguments: Value) -> Vec<CanonicalMessage> {
        vec![
            CanonicalMessage {
                message_id: "assistant".into(),
                role: Role::Assistant,
                origin: Origin::Assistant,
                content: MessageContent::Assistant {
                    text: String::new(),
                    thinking: String::new(),
                    tool_round_id: None,
                    replay_state: None,
                    tool_calls: vec![ToolCallContent {
                        index: 0,
                        call_id: "todo-call".into(),
                        name: "TodoWrite".into(),
                        arguments,
                    }],
                },
                runtime_event_id: None,
            },
            CanonicalMessage {
                message_id: "result".into(),
                role: Role::Tool,
                origin: Origin::Tool,
                content: MessageContent::ToolResult(ToolResultContent {
                    call_id: "todo-call".into(),
                    name: "TodoWrite".into(),
                    content: "{}".into(),
                    is_error: false,
                    image: None,
                    provider_parts: Vec::new(),
                }),
                runtime_event_id: None,
            },
        ]
    }
}
