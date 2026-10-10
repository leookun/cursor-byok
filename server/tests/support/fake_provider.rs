//! Provides deterministic provider streams for integration tests.
#![allow(dead_code)]

use std::{
    collections::VecDeque,
    sync::{Arc, Mutex},
};

use cursor_server::{
    model::{ModelInvocation, ModelRequest},
    provider::{ModelEvent, Provider, ProviderStream},
    Error,
};
use futures_util::{stream, StreamExt};
use tokio_util::sync::CancellationToken;

enum FakeResponse {
    Events(Vec<Result<ModelEvent, Error>>),
    Gated {
        ready: Arc<tokio::sync::Notify>,
        events: Vec<Result<ModelEvent, Error>>,
    },
    Pending,
}

#[derive(Clone, Default)]
pub struct FakeProvider {
    responses: Arc<Mutex<VecDeque<FakeResponse>>>,
    requests: Arc<Mutex<Vec<ModelRequest>>>,
    call_ids: Arc<Mutex<Vec<String>>>,
}

impl FakeProvider {
    pub fn push(&self, events: Vec<ModelEvent>) {
        self.push_results(events.into_iter().map(Ok).collect());
    }
    pub fn push_results(&self, events: Vec<Result<ModelEvent, Error>>) {
        self.responses
            .lock()
            .unwrap()
            .push_back(FakeResponse::Events(events));
    }
    pub fn push_error(&self, error: Error) {
        self.responses
            .lock()
            .unwrap()
            .push_back(FakeResponse::Events(vec![Err(error)]));
    }
    pub fn push_pending(&self) {
        self.responses
            .lock()
            .unwrap()
            .push_back(FakeResponse::Pending);
    }
    pub fn push_gated(&self, events: Vec<ModelEvent>) -> Arc<tokio::sync::Notify> {
        let ready = Arc::new(tokio::sync::Notify::new());
        self.responses
            .lock()
            .unwrap()
            .push_back(FakeResponse::Gated {
                ready: ready.clone(),
                events: events.into_iter().map(Ok).collect(),
            });
        ready
    }
    pub fn call_ids(&self) -> Vec<String> {
        self.call_ids.lock().unwrap().clone()
    }
    pub fn requests(&self) -> Vec<ModelRequest> {
        self.requests.lock().unwrap().clone()
    }
}

impl Provider for FakeProvider {
    fn stream(
        &self,
        invocation: ModelInvocation,
        _cancellation: CancellationToken,
    ) -> ProviderStream {
        self.call_ids.lock().unwrap().push(invocation.call_id);
        self.requests.lock().unwrap().push(invocation.request);
        let events = self
            .responses
            .lock()
            .unwrap()
            .pop_front()
            .expect("fake response configured");
        match events {
            FakeResponse::Events(events) => Box::pin(stream::iter(events)),
            FakeResponse::Gated { ready, events } => Box::pin(
                stream::once(async move {
                    ready.notified().await;
                    events
                })
                .flat_map(stream::iter),
            ),
            FakeResponse::Pending => Box::pin(stream::pending()),
        }
    }
}
