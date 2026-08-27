use base64::{engine::general_purpose::STANDARD_NO_PAD, Engine};
use prost::Message;

use crate::{
    cursor::CursorSessionHandle,
    cursor::{
        connect::{
            encode_end_stream, encode_error_end_stream, ConnectCode, ConnectErrorDetail,
            ConnectStreamError,
        },
        proto::aiserver::v1 as ai,
    },
    Error, Result,
};

pub fn finish_success(handle: &CursorSessionHandle) {
    handle.emit_frame(encode_end_stream());
    handle.close_output();
}

pub fn fail(handle: &CursorSessionHandle, error: &Error) -> Result<()> {
    let stream_error = match error {
        Error::Provider(_) | Error::Http(_) => provider_error(error),
        Error::Protocol(message) => plain_message(ConnectCode::InvalidArgument, message.clone()),
        Error::Decode(_) | Error::Json(_) => plain_error(ConnectCode::InvalidArgument, error),
        Error::RunNotFound(_) => plain_error(ConnectCode::NotFound, error),
        Error::Cancelled => plain_error(ConnectCode::Canceled, error),
        Error::Store(_) => detailed_error(
            ConnectCode::InvalidArgument,
            ai::error_details::Error::CustomMessage,
            "Conversation Error",
            error,
            true,
        ),
        Error::Config(_)
        | Error::Database(_)
        | Error::Migration(_)
        | Error::Encode(_)
        | Error::Io(_) => detailed_error(
            ConnectCode::Internal,
            ai::error_details::Error::Internal,
            "Internal Error",
            error,
            true,
        ),
    };
    handle.emit_frame(encode_error_end_stream(&stream_error)?);
    handle.close_output();
    Ok(())
}

pub fn cancel(handle: &CursorSessionHandle) -> Result<()> {
    handle.emit_frame(encode_error_end_stream(&ConnectStreamError {
        code: ConnectCode::Canceled,
        message: "run was cancelled".into(),
        details: Vec::new(),
    })?);
    handle.close_output();
    Ok(())
}

fn plain_error(code: ConnectCode, error: &Error) -> ConnectStreamError {
    plain_message(code, error.to_string())
}

fn plain_message(code: ConnectCode, message: String) -> ConnectStreamError {
    ConnectStreamError {
        code,
        message,
        details: Vec::new(),
    }
}

fn provider_error(error: &Error) -> ConnectStreamError {
    detailed_error(
        ConnectCode::Unavailable,
        ai::error_details::Error::ProviderError,
        "Provider Error",
        error,
        false,
    )
}

fn detailed_error(
    code: ConnectCode,
    kind: ai::error_details::Error,
    title: &str,
    error: &Error,
    should_show_immediate_error: bool,
) -> ConnectStreamError {
    let detail = ai::ErrorDetails {
        error: kind as i32,
        details: Some(ai::CustomErrorDetails {
            title: title.into(),
            detail: error.to_string(),
            allow_command_links_potentially_unsafe_please_only_use_for_handwritten_trusted_markdown:
                Some(true),
            is_retryable: Some(true),
            show_request_id: Some(true),
            should_show_immediate_error: Some(should_show_immediate_error),
        }),
        is_expected: Some(true),
    };
    ConnectStreamError {
        code,
        message: error.to_string(),
        details: vec![ConnectErrorDetail {
            type_name: "aiserver.v1.ErrorDetails".into(),
            value: STANDARD_NO_PAD.encode(detail.encode_to_vec()),
        }],
    }
}
