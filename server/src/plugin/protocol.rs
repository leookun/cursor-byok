//! Defines newline-delimited messages exchanged with a plugin worker.
use serde::{Deserialize, Serialize};

use crate::{
    provider::failure::{FailureKind, ProviderFailure},
    Error,
};

/// SDK failure metadata; the human-readable message remains on the result/error.
#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct FailureMetadata {
    pub kind: FailureKind,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<u16>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub retry_after_ms: Option<u64>,
}

impl FailureMetadata {
    pub fn into_error(self, message: String) -> Error {
        Error::Upstream(ProviderFailure {
            kind: self.kind,
            status: self.status,
            retry_after_ms: self.retry_after_ms,
            message,
        })
    }

    pub fn from_error(error: &Error) -> Option<Self> {
        // Only network transport failures are transient. Configuration errors,
        // protocol violations and arbitrary plugin exceptions are not retried.
        let failure = match error {
            Error::Upstream(failure) => failure.clone(),
            Error::Http(error)
                if error.is_connect()
                    || error.is_timeout()
                    || error.is_body()
                    || error.is_request() =>
            {
                ProviderFailure {
                    kind: FailureKind::Transient,
                    status: error.status().map(|status| status.as_u16()),
                    retry_after_ms: None,
                    message: error.to_string(),
                }
            }
            _ => return None,
        };
        Some(Self {
            kind: failure.kind,
            status: failure.status,
            retry_after_ms: failure.retry_after_ms,
        })
    }
}

#[derive(Debug, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum HostMessage<'a> {
    Request {
        id: &'a str,
        method: &'a str,
        params: &'a serde_json::Value,
    },
    Cancel {
        id: &'a str,
    },
    HostResult {
        id: &'a str,
        result: &'a serde_json::Value,
    },
    HostError {
        id: &'a str,
        error: &'a str,
        #[serde(skip_serializing_if = "Option::is_none")]
        failure: Option<FailureMetadata>,
    },
}

#[derive(Debug, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum WorkerMessage {
    Result {
        id: String,
        #[serde(default)]
        result: serde_json::Value,
        #[serde(default)]
        error: Option<String>,
        #[serde(default)]
        failure: Option<FailureMetadata>,
    },
    /// 流式请求(provider.invoke)在最终 Result 之前发出的模型事件。
    Event {
        id: String,
        event: serde_json::Value,
    },
    HostCall {
        id: String,
        #[serde(rename = "requestId")]
        request_id: String,
        method: String,
        #[serde(default)]
        params: serde_json::Value,
    },
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn typed_worker_errors_roundtrip_without_classifying_plugin_exceptions() {
        let message: WorkerMessage = serde_json::from_value(serde_json::json!({
            "type": "result", "id": "request-1", "error": "connection closed",
            "failure": {"kind": "transient", "retryAfterMs": 1000},
        }))
        .unwrap();
        let WorkerMessage::Result { error, failure, .. } = message else {
            panic!("expected result")
        };
        let Error::Upstream(failure) = failure.unwrap().into_error(error.unwrap()) else {
            panic!("expected upstream error")
        };
        assert_eq!(failure.kind, FailureKind::Transient);
        assert_eq!(failure.retry_after_ms, Some(1000));
        assert!(FailureMetadata::from_error(&Error::Provider("network timeout".into())).is_none());
        assert!(FailureMetadata::from_error(&Error::Config("bad URL".into())).is_none());
        let metadata = FailureMetadata::from_error(&Error::Upstream(failure)).unwrap();
        assert_eq!(
            serde_json::to_value(metadata).unwrap(),
            serde_json::json!({
                "kind": "transient", "retryAfterMs": 1000,
            })
        );
    }

    #[test]
    fn parses_multiplexed_host_call_and_events() {
        let message: WorkerMessage = serde_json::from_value(serde_json::json!({
            "type": "host_call",
            "id": "host-2",
            "requestId": "request-1",
            "method": "network.fetch",
            "params": { "url": "https://example.com" }
        }))
        .unwrap();
        match message {
            WorkerMessage::HostCall {
                id,
                request_id,
                method,
                ..
            } => {
                assert_eq!(id, "host-2");
                assert_eq!(request_id, "request-1");
                assert_eq!(method, "network.fetch");
            }
            _ => panic!("expected host call"),
        }

        let message: WorkerMessage = serde_json::from_value(serde_json::json!({
            "type": "event",
            "id": "request-1",
            "event": { "type": "text-delta", "text": "hi" }
        }))
        .unwrap();
        match message {
            WorkerMessage::Event { id, event } => {
                assert_eq!(id, "request-1");
                assert_eq!(event["type"], "text-delta");
            }
            _ => panic!("expected event"),
        }
    }
}
