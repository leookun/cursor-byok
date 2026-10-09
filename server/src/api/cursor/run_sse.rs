//! Subscribes Cursor RunSSE clients to replayable Transport output.
use axum::{
    body::Body,
    http::{header, HeaderValue, Response, StatusCode},
};
use bytes::Bytes;
use std::convert::Infallible;
use tokio::sync::mpsc;
use tokio_stream::StreamExt;

use crate::{
    cursor::{
        protocol::connect::{self, END_STREAM_FLAG},
        services::observability::CursorTraceRecorder,
        transport::{TransportHandle, TransportRegistry},
    },
    Result,
};

pub async fn stream(registry: &TransportRegistry, request_id: &str) -> Result<Response<Body>> {
    open_stream(registry, request_id, "text/event-stream").await
}

/// HTTP/2 `AgentService/Run` reads the same frames as RunSSE.
pub async fn connect_stream(
    registry: &TransportRegistry,
    request_id: &str,
) -> Result<Response<Body>> {
    open_stream(registry, request_id, "application/connect+proto").await
}

async fn open_stream(
    registry: &TransportRegistry,
    request_id: &str,
    content_type: &'static str,
) -> Result<Response<Body>> {
    let handle = registry.get_or_create(request_id).await?;
    let receiver = handle.subscribe();
    let trace = handle.trace().cloned();
    if let Some(trace) = &trace {
        trace.response_started(StatusCode::OK.as_u16());
    }
    let body_stream = local_body_stream(receiver, handle, trace);
    let mut response = Response::new(Body::from_stream(body_stream));
    *response.status_mut() = StatusCode::OK;
    response
        .headers_mut()
        .insert(header::CONTENT_TYPE, HeaderValue::from_static(content_type));
    response
        .headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-cache"));
    response
        .headers_mut()
        .insert("connect-protocol-version", HeaderValue::from_static("1"));
    Ok(response)
}

fn local_body_stream(
    mut receiver: mpsc::UnboundedReceiver<Bytes>,
    handle: TransportHandle,
    trace: Option<CursorTraceRecorder>,
) -> impl tokio_stream::Stream<Item = std::result::Result<Bytes, Infallible>> {
    async_stream::stream! {
        let mut guard = LocalRunGuard::new(handle);
        let mut trace = TraceStreamSink::new(trace, "byok_server");
        while let Some(chunk) = receiver.recv().await {
            let terminal = is_end_stream_frame(&chunk);
            trace.chunk(&chunk);
            if terminal {
                guard.complete();
                trace.finish(end_stream_error(&chunk));
            }
            yield Ok::<Bytes, Infallible>(chunk);
            if terminal {
                return;
            }
        }
        guard.complete();
        trace.finish(None);
    }
}

fn is_end_stream_frame(frame: &Bytes) -> bool {
    frame
        .first()
        .is_some_and(|flags| flags & END_STREAM_FLAG != 0)
}

fn end_stream_error(frame: &Bytes) -> Option<String> {
    connect::decode_frames(frame)
        .ok()?
        .into_iter()
        .find_map(|(flags, payload)| {
            if flags & END_STREAM_FLAG == 0 {
                return None;
            }
            let value = serde_json::from_slice::<serde_json::Value>(&payload).ok()?;
            let error = value.get("error")?;
            let code = error.get("code").and_then(serde_json::Value::as_str);
            let message = error
                .get("message")
                .and_then(serde_json::Value::as_str)
                .filter(|message| !message.is_empty());
            Some(match (code, message) {
                (Some(code), Some(message)) => format!("{code}: {message}"),
                (Some(code), None) => code.to_string(),
                (None, Some(message)) => message.to_string(),
                (None, None) => error.to_string(),
            })
        })
}

struct LocalRunGuard {
    handle: TransportHandle,
    completed: bool,
}

impl LocalRunGuard {
    fn new(handle: TransportHandle) -> Self {
        Self {
            handle,
            completed: false,
        }
    }

    fn complete(&mut self) {
        self.completed = true;
    }
}

impl Drop for LocalRunGuard {
    fn drop(&mut self) {
        if !self.completed {
            let handle = self.handle.clone();
            tokio::spawn(async move {
                handle.disconnect().await;
            });
        }
    }
}

pub async fn upstream(
    registry: TransportRegistry,
    request_id: String,
    generation: u64,
    response: Response<Body>,
    trace: Option<CursorTraceRecorder>,
) -> Response<Body> {
    let (parts, body) = response.into_parts();
    if let Some(trace) = &trace {
        trace.response_started(parts.status.as_u16());
    }
    let stream = async_stream::stream! {
        let _guard = UpstreamRunGuard {
            registry,
            request_id,
            generation,
        };
        let mut trace = TraceStreamSink::new(trace, "cursor_official");
        let mut body = body.into_data_stream();
        while let Some(chunk) = body.next().await {
            match chunk {
                Ok(chunk) => {
                    trace.chunk(&chunk);
                    yield Ok::<Bytes, axum::Error>(chunk);
                }
                Err(error) => {
                    trace.finish(Some(error.to_string()));
                    yield Err(error);
                    return;
                }
            }
        }
        trace.finish(None);
    };
    Response::from_parts(parts, Body::from_stream(stream))
}

enum TraceStreamEvent {
    Chunk(Bytes),
    Finish(Option<String>),
}

struct TraceStreamSink {
    sender: Option<mpsc::UnboundedSender<TraceStreamEvent>>,
}

impl TraceStreamSink {
    fn new(trace: Option<CursorTraceRecorder>, source: &'static str) -> Self {
        let Some(trace) = trace else {
            return Self { sender: None };
        };
        let (sender, mut receiver) = mpsc::unbounded_channel();
        tokio::spawn(async move {
            while let Some(event) = receiver.recv().await {
                match event {
                    TraceStreamEvent::Chunk(chunk) => {
                        trace.response_chunk(source, chunk);
                    }
                    TraceStreamEvent::Finish(error) => {
                        trace.finish(error.as_deref());
                        return;
                    }
                }
            }
            trace.finish(None);
        });
        Self {
            sender: Some(sender),
        }
    }

    fn chunk(&self, chunk: &Bytes) {
        if let Some(sender) = &self.sender {
            let _ = sender.send(TraceStreamEvent::Chunk(chunk.clone()));
        }
    }

    fn finish(&mut self, error: Option<String>) {
        if let Some(sender) = self.sender.take() {
            let _ = sender.send(TraceStreamEvent::Finish(error));
        }
    }
}

impl Drop for TraceStreamSink {
    fn drop(&mut self) {
        if self.sender.is_some() {
            self.finish(Some(
                "response stream dropped before completion".to_string(),
            ));
        }
    }
}

struct UpstreamRunGuard {
    registry: TransportRegistry,
    request_id: String,
    generation: u64,
}

impl Drop for UpstreamRunGuard {
    fn drop(&mut self) {
        self.registry
            .finish_upstream(self.request_id.clone(), self.generation);
    }
}
