//! Decodes Tool execution responses received from Cursor.
use crate::{
    cursor::{
        protocol::{events, proto::agent::v1 as pb},
        tools::{
            compat, edit,
            runtime::{CursorToolRuntime, ExecStage, PendingExec, SembleSnapshot},
            tool_call_result::{self as result, ToolCompletion},
        },
    },
    model::ToolCall,
    search::{self, host_snapshot},
    Error, Result,
};

use super::request::edit_write_request;

pub enum ClientExecEvent {
    Delta(Box<pb::AgentServerMessage>),
    Message(Box<pb::AgentServerMessage>),
    Completed(Box<ToolCompletion>),
    Pending,
}

pub async fn client_event(
    message: &pb::ExecClientMessage,
    pending: &CursorToolRuntime,
) -> Result<ClientExecEvent> {
    if pending.is_interrupted(message.id).await {
        if message.message.as_ref().is_some_and(is_terminal) {
            pending.discard_exec(message.id).await;
        }
        return Ok(ClientExecEvent::Pending);
    }
    let call = match pending.exec_call(message.id).await {
        Some(call) => call,
        None if pending.completed_call(message.id).await.is_some() => {
            tracing::warn!(id = message.id, "ignoring duplicate terminal tool response");
            return Ok(ClientExecEvent::Pending);
        }
        None => {
            tracing::warn!(
                id = message.id,
                "ignoring response for unknown tool execution"
            );
            return Ok(ClientExecEvent::Pending);
        }
    };
    let Some(wire_result) = &message.message else {
        return Ok(ClientExecEvent::Pending);
    };
    let pb::exec_client_message::Message::ShellStream(stream) = wire_result else {
        let entry = take(message.id, pending).await?;
        return match &entry.stage {
            ExecStage::EditRead => advance_edit(entry, wire_result, pending).await,
            ExecStage::SembleSnapshot(snapshot) => {
                let snapshot = snapshot.clone();
                complete_semble_snapshot(entry, snapshot, wire_result.clone()).await
            }
            ExecStage::Direct | ExecStage::DynamicMcp(_) | ExecStage::EditWrite(_) => {
                completed(entry, wire_result.clone())
            }
        };
    };
    use pb::shell_stream::Event;
    let is_semble = pending.exec_stage_is_semble_snapshot(message.id).await;
    let event = match &stream.event {
        Some(Event::Stdout(stdout)) => {
            if pending.append_stdout(message.id, &stdout.data).await {
                if is_semble {
                    ClientExecEvent::Pending
                } else {
                    ClientExecEvent::Delta(Box::new(shell_delta(&call, true, &stdout.data)))
                }
            } else {
                ClientExecEvent::Pending
            }
        }
        Some(Event::Stderr(stderr)) => {
            if pending.append_stderr(message.id, &stderr.data).await {
                if is_semble {
                    ClientExecEvent::Pending
                } else {
                    ClientExecEvent::Delta(Box::new(shell_delta(&call, false, &stderr.data)))
                }
            } else {
                ClientExecEvent::Pending
            }
        }
        Some(Event::Start(_)) | Some(Event::HookContext(_)) => ClientExecEvent::Pending,
        Some(Event::Exit(exit)) => {
            let entry = take(message.id, pending).await?;
            let shell = shell_exit_result(message, exit, &entry.stdout, &entry.stderr);
            finish_shell(entry, pb::exec_client_message::Message::ShellResult(shell)).await?
        }
        Some(Event::Backgrounded(backgrounded)) => {
            let entry = take(message.id, pending).await?;
            if matches!(entry.stage, ExecStage::SembleSnapshot(_)) {
                return Ok(ClientExecEvent::Completed(Box::new(result::semble(
                    &entry.call,
                    entry.started_at_ms,
                    Err(
                        "Semble host snapshot was backgrounded before completion; retry with a smaller repository"
                            .into(),
                    ),
                )?)));
            }
            let shell = shell_backgrounded_result(
                backgrounded,
                &entry.stdout,
                &entry.stderr,
                &entry.context.terminals_folder,
            );
            completed(entry, pb::exec_client_message::Message::ShellResult(shell))?
        }
        Some(Event::Rejected(value)) => {
            let shell = pb::ShellResult {
                result: Some(pb::shell_result::Result::Rejected(value.clone())),
                ..Default::default()
            };
            finish_terminal_shell(message.id, pending, shell).await?
        }
        Some(Event::PermissionDenied(value)) => {
            let shell = pb::ShellResult {
                result: Some(pb::shell_result::Result::PermissionDenied(value.clone())),
                ..Default::default()
            };
            finish_terminal_shell(message.id, pending, shell).await?
        }
        Some(Event::SandboxUnsupported(value)) => {
            let shell = pb::ShellResult {
                result: Some(pb::shell_result::Result::SpawnError(pb::ShellSpawnError {
                    command: value.command.clone(),
                    working_directory: value.working_directory.clone(),
                    error: value.reason.clone(),
                })),
                ..Default::default()
            };
            finish_terminal_shell(message.id, pending, shell).await?
        }
        None => ClientExecEvent::Pending,
    };
    Ok(event)
}

pub async fn stream_closed(id: u32, pending: &CursorToolRuntime) -> Result<Option<ToolCompletion>> {
    if pending.is_interrupted(id).await {
        pending.discard_exec(id).await;
        return Ok(None);
    }
    let Some(entry) = pending.take_exec(id).await else {
        return Ok(None);
    };
    if let ExecStage::SembleSnapshot(_) = &entry.stage {
        return Ok(Some(result::semble(
            &entry.call,
            entry.started_at_ms,
            Err("Cursor Exec stream closed before the Semble host snapshot finished".into()),
        )?));
    }
    let error = "Cursor Exec stream closed before returning a terminal result";
    if entry.call.name.eq_ignore_ascii_case("Shell") || entry.call.name.eq_ignore_ascii_case("Bash")
    {
        let command = entry
            .call
            .arguments
            .get("command")
            .and_then(serde_json::Value::as_str)
            .unwrap_or_default()
            .to_string();
        let working_directory = entry
            .call
            .arguments
            .get("working_directory")
            .and_then(serde_json::Value::as_str)
            .unwrap_or_default()
            .to_string();
        return Ok(Some(result::from_exec(
            entry,
            &pb::exec_client_message::Message::ShellResult(pb::ShellResult {
                result: Some(pb::shell_result::Result::SpawnError(pb::ShellSpawnError {
                    command,
                    working_directory,
                    error: error.into(),
                })),
                ..Default::default()
            }),
        )?));
    }
    let rendered = match &entry.stage {
        ExecStage::DynamicMcp(definition) => {
            Ok(super::render_dynamic_mcp(&entry.call, definition, false))
        }
        _ => super::render_tool_call(&entry.call, false),
    };
    let rendered = match rendered {
        Ok(rendered) => rendered,
        Err(Error::Protocol(message)) => {
            return Ok(Some(compat::failure_with_message(&entry.call, message)));
        }
        Err(Error::Json(error)) => {
            return Ok(Some(compat::failure_with_message(
                &entry.call,
                error.to_string(),
            )));
        }
        Err(error) => return Err(error),
    };
    Ok(Some(ToolCompletion::from_rendered(
        &entry.call,
        entry.started_at_ms,
        error.into(),
        true,
        rendered,
    )?))
}

fn is_terminal(message: &pb::exec_client_message::Message) -> bool {
    use pb::{exec_client_message::Message, shell_stream::Event};

    match message {
        Message::ShellStream(stream) => matches!(
            stream.event.as_ref(),
            Some(Event::Exit(_))
                | Some(Event::Backgrounded(_))
                | Some(Event::Rejected(_))
                | Some(Event::PermissionDenied(_))
                | Some(Event::SandboxUnsupported(_))
        ),
        _ => true,
    }
}

async fn advance_edit(
    entry: PendingExec,
    result: &pb::exec_client_message::Message,
    registry: &CursorToolRuntime,
) -> Result<ClientExecEvent> {
    let read = match result {
        pb::exec_client_message::Message::ReadResult(result)
        | pb::exec_client_message::Message::RedactedReadResult(result) => result,
        _ => {
            let message = format!("expected ReadResult for edit tool {}", entry.call.name);
            return Ok(ClientExecEvent::Completed(Box::new(
                compat::failure_with_message(&entry.call, message),
            )));
        }
    };
    let write = match edit::after_read(&entry.call, read) {
        Ok(write) => write,
        Err(error) => {
            return Ok(ClientExecEvent::Completed(Box::new(result::edit_failure(
                entry, error,
            )?)))
        }
    };
    let id = registry
        .reserve_edit_write(
            &entry.call,
            &entry.context,
            write.clone(),
            entry.started_at_ms,
        )
        .await?;
    Ok(ClientExecEvent::Message(Box::new(edit_write_request(
        id,
        &entry.call,
        &write,
    )?)))
}

async fn finish_terminal_shell(
    id: u32,
    pending: &CursorToolRuntime,
    shell: pb::ShellResult,
) -> Result<ClientExecEvent> {
    let entry = take(id, pending).await?;
    finish_shell(entry, pb::exec_client_message::Message::ShellResult(shell)).await
}

async fn finish_shell(
    entry: PendingExec,
    wire_result: pb::exec_client_message::Message,
) -> Result<ClientExecEvent> {
    match &entry.stage {
        ExecStage::SembleSnapshot(snapshot) => {
            let snapshot = snapshot.clone();
            complete_semble_snapshot(entry, snapshot, wire_result).await
        }
        _ => completed(entry, wire_result),
    }
}

async fn complete_semble_snapshot(
    entry: PendingExec,
    snapshot: SembleSnapshot,
    wire_result: pb::exec_client_message::Message,
) -> Result<ClientExecEvent> {
    let output = match snapshot_search_output(&entry, &snapshot, &wire_result).await {
        Ok(value) => Ok(value),
        Err(error) => Err(error),
    };
    Ok(ClientExecEvent::Completed(Box::new(result::semble(
        &entry.call,
        entry.started_at_ms,
        output,
    )?)))
}

async fn snapshot_search_output(
    entry: &PendingExec,
    snapshot: &SembleSnapshot,
    wire_result: &pb::exec_client_message::Message,
) -> std::result::Result<serde_json::Value, String> {
    let stdout = shell_stdout(wire_result)?;
    let dump = host_snapshot::parse_dump(&stdout)?;
    let root = host_snapshot::materialize_dump(
        &entry.context.conversation_id,
        &entry.call.call_id,
        &snapshot.remote_root,
        &dump,
    )?;
    let tool_name = entry
        .call
        .name
        .chars()
        .filter(|character| character.is_ascii_alphanumeric())
        .flat_map(char::to_lowercase)
        .collect::<String>();
    search::execute_semble_on_root(
        &tool_name,
        entry.call.arguments.clone(),
        root,
        snapshot.remote_root.clone(),
        host_snapshot::source_identity(&snapshot.remote_root),
        None,
    )
    .await
}

fn shell_stdout(
    wire_result: &pb::exec_client_message::Message,
) -> std::result::Result<String, String> {
    use pb::{exec_client_message::Message, shell_result::Result as ShellResult};
    let Message::ShellResult(result) = wire_result else {
        return Err("Semble host snapshot expected a Shell result".into());
    };
    match result.result.as_ref() {
        Some(ShellResult::Success(success)) => {
            if success.output_location.is_some() {
                return Err(
                    "Semble host snapshot output was spilled to a file; reduce repository size or raise the dump bound"
                        .into(),
                );
            }
            if !success.stdout.is_empty() {
                return Ok(success.stdout.clone());
            }
            if let Some(interleaved) = success.interleaved_output.as_ref() {
                if !interleaved.is_empty() {
                    return Ok(interleaved.clone());
                }
            }
            Err("Semble host snapshot completed without stdout".into())
        }
        Some(ShellResult::Failure(failure)) => {
            let detail = if failure.stderr.is_empty() {
                failure.stdout.clone()
            } else if failure.stdout.is_empty() {
                failure.stderr.clone()
            } else {
                format!("{}\n{}", failure.stdout, failure.stderr)
            };
            Err(if detail.is_empty() {
                format!(
                    "Semble host snapshot failed with exit code {}",
                    failure.exit_code
                )
            } else {
                detail
            })
        }
        Some(ShellResult::Rejected(rejected)) => Err(format!(
            "Semble host snapshot rejected: {}",
            rejected.reason
        )),
        Some(ShellResult::PermissionDenied(denied)) => Err(format!(
            "Semble host snapshot permission denied: {}",
            denied.error
        )),
        Some(ShellResult::SpawnError(error)) => {
            Err(format!("Semble host snapshot spawn error: {}", error.error))
        }
        Some(ShellResult::Timeout(timeout)) => Err(format!(
            "Semble host snapshot timed out after {}ms",
            timeout.timeout_ms
        )),
        None => Err("Semble host snapshot returned an empty Shell result".into()),
    }
}

async fn take(id: u32, pending: &CursorToolRuntime) -> Result<PendingExec> {
    pending
        .take_exec(id)
        .await
        .ok_or_else(|| Error::Protocol(format!("unknown terminal Exec id: {id}")))
}

fn completed(
    pending: PendingExec,
    result: pb::exec_client_message::Message,
) -> Result<ClientExecEvent> {
    let call = pending.call.clone();
    let completion = match result::from_exec(pending, &result) {
        Ok(completion) => completion,
        Err(Error::Protocol(message)) => compat::failure_with_message(&call, message),
        Err(Error::Json(error)) => compat::failure_with_message(&call, error.to_string()),
        Err(error) => return Err(error),
    };
    Ok(ClientExecEvent::Completed(Box::new(completion)))
}

fn shell_exit_result(
    message: &pb::ExecClientMessage,
    exit: &pb::ShellStreamExit,
    stdout: &str,
    stderr: &str,
) -> pb::ShellResult {
    let result = if exit.code == 0 && !exit.aborted {
        pb::shell_result::Result::Success(pb::ShellSuccess {
            working_directory: exit.cwd.clone(),
            exit_code: exit.code as i32,
            stdout: stdout.into(),
            stderr: stderr.into(),
            interleaved_output: Some(format!("{stdout}{stderr}")),
            local_execution_time_ms: exit
                .local_execution_time_ms
                .or(message.local_execution_time_ms),
            ..Default::default()
        })
    } else {
        pb::shell_result::Result::Failure(pb::ShellFailure {
            working_directory: exit.cwd.clone(),
            exit_code: exit.code as i32,
            stdout: stdout.into(),
            stderr: stderr.into(),
            interleaved_output: Some(format!("{stdout}{stderr}")),
            abort_reason: exit.abort_reason,
            aborted: exit.aborted,
            local_execution_time_ms: exit
                .local_execution_time_ms
                .or(message.local_execution_time_ms),
            ..Default::default()
        })
    };
    pb::ShellResult {
        result: Some(result),
        is_background: Some(false),
        ..Default::default()
    }
}

fn shell_backgrounded_result(
    backgrounded: &pb::ShellStreamBackgrounded,
    stdout: &str,
    stderr: &str,
    terminals_folder: &str,
) -> pb::ShellResult {
    pb::ShellResult {
        result: Some(pb::shell_result::Result::Success(pb::ShellSuccess {
            command: backgrounded.command.clone(),
            working_directory: backgrounded.working_directory.clone(),
            stdout: stdout.into(),
            stderr: stderr.into(),
            shell_id: Some(backgrounded.shell_id),
            pid: backgrounded.pid,
            ms_to_wait: backgrounded.ms_to_wait,
            background_reason: backgrounded.reason,
            interleaved_output: Some(format!("{stdout}{stderr}")),
            ..Default::default()
        })),
        is_background: Some(true),
        terminals_folder: (!terminals_folder.is_empty()).then(|| terminals_folder.into()),
        pid: backgrounded.pid,
        ..Default::default()
    }
}

fn shell_delta(call: &ToolCall, stdout: bool, content: &str) -> pb::AgentServerMessage {
    let delta = if stdout {
        pb::shell_tool_call_delta::Delta::Stdout(pb::ShellToolCallStdoutDelta {
            content: content.into(),
        })
    } else {
        pb::shell_tool_call_delta::Delta::Stderr(pb::ShellToolCallStderrDelta {
            content: content.into(),
        })
    };
    events::server_interaction(pb::interaction_update::Message::ToolCallDelta(Box::new(
        pb::ToolCallDeltaUpdate {
            call_id: call.call_id.clone(),
            tool_call_delta: Some(Box::new(pb::ToolCallDelta {
                delta: Some(pb::tool_call_delta::Delta::ShellToolCallDelta(
                    pb::ShellToolCallDelta { delta: Some(delta) },
                )),
            })),
            model_call_id: call.model_call_id.clone(),
        },
    )))
}
