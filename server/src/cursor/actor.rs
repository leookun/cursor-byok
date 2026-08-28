use std::sync::Arc;

use tokio::sync::mpsc;

use crate::{
    cursor::prompting::PromptCompiler,
    cursor::{
        blob_sync::BlobSynchronizer,
        checkpoint::CheckpointBuilder,
        context_sync::RequestContextSynchronizer,
        proto::agent::v1 as pb,
        request,
        session::CursorSession,
        tools::{
            codec, result::tool_result_channel, runtime::CursorToolRuntime, ClientToolEvent,
            ToolDispatcher,
        },
    },
    provider::Provider,
    run::{RunActor, RunRegistry},
    store::Store,
};

use super::{inbox::OrderedInbox, lifecycle, CursorCommand, CursorSessionHandle};

pub struct CursorActor;

#[derive(Clone)]
pub(crate) struct RunDependencies {
    pub store: Store,
    pub provider: Arc<dyn Provider>,
    pub compiler: PromptCompiler,
    pub run_registry: RunRegistry,
}

impl CursorActor {
    pub(crate) fn spawn(
        handle: CursorSessionHandle,
        mut receiver: mpsc::Receiver<CursorCommand>,
        dependencies: RunDependencies,
        blob_sync: BlobSynchronizer,
        next_append_seqno: i64,
    ) {
        tokio::spawn(async move {
            let mut inbox = OrderedInbox::starting_at(next_append_seqno);
            let (results_tx, results_rx) = tool_result_channel();
            let (runtime_actions_tx, runtime_actions_rx) = mpsc::unbounded_channel();
            let tool_runtime = CursorToolRuntime::default();
            let context_sync =
                RequestContextSynchronizer::new(handle.clone(), dependencies.store.clone());
            let tools = ToolDispatcher::with_results(
                tool_runtime.clone(),
                results_tx.clone(),
                dependencies.store.clone(),
            );
            let mut run_resources = Some((results_rx, runtime_actions_rx, dependencies));
            loop {
                let command = match receiver.recv().await {
                    Some(command) => command,
                    None => {
                        lifecycle::cancel(&handle).ok();
                        break;
                    }
                };
                match command {
                    CursorCommand::Abort => {
                        handle.mark_conversation_cancelled();
                        lifecycle::cancel(&handle).ok();
                    }
                    CursorCommand::Finished => {
                        break;
                    }
                    CursorCommand::Append { seqno, message } => {
                        for (_seqno, message) in inbox.push(seqno, *message) {
                            {
                                match message.message {
                                    Some(pb::agent_client_message::Message::RunRequest(
                                        request,
                                    )) => {
                                        if let Some(conversation_id) =
                                            request.conversation_id.as_deref()
                                        {
                                            if let Err(error) =
                                                handle.set_conversation_id(conversation_id)
                                            {
                                                tracing::error!(
                                                    request_id = handle.request_id(),
                                                    %error,
                                                    "invalid Cursor conversation id"
                                                );
                                                let _ =
                                                    crate::cursor::lifecycle::fail(&handle, &error);
                                                let _ =
                                                    handle.command(CursorCommand::Finished).await;
                                                return;
                                            }
                                        }
                                        if let Some((results, runtime_actions, dependencies)) =
                                            run_resources.take()
                                        {
                                            let handle = handle.clone();
                                            let blob_sync = blob_sync.clone();
                                            let context_sync = context_sync.clone();
                                            let tools = tools.clone();
                                            let tool_runtime = tool_runtime.clone();
                                            tokio::spawn(async move {
                                                let mut checkpoint = CheckpointBuilder::new(
                                                    dependencies.store.clone(),
                                                    blob_sync.clone(),
                                                    handle
                                                        .parent()
                                                        .map(|parent| parent.tool_call_id.clone()),
                                                    request.conversation_state.clone(),
                                                );
                                                let prepared = async {
                                                    let parent = match handle.parent() {
                                                        Some(parent) => {
                                                            let parent_run_id = dependencies
                                                                .store
                                                                .active_run_for_cursor_request(
                                                                    &parent.request_id,
                                                                )
                                                                .await?
                                                                .ok_or_else(|| {
                                                                    crate::Error::Protocol(format!(
                                                                        "Cursor parent request {} has no active local Run",
                                                                        parent.request_id
                                                                    ))
                                                                })?;
                                                            Some((
                                                                parent_run_id,
                                                                parent.tool_call_id.clone(),
                                                            ))
                                                        }
                                                        None => None,
                                                    };
                                                    request::prepare(
                                                        handle.request_id(),
                                                        &request,
                                                        parent,
                                                        request::PrepareDependencies {
                                                            compiler: &dependencies.compiler,
                                                            store: &dependencies.store,
                                                            checkpoint: &checkpoint,
                                                            blob_sync: &blob_sync,
                                                            context_sync: &context_sync,
                                                        },
                                                    )
                                                    .await
                                                }
                                                .await;
                                                let (prepared, context) = match prepared {
                                                    Ok(prepared) => prepared,
                                                    Err(error) => {
                                                        tracing::error!(
                                                            request_id = handle.request_id(),
                                                            %error,
                                                            "failed to prepare Cursor Run"
                                                        );
                                                        let _ = crate::cursor::lifecycle::fail(
                                                            &handle, &error,
                                                        );
                                                        let _ = handle
                                                            .command(CursorCommand::Finished)
                                                            .await;
                                                        return;
                                                    }
                                                };
                                                checkpoint.configure(
                                                    prepared.model.model_id.clone(),
                                                    prepared.model.context_window_tokens,
                                                    context.checkpoint_prompt.instructions.clone(),
                                                    context.checkpoint_prompt.tools.clone(),
                                                    context.dynamic_tools.keys().cloned().collect(),
                                                    context.turn_user.clone(),
                                                );
                                                if context.background_completion
                                                    && dependencies
                                                        .run_registry
                                                        .insert_messages(
                                                            &prepared.conversation_id,
                                                            prepared.initial_messages.clone(),
                                                        )
                                                        .await
                                                {
                                                    crate::cursor::lifecycle::finish_success(
                                                        &handle,
                                                    );
                                                    let _ = handle
                                                        .command(CursorCommand::Finished)
                                                        .await;
                                                    return;
                                                }
                                                let cancellation = handle.cancellation();
                                                let (port, core) = crate::client::session(256);
                                                let core_commands = core.commands.clone();
                                                let actor = RunActor::new(
                                                    dependencies.store.clone(),
                                                    dependencies.provider,
                                                    dependencies.run_registry,
                                                );
                                                let core_run = actor
                                                    .spawn(
                                                        prepared,
                                                        port,
                                                        core_commands,
                                                        cancellation,
                                                    )
                                                    .await;
                                                let session = CursorSession::new(
                                                    handle.clone(),
                                                    dependencies.store,
                                                    context,
                                                    core,
                                                    super::session::CursorSessionRuntime {
                                                        tools,
                                                        results,
                                                        runtime_actions,
                                                        compiler: dependencies.compiler,
                                                        blob_sync,
                                                        checkpoint,
                                                        tool_runtime,
                                                    },
                                                );
                                                if let Err(error) = session.run().await {
                                                    tracing::error!(
                                                        request_id = handle.request_id(),
                                                        %error,
                                                        "Cursor session failed"
                                                    );
                                                    let _ = crate::cursor::lifecycle::fail(
                                                        &handle, &error,
                                                    );
                                                }
                                                let _ = core_run.await;
                                                let _ =
                                                    handle.command(CursorCommand::Finished).await;
                                            });
                                        } else {
                                            let error = crate::Error::Protocol(format!(
                                                "duplicate RunRequest for request_id: {}",
                                                handle.request_id()
                                            ));
                                            tracing::error!(
                                                request_id = handle.request_id(),
                                                %error,
                                                "rejected duplicate Cursor RunRequest"
                                            );
                                            results_tx.send_error(error);
                                        }
                                    }
                                    Some(pb::agent_client_message::Message::ExecClientMessage(
                                        message,
                                    )) => {
                                        if context_sync.handle_client(&message).await {
                                            continue;
                                        }
                                        match codec::client_event(&message, &tool_runtime).await {
                                            Ok(codec::ClientExecEvent::Delta(message)) => {
                                                let _ = handle.emit(&message);
                                            }
                                            Ok(codec::ClientExecEvent::Message(message)) => {
                                                let _ = handle.emit(&message);
                                            }
                                            Ok(codec::ClientExecEvent::Completed(result)) => {
                                                results_tx.send(*result)
                                            }
                                            Ok(codec::ClientExecEvent::Pending) => {}
                                            Err(error) => results_tx.send_error(error),
                                        }
                                    }
                                    Some(
                                        pb::agent_client_message::Message::ExecClientControlMessage(
                                            message,
                                        ),
                                    ) => {
                                        use pb::exec_client_control_message::Message;
                                        match message.message {
                                            Some(Message::StreamClose(close)) => {
                                                if context_sync.handle_stream_close(close.id).await
                                                {
                                                    continue;
                                                }
                                                match codec::stream_closed(close.id, &tool_runtime)
                                                    .await
                                                {
                                                    Ok(Some(completion)) => {
                                                        results_tx.send(completion)
                                                    }
                                                    Ok(None) => {}
                                                    Err(error) => results_tx.send_error(error),
                                                }
                                            }
                                            Some(Message::Throw(throw)) => {
                                                if context_sync
                                                    .handle_throw(
                                                        throw.id,
                                                        format!(
                                                            "Cursor request context failed: {}",
                                                            throw.error
                                                        ),
                                                    )
                                                    .await
                                                {
                                                    continue;
                                                }
                                                if tool_runtime.is_interrupted(throw.id).await {
                                                    tool_runtime.discard_exec(throw.id).await;
                                                    continue;
                                                }
                                                match tool_runtime.take_exec(throw.id).await {
                                                    Some(pending) => results_tx.send_error(
                                                        crate::Error::Protocol(format!(
                                                            "Exec {} failed: {}",
                                                            pending.call.call_id, throw.error
                                                        )),
                                                    ),
                                                    None => results_tx.send_error(
                                                        crate::Error::Protocol(format!(
                                                            "unknown ExecClientThrow id: {}",
                                                            throw.id
                                                        )),
                                                    ),
                                                }
                                            }
                                            Some(Message::Heartbeat(_)) | None => {}
                                        }
                                    }
                                    Some(
                                        pb::agent_client_message::Message::InteractionResponse(
                                            message,
                                        ),
                                    ) => match tools.interaction_response(&message).await {
                                        Ok(ClientToolEvent::Completed(completion)) => {
                                            results_tx.send(*completion)
                                        }
                                        Ok(ClientToolEvent::Pending) => {}
                                        Err(error) => results_tx.send_error(error),
                                    },
                                    Some(pb::agent_client_message::Message::KvClientMessage(
                                        message,
                                    )) => {
                                        let _ = blob_sync.handle_client(message).await;
                                    }
                                    // TODO: ConversationAction has two different delivery paths that
                                    // must not be conflated:
                                    //
                                    // 1. AgentRunRequest.action starts/resumes a Run. request::prepare
                                    //    currently consumes UserMessageAction,
                                    //    BackgroundTaskCompletionAction, SummarizeAction and
                                    //    ExecutePlanAction. ResumeAction is consumed explicitly by
                                    //    request::prepare, including ResumeAction.request_context and a
                                    //    hidden continuation prompt when no pending tool result exists.
                                    // 2. AgentClientMessage::ConversationAction arrives while a Bidi Run
                                    //    is already active and needs a runtime dispatcher here. Supporting
                                    //    an Action in request::prepare does not mean this path supports it.
                                    //
                                    // Cursor 3.16 sends a queued follow-up as InjectContextAction.
                                    // It targets expected_run_id and asks the active Run to yield to the
                                    // queued message. The session owns this path because interruption must
                                    // abort active execs and publish a recoverable checkpoint before the
                                    // old Run ends. It must not be reduced to handle.cancel() here.
                                    //
                                    // The remaining unimplemented Action variants are
                                    // ShellCommandAction, StartPlanAction,
                                    // AsyncAskQuestionCompletionAction, BackgroundShellAction,
                                    // BackgroundSubagentAction,
                                    // SubscriptionNotificationAction and GoalContinuationAction.
                                    // Variants whose wire behavior is not captured yet need evidence
                                    // before assigning semantics. Every unsupported runtime Action must
                                    // return an explicit Protocol Error rather than falling through silently.
                                    Some(
                                        pb::agent_client_message::Message::ConversationAction(
                                            action,
                                        ),
                                    ) => match action.action {
                                        Some(
                                            pb::conversation_action::Action::UserMessageAction(_),
                                        ) => {
                                            handle.mark_conversation_cancelled();
                                            handle.cancel();
                                        }
                                        Some(pb::conversation_action::Action::CancelAction(_)) => {
                                            handle.mark_conversation_cancelled();
                                            handle.cancel();
                                        }
                                        Some(
                                            pb::conversation_action::Action::InjectContextAction(
                                                action,
                                            ),
                                        ) => {
                                            if runtime_actions_tx.send(action).is_err() {
                                                results_tx.send_error(crate::Error::Protocol(
                                                    "InjectContextAction arrived without an active Run"
                                                        .into(),
                                                ));
                                            }
                                        }
                                        Some(
                                            pb::conversation_action::Action::CancelSubagentAction(
                                                action,
                                            ),
                                        ) => {
                                            if let Some(id) = tool_runtime
                                                .running_task_exec_id(&action.subagent_id)
                                                .await
                                            {
                                                let _ = handle.emit(&codec::abort(id));
                                            }
                                        }
                                        Some(action) => {
                                            results_tx.send_error(crate::Error::Protocol(format!(
                                                "unsupported runtime ConversationAction: {}",
                                                runtime_action_name(&action)
                                            )));
                                        }
                                        None => results_tx.send_error(crate::Error::Protocol(
                                            "runtime ConversationAction has no action".into(),
                                        )),
                                    },
                                    _ => {}
                                }
                            }
                        }
                    }
                }
            }
        });
    }
}

fn runtime_action_name(action: &pb::conversation_action::Action) -> &'static str {
    use pb::conversation_action::Action;

    match action {
        Action::UserMessageAction(_) => "UserMessageAction",
        Action::ResumeAction(_) => "ResumeAction",
        Action::CancelAction(_) => "CancelAction",
        Action::SummarizeAction(_) => "SummarizeAction",
        Action::ShellCommandAction(_) => "ShellCommandAction",
        Action::StartPlanAction(_) => "StartPlanAction",
        Action::ExecutePlanAction(_) => "ExecutePlanAction",
        Action::AsyncAskQuestionCompletionAction(_) => "AsyncAskQuestionCompletionAction",
        Action::CancelSubagentAction(_) => "CancelSubagentAction",
        Action::BackgroundTaskCompletionAction(_) => "BackgroundTaskCompletionAction",
        Action::BackgroundShellAction(_) => "BackgroundShellAction",
        Action::BackgroundSubagentAction(_) => "BackgroundSubagentAction",
        Action::SubscriptionNotificationAction(_) => "SubscriptionNotificationAction",
        Action::GoalContinuationAction(_) => "GoalContinuationAction",
        Action::InjectContextAction(_) => "InjectContextAction",
    }
}
