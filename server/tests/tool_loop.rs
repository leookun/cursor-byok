#[path = "support/fake_provider.rs"]
mod fake_provider;
#[path = "support/fixtures.rs"]
mod fixtures;

use std::{
    collections::{BTreeMap, HashSet},
    sync::Arc,
};

use cursor_server::{
    cursor::prompting::{PromptAssets, PromptCompiler},
    cursor::{
        connect,
        proto::agent::v1 as pb,
        tools::{
            codec,
            runtime::{CursorToolRuntime, ExecContext},
            ClientToolEvent, ToolBatchState, ToolDispatcher,
        },
    },
    cursor::{CursorCommand, CursorSessionRegistry},
    model::{
        MessageContent, ModelConfigInput, ModelType, ProjectedContent, ToolCall,
        OPENAI_CHAT_ENDPOINT,
    },
    provider::{FinishReason, ModelEvent},
};
use prost::Message;
use serde_json::json;

fn call(id: &str, name: &str) -> ToolCall {
    ToolCall {
        index: 0,
        call_id: id.into(),
        model_call_id: "model:0".into(),
        name: name.into(),
        arguments_text: "{}".into(),
        arguments: json!({}),
    }
}

fn exec_context() -> ExecContext {
    ExecContext {
        conversation_id: "conversation".into(),
        root_conversation_id: "conversation".into(),
        default_subagent_model: "model".into(),
        subagent_model: None,
        terminals_folder: "/tmp/terminals".into(),
        admin_command_denylist: Vec::new(),
        allow_subagents: true,
        subagents_disabled: false,
        mcp_routes: std::collections::HashMap::new(),
    }
}

fn mcp_context(server: &str, provider: &str, tool: &str) -> ExecContext {
    let mut context = exec_context();
    context.mcp_routes.insert(
        (server.into(), tool.into()),
        cursor_server::cursor::tools::runtime::McpRoute {
            name: format!("{server}-{tool}"),
            provider_identifier: provider.into(),
            tool_name: tool.into(),
            description: "fixture MCP tool".into(),
        },
    );
    context
}

#[test]
fn dynamic_mcp_call_routes_to_the_captured_exec_message() {
    let call = ToolCall {
        index: 0,
        call_id: "mcp-call".into(),
        model_call_id: "model:0".into(),
        name: "mcp_repo_lookup".into(),
        arguments_text: "{\"query\":\"x\"}".into(),
        arguments: json!({"query": "x"}),
    };
    let definition = pb::McpToolDefinition {
        name: "mcp_repo_lookup".into(),
        provider_identifier: "repo".into(),
        tool_name: "lookup".into(),
        description: "lookup".into(),
        input_schema: None,
        input_schema_json: None,
    };
    let message = codec::mcp_request(7, &call, &definition).unwrap();
    let Some(pb::agent_server_message::Message::ExecServerMessage(exec)) = message.message else {
        panic!("expected ExecServerMessage")
    };
    let Some(pb::exec_server_message::Message::McpArgs(args)) = exec.message else {
        panic!("expected McpArgs")
    };
    assert_eq!(exec.exec_id, "mcp-call");
    assert_eq!(args.provider_identifier, "repo");
    assert_eq!(args.tool_name, "lookup");
    assert_eq!(
        args.args["query"].kind,
        Some(prost_types::value::Kind::StringValue("x".into()))
    );
}

#[tokio::test]
async fn dynamic_mcp_uses_one_definition_for_stream_ui_exec_and_result() {
    let definition = pb::McpToolDefinition {
        name: "cursor-ide-browser-browser_navigate".into(),
        provider_identifier: "cursor-ide-browser".into(),
        tool_name: "browser_navigate".into(),
        description: "Navigate the browser".into(),
        ..Default::default()
    };
    let definitions = BTreeMap::from([(definition.name.clone(), definition.clone())]);
    let event = cursor_server::provider::ModelEvent::ToolCallStart {
        index: 0,
        call_id: "browser-call".into(),
        name: definition.name.clone(),
    };
    let partial =
        cursor_server::cursor::interaction::response_event(&event, "model:0", &definitions)
            .unwrap()
            .unwrap();
    let Some(pb::agent_server_message::Message::InteractionUpdate(update)) = partial.message else {
        panic!("expected interaction update")
    };
    let Some(pb::interaction_update::Message::PartialToolCall(partial)) = update.message else {
        panic!("expected partial tool call")
    };
    let partial = partial.tool_call.unwrap();
    assert_eq!(partial.started_at_ms, None);
    let Some(pb::tool_call::Tool::McpToolCall(tool)) = partial.tool else {
        panic!("expected MCP placeholder")
    };
    assert_eq!(tool.args.unwrap().tool_name, "browser_navigate");

    let runtime = CursorToolRuntime::default();
    let dispatcher = ToolDispatcher::new(runtime.clone());
    let mut invocation = call("browser-call", &definition.name);
    invocation.arguments = json!({"url": "https://example.com"});
    let dispatched = dispatcher
        .start_batch(
            &[invocation],
            ToolBatchState {
                completed: &HashSet::new(),
                started: &HashSet::new(),
                response_text: "",
                response_thinking: "",
            },
            &[],
            &definitions,
            &exec_context(),
        )
        .await
        .unwrap();
    assert_eq!(dispatched[0].messages.len(), 2);
    let Some(pb::agent_server_message::Message::InteractionUpdate(update)) =
        dispatched[0].messages[0].message.as_ref()
    else {
        panic!("expected tool-start interaction")
    };
    let Some(pb::interaction_update::Message::ToolCallStarted(started)) = update.message.as_ref()
    else {
        panic!("expected tool-start message")
    };
    assert!(started.tool_call.as_ref().unwrap().started_at_ms.is_some());
    let Some(pb::agent_server_message::Message::ExecServerMessage(exec)) =
        dispatched[0].messages[1].message.as_ref()
    else {
        panic!("expected MCP Exec")
    };
    let event = codec::client_event(
        &pb::ExecClientMessage {
            id: exec.id,
            message: Some(pb::exec_client_message::Message::McpResult(pb::McpResult {
                result: Some(pb::mcp_result::Result::Success(pb::McpSuccess {
                    content: vec![pb::McpToolResultContentItem {
                        content: Some(pb::mcp_tool_result_content_item::Content::Text(
                            pb::McpTextContent {
                                text: "navigated".into(),
                                output_location: None,
                            },
                        )),
                    }],
                    is_error: false,
                    structured_content: None,
                })),
            })),
            ..Default::default()
        },
        &runtime,
    )
    .await
    .unwrap();
    let codec::ClientExecEvent::Completed(completion) = event else {
        panic!("expected completed MCP result")
    };
    let Some(pb::tool_call::Tool::McpToolCall(tool)) = &completion.tool_call().tool else {
        panic!("expected rendered MCP result")
    };
    assert_eq!(tool.args.as_ref().unwrap().name, definition.name);
    assert!(tool.result.is_some());
}

#[tokio::test]
async fn call_mcp_tool_uses_the_request_descriptor_and_returns_client_errors_to_the_model() {
    let runtime = CursorToolRuntime::default();
    let dispatcher = ToolDispatcher::new(runtime.clone());
    let completed = HashSet::new();
    let started = HashSet::new();
    let mut invocation = call("call-mcp", "CallMcpTool");
    invocation.arguments = json!({
        "server": "plugin-browser-use-browser-use",
        "toolName": "browser_exec",
        "description": "run browser code",
        "arguments": {"code": "print('ok')"}
    });
    let requests = dispatcher
        .start_batch(
            &[invocation],
            ToolBatchState {
                completed: &completed,
                started: &started,
                response_text: "",
                response_thinking: "",
            },
            &[],
            &BTreeMap::new(),
            &mcp_context(
                "plugin-browser-use-browser-use",
                "browser-use",
                "browser_exec",
            ),
        )
        .await
        .unwrap();
    let Some(pb::agent_server_message::Message::ExecServerMessage(exec)) =
        requests[0].messages[1].message.as_ref()
    else {
        panic!("expected MCP Exec")
    };
    let Some(pb::exec_server_message::Message::McpArgs(args)) = exec.message.as_ref() else {
        panic!("expected McpArgs")
    };
    assert_eq!(args.name, "plugin-browser-use-browser-use-browser_exec");
    assert_eq!(args.provider_identifier, "browser-use");
    assert_eq!(args.tool_name, "browser_exec");
    assert_eq!(args.server_identifier, "plugin-browser-use-browser-use");
    assert_eq!(
        args.args["code"].kind,
        Some(prost_types::value::Kind::StringValue("print('ok')".into()))
    );

    let event = codec::client_event(
        &pb::ExecClientMessage {
            id: exec.id,
            message: Some(pb::exec_client_message::Message::McpResult(pb::McpResult {
                result: Some(pb::mcp_result::Result::Error(pb::McpError {
                    error: "invalid browser arguments".into(),
                })),
            })),
            ..Default::default()
        },
        &runtime,
    )
    .await
    .unwrap();
    let codec::ClientExecEvent::Completed(completion) = event else {
        panic!("expected MCP completion")
    };
    assert_eq!(completion.result().content, "invalid browser arguments");
    assert!(completion.result().is_error);
}

#[tokio::test]
async fn mcp_auth_uses_the_cursor_auth_interaction_without_a_tool_definition() {
    let runtime = CursorToolRuntime::default();
    let dispatcher = ToolDispatcher::new(runtime.clone());
    let completed = HashSet::new();
    let started = HashSet::new();
    let mut auth = call("auth-gmail", "CallMcpTool");
    auth.arguments = json!({
        "server": "plugin-gmail-gmail",
        "toolName": "mcp_auth",
        "arguments": {}
    });
    let request = dispatcher
        .start_batch(
            &[auth],
            ToolBatchState {
                completed: &completed,
                started: &started,
                response_text: "",
                response_thinking: "",
            },
            &[],
            &BTreeMap::new(),
            &exec_context(),
        )
        .await
        .unwrap();
    let Some(pb::agent_server_message::Message::InteractionQuery(query)) =
        request[0].messages[1].message.as_ref()
    else {
        panic!("expected MCP auth interaction")
    };
    let Some(pb::interaction_query::Query::McpAuthRequestQuery(auth)) = query.query.as_ref() else {
        panic!("expected MCP auth query")
    };
    let args = auth.args.as_ref().unwrap();
    assert_eq!(args.server_identifier, "plugin-gmail-gmail");
    assert_eq!(args.tool_call_id, "auth-gmail");

    let event = dispatcher
        .interaction_response(&pb::InteractionResponse {
            id: query.id,
            result: Some(pb::interaction_response::Result::McpAuthRequestResponse(
                pb::McpAuthRequestResponse {
                    result: Some(pb::mcp_auth_request_response::Result::Approved(
                        pb::mcp_auth_request_response::Approved {},
                    )),
                },
            )),
        })
        .await
        .unwrap();
    let ClientToolEvent::Completed(completion) = event else {
        panic!("expected MCP auth completion")
    };
    let Some(pb::tool_call::Tool::McpAuthToolCall(auth)) = &completion.tool_call().tool else {
        panic!("expected MCP auth tool call")
    };
    assert!(matches!(
        auth.result.as_ref().and_then(|result| result.result.as_ref()),
        Some(pb::mcp_auth_result::Result::Success(success))
            if success.server_identifier == "plugin-gmail-gmail"
    ));
}

#[tokio::test]
async fn unknown_mcp_descriptor_returns_a_tool_error_without_client_discovery() {
    let dispatcher = ToolDispatcher::new(CursorToolRuntime::default());
    let completed = HashSet::new();
    let started = HashSet::new();
    let mut invocation = call("call-fast-context", "CallMcpTool");
    invocation.arguments = json!({
        "server": "fast-context",
        "toolName": "fast_context_search",
        "arguments": {"query": "MCP dispatch"}
    });
    let dispatched = dispatcher
        .start_batch(
            &[invocation],
            ToolBatchState {
                completed: &completed,
                started: &started,
                response_text: "",
                response_thinking: "",
            },
            &[],
            &BTreeMap::new(),
            &exec_context(),
        )
        .await
        .unwrap();
    assert_eq!(dispatched[0].messages.len(), 1);
    let completion = dispatched[0]
        .completion
        .as_ref()
        .expect("missing descriptor should complete as a tool error");
    assert!(completion.result().is_error);
    assert!(completion.result().content.contains("descriptor not found"));
}

#[tokio::test]
async fn shell_uses_background_timeout_and_preserves_stream_identity() {
    let mut shell = call("call-shell", "Shell");
    shell.arguments = json!({
        "command": "python3 -m http.server 8000",
        "working_directory": "/tmp/project",
        "block_until_ms": 3000,
        "description": "Start HTTP server"
    });
    let context = exec_context();
    let request = codec::request(7, &shell, &context).unwrap();
    let Some(pb::agent_server_message::Message::ExecServerMessage(request)) = request.message
    else {
        panic!("expected ExecServerMessage")
    };
    assert_eq!(request.accept_hook_additional_contexts, Some(true));
    let Some(pb::exec_server_message::Message::ShellStreamArgs(args)) = request.message else {
        panic!("expected ShellArgs")
    };
    assert_eq!(args.timeout, 3000);
    assert_eq!(
        args.timeout_behavior,
        pb::TimeoutBehavior::Background as i32
    );
    assert_eq!(args.hard_timeout, Some(86_400_000));
    assert_eq!(args.description.as_deref(), Some("Start HTTP server"));
    assert!(args.close_stdin);
    assert_eq!(args.conversation_id.as_deref(), Some("conversation"));
    assert_eq!(args.file_output_threshold_bytes, Some(40_000));
    assert_eq!(args.simple_commands, ["python3 -m http.server 8000"]);
    let parsing = args.parsing_result.as_ref().unwrap();
    assert!(!parsing.parsing_failed);
    assert_eq!(parsing.executable_commands.len(), 1);
    let executable = &parsing.executable_commands[0];
    assert_eq!(executable.name, "python3");
    assert_eq!(executable.full_text, "python3 -m http.server 8000");
    assert_eq!(
        executable
            .args
            .iter()
            .map(|argument| (argument.r#type.as_str(), argument.value.as_str()))
            .collect::<Vec<_>>(),
        [("word", "-m"), ("word", "http.server"), ("word", "8000")]
    );

    let rendered = cursor_server::cursor::interaction::render_tool_call(&shell, false).unwrap();
    let Some(pb::tool_call::Tool::ShellToolCall(rendered)) = rendered.tool else {
        panic!("expected rendered ShellToolCall")
    };
    assert_eq!(rendered.description.as_deref(), Some("Start HTTP server"));
    assert_eq!(
        rendered.args.and_then(|args| args.description),
        Some("Start HTTP server".into())
    );

    let pending = CursorToolRuntime::default();
    let id = pending.reserve_exec(&shell, &context).await.unwrap();
    let delta = codec::client_event(
        &pb::ExecClientMessage {
            id,
            message: Some(pb::exec_client_message::Message::ShellStream(
                pb::ShellStream {
                    event: Some(pb::shell_stream::Event::Stdout(pb::ShellStreamStdout {
                        data: "Serving HTTP on port 8000\n".into(),
                    })),
                },
            )),
            ..Default::default()
        },
        &pending,
    )
    .await
    .unwrap();
    let codec::ClientExecEvent::Delta(delta) = delta else {
        panic!("expected Shell stdout delta")
    };
    let Some(pb::agent_server_message::Message::InteractionUpdate(delta)) = delta.message else {
        panic!("expected InteractionUpdate")
    };
    let Some(pb::interaction_update::Message::ToolCallDelta(delta)) = delta.message else {
        panic!("expected ToolCallDelta")
    };
    assert_eq!(delta.call_id, "call-shell");
    assert_eq!(delta.model_call_id, "model:0");
    let Some(pb::tool_call_delta::Delta::ShellToolCallDelta(shell_delta)) =
        delta.tool_call_delta.and_then(|delta| delta.delta)
    else {
        panic!("expected ShellToolCallDelta")
    };
    let Some(pb::shell_tool_call_delta::Delta::Stdout(stdout)) = shell_delta.delta else {
        panic!("expected stdout")
    };
    assert_eq!(stdout.content, "Serving HTTP on port 8000\n");

    let completion = codec::client_event(
        &pb::ExecClientMessage {
            id,
            message: Some(pb::exec_client_message::Message::ShellStream(
                pb::ShellStream {
                    event: Some(pb::shell_stream::Event::Backgrounded(
                        pb::ShellStreamBackgrounded {
                            shell_id: 42,
                            command: "python3 -m http.server 8000".into(),
                            working_directory: "/tmp/project".into(),
                            pid: Some(1234),
                            ms_to_wait: Some(3000),
                            reason: Some(pb::ShellBackgroundReason::Timeout as i32),
                        },
                    )),
                },
            )),
            ..Default::default()
        },
        &pending,
    )
    .await
    .unwrap();
    let codec::ClientExecEvent::Completed(completion) = completion else {
        panic!("expected background completion")
    };
    assert_eq!(
        completion.result().content,
        (
            "shell running in background shell_id=42 pid=1234 terminals_folder=/tmp/terminals\nServing HTTP on port 8000\n"
        )
    );
    let Some(pb::tool_call::Tool::ShellToolCall(tool)) = &completion.tool_call().tool else {
        panic!("expected ShellToolCall")
    };
    let result = tool.result.as_ref().expect("background ShellResult");
    assert_eq!(result.is_background, Some(true));
    assert_eq!(result.terminals_folder.as_deref(), Some("/tmp/terminals"));
    assert_eq!(result.pid, Some(1234));
    assert!(
        pending.drain_running().await.is_empty(),
        "a backgrounded Shell is no longer an abortable Run Exec"
    );
}

#[tokio::test]
async fn exec_ids_are_monotonic_and_released_ids_are_not_reused() {
    let pending = CursorToolRuntime::default();
    let first = pending
        .reserve_exec(&call("call-1", "Read"), &exec_context())
        .await
        .unwrap();
    assert_eq!(first, 1);
    assert_eq!(
        pending.exec_call(first).await.map(|call| call.call_id),
        Some("call-1".into())
    );
    pending.discard_exec(first).await;
    assert!(pending.exec_call(first).await.is_none());

    let second = pending
        .reserve_exec(&call("call-2", "Read"), &exec_context())
        .await
        .unwrap();
    assert_eq!(second, 2, "released Exec ids must not be reused in one Run");

    let interaction = pending
        .reserve_interaction(&call("call-3", "AskQuestion"))
        .await
        .unwrap();
    assert_eq!(
        interaction, 3,
        "Exec and Interaction share one wire-id space"
    );
}

#[tokio::test]
async fn empty_exec_client_message_is_not_a_terminal_result() {
    let pending = CursorToolRuntime::default();
    let id = pending
        .reserve_exec(&call("call-1", "Read"), &exec_context())
        .await
        .unwrap();
    let event = codec::client_event(
        &pb::ExecClientMessage {
            id,
            message: None,
            ..Default::default()
        },
        &pending,
    )
    .await
    .unwrap();
    assert!(matches!(event, codec::ClientExecEvent::Pending));
    assert_eq!(
        pending.exec_call(id).await.map(|call| call.call_id),
        Some("call-1".into())
    );
}

#[tokio::test]
async fn tool_success_is_not_inferred_from_debug_text() {
    let pending = CursorToolRuntime::default();
    let mut write = call("call-1", "Write");
    write.arguments = json!({"path": "/tmp/a", "contents": "x"});
    let id = pending.reserve_exec(&write, &exec_context()).await.unwrap();
    let event = codec::client_event(
        &pb::ExecClientMessage {
            id,
            message: Some(pb::exec_client_message::Message::WriteResult(
                pb::WriteResult {
                    result: Some(pb::write_result::Result::Success(pb::WriteSuccess {
                        path: "/tmp/a".into(),
                        file_content_after_write: Some("enum Error { Example }".into()),
                        ..Default::default()
                    })),
                },
            )),
            ..Default::default()
        },
        &pending,
    )
    .await
    .unwrap();
    let codec::ClientExecEvent::Completed(completion) = event else {
        panic!("expected terminal write result")
    };
    assert!(!completion.result().is_error);
    assert!(matches!(
        completion.tool_call().tool,
        Some(pb::tool_call::Tool::EditToolCall(_))
    ));
}

#[tokio::test]
async fn new_task_result_exposes_the_subagent_name_and_id_to_the_model() {
    let pending = CursorToolRuntime::default();
    let mut task = call("call-task", "Task");
    task.arguments = json!({
        "description": "Analyze game logic",
        "prompt": "Inspect the game",
        "run_in_background": true,
        "subagent_type": "generalPurpose"
    });
    let id = pending.reserve_exec(&task, &exec_context()).await.unwrap();
    let event = codec::client_event(
        &pb::ExecClientMessage {
            id,
            message: Some(pb::exec_client_message::Message::SubagentResult(
                pb::SubagentResult {
                    result: Some(pb::subagent_result::Result::Success(pb::SubagentSuccess {
                        agent_id: "child-id".into(),
                        ..Default::default()
                    })),
                },
            )),
            ..Default::default()
        },
        &pending,
    )
    .await
    .unwrap();
    let codec::ClientExecEvent::Completed(completion) = event else {
        panic!("expected terminal Task result")
    };

    assert_eq!(
        completion.result().content,
        "Subagent name: Analyze game logic\nSubagent ID: child-id"
    );
    let Some(pb::tool_call::Tool::TaskToolCall(tool)) = &completion.tool_call().tool else {
        panic!("expected TaskToolCall")
    };
    let Some(pb::task_result::Result::Success(success)) = tool
        .result
        .as_ref()
        .and_then(|result| result.result.as_ref())
    else {
        panic!("expected typed Task success")
    };
    assert_eq!(success.agent_id.as_deref(), Some("child-id"));
}

#[tokio::test]
async fn an_exec_result_must_match_the_reserved_tool() {
    let pending = CursorToolRuntime::default();
    let id = pending
        .reserve_exec(&call("call-1", "Read"), &exec_context())
        .await
        .unwrap();
    let result = codec::client_event(
        &pb::ExecClientMessage {
            id,
            message: Some(pb::exec_client_message::Message::WriteResult(
                pb::WriteResult {
                    result: Some(pb::write_result::Result::Success(pb::WriteSuccess {
                        path: "/tmp/a".into(),
                        ..Default::default()
                    })),
                },
            )),
            ..Default::default()
        },
        &pending,
    )
    .await;
    let Err(error) = result else {
        panic!("mismatched result must fail")
    };
    assert!(error
        .to_string()
        .contains("unexpected Exec result for tool Read"));
    assert!(pending.exec_call(id).await.is_none());
    assert_eq!(pending.completed_call(id).await.as_deref(), Some("call-1"));
    let duplicate = codec::client_event(
        &pb::ExecClientMessage {
            id,
            message: None,
            ..Default::default()
        },
        &pending,
    )
    .await;
    let Err(duplicate) = duplicate else {
        panic!("duplicate terminal result must fail")
    };
    assert!(duplicate.to_string().contains("duplicate terminal"));
}

#[tokio::test]
async fn unknown_exec_id_is_a_protocol_error() {
    let result = codec::client_event(
        &pb::ExecClientMessage {
            id: 999,
            message: Some(pb::exec_client_message::Message::ReadResult(
                pb::ReadResult::default(),
            )),
            ..Default::default()
        },
        &CursorToolRuntime::default(),
    )
    .await;
    let Err(error) = result else {
        panic!("unknown Exec id must fail")
    };
    assert!(matches!(
        error,
        cursor_server::Error::Protocol(message)
            if message == "unknown ExecClientMessage id: 999"
    ));
}

#[tokio::test]
async fn await_shell_consumes_the_background_output_file_terminal_state() {
    let runtime = CursorToolRuntime::default();
    let dispatcher = ToolDispatcher::new(runtime.clone());
    let mut await_call = call("await-call", "AwaitShell");
    await_call.arguments = json!({
        "shell_id": "42",
        "block_until_ms": 1000,
        "pattern": "ready",
    });
    await_call.arguments_text = await_call.arguments.to_string();
    let completed = HashSet::new();
    let started = HashSet::new();
    let dispatched = dispatcher
        .start_batch(
            &[await_call],
            ToolBatchState {
                completed: &completed,
                started: &started,
                response_text: "",
                response_thinking: "",
            },
            &[],
            &BTreeMap::new(),
            &exec_context(),
        )
        .await
        .unwrap();
    let exec = dispatched[0]
        .messages
        .iter()
        .find_map(|message| match message.message.as_ref() {
            Some(pb::agent_server_message::Message::ExecServerMessage(exec)) => Some(exec),
            _ => None,
        })
        .unwrap();
    let Some(pb::exec_server_message::Message::ReadArgs(read)) = exec.message.as_ref() else {
        panic!("expected AwaitShell ReadArgs")
    };
    assert_eq!(read.path, "/tmp/terminals/42.txt");

    let event = codec::client_event(
        &pb::ExecClientMessage {
            id: exec.id,
            message: Some(pb::exec_client_message::Message::ReadResult(
                pb::ReadResult {
                    result: Some(pb::read_result::Result::Success(pb::ReadSuccess {
                        output: Some(pb::read_success::Output::Content(
                            "server ready\nexit_code: 0\n".into(),
                        )),
                        ..Default::default()
                    })),
                },
            )),
            ..Default::default()
        },
        &runtime,
    )
    .await
    .unwrap();
    let codec::ClientExecEvent::Completed(completion) = event else {
        panic!("expected completed AwaitShell")
    };
    assert_eq!(completion.result().call_id, "await-call");
    assert!(!completion.result().is_error);
    let Some(pb::tool_call::Tool::AwaitToolCall(tool)) = completion.tool_call().tool.as_ref()
    else {
        panic!("expected AwaitToolCall")
    };
    let pb::await_result::Result::Success(success) =
        tool.result.as_ref().unwrap().result.as_ref().unwrap()
    else {
        panic!("expected Await success")
    };
    let pb::await_success::AwaitResult::Complete(complete) = success.await_result.as_ref().unwrap()
    else {
        panic!("expected completed background task")
    };
    assert_eq!(complete.task_id, "42");
    assert_eq!(complete.exit_code, Some(0));
    assert_eq!(complete.regex_match.as_deref(), Some("ready"));
}

#[tokio::test]
async fn provider_tool_use_waits_for_client_result_then_calls_provider_again() {
    let (directory, store) = fixtures::temp_store().await;
    let provider = fake_provider::FakeProvider::default();
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "ignored".into(),
        },
        ModelEvent::ToolCallStart {
            index: 0,
            call_id: "call-1".into(),
            name: "Read".into(),
        },
        ModelEvent::ToolCallArgumentsDelta {
            index: 0,
            delta: "{\"path\":\"/tmp/a\"}".into(),
        },
        ModelEvent::ToolCallEnd { index: 0 },
        ModelEvent::Done(FinishReason::ToolUse),
    ]);
    provider.push(vec![
        ModelEvent::Start {
            model_call_id: "ignored".into(),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta("done".into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]);
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = CursorSessionRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
        Default::default(),
    );
    let handle = registry.get_or_create("tool-request").await.unwrap();
    let mut output = handle.subscribe();
    handle
        .command(CursorCommand::Append {
            seqno: 0,
            message: Box::new(client_run()),
        })
        .await
        .unwrap();
    let mut seqno = 1;
    let mut saw_exec = false;
    let mut saw_typed_completion = false;
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .unwrap();
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(
                serde_json::from_slice::<serde_json::Value>(&payload).unwrap(),
                json!({})
            );
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                handle
                    .command(CursorCommand::Append {
                        seqno,
                        message: Box::new(kv_ack(kv.id)),
                    })
                    .await
                    .unwrap();
                seqno += 1;
            }
            Some(pb::agent_server_message::Message::ExecServerMessage(exec)) => {
                saw_exec = true;
                let exec_id = exec.id;
                handle
                    .command(CursorCommand::Append {
                        seqno,
                        message: Box::new(pb::AgentClientMessage {
                            message: Some(pb::agent_client_message::Message::ExecClientMessage(
                                pb::ExecClientMessage {
                                    id: exec_id,
                                    exec_id: String::new(),
                                    message: Some(pb::exec_client_message::Message::ReadResult(
                                        pb::ReadResult {
                                            result: Some(pb::read_result::Result::Success(
                                                pb::ReadSuccess {
                                                    path: "/tmp/a".into(),
                                                    total_lines: 1,
                                                    file_size: 1,
                                                    output: Some(
                                                        pb::read_success::Output::Content(
                                                            "x".into(),
                                                        ),
                                                    ),
                                                    ..Default::default()
                                                },
                                            )),
                                        },
                                    )),
                                    ..Default::default()
                                },
                            )),
                        }),
                    })
                    .await
                    .unwrap();
                seqno += 1;
                handle
                    .command(CursorCommand::Append {
                        seqno,
                        message: Box::new(pb::AgentClientMessage {
                            message: Some(
                                pb::agent_client_message::Message::ExecClientControlMessage(
                                    pb::ExecClientControlMessage {
                                        message: Some(
                                            pb::exec_client_control_message::Message::StreamClose(
                                                pb::ExecClientStreamClose { id: exec_id },
                                            ),
                                        ),
                                    },
                                ),
                            ),
                        }),
                    })
                    .await
                    .unwrap();
                seqno += 1;
            }
            Some(pb::agent_server_message::Message::InteractionUpdate(update)) => {
                if let Some(pb::interaction_update::Message::ToolCallCompleted(completed)) =
                    update.message
                {
                    let tool_call = completed.tool_call.expect("completed ToolCall");
                    assert!(tool_call.started_at_ms.unwrap_or_default() > 1);
                    assert!(tool_call.completed_at_ms.unwrap_or_default() > 1);
                    assert!(tool_call.completed_at_ms >= tool_call.started_at_ms);
                    let Some(pb::tool_call::Tool::ReadToolCall(read)) = tool_call.tool else {
                        panic!("expected completed ReadToolCall")
                    };
                    let result = read.result.expect("typed ReadToolResult");
                    assert!(matches!(
                        result.result,
                        Some(pb::read_tool_result::Result::Success(_))
                    ));
                    saw_typed_completion = true;
                }
            }
            _ => {}
        }
    }
    assert!(saw_exec);
    assert!(saw_typed_completion);
    assert_eq!(provider.requests().len(), 2);
    let database = sqlx::SqlitePool::connect(&format!(
        "sqlite://{}",
        directory.path().join("test.db").display()
    ))
    .await
    .unwrap();
    let provider_call_index: i64 =
        sqlx::query_scalar("SELECT provider_call_index FROM runs WHERE cursor_request_id = ?")
            .bind("tool-request")
            .fetch_one(&database)
            .await
            .unwrap();
    assert_eq!(provider_call_index, 1);
    let messages = store
        .load_current_messages(&cursor_server::model::ConversationId::new(
            "tool-conversation",
        ))
        .await
        .unwrap();
    let result_position = messages
        .iter()
        .position(|message| matches!(message.content, MessageContent::ToolResult(_)))
        .expect("tool result persisted");
    let MessageContent::Assistant { tool_calls, .. } = &messages[result_position - 1].content
    else {
        panic!("tool result must immediately follow its assistant tool call")
    };
    assert_eq!(tool_calls.len(), 1);
    assert_eq!(tool_calls[0].call_id, "call-1");
}

#[tokio::test]
async fn one_run_can_auto_compact_again_after_more_tool_output() {
    let (directory, store) = fixtures::temp_store().await;
    let model = store
        .create_model(&ModelConfigInput {
            sort_order: 0,
            display_name: "Repeated compaction".into(),
            model_type: ModelType::OpenAi,
            base_url: "https://example.com/v1/chat/completions".into(),
            use_full_url: true,
            api_key: "test-key".into(),
            tooltip_data: "Repeated compaction".into(),
            model_id: "repeated-compaction-model".into(),
            reasoning_effort: None,
            openai_endpoint: OPENAI_CHAT_ENDPOINT.into(),
            openai_extra_params_enabled: false,
            openai_extra_params: json!({}),
            custom_headers_enabled: false,
            custom_headers: json!({}),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: json!({}),
            context_window_tokens: Some(40_000),
            max_completion_tokens: None,
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
        })
        .await
        .unwrap();
    let provider = fake_provider::FakeProvider::default();
    provider.push(tool_call_response("call-1"));
    provider.push(text_events("first summary"));
    provider.push(tool_call_response("call-2"));
    provider.push(text_events("second summary"));
    provider.push(text_events("done"));
    let assets = PromptAssets::load(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("prompt/cursor")
            .as_path(),
    )
    .unwrap();
    let registry = CursorSessionRegistry::new(
        store.clone(),
        Arc::new(provider.clone()),
        PromptCompiler::new(assets),
        Default::default(),
    );
    let handle = registry
        .get_or_create("repeated-compaction-request")
        .await
        .unwrap();
    let mut output = handle.subscribe();
    handle
        .command(CursorCommand::Append {
            seqno: 0,
            message: Box::new(client_run_for_model(
                "repeated-compaction-conversation",
                "repeated-compaction-request",
                &model.model_hash,
                Some("40k"),
            )),
        })
        .await
        .unwrap();
    let mut seqno = 1;
    let oversized = format!("HEAD{}TAIL", "x".repeat(4 * 1024 * 1024));
    loop {
        let frame = tokio::time::timeout(std::time::Duration::from_secs(5), output.recv())
            .await
            .unwrap()
            .unwrap();
        let (flags, payload) = connect::decode_frames(&frame).unwrap().pop().unwrap();
        if flags & connect::END_STREAM_FLAG != 0 {
            assert_eq!(
                serde_json::from_slice::<serde_json::Value>(&payload).unwrap(),
                json!({})
            );
            break;
        }
        let server = pb::AgentServerMessage::decode(payload).unwrap();
        match server.message {
            Some(pb::agent_server_message::Message::KvServerMessage(kv)) => {
                handle
                    .command(CursorCommand::Append {
                        seqno,
                        message: Box::new(kv_ack(kv.id)),
                    })
                    .await
                    .unwrap();
                seqno += 1;
            }
            Some(pb::agent_server_message::Message::ExecServerMessage(exec)) => {
                let exec_id = exec.id;
                handle
                    .command(CursorCommand::Append {
                        seqno,
                        message: Box::new(pb::AgentClientMessage {
                            message: Some(pb::agent_client_message::Message::ExecClientMessage(
                                pb::ExecClientMessage {
                                    id: exec_id,
                                    message: Some(pb::exec_client_message::Message::ReadResult(
                                        pb::ReadResult {
                                            result: Some(pb::read_result::Result::Success(
                                                pb::ReadSuccess {
                                                    path: "/tmp/large.txt".into(),
                                                    total_lines: 1,
                                                    file_size: oversized.len() as i64,
                                                    output: Some(
                                                        pb::read_success::Output::Content(
                                                            oversized.clone(),
                                                        ),
                                                    ),
                                                    ..Default::default()
                                                },
                                            )),
                                        },
                                    )),
                                    ..Default::default()
                                },
                            )),
                        }),
                    })
                    .await
                    .unwrap();
                seqno += 1;
                handle
                    .command(CursorCommand::Append {
                        seqno,
                        message: Box::new(pb::AgentClientMessage {
                            message: Some(
                                pb::agent_client_message::Message::ExecClientControlMessage(
                                    pb::ExecClientControlMessage {
                                        message: Some(
                                            pb::exec_client_control_message::Message::StreamClose(
                                                pb::ExecClientStreamClose { id: exec_id },
                                            ),
                                        ),
                                    },
                                ),
                            ),
                        }),
                    })
                    .await
                    .unwrap();
                seqno += 1;
            }
            _ => {}
        }
    }

    let requests = provider.requests();
    let shapes = requests
        .iter()
        .map(|request| {
            (
                request.model.context_window_tokens,
                request.prompt.tools.len(),
                request.history.len(),
                request
                    .history
                    .iter()
                    .filter_map(|message| match &message.content {
                        ProjectedContent::ToolResult(result) => Some(result.content.len()),
                        _ => None,
                    })
                    .collect::<Vec<_>>(),
            )
        })
        .collect::<Vec<_>>();
    assert_eq!(requests.len(), 5, "provider requests: {shapes:?}");
    assert!(!requests[0].prompt.tools.is_empty());
    assert!(requests[1].prompt.tools.is_empty());
    assert!(!requests[2].prompt.tools.is_empty());
    assert!(requests[3].prompt.tools.is_empty());
    assert!(!requests[4].prompt.tools.is_empty());
    for request in [&requests[1], &requests[3]] {
        let result = request
            .history
            .iter()
            .find_map(|message| match &message.content {
                ProjectedContent::ToolResult(result) => Some(result),
                _ => None,
            })
            .expect("compaction request must contain a projected tool result");
        assert!(result.content.len() <= 64 * 1024);
        assert!(result.content.contains("omitted middle"));
    }
    let database = sqlx::SqlitePool::connect(&format!(
        "sqlite://{}",
        directory.path().join("test.db").display()
    ))
    .await
    .unwrap();
    let full_result_lengths =
        sqlx::query_scalar::<_, i64>("SELECT length(result_content) FROM tool_round_calls")
            .fetch_all(&database)
            .await
            .unwrap();
    assert_eq!(full_result_lengths.len(), 2);
    assert!(full_result_lengths
        .iter()
        .all(|length| *length > 4 * 1024 * 1024));
}

fn tool_call_response(call_id: &str) -> Vec<ModelEvent> {
    vec![
        ModelEvent::Start {
            model_call_id: format!("model-{call_id}"),
        },
        ModelEvent::ToolCallStart {
            index: 0,
            call_id: call_id.into(),
            name: "Read".into(),
        },
        ModelEvent::ToolCallArgumentsDelta {
            index: 0,
            delta: "{\"path\":\"/tmp/large.txt\"}".into(),
        },
        ModelEvent::ToolCallEnd { index: 0 },
        ModelEvent::Done(FinishReason::ToolUse),
    ]
}

fn text_events(text: &str) -> Vec<ModelEvent> {
    vec![
        ModelEvent::Start {
            model_call_id: format!("model-{text}"),
        },
        ModelEvent::TextStart,
        ModelEvent::TextDelta(text.into()),
        ModelEvent::TextEnd,
        ModelEvent::Done(FinishReason::Stop),
    ]
}

fn client_run() -> pb::AgentClientMessage {
    client_run_for_model("tool-conversation", "tool-request", "test-model", None)
}

fn client_run_for_model(
    conversation_id: &str,
    run_id: &str,
    model_id: &str,
    context: Option<&str>,
) -> pb::AgentClientMessage {
    let user = pb::UserMessage {
        text: "read it".into(),
        message_id: "user".into(),
        mode: pb::AgentMode::Agent as i32,
        ..Default::default()
    };
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::RunRequest(
            pb::AgentRunRequest {
                action: Some(pb::ConversationAction {
                    action: Some(pb::conversation_action::Action::UserMessageAction(
                        pb::UserMessageAction {
                            user_message: Some(user),
                            ..Default::default()
                        },
                    )),
                    ..Default::default()
                }),
                conversation_id: Some(conversation_id.into()),
                run_id: Some(run_id.into()),
                requested_model: Some(pb::RequestedModel {
                    model_id: model_id.into(),
                    parameters: context
                        .into_iter()
                        .map(|value| pb::requested_model::ModelParameterValue {
                            id: "context".into(),
                            value: value.into(),
                        })
                        .collect(),
                    ..Default::default()
                }),
                ..Default::default()
            },
        )),
    }
}

fn kv_ack(id: u32) -> pb::AgentClientMessage {
    pb::AgentClientMessage {
        message: Some(pb::agent_client_message::Message::KvClientMessage(
            pb::KvClientMessage {
                id,
                message: Some(pb::kv_client_message::Message::SetBlobResult(
                    pb::SetBlobResult { error: None },
                )),
            },
        )),
    }
}
