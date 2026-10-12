//! Defines the server-wide error type and error conversions.
use axum::{
    http::StatusCode,
    response::{IntoResponse, Response},
    Json,
};

pub type Result<T, E = Error> = std::result::Result<T, E>;

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("configuration error: {0}")]
    Config(String),
    #[error("protocol error: {0}")]
    Protocol(String),
    #[error("provider error: {0}")]
    Provider(String),
    #[error("account error: {message}")]
    Resource { message: String, retryable: bool },
    #[error("store error: {0}")]
    Store(String),
    #[error("run was cancelled")]
    Cancelled,
    #[error("run not found: {0}")]
    RunNotFound(String),
    #[error("database error: {0}")]
    Database(#[from] sqlx::Error),
    #[error("database migration error: {0}")]
    Migration(#[from] sqlx::migrate::MigrateError),
    #[error(
        "database migration stage '{stage}' timed out after {timeout_seconds} seconds; close other Cursor BYOK processes and try again"
    )]
    MigrationTimeout { stage: String, timeout_seconds: u64 },
    #[error("http error: {0}")]
    Http(#[from] reqwest::Error),
    #[error("protobuf decode error: {0}")]
    Decode(#[from] prost::DecodeError),
    #[error("protobuf encode error: {0}")]
    Encode(#[from] prost::EncodeError),
    #[error("json error: {0}")]
    Json(#[from] serde_json::Error),
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),
}

impl IntoResponse for Error {
    fn into_response(self) -> Response {
        let status = match self {
            Self::Config(_) | Self::Protocol(_) | Self::Decode(_) | Self::Json(_) => {
                StatusCode::BAD_REQUEST
            }
            Self::RunNotFound(_) => StatusCode::NOT_FOUND,
            Self::Provider(_) | Self::Resource { .. } | Self::Http(_) => StatusCode::BAD_GATEWAY,
            Self::Cancelled => StatusCode::CONFLICT,
            Self::Store(_)
            | Self::Database(_)
            | Self::Migration(_)
            | Self::MigrationTimeout { .. }
            | Self::Encode(_)
            | Self::Io(_) => StatusCode::INTERNAL_SERVER_ERROR,
        };
        // 所有回给 UI 的错误统一落日志,否则失败原因只出现在前端提示里。
        tracing::warn!(%status, error = %self, "request failed");
        let code = match status {
            StatusCode::BAD_REQUEST => "invalid_argument",
            StatusCode::NOT_FOUND => "not_found",
            StatusCode::CONFLICT => "aborted",
            StatusCode::BAD_GATEWAY => "unavailable",
            _ => "internal",
        };
        (
            status,
            Json(serde_json::json!({ "code": code, "message": self.to_string() })),
        )
            .into_response()
    }
}
