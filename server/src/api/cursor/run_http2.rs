//! Serves the HTTP/2 agent connection Cursor opens itself.
//!
//! `AgentService/Run` for a configured model is handled here. Every other
//! call on that connection, including speech-to-text and Cursor's own models,
//! is relayed to `agentn.api5.cursor.sh`. The relay carries no HTTP trailers,
//! which holds for Connect (status travels in the end-stream frame) but not gRPC.
use std::io::Read;

use axum::{
    body::{Body, BodyDataStream},
    extract::{DefaultBodyLimit, Extension, State},
    http::{header, HeaderValue, Request, Response, StatusCode},
    routing::post,
    Router,
};
use bytes::{Bytes, BytesMut};
use futures_util::StreamExt;
use prost::Message;

use crate::{
    api::cursor::{
        bidi::{self, DecodedAppend},
        handlers,
        proxy::{self, CursorProxy},
        run_sse,
    },
    cursor::{
        protocol::{
            connect::{self, ConnectCode, ConnectStreamError, END_STREAM_FLAG},
            proto::agent::v1 as agent,
        },
        transport::TransportRegistry,
    },
    model::ConversationId,
    network::NetworkClients,
    Error, Result,
};

const COMPRESSED_FLAG: u8 = 0x01;
const MAX_MESSAGE_LEN: usize = 64 * 1024 * 1024;

pub fn router(registry: TransportRegistry, clients: NetworkClients) -> Router {
    Router::new()
        .route("/agent.v1.AgentService/Run", post(run))
        .fallback(relay)
        .layer(Extension(CursorProxy::agent(clients)))
        .layer(DefaultBodyLimit::disable())
        .with_state(registry)
}

async fn relay(
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    tracing::info!(
        path = request.uri().path(),
        "relaying HTTP/2 agent call to Cursor upstream"
    );
    proxy::forward(Extension(proxy), request).await
}

async fn run(
    State(registry): State<TransportRegistry>,
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Response<Body> {
    match route_run(registry, proxy, request).await {
        Ok(response) => response,
        Err(error) => connect_error(error),
    }
}

async fn route_run(
    registry: TransportRegistry,
    proxy: CursorProxy,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let (parts, body) = request.into_parts();
    let request_id = match handlers::header_text(&parts.headers, "x-request-id")? {
        Some(request_id) => request_id.to_owned(),
        None => {
            tracing::warn!("HTTP/2 agent run has no x-request-id; subagents cannot link to it");
            uuid::Uuid::new_v4().to_string()
        }
    };
    let mut reader = ConnectReader {
        body: body.into_data_stream(),
        buffer: BytesMut::new(),
    };

    // Frames read before the routing decision are replayed when relaying.
    let mut seen = Vec::new();
    let mut appends = Vec::new();
    while let Some(frame) = reader.next_frame().await? {
        seen.push(frame.clone());
        let Some(message) = decode_message(&frame)? else {
            break;
        };
        let is_run_request = matches!(
            message.message,
            Some(agent::agent_client_message::Message::RunRequest(_))
        );
        appends.push(DecodedAppend {
            request_id: request_id.clone(),
            seqno: appends.len() as i64,
            message,
        });
        if is_run_request {
            break;
        }
    }
    let run_request = appends.last();
    let model_id = run_request
        .and_then(DecodedAppend::model_id)
        .map(str::to_owned);
    let conversation_id = run_request
        .and_then(DecodedAppend::conversation_id)
        .filter(|id| !id.is_empty())
        .map(str::to_owned);
    let local = match (&model_id, &conversation_id) {
        (Some(model_id), conversation_id) => {
            let local = handlers::is_local_model(registry.store(), model_id).await?;
            if let Some(conversation_id) = conversation_id {
                registry
                    .set_conversation_local(conversation_id, local)
                    .await;
            }
            local
        }
        (None, Some(conversation_id)) => {
            let remembered = registry.conversation_local(conversation_id).await;
            let stored = match remembered {
                Some(_) => false,
                None => registry
                    .store()
                    .conversation(&ConversationId::new(conversation_id.as_str()))
                    .await?
                    .is_some(),
            };
            continues_locally(remembered, stored)
        }
        (None, None) => false,
    };
    if !local {
        tracing::info!(
            model_id = model_id.as_deref().unwrap_or(""),
            "routing HTTP/2 agent run to Cursor upstream"
        );
        if model_id.is_some() {
            registry.trace(&request_id).begin(
                conversation_id.as_deref(),
                "cursor_official",
                model_id.as_deref(),
            );
        }
        let request = Request::from_parts(parts, reader.into_body(seen));
        return proxy::forward(Extension(proxy), request).await;
    }

    let parent = handlers::parent_headers(&parts.headers)?;
    registry.trace(&request_id).begin(
        conversation_id.as_deref(),
        "local_byok",
        model_id.as_deref(),
    );
    tracing::info!(
        request_id,
        model_id = model_id.as_deref().unwrap_or(""),
        "routing HTTP/2 agent run to BYOK provider"
    );
    let mut seqno = appends.len() as i64;
    for append in appends {
        bidi::append(&registry, append, parent.clone()).await?;
    }
    let response = run_sse::connect_stream(&registry, &request_id).await?;
    tokio::spawn(async move {
        loop {
            let message = match reader.next_message().await {
                Ok(Some(message)) => message,
                Ok(None) => return,
                Err(error) => {
                    tracing::warn!(request_id, %error, "HTTP/2 agent run body ended with an error");
                    break;
                }
            };
            let append = DecodedAppend {
                request_id: request_id.clone(),
                seqno,
                message,
            };
            seqno += 1;
            if let Err(error) = bidi::append(&registry, append, parent.clone()).await {
                tracing::warn!(request_id, %error, "HTTP/2 agent run append failed");
                break;
            }
        }
        if let Some(handle) = registry.local(&request_id).await {
            handle.disconnect().await;
        }
    });
    Ok(response)
}

/// A turn without a model continues with the conversation's last model.
/// After a restart only the conversations stored locally are known.
fn continues_locally(remembered: Option<bool>, stored: bool) -> bool {
    remembered.unwrap_or(stored)
}

/// Reads Connect frames from a streaming request body.
struct ConnectReader {
    body: BodyDataStream,
    buffer: BytesMut,
}

impl ConnectReader {
    async fn next_frame(&mut self) -> Result<Option<Bytes>> {
        loop {
            if let Some(frame) = connect::take_frame(&mut self.buffer, MAX_MESSAGE_LEN)? {
                return Ok(Some(frame));
            }
            match self.body.next().await {
                Some(chunk) => {
                    self.buffer.extend_from_slice(&chunk.map_err(|error| {
                        Error::Protocol(format!("read HTTP/2 agent run: {error}"))
                    })?)
                }
                None if self.buffer.is_empty() => return Ok(None),
                None => {
                    return Err(Error::Protocol(
                        "truncated Connect frame in HTTP/2 agent run".into(),
                    ))
                }
            }
        }
    }

    async fn next_message(&mut self) -> Result<Option<agent::AgentClientMessage>> {
        match self.next_frame().await? {
            Some(frame) => decode_message(&frame),
            None => Ok(None),
        }
    }

    fn into_body(self, mut seen: Vec<Bytes>) -> Body {
        if !self.buffer.is_empty() {
            seen.push(self.buffer.freeze());
        }
        let prefix = futures_util::stream::iter(seen.into_iter().map(Ok::<_, axum::Error>));
        Body::from_stream(prefix.chain(self.body))
    }
}

/// Returns `None` for the end-of-stream frame.
fn decode_message(frame: &Bytes) -> Result<Option<agent::AgentClientMessage>> {
    let flags = frame[0];
    if flags & END_STREAM_FLAG != 0 {
        return Ok(None);
    }
    let payload = &frame[5..];
    if flags & COMPRESSED_FLAG == 0 {
        return Ok(Some(agent::AgentClientMessage::decode(payload)?));
    }
    let mut output = Vec::new();
    flate2::read::GzDecoder::new(payload)
        .take(MAX_MESSAGE_LEN as u64 + 1)
        .read_to_end(&mut output)
        .map_err(|error| Error::Protocol(format!("gzip Connect frame: {error}")))?;
    if output.len() > MAX_MESSAGE_LEN {
        return Err(Error::Protocol(
            "decompressed Connect frame is too large".into(),
        ));
    }
    Ok(Some(agent::AgentClientMessage::decode(output.as_slice())?))
}

/// Connect streaming reports errors in an end-of-stream frame on HTTP 200.
fn connect_error(error: Error) -> Response<Body> {
    tracing::warn!(%error, "HTTP/2 agent run failed");
    let code = match error {
        Error::Protocol(_) | Error::Decode(_) => ConnectCode::InvalidArgument,
        Error::Http(_) | Error::Provider(_) => ConnectCode::Unavailable,
        _ => ConnectCode::Internal,
    };
    let frame = connect::encode_error_end_stream(&ConnectStreamError {
        code,
        message: error.to_string(),
        details: Vec::new(),
    })
    .expect("encode Connect error frame");
    let mut response = Response::new(Body::from(frame));
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/connect+proto"),
    );
    *response.status_mut() = StatusCode::OK;
    response
}

#[cfg(test)]
mod tests {
    use std::io::Write;

    use super::*;

    fn run_request(model_id: &str) -> agent::AgentClientMessage {
        agent::AgentClientMessage {
            message: Some(agent::agent_client_message::Message::RunRequest(
                agent::AgentRunRequest {
                    requested_model: Some(agent::RequestedModel {
                        model_id: model_id.into(),
                        ..Default::default()
                    }),
                    ..Default::default()
                },
            )),
        }
    }

    fn frame(flags: u8, payload: &[u8]) -> Vec<u8> {
        let mut bytes = vec![flags];
        bytes.extend_from_slice(&(payload.len() as u32).to_be_bytes());
        bytes.extend_from_slice(payload);
        bytes
    }

    fn reader(chunks: Vec<Vec<u8>>) -> ConnectReader {
        let stream = futures_util::stream::iter(
            chunks
                .into_iter()
                .map(|chunk| Ok::<_, std::io::Error>(Bytes::from(chunk))),
        );
        ConnectReader {
            body: Body::from_stream(stream).into_data_stream(),
            buffer: BytesMut::new(),
        }
    }

    fn model_id(message: agent::AgentClientMessage) -> String {
        DecodedAppend {
            request_id: String::new(),
            seqno: 0,
            message,
        }
        .model_id()
        .unwrap()
        .to_owned()
    }

    #[test]
    fn model_less_turns_follow_the_conversations_last_model() {
        // Switched from a local model to a Cursor model in the same chat.
        assert!(!continues_locally(Some(false), true));
        assert!(continues_locally(Some(true), false));
        // After a restart the store decides.
        assert!(continues_locally(None, true));
        assert!(!continues_locally(None, false));
    }

    #[tokio::test]
    async fn reads_frames_split_across_chunks_and_gzip_frames() {
        let plain = frame(0, &run_request("plain").encode_to_vec());
        let mut gzip = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
        gzip.write_all(&run_request("gzip").encode_to_vec())
            .unwrap();
        let compressed = frame(COMPRESSED_FLAG, &gzip.finish().unwrap());
        let mut bytes = plain.clone();
        bytes.extend_from_slice(&compressed);
        bytes.extend_from_slice(&frame(END_STREAM_FLAG, b"{}"));
        let (head, tail) = bytes.split_at(3);
        let mut reader = reader(vec![head.to_vec(), tail.to_vec()]);

        let first = reader.next_message().await.unwrap().unwrap();
        assert_eq!(model_id(first), "plain");
        let second = reader.next_message().await.unwrap().unwrap();
        assert_eq!(model_id(second), "gzip");
        assert!(reader.next_message().await.unwrap().is_none());
        assert!(reader.buffer.is_empty());
    }

    #[tokio::test]
    async fn relay_body_replays_frames_read_before_routing() {
        let first = frame(0, &run_request("cursor-model").encode_to_vec());
        let second = frame(0, b"");
        let mut bytes = first.clone();
        bytes.extend_from_slice(&second[..2]);
        let mut reader = reader(vec![bytes, second[2..].to_vec(), b"rest".to_vec()]);
        let seen = vec![reader.next_frame().await.unwrap().unwrap()];

        let body = axum::body::to_bytes(reader.into_body(seen), usize::MAX)
            .await
            .unwrap();
        let mut expected = first;
        expected.extend_from_slice(&second);
        expected.extend_from_slice(b"rest");
        assert_eq!(body.as_ref(), expected.as_slice());
    }
}
