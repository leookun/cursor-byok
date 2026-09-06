//! Correlates Tool completion events and gates final result delivery.
use std::collections::BTreeMap;

use crate::{cursor::protocol::proto::agent::v1 as pb, model::limit_tool_result_text};

const KIB: usize = 1024;
const READ_CONTENT_LIMIT: usize = 64 * KIB;
const READ_BINARY_LIMIT: usize = 32 * KIB;
const SHELL_STREAM_LIMIT: usize = 16 * KIB;
const SHELL_INTERLEAVED_LIMIT: usize = 32 * KIB;
const GREP_CONTENT_LIMIT: usize = 32 * KIB;
const GREP_MATCH_LIMIT: usize = 2 * KIB;
const GREP_MATCHES_PER_FILE: usize = 100;
const GREP_TOTAL_MATCHES: usize = 300;
const GREP_LIST_LIMIT: usize = 300;
const GLOB_FILE_LIMIT: usize = 200;
const EDIT_RESULT_LIMIT: usize = 32 * KIB;
const PATCH_EDIT_RESULT_LIMIT: usize = 4 * KIB;
const MCP_TEXT_LIMIT: usize = 32 * KIB;
const MCP_CONTENT_ITEM_LIMIT: usize = 20;
const MCP_STRUCTURED_LIMIT: usize = 32 * KIB;
const MCP_BINARY_LIMIT: usize = 32 * KIB;
const MCP_RESOURCE_LIMIT: usize = 200;
const MCP_RESOURCE_DESCRIPTION_LIMIT: usize = KIB;
const WEB_FETCH_LIMIT: usize = 32 * KIB;
const WEB_SEARCH_LIMIT: usize = 16 * KIB;
const WEB_SEARCH_TITLE_LIMIT: usize = 512;
const WEB_SEARCH_SNIPPET_LIMIT: usize = 2 * KIB;

pub(super) fn tool_completion(
    tool_name: &str,
    tool: &mut pb::tool_call::Tool,
    content: &mut String,
) {
    use pb::tool_call::Tool;

    match tool {
        Tool::ShellToolCall(tool) => gate_shell(tool),
        Tool::GrepToolCall(tool) => gate_grep(tool),
        Tool::GlobToolCall(tool) => gate_glob(tool),
        Tool::ReadToolCall(tool) => gate_read(tool),
        Tool::EditToolCall(tool) => gate_edit(tool_name, tool),
        Tool::McpToolCall(tool) => gate_mcp(tool),
        Tool::ListMcpResourcesToolCall(tool) => gate_mcp_resources(tool),
        Tool::ReadMcpResourceToolCall(tool) => gate_mcp_resource(tool),
        Tool::GetMcpToolsToolCall(tool) => gate_mcp_tools(tool),
        Tool::WebFetchToolCall(tool) => gate_web_fetch(tool),
        Tool::WebSearchToolCall(tool) => gate_web_search(tool),
        Tool::GenerateImageToolCall(tool) => gate_generate_image(tool),
        _ => {}
    }
    *content = limit_tool_result_text(tool_name, content);
}

pub(super) fn exec_message(message: &mut pb::exec_client_message::Message) {
    use pb::exec_client_message::Message;
    match message {
        Message::ShellResult(result) | Message::MiniSweAgentBashResult(result) => {
            gate_shell_result(result)
        }
        _ => {}
    }
}

fn gate_shell(tool: &mut pb::ShellToolCall) {
    if let Some(result) = tool.result.as_mut() {
        gate_shell_result(result);
    }
}

fn gate_shell_result(result: &mut pb::ShellResult) {
    use pb::shell_result::Result;
    match result.result.as_mut() {
        Some(Result::Success(success)) => {
            success.stdout = truncate_edges("Shell stdout", &success.stdout, SHELL_STREAM_LIMIT);
            success.stderr = truncate_edges("Shell stderr", &success.stderr, SHELL_STREAM_LIMIT);
            if let Some(interleaved) = success.interleaved_output.as_mut() {
                *interleaved = truncate_edges(
                    "Shell interleaved output",
                    interleaved,
                    SHELL_INTERLEAVED_LIMIT,
                );
            }
        }
        Some(Result::Failure(failure)) => {
            failure.stdout = truncate_edges("Shell stdout", &failure.stdout, SHELL_STREAM_LIMIT);
            failure.stderr = truncate_edges("Shell stderr", &failure.stderr, SHELL_STREAM_LIMIT);
            if let Some(interleaved) = failure.interleaved_output.as_mut() {
                *interleaved = truncate_edges(
                    "Shell interleaved output",
                    interleaved,
                    SHELL_INTERLEAVED_LIMIT,
                );
            }
        }
        _ => {}
    }
}

fn gate_read(tool: &mut pb::ReadToolCall) {
    let Some(pb::read_tool_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    let Some(output) = success.output.as_mut() else {
        return;
    };
    match output {
        pb::read_tool_success::Output::Content(value) => {
            let next = truncate_text("Read", value, READ_CONTENT_LIMIT);
            if next != *value {
                *value = next;
                success.exceeded_limit = true;
            }
        }
        pb::read_tool_success::Output::Data(value) if value.len() > READ_BINARY_LIMIT => {
            let notice = truncation_notice("Read binary data", READ_BINARY_LIMIT, 0, value.len());
            success.output = Some(pb::read_tool_success::Output::Content(notice));
            success.exceeded_limit = true;
        }
        _ => {}
    }
}

fn gate_glob(tool: &mut pb::GlobToolCall) {
    let Some(pb::glob_tool_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    let original = success.files.len();
    if original <= GLOB_FILE_LIMIT {
        if success.total_files <= 0 {
            success.total_files = original as i32;
        }
        return;
    }
    success.files.truncate(GLOB_FILE_LIMIT);
    success.total_files = success.total_files.max(original as i32);
    success.client_truncated = true;
}

fn gate_grep(tool: &mut pb::GrepToolCall) {
    let Some(pb::grep_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    let mut budget = GrepBudget {
        content_bytes: GREP_CONTENT_LIMIT,
        matches: GREP_TOTAL_MATCHES,
    };
    let mut workspace_names = success
        .workspace_results
        .keys()
        .cloned()
        .collect::<Vec<_>>();
    workspace_names.sort_unstable();
    for name in workspace_names {
        if let Some(result) = success.workspace_results.get_mut(&name) {
            gate_grep_union(result, &mut budget);
        }
    }
    if let Some(result) = success.active_editor_result.as_mut() {
        gate_grep_union(result, &mut budget);
    }
}

struct GrepBudget {
    content_bytes: usize,
    matches: usize,
}

fn gate_grep_union(result: &mut pb::GrepUnionResult, budget: &mut GrepBudget) {
    use pb::grep_union_result::Result;
    match result.result.as_mut() {
        Some(Result::Content(content)) => gate_grep_content(content, budget),
        Some(Result::Files(files)) => {
            let original = files.files.len();
            if original > GREP_LIST_LIMIT {
                files.files.truncate(GREP_LIST_LIMIT);
                files.client_truncated = true;
            }
            if files.total_files <= 0 {
                files.total_files = original as i32;
            }
        }
        Some(Result::Count(counts)) => {
            let original = counts.counts.len();
            if original > GREP_LIST_LIMIT {
                counts.counts.truncate(GREP_LIST_LIMIT);
                counts.client_truncated = true;
            }
            if counts.total_files <= 0 {
                counts.total_files = original as i32;
            }
        }
        None => {}
    }
}

fn gate_grep_content(content: &mut pb::GrepContentResult, budget: &mut GrepBudget) {
    if content
        .matches
        .iter()
        .flat_map(|file| &file.matches)
        .any(is_grep_notice)
    {
        return;
    }
    let original_bytes = grep_content_bytes(&content.matches);
    let original_files = content.matches.len();
    let mut truncated = false;
    let mut files = Vec::with_capacity(original_files);

    for file in &content.matches {
        if budget.matches == 0 || budget.content_bytes == 0 {
            truncated = true;
            break;
        }
        let mut next = pb::GrepFileMatch {
            file: file.file.clone(),
            matches: Vec::new(),
        };
        for matched in &file.matches {
            if is_grep_notice(matched) {
                next.matches.push(matched.clone());
                continue;
            }
            if next.matches.len() >= GREP_MATCHES_PER_FILE
                || budget.matches == 0
                || budget.content_bytes == 0
            {
                truncated = true;
                break;
            }
            let mut next_match = matched.clone();
            let original = next_match.content.clone();
            next_match.content = truncate_text("Grep match", &original, GREP_MATCH_LIMIT);
            if next_match.content != original {
                next_match.content_truncated = true;
                truncated = true;
            }
            if next_match.content.len() > budget.content_bytes {
                next_match.content =
                    truncate_text("Grep", &next_match.content, budget.content_bytes);
                next_match.content_truncated = true;
                truncated = true;
            }
            if next_match.content.trim().is_empty() {
                truncated = true;
                break;
            }
            budget.content_bytes = budget
                .content_bytes
                .saturating_sub(next_match.content.len());
            budget.matches -= 1;
            next.matches.push(next_match);
        }
        if next.matches.len() < file.matches.len() {
            truncated = true;
        }
        if !next.matches.is_empty() {
            files.push(next);
        }
    }
    if files.len() < original_files {
        truncated = true;
    }
    if truncated {
        content.client_truncated = true;
        add_grep_notice(&mut files, original_bytes);
    }
    content.matches = files;
}

fn add_grep_notice(files: &mut Vec<pb::GrepFileMatch>, original_bytes: usize) {
    if files
        .iter()
        .flat_map(|file| &file.matches)
        .any(is_grep_notice)
    {
        return;
    }
    loop {
        let used = grep_content_bytes(files);
        let notice = truncation_notice("Grep", GREP_CONTENT_LIMIT, used, original_bytes);
        if used.saturating_add(notice.len()) <= GREP_CONTENT_LIMIT {
            let matched = pb::GrepContentMatch {
                line_number: 0,
                content: notice,
                content_truncated: true,
                is_context_line: true,
            };
            if let Some(file) = files.last_mut() {
                file.matches.push(matched);
            } else {
                files.push(pb::GrepFileMatch {
                    file: "[truncated]".into(),
                    matches: vec![matched],
                });
            }
            return;
        }
        let Some(file) = files.last_mut() else {
            return;
        };
        file.matches.pop();
        if file.matches.is_empty() {
            files.pop();
        }
    }
}

fn is_grep_notice(matched: &pb::GrepContentMatch) -> bool {
    matched.line_number == 0
        && matched.content_truncated
        && matched
            .content
            .starts_with("[truncated: Grep result exceeded")
}

fn grep_content_bytes(files: &[pb::GrepFileMatch]) -> usize {
    files
        .iter()
        .flat_map(|file| &file.matches)
        .map(|matched| matched.content.len())
        .sum()
}

fn gate_edit(tool_name: &str, tool: &mut pb::EditToolCall) {
    let Some(pb::edit_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    let limit = match tool_name.trim() {
        "PatchEdit" | "PatchEditLines" | "PatchEditSpan" | "StrReplace" => PATCH_EDIT_RESULT_LIMIT,
        _ => EDIT_RESULT_LIMIT,
    };
    if let Some(diff) = success.diff_string.as_mut() {
        *diff = truncate_text(tool_name, diff, limit);
        success.before_full_file_content = None;
        success.after_full_file_content.clear();
    } else {
        success.before_full_file_content = None;
        success.after_full_file_content =
            truncate_text(tool_name, &success.after_full_file_content, limit);
    }
}

fn gate_mcp(tool: &mut pb::McpToolCall) {
    let Some(pb::mcp_tool_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    if success.content.iter().any(is_mcp_notice) {
        return;
    }
    let mut notices = Vec::new();
    if structured_json_len(&success.structured_content) > MCP_STRUCTURED_LIMIT {
        let original = structured_json_len(&success.structured_content);
        success.structured_content = truncated_struct(original, MCP_STRUCTURED_LIMIT);
        notices.push(truncation_notice(
            "MCP structured_content",
            MCP_STRUCTURED_LIMIT,
            0,
            original,
        ));
    }
    let original_items = success.content.len();
    if original_items > MCP_CONTENT_ITEM_LIMIT {
        success.content.truncate(MCP_CONTENT_ITEM_LIMIT);
        notices.push(format!(
            "[truncated: MCP content items exceeded {MCP_CONTENT_ITEM_LIMIT} items; showing {MCP_CONTENT_ITEM_LIMIT} of {original_items} items]"
        ));
    }
    let original_text_bytes = success.content.iter().fold(0usize, |total, item| {
        let bytes = match item.content.as_ref() {
            Some(pb::mcp_tool_result_content_item::Content::Text(text)) => text.text.len(),
            _ => 0,
        };
        total.saturating_add(bytes)
    });
    let mut remaining_text = MCP_TEXT_LIMIT;
    let mut text_truncated = false;
    let mut content = Vec::with_capacity(success.content.len() + notices.len());
    for mut item in std::mem::take(&mut success.content) {
        // MCP images are sent to the client as inline binary data. Truncating
        // an encoded image at an arbitrary byte boundary corrupts the image
        // and makes the client's image/screenshot fallback fail. The model
        // receives only the textual MCP summary below, which is bounded by
        // MCP_TEXT_LIMIT, so the image does not need this text-result gate.
        if let Some(pb::mcp_tool_result_content_item::Content::Text(text)) = item.content.as_mut() {
            let original = text.text.clone();
            let next = truncate_text("MCP content item", &original, MCP_TEXT_LIMIT);
            if remaining_text == 0 {
                text_truncated |= !next.is_empty();
                continue;
            }
            text.text = truncate_text("MCP text", &next, remaining_text);
            text_truncated |= text.text != next;
            remaining_text = remaining_text.saturating_sub(text.text.len());
        }
        content.push(item);
    }
    if text_truncated {
        notices.push(truncation_notice(
            "MCP text",
            MCP_TEXT_LIMIT,
            MCP_TEXT_LIMIT.saturating_sub(remaining_text),
            original_text_bytes,
        ));
    }
    content.extend(notices.into_iter().map(mcp_notice));
    success.content = content;
}

fn mcp_notice(text: String) -> pb::McpToolResultContentItem {
    pb::McpToolResultContentItem {
        content: Some(pb::mcp_tool_result_content_item::Content::Text(
            pb::McpTextContent {
                text,
                output_location: None,
            },
        )),
    }
}

fn is_mcp_notice(item: &pb::McpToolResultContentItem) -> bool {
    matches!(
        item.content.as_ref(),
        Some(pb::mcp_tool_result_content_item::Content::Text(text))
            if text.text.starts_with("[truncated:")
    )
}

fn structured_json_len(value: &Option<prost_types::Struct>) -> usize {
    value
        .as_ref()
        .and_then(|value| {
            serde_json::to_vec(&serde_json::Value::Object(
                value
                    .fields
                    .iter()
                    .map(|(key, value)| (key.clone(), super::prost_json(value)))
                    .collect(),
            ))
            .ok()
        })
        .map_or(0, |value| value.len())
}

fn truncated_struct(original: usize, limit: usize) -> Option<prost_types::Struct> {
    Some(prost_types::Struct {
        fields: BTreeMap::from([
            ("_truncated".into(), prost_bool(true)),
            ("original_json_bytes".into(), prost_number(original as f64)),
            ("limit_bytes".into(), prost_number(limit as f64)),
        ]),
    })
}

fn prost_bool(value: bool) -> prost_types::Value {
    prost_types::Value {
        kind: Some(prost_types::value::Kind::BoolValue(value)),
    }
}

fn prost_number(value: f64) -> prost_types::Value {
    prost_types::Value {
        kind: Some(prost_types::value::Kind::NumberValue(value)),
    }
}

fn gate_mcp_resources(tool: &mut pb::ListMcpResourcesToolCall) {
    let Some(pb::list_mcp_resources_exec_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    if success
        .resources
        .iter()
        .any(|resource| resource.uri == "truncated:list-mcp-resources")
    {
        return;
    }
    let original = success.resources.len();
    success.resources.truncate(MCP_RESOURCE_LIMIT);
    for resource in &mut success.resources {
        if let Some(description) = resource.description.as_mut() {
            *description = truncate_text(
                "MCP resource description",
                description,
                MCP_RESOURCE_DESCRIPTION_LIMIT,
            );
        }
    }
    if success.resources.len() < original {
        success
            .resources
            .push(pb::list_mcp_resources_exec_result::McpResource {
                uri: "truncated:list-mcp-resources".into(),
                name: Some("truncated".into()),
                description: Some(format!(
                    "[truncated: ListMcpResources result exceeded {MCP_RESOURCE_LIMIT} resources; showing {} of {original} resources]",
                    success.resources.len()
                )),
                ..Default::default()
            });
    }
}

fn gate_mcp_resource(tool: &mut pb::ReadMcpResourceToolCall) {
    let Some(pb::read_mcp_resource_exec_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    match success.content.as_mut() {
        Some(pb::read_mcp_resource_success::Content::Text(text)) => {
            *text = truncate_text("FetchMcpResource", text, MCP_TEXT_LIMIT);
        }
        Some(pb::read_mcp_resource_success::Content::Blob(blob))
            if blob.len() > MCP_BINARY_LIMIT =>
        {
            let notice =
                truncation_notice("FetchMcpResource blob", MCP_BINARY_LIMIT, 0, blob.len());
            success.content = Some(pb::read_mcp_resource_success::Content::Text(notice));
        }
        _ => {}
    }
}

fn gate_mcp_tools(tool: &mut pb::GetMcpToolsToolCall) {
    let Some(pb::get_mcp_tools_agent_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    success.content = truncate_text("GetMcpTools", &success.content, MCP_TEXT_LIMIT);
}

fn gate_web_fetch(tool: &mut pb::WebFetchToolCall) {
    let Some(pb::web_fetch_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    success.markdown = truncate_text("WebFetch", &success.markdown, WEB_FETCH_LIMIT);
}

fn gate_web_search(tool: &mut pb::WebSearchToolCall) {
    let Some(pb::web_search_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    for reference in &mut success.references {
        reference.title =
            truncate_text("WebSearch title", &reference.title, WEB_SEARCH_TITLE_LIMIT);
        reference.chunk = truncate_text(
            "WebSearch snippet",
            &reference.chunk,
            WEB_SEARCH_SNIPPET_LIMIT,
        );
    }
    let original = web_search_bytes(&success.references);
    while success.references.len() > 1 && web_search_bytes(&success.references) > WEB_SEARCH_LIMIT {
        success.references.pop();
    }
    if original > WEB_SEARCH_LIMIT {
        let total = web_search_bytes(&success.references);
        if let Some(reference) = success.references.last_mut() {
            let other = total.saturating_sub(reference.chunk.len());
            let notice = truncation_notice(
                "WebSearch",
                WEB_SEARCH_LIMIT,
                WEB_SEARCH_LIMIT.saturating_sub(other),
                original,
            );
            let available = WEB_SEARCH_LIMIT.saturating_sub(other + notice.len() + 2);
            reference.chunk = format!(
                "{}\n\n{notice}",
                utf8_prefix(&reference.chunk, available).trim_end_matches('\n')
            );
        }
    }
}

fn web_search_bytes(references: &[pb::WebSearchReference]) -> usize {
    references
        .iter()
        .map(|reference| reference.title.len() + reference.url.len() + reference.chunk.len())
        .sum()
}

fn gate_generate_image(tool: &mut pb::GenerateImageToolCall) {
    let Some(pb::generate_image_result::Result::Success(success)) = tool
        .result
        .as_mut()
        .and_then(|result| result.result.as_mut())
    else {
        return;
    };
    if !success.image_data.trim().is_empty()
        && !success
            .image_data
            .starts_with("[base64 image data omitted from replay; bytes=")
    {
        let original = success.image_data.trim().len();
        success.image_data = format!("[base64 image data omitted from replay; bytes={original}]");
    }
}

fn truncate_text(tool_name: &str, content: &str, limit: usize) -> String {
    if content.len() <= limit {
        return content.to_string();
    }
    let original = content.len();
    let mut shown = limit;
    let mut previous = None;
    loop {
        let notice = format!(
            "\n\n[truncated: {tool_name} result exceeded {limit} bytes; showing {shown} of {original} bytes]"
        );
        if notice.len() >= limit {
            // The notice alone would blow the budget; keep a plain prefix so the
            // result never costs more than `limit` bytes.
            return utf8_prefix(content, limit).to_string();
        }
        let kept = utf8_prefix(content, limit - notice.len());
        // `notice.len()` grows with the digit count of `shown`, so `kept.len()`
        // can alternate between two values across a power-of-ten boundary.
        if kept.len() == shown || previous == Some(kept.len()) {
            return truncated_prefix_with_notice(tool_name, content, limit, original, kept.len());
        }
        previous = Some(shown);
        shown = kept.len();
    }
}

fn truncated_prefix_with_notice(
    tool_name: &str,
    content: &str,
    limit: usize,
    original: usize,
    initial_cap: usize,
) -> String {
    let mut cap = initial_cap;
    loop {
        let kept = utf8_prefix(content, cap).trim_end_matches('\n');
        let notice = format!(
            "\n\n[truncated: {tool_name} result exceeded {limit} bytes; showing {} of {original} bytes]",
            kept.len()
        );
        if notice.len() >= limit {
            return utf8_prefix(content, limit).to_string();
        }
        let available = limit - notice.len();
        if kept.len() <= available {
            return format!("{kept}{notice}");
        }
        cap = available;
    }
}

fn truncate_edges(tool_name: &str, content: &str, limit: usize) -> String {
    if content.len() <= limit {
        return content.to_string();
    }
    let original = content.len();
    let mut shown = limit;
    loop {
        let notice = format!(
            "\n\n[truncated: {tool_name} result exceeded {limit} bytes; omitted middle; showing {shown} of {original} bytes]\n\n"
        );
        let available = limit.saturating_sub(notice.len());
        let head = utf8_prefix(content, available / 2);
        let tail = utf8_suffix(content, available.saturating_sub(head.len()));
        let next_shown = head.len().saturating_add(tail.len());
        if next_shown == shown {
            return format!("{head}{notice}{tail}");
        }
        shown = next_shown;
    }
}

fn truncation_notice(tool_name: &str, limit: usize, shown: usize, original: usize) -> String {
    format!(
        "[truncated: {tool_name} result exceeded {limit} bytes; showing {shown} of {original} bytes]"
    )
}

fn utf8_prefix(value: &str, limit: usize) -> &str {
    let mut end = limit.min(value.len());
    while end > 0 && !value.is_char_boundary(end) {
        end -= 1;
    }
    &value[..end]
}

fn utf8_suffix(value: &str, limit: usize) -> &str {
    let mut start = value.len().saturating_sub(limit);
    while start < value.len() && !value.is_char_boundary(start) {
        start += 1;
    }
    &value[start..]
}

#[cfg(test)]
mod tests {
    use super::*;

    fn grep_tool(matches: Vec<String>) -> pb::tool_call::Tool {
        pb::tool_call::Tool::GrepToolCall(pb::GrepToolCall {
            args: None,
            result: Some(pb::GrepResult {
                result: Some(pb::grep_result::Result::Success(pb::GrepSuccess {
                    active_editor_result: Some(pb::GrepUnionResult {
                        result: Some(pb::grep_union_result::Result::Content(
                            pb::GrepContentResult {
                                matches: vec![pb::GrepFileMatch {
                                    file: "src/lib.rs".into(),
                                    matches: matches
                                        .into_iter()
                                        .enumerate()
                                        .map(|(index, content)| pb::GrepContentMatch {
                                            line_number: index as i32 + 1,
                                            content,
                                            ..Default::default()
                                        })
                                        .collect(),
                                }],
                                ..Default::default()
                            },
                        )),
                    }),
                    ..Default::default()
                })),
            }),
        })
    }

    fn mcp_tool(texts: Vec<String>) -> pb::McpToolCall {
        pb::McpToolCall {
            args: None,
            result: Some(pb::McpToolResult {
                result: Some(pb::mcp_tool_result::Result::Success(pb::McpSuccess {
                    content: texts
                        .into_iter()
                        .map(|text| pb::McpToolResultContentItem {
                            content: Some(pb::mcp_tool_result_content_item::Content::Text(
                                pb::McpTextContent {
                                    text,
                                    output_location: None,
                                },
                            )),
                        })
                        .collect(),
                    is_error: false,
                    structured_content: None,
                })),
            }),
            description: None,
        }
    }

    #[test]
    fn truncate_text_never_exceeds_its_limit() {
        let content = "b".repeat(200);
        for limit in 1..=250 {
            let output = truncate_text("Grep", &content, limit);
            assert!(
                output.len() <= limit,
                "limit {limit} produced {} bytes",
                output.len()
            );
        }
    }

    #[test]
    fn truncate_text_terminates_when_the_notice_length_oscillates() {
        // `limit` values where the notice grows and shrinks with the digit count
        // of the reported byte count, so the fixed point is never reached.
        assert!(truncate_text("Grep", &"b".repeat(200), 78).len() <= 78);
        assert!(truncate_text("Grep", &"b".repeat(200), 170).len() <= 170);
        assert!(truncate_text("MCP text", &"b".repeat(500), 82).len() <= 82);
    }

    #[test]
    fn truncate_text_reports_the_actual_utf8_prefix_size() {
        let content = "😀".repeat(1_000);
        let output = truncate_text("Grep", &content, 81);
        let (kept, notice) = output
            .split_once("\n\n[truncated:")
            .expect("the truncation notice fits");
        assert!(
            notice.contains(&format!("showing {} of", kept.len())),
            "notice must report the actual UTF-8 prefix size: {output}"
        );
        assert!(output.len() <= 81);
    }

    #[test]
    fn mcp_total_text_truncation_always_adds_a_notice() {
        let mut tool = mcp_tool(vec!["a".repeat(MCP_TEXT_LIMIT - 8), "b".repeat(100)]);
        gate_mcp(&mut tool);
        let success = match tool.result.unwrap().result.unwrap() {
            pb::mcp_tool_result::Result::Success(success) => success,
            _ => panic!("expected MCP success"),
        };
        assert_eq!(success.content.len(), 3);
        assert!(is_mcp_notice(success.content.last().unwrap()));
    }

    #[test]
    fn grep_content_gate_survives_a_nearly_exhausted_byte_budget() {
        // 16 matches leave 16 bytes of the 32 KiB content budget, which is less
        // than the truncation notice for the 17th match.
        let mut matches = vec!["a".repeat(2047); 16];
        matches.push("b".repeat(100));
        let mut tool = grep_tool(matches);
        let mut content = String::new();
        tool_completion("Grep", &mut tool, &mut content);
    }

    #[test]
    fn grep_content_gate_terminates_on_an_oscillating_remaining_budget() {
        // The same path, tuned so the remaining budget lands on a `limit` where
        // the truncation notice length oscillates.
        let mut matches = vec!["a".repeat(2043); 15];
        matches.push("a".repeat(2045));
        matches.push("b".repeat(200));
        let mut tool = grep_tool(matches);
        let mut content = String::new();
        tool_completion("Grep", &mut tool, &mut content);
    }

    #[test]
    fn list_mcp_resources_reports_the_cap_it_actually_applied() {
        let resources = (0..MCP_RESOURCE_LIMIT + 50)
            .map(|index| pb::list_mcp_resources_exec_result::McpResource {
                uri: format!("mcp://resource/{index}"),
                ..Default::default()
            })
            .collect();
        let mut tool = pb::ListMcpResourcesToolCall {
            args: None,
            result: Some(pb::ListMcpResourcesExecResult {
                result: Some(pb::list_mcp_resources_exec_result::Result::Success(
                    pb::ListMcpResourcesSuccess { resources },
                )),
            }),
        };

        gate_mcp_resources(&mut tool);

        let pb::list_mcp_resources_exec_result::Result::Success(success) =
            tool.result.unwrap().result.unwrap()
        else {
            panic!("expected a successful result");
        };
        let notice = success.resources.last().unwrap();
        assert_eq!(notice.uri, "truncated:list-mcp-resources");
        assert_eq!(
            notice.description.as_deref(),
            Some(
                "[truncated: ListMcpResources result exceeded 200 resources; \
                 showing 200 of 250 resources]"
            )
        );
    }
}
