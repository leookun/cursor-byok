//! Maintains edit-specific Tool state and projections.
use serde_json::Value;
use similar::{ChangeTag, TextDiff};

use crate::{model::ToolCall, Error, Result};

use crate::cursor::protocol::proto::agent::v1 as pb;

#[derive(Clone, Debug)]
pub(crate) struct EditWrite {
    pub before: String,
    pub after: String,
}

pub(crate) fn path(call: &ToolCall) -> Result<String> {
    if normalized(&call.name) == "editnotebook" {
        string(call, "target_notebook")
    } else {
        // Claude 系模型常按 Claude Code 习惯输出 file_path/filePath,做别名兼容。
        string_any(call, &["path", "file_path", "filePath"])
    }
}

pub(crate) fn execution_path(call: &ToolCall) -> Result<Option<String>> {
    match normalized(&call.name).as_str() {
        "write" | "strreplace" | "editnotebook" => path(call).map(Some),
        _ => Ok(None),
    }
}

pub(crate) fn after_read(
    call: &ToolCall,
    result: &pb::ReadResult,
) -> std::result::Result<EditWrite, String> {
    let before = match result.result.as_ref() {
        Some(pb::read_result::Result::Success(success)) => {
            if success.truncated {
                return Err("cannot edit a truncated Read result".into());
            }
            match success.output.as_ref() {
                Some(pb::read_success::Output::Content(content)) => normalize_newlines(content),
                Some(pb::read_success::Output::Data(_)) => {
                    return Err("cannot edit a binary file".into());
                }
                None => return Err("Read result has no file content".into()),
            }
        }
        Some(pb::read_result::Result::FileNotFound(_)) if normalized(&call.name) == "write" => {
            String::new()
        }
        Some(pb::read_result::Result::FileNotFound(_)) => {
            return Err("file not found".into());
        }
        Some(pb::read_result::Result::Error(value)) => return Err(value.error.clone()),
        Some(pb::read_result::Result::Rejected(value)) => return Err(value.reason.clone()),
        Some(pb::read_result::Result::PermissionDenied(_)) => {
            return Err("read permission denied".into());
        }
        Some(pb::read_result::Result::InvalidFile(value)) => {
            return Err(value.reason.clone());
        }
        None => return Err("Read result is empty".into()),
    };
    let after = match normalized(&call.name).as_str() {
        "write" => {
            // Claude Code 习惯的 content 作为 contents 的别名兼容。
            normalize_newlines(
                &string_any(call, &["contents", "content"]).map_err(|error| error.to_string())?,
            )
        }
        "strreplace" => replace_string(call, &before)?,
        "editnotebook" => edit_notebook(call, &before)?,
        _ => return Err(format!("{} is not an edit tool", call.name)),
    };
    Ok(EditWrite { before, after })
}

pub(crate) fn success(path: String, write: &EditWrite) -> pb::EditResult {
    let diff = TextDiff::from_lines(&write.before, &write.after);
    let (mut added, mut removed) = (0, 0);
    for change in diff.iter_all_changes() {
        match change.tag() {
            ChangeTag::Delete => removed += 1,
            ChangeTag::Insert => added += 1,
            ChangeTag::Equal => {}
        }
    }
    pb::EditResult {
        result: Some(pb::edit_result::Result::Success(pb::EditSuccess {
            path,
            lines_added: Some(added),
            lines_removed: Some(removed),
            diff_string: Some(diff.unified_diff().to_string()),
            before_full_file_content: Some(write.before.clone()),
            after_full_file_content: write.after.clone(),
            message: None,
        })),
    }
}

pub(crate) fn failure(path: String, error: impl Into<String>) -> pb::EditResult {
    let error = error.into();
    pb::EditResult {
        result: Some(pb::edit_result::Result::Error(pb::EditError {
            path,
            error: error.clone(),
            model_visible_error: Some(error),
        })),
    }
}

pub(crate) fn normalize_newlines(value: &str) -> String {
    let normalized = value.replace("\r\n", "\n");
    normalized.replace('\r', "\n")
}

fn replace_string(call: &ToolCall, before: &str) -> std::result::Result<String, String> {
    let old = normalize_newlines(&string(call, "old_string").map_err(|error| error.to_string())?);
    let new = normalize_newlines(&string(call, "new_string").map_err(|error| error.to_string())?);
    if old.is_empty() {
        return Err("old_string must not be empty".into());
    }
    let occurrences = before.match_indices(&old).count();
    let replace_all = call
        .arguments
        .get("replace_all")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    match (replace_all, occurrences) {
        (_, 0) => Err("old_string was not found".into()),
        (false, 1) => Ok(before.replacen(&old, &new, 1)),
        (false, count) => Err(format!(
            "old_string is not unique; found {count} occurrences"
        )),
        (true, _) => Ok(before.replace(&old, &new)),
    }
}

fn edit_notebook(call: &ToolCall, before: &str) -> std::result::Result<String, String> {
    let mut notebook: Value =
        serde_json::from_str(before).map_err(|error| format!("invalid notebook JSON: {error}"))?;
    let cells = notebook
        .get_mut("cells")
        .and_then(Value::as_array_mut)
        .ok_or_else(|| "notebook has no cells array".to_string())?;
    let index = call
        .arguments
        .get("cell_idx")
        .and_then(Value::as_u64)
        .and_then(|value| usize::try_from(value).ok())
        .ok_or_else(|| "EditNotebook is missing cell_idx".to_string())?;
    let new = normalize_newlines(&string(call, "new_string").map_err(|error| error.to_string())?);
    if call
        .arguments
        .get("is_new_cell")
        .and_then(Value::as_bool)
        .unwrap_or(false)
    {
        if index > cells.len() {
            return Err(format!("cell_idx {index} is past the end of the notebook"));
        }
        let language = string(call, "cell_language").map_err(|error| error.to_string())?;
        let cell_type = if language == "markdown" || language == "raw" {
            language.as_str()
        } else {
            "code"
        };
        let mut cell = serde_json::json!({
            "cell_type": cell_type,
            "metadata": {},
            "source": source_lines(&new),
        });
        if cell_type == "code" {
            cell["execution_count"] = Value::Null;
            cell["outputs"] = Value::Array(Vec::new());
        }
        cells.insert(index, cell);
    } else {
        let cell = cells
            .get_mut(index)
            .ok_or_else(|| format!("cell_idx {index} does not exist"))?;
        let source = cell
            .get("source")
            .map(notebook_source)
            .transpose()?
            .unwrap_or_default();
        let old =
            normalize_newlines(&string(call, "old_string").map_err(|error| error.to_string())?);
        if old.is_empty() {
            return Err("old_string must not be empty".into());
        }
        let occurrences = source.match_indices(&old).count();
        let edited = match occurrences {
            0 => return Err("old_string was not found in the notebook cell".into()),
            1 => source.replacen(&old, &new, 1),
            count => {
                return Err(format!(
                    "old_string is not unique in the notebook cell; found {count} occurrences"
                ))
            }
        };
        cell["source"] = Value::Array(source_lines(&edited));
    }
    serde_json::to_string_pretty(&notebook)
        .map(|value| format!("{value}\n"))
        .map_err(|error| error.to_string())
}

fn notebook_source(value: &Value) -> std::result::Result<String, String> {
    match value {
        Value::String(value) => Ok(normalize_newlines(value)),
        Value::Array(lines) => lines
            .iter()
            .map(|line| {
                line.as_str()
                    .ok_or_else(|| "notebook cell source contains a non-string".to_string())
            })
            .collect::<std::result::Result<Vec<_>, _>>()
            .map(|lines| normalize_newlines(&lines.concat())),
        _ => Err("notebook cell source is not text".into()),
    }
}

fn source_lines(value: &str) -> Vec<Value> {
    if value.is_empty() {
        Vec::new()
    } else {
        value
            .split_inclusive('\n')
            .map(|line| Value::String(line.to_string()))
            .collect()
    }
}

fn string(call: &ToolCall, field: &str) -> Result<String> {
    string_any(call, &[field])
}

fn string_any(call: &ToolCall, fields: &[&str]) -> Result<String> {
    for field in fields {
        if let Some(value) = call.arguments.get(field).and_then(Value::as_str) {
            return Ok(value.to_owned());
        }
    }
    Err(Error::Protocol(format!(
        "{} is missing {}",
        call.name, fields[0]
    )))
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

    use super::edit_notebook;
    use crate::model::ToolCall;

    fn notebook_call(old_string: &str) -> ToolCall {
        ToolCall {
            index: 0,
            call_id: "call".into(),
            model_call_id: "model".into(),
            name: "EditNotebook".into(),
            arguments_text: String::new(),
            arguments: json!({
                "target_notebook": "/notebook.ipynb",
                "cell_idx": 0,
                "old_string": old_string,
                "new_string": "replacement",
            }),
            argument_error: None,
        }
    }

    fn single_cell_notebook() -> String {
        json!({
            "cells": [{"cell_type": "code", "source": ["print('hi')\n"]}],
        })
        .to_string()
    }

    #[test]
    fn edit_notebook_rejects_empty_old_string() {
        // StrReplace rejects an empty old_string; EditNotebook must do the same
        // instead of prepending new_string (empty cell) or reporting a
        // misleading "not unique" error (non-empty cell).
        let error = edit_notebook(&notebook_call(""), &single_cell_notebook()).unwrap_err();
        assert_eq!(error, "old_string must not be empty");
    }

    #[test]
    fn edit_notebook_replaces_a_unique_old_string() {
        let edited = edit_notebook(&notebook_call("hi"), &single_cell_notebook()).unwrap();
        assert!(edited.contains("print('replacement')"));
    }
}
