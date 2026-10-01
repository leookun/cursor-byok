//! Dispatches search Tool calls.
//! Cursor tool orchestration for application-owned Semble search.

use crate::{
    cursor::tools::{
        codec,
        runtime::{now_ms, CursorToolRuntime, ExecContext},
        tool_call_result::{self as result, ToolResultSender},
    },
    model::ToolCall,
    search::{self, host_snapshot},
    store::Store,
    Result,
};

use super::ToolStart;

pub(super) async fn start(
    runtime: &CursorToolRuntime,
    results: &ToolResultSender,
    call: &ToolCall,
    context: &ExecContext,
    store: Option<Store>,
) -> Result<ToolStart> {
    let tool_name = super::normalized(&call.name);
    let repo = call
        .arguments
        .get("repo")
        .and_then(serde_json::Value::as_str)
        .unwrap_or_default()
        .trim();
    if host_snapshot::is_http_repo(repo) {
        return start_http(results, call, tool_name, store);
    }
    if !host_snapshot::is_absolute_fs_repo(repo) {
        let started_at_ms = now_ms();
        return Ok(ToolStart {
            messages: Vec::new(),
            completion: Some(result::semble(
                call,
                started_at_ms,
                Err(
                    "Semble repo must be an absolute Cursor-host filesystem path or an HTTP(S) Git URL"
                        .into(),
                ),
            )?),
        });
    }

    let id = runtime
        .reserve_semble_snapshot(call, context, repo.to_string())
        .await?;
    Ok(ToolStart {
        messages: vec![codec::semble_snapshot_request(id, call, context, repo)?],
        completion: None,
    })
}

fn start_http(
    results: &ToolResultSender,
    call: &ToolCall,
    tool_name: String,
    store: Option<Store>,
) -> Result<ToolStart> {
    let arguments = call.arguments.clone();
    let call = call.clone();
    let results = results.clone();
    let started_at_ms = now_ms();
    tokio::spawn(async move {
        let output = search::execute_semble(&tool_name, arguments, store).await;
        match result::semble(&call, started_at_ms, output) {
            Ok(completion) => results.send(completion),
            Err(error) => results.send_error(error),
        }
    });
    Ok(ToolStart {
        messages: Vec::new(),
        completion: None,
    })
}
