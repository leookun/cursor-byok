//! Encodes and decodes Connect protocol frames.
use bytes::{BufMut, Bytes, BytesMut};
use prost::Message;
use serde::Serialize;

use crate::{Error, Result};

pub const END_STREAM_FLAG: u8 = 0x02;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ConnectCode {
    Canceled,
    InvalidArgument,
    NotFound,
    Unavailable,
    Internal,
}

impl ConnectCode {
    fn as_str(self) -> &'static str {
        match self {
            Self::Canceled => "canceled",
            Self::InvalidArgument => "invalid_argument",
            Self::NotFound => "not_found",
            Self::Unavailable => "unavailable",
            Self::Internal => "internal",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct ConnectErrorDetail {
    #[serde(rename = "type")]
    pub type_name: String,
    pub value: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConnectStreamError {
    pub code: ConnectCode,
    pub message: String,
    pub details: Vec<ConnectErrorDetail>,
}

#[derive(Serialize)]
struct EndStreamResponse<'a> {
    error: WireError<'a>,
}

#[derive(Serialize)]
struct WireError<'a> {
    code: &'static str,
    #[serde(skip_serializing_if = "str::is_empty")]
    message: &'a str,
    #[serde(skip_serializing_if = "details_are_empty")]
    details: &'a [ConnectErrorDetail],
}

fn details_are_empty(details: &&[ConnectErrorDetail]) -> bool {
    details.is_empty()
}

pub fn encode_message<M: Message>(message: &M) -> Result<Bytes> {
    let len = message.encoded_len();
    let mut output = BytesMut::with_capacity(5 + len);
    output.put_u8(0);
    output.put_u32(len as u32);
    message.encode(&mut output)?;
    Ok(output.freeze())
}

pub fn encode_end_stream() -> Bytes {
    encode_end_stream_payload(b"{}")
}

pub fn encode_error_end_stream(error: &ConnectStreamError) -> Result<Bytes> {
    let payload = serde_json::to_vec(&EndStreamResponse {
        error: WireError {
            code: error.code.as_str(),
            message: &error.message,
            details: &error.details,
        },
    })?;
    Ok(encode_end_stream_payload(&payload))
}

fn encode_end_stream_payload(payload: &[u8]) -> Bytes {
    let mut output = BytesMut::with_capacity(5 + payload.len());
    output.put_u8(END_STREAM_FLAG);
    output.put_u32(payload.len() as u32);
    output.extend_from_slice(payload);
    output.freeze()
}

pub fn decode_unary<M: Message + Default>(body: &[u8]) -> Result<M> {
    if body.len() >= 5 {
        let flags = body[0];
        let length = u32::from_be_bytes([body[1], body[2], body[3], body[4]]) as usize;
        if flags & END_STREAM_FLAG == 0 && length == body.len() - 5 {
            return Ok(M::decode(&body[5..])?);
        }
    }
    Ok(M::decode(body)?)
}

/// Splits one complete frame, header included, off the front of `buffer`.
pub fn take_frame(buffer: &mut BytesMut, max_len: usize) -> Result<Option<Bytes>> {
    if buffer.len() < 5 {
        return Ok(None);
    }
    let length = u32::from_be_bytes([buffer[1], buffer[2], buffer[3], buffer[4]]) as usize;
    if length > max_len {
        return Err(Error::Protocol(format!("Connect frame is {length} bytes")));
    }
    if buffer.len() < 5 + length {
        return Ok(None);
    }
    Ok(Some(buffer.split_to(5 + length).freeze()))
}

pub fn decode_frames(mut body: &[u8]) -> Result<Vec<(u8, Bytes)>> {
    let mut frames = Vec::new();
    while !body.is_empty() {
        if body.len() < 5 {
            return Err(Error::Protocol("truncated Connect envelope".into()));
        }
        let flags = body[0];
        let length = u32::from_be_bytes([body[1], body[2], body[3], body[4]]) as usize;
        body = &body[5..];
        if body.len() < length {
            return Err(Error::Protocol("truncated Connect payload".into()));
        }
        frames.push((flags, Bytes::copy_from_slice(&body[..length])));
        body = &body[length..];
    }
    Ok(frames)
}
