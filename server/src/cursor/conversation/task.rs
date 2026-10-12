//! Owns Task executions across foreground rounds and completion-driven continuations.
use std::{
    collections::{BTreeMap, HashMap},
    sync::Arc,
};

use parking_lot::Mutex;

use crate::{
    cursor::{
        compile,
        tools::tool_call_result::{ToolCompletion, ToolResultSender},
        transport::TransportHandle,
    },
    model::{CanonicalMessage, ToolCall, ToolResult, ToolRoundId},
};

use super::TransportCommand;

#[derive(Clone)]
pub(crate) struct Tasks {
    pub owner: u64,
    state: Arc<Mutex<State>>,
}

#[derive(Default)]
struct State {
    executions: HashMap<u32, Task>,
    deliveries: BTreeMap<String, (CanonicalMessage, bool)>,
    presentations: BTreeMap<String, (ToolCall, ToolCompletion)>,
    cancelled: bool,
}

struct Task {
    round: ToolRoundId,
    call: ToolCall,
    detached: bool,
}

impl Tasks {
    pub fn new(owner: u64) -> Self {
        Self {
            owner,
            state: Arc::default(),
        }
    }

    pub fn register(&self, round: &ToolRoundId, executions: Vec<(u32, ToolCall)>) {
        let mut state = self.state.lock();
        if state.cancelled {
            return;
        }
        for (id, call) in executions {
            state.executions.entry(id).or_insert_with(|| Task {
                round: round.clone(),
                call,
                detached: false,
            });
        }
    }

    pub fn detach(&self, round: &ToolRoundId) -> Vec<ToolResult> {
        let mut state = self.state.lock();
        let mut results = Vec::new();
        for task in state
            .executions
            .values_mut()
            .filter(|task| &task.round == round)
        {
            task.detached = true;
            results.push(ToolResult {
                call_id: task.call.call_id.clone(),
                content: "Task is still running. Its result will be delivered in a later completion notification; do not restart it.".into(),
                is_error: false,
                image: None,
            });
        }
        results.sort_by(|a, b| a.call_id.cmp(&b.call_id));
        results
    }

    /// Routing and channel submission share the detach lock: an already decoded
    /// result cannot slip into a closed foreground receiver after detach drains it.
    pub fn receive(
        &self,
        completion: ToolCompletion,
        sender: &ToolResultSender,
        handle: &TransportHandle,
    ) {
        let mut state = self.state.lock();
        if state.cancelled {
            return;
        }
        if !self.route_detached(&mut state, &completion, handle) {
            sender.send(completion);
        }
    }

    pub fn accept_foreground(&self, completion: &ToolCompletion, handle: &TransportHandle) -> bool {
        let mut state = self.state.lock();
        if state.cancelled {
            return false;
        }
        if self.route_detached(&mut state, completion, handle) {
            return false;
        }
        if let Some(id) = completion.exec_id {
            state.executions.remove(&id);
        }
        true
    }

    fn route_detached(
        &self,
        state: &mut State,
        completion: &ToolCompletion,
        handle: &TransportHandle,
    ) -> bool {
        let Some(id) = completion.exec_id else {
            return false;
        };
        if !state.executions.get(&id).is_some_and(|task| task.detached) {
            return false;
        }
        let task = state.executions.remove(&id).expect("detached Task exists");
        let message = compile::task_completion(&task.round, completion.result());
        state.presentations.insert(
            message
                .runtime_event_id
                .clone()
                .expect("Task event identity"),
            (task.call, completion.clone()),
        );
        state.deliveries.insert(
            message
                .runtime_event_id
                .clone()
                .expect("Task event identity"),
            (message.clone(), false),
        );
        let owner = self.owner;
        let handle = handle.clone();
        tokio::spawn(async move {
            let _ = handle
                .command(TransportCommand::TaskCompleted { owner, message })
                .await;
        });
        true
    }

    pub fn take_presentation(&self, event_id: &str) -> Option<(ToolCall, ToolCompletion)> {
        self.state.lock().presentations.remove(event_id)
    }

    pub fn has_pending(&self) -> bool {
        let state = self.state.lock();
        !state.cancelled && (!state.executions.is_empty() || !state.deliveries.is_empty())
    }

    pub fn delivered(&self, event_id: &str, result: crate::run::CommandResult) -> bool {
        let mut state = self.state.lock();
        if state.cancelled {
            return false;
        }
        if matches!(
            result,
            crate::run::CommandResult::RunClosing | crate::run::CommandResult::RunEnded
        ) {
            if let Some((_, ready)) = state.deliveries.get_mut(event_id) {
                *ready = true;
            }
            true
        } else {
            state.deliveries.remove(event_id);
            false
        }
    }

    pub fn take_ready(&self) -> Vec<CanonicalMessage> {
        let mut state = self.state.lock();
        let ids = state
            .deliveries
            .iter()
            .filter(|(_, (_, ready))| *ready)
            .map(|(id, _)| id.clone())
            .collect::<Vec<_>>();
        ids.into_iter()
            .filter_map(|id| state.deliveries.remove(&id).map(|(message, _)| message))
            .collect()
    }

    pub fn cancel(&self) {
        let mut state = self.state.lock();
        state.cancelled = true;
        state.executions.clear();
        state.deliveries.clear();
        state.presentations.clear();
    }

    pub fn is_cancelled(&self) -> bool {
        self.state.lock().cancelled
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cursor::{
        services::observability::CursorTraceService,
        tools::{compat, tool_call_result::tool_result_channel},
        transport::OutputHub,
    };

    #[tokio::test]
    async fn completion_on_either_side_of_detach_reaches_the_conversation_once() {
        let directory = tempfile::tempdir().unwrap();
        let store = crate::store::Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        let trace = CursorTraceService::new(store);
        for decoded_first in [true, false] {
            let (commands, mut events) = tokio::sync::mpsc::channel(8);
            let handle = TransportHandle::new(
                "task-test".into(),
                commands,
                Arc::new(OutputHub::default()),
                trace.recorder("task-test"),
            );
            let tasks = Tasks::new(1);
            let round = ToolRoundId::new("original-run:round:1");
            let call = ToolCall {
                index: 0,
                call_id: "task".into(),
                model_call_id: "model".into(),
                name: "Task".into(),
                arguments_text: "{}".into(),
                arguments: serde_json::json!({}),
                argument_error: None,
            };
            tasks.register(&round, vec![(9, call.clone())]);
            let mut completion = compat::failure_with_message(&call, "TASK_FAILURE_MARKER".into());
            completion.exec_id = Some(9);
            let (sender, mut receiver) = tool_result_channel();
            if decoded_first {
                tasks.receive(completion.clone(), &sender, &handle);
            }
            assert!(!tasks.detach(&round)[0].is_error);
            if decoded_first {
                let queued = receiver.try_recv().unwrap().unwrap();
                assert!(!tasks.accept_foreground(&queued, &handle));
            } else {
                drop(receiver); // The parent output loop has already ended.
                tasks.receive(completion, &sender, &handle);
            }
            let event = tokio::time::timeout(std::time::Duration::from_secs(1), events.recv())
                .await
                .unwrap()
                .unwrap();
            let TransportCommand::TaskCompleted { owner, message } = event else {
                panic!("expected completion");
            };
            assert_eq!(owner, 1);
            let event_id = message.runtime_event_id.as_ref().unwrap();
            assert_eq!(event_id, "task-completed:original-run:round:1:task");
            assert!(serde_json::to_string(&message)
                .unwrap()
                .contains("TASK_FAILURE_MARKER"));
            assert!(tasks.delivered(event_id, crate::run::CommandResult::RunClosing));
            assert_eq!(tasks.take_ready(), vec![message]);
            assert!(tasks.take_ready().is_empty());
            assert!(!tasks.has_pending());
            assert!(events.try_recv().is_err());
        }
    }
}
