//! Defines the provider interface and exports provider implementations.
mod anthropic;
mod attempt;
mod event;
pub mod failure;
mod normalize;
mod openai_chat;
mod openai_responses;
mod recorder;
mod request_template;
mod router;

use std::pin::Pin;

use futures_util::Stream;
use tokio_util::sync::CancellationToken;

use crate::{model::ModelInvocation, Result};

pub use anthropic::AnthropicProvider;
pub use event::*;
pub use openai_chat::OpenAiChatProvider;
pub use openai_responses::OpenAiResponsesProvider;
pub use recorder::CallRecorder;
pub(crate) use router::custom_headers;

pub(crate) fn native_usage(
    kind: crate::model::ProviderType,
    value: &serde_json::Value,
) -> crate::model::Usage {
    match kind {
        crate::model::ProviderType::OpenAiChat => openai_chat::openai_usage(value),
        crate::model::ProviderType::OpenAiResponses => openai_responses::responses_usage(value),
        crate::model::ProviderType::Anthropic => anthropic::anthropic_usage(value),
        crate::model::ProviderType::Plugin => {
            unreachable!("plugin calls have no native HTTP protocol")
        }
    }
}
pub use router::{build as build_provider, ProviderRouter};

pub type ProviderStream = Pin<Box<dyn Stream<Item = Result<ModelEvent>> + Send>>;

pub trait Provider: Send + Sync {
    fn alias_resolver(&self) -> Option<crate::alias::AliasResolver> {
        None
    }

    fn stream(
        &self,
        invocation: ModelInvocation,
        cancellation: CancellationToken,
    ) -> ProviderStream;
}

fn map_sse_error(
    label: &str,
    error: eventsource_stream::EventStreamError<crate::Error>,
) -> crate::Error {
    match error {
        eventsource_stream::EventStreamError::Transport(error) => error,
        eventsource_stream::EventStreamError::Utf8(error) => {
            crate::Error::Provider(format!("{label} SSE UTF-8 error: {error}"))
        }
        eventsource_stream::EventStreamError::Parser(error) => {
            crate::Error::Provider(format!("{label} SSE parse error: {error}"))
        }
    }
}

fn provider_event_error(label: &str, value: &serde_json::Value) -> Option<crate::Error> {
    let kind = value.get("type").and_then(serde_json::Value::as_str);
    let direct_error = value.get("error").filter(|error| !error.is_null());
    if !matches!(kind, Some("error" | "response.failed")) && direct_error.is_none() {
        return None;
    }

    let message = value
        .get("message")
        .and_then(serde_json::Value::as_str)
        .or_else(|| {
            value
                .pointer("/error/message")
                .and_then(serde_json::Value::as_str)
        })
        .or_else(|| {
            value
                .pointer("/response/error/message")
                .and_then(serde_json::Value::as_str)
        })
        .or_else(|| direct_error.and_then(serde_json::Value::as_str))
        .or_else(|| {
            value
                .pointer("/response/error")
                .and_then(serde_json::Value::as_str)
        })
        .unwrap_or("provider returned an error event without a message");

    let code = value
        .pointer("/error/code")
        .or_else(|| value.pointer("/error/type"))
        .or_else(|| value.pointer("/response/error/code"))
        .or_else(|| value.get("code"))
        .and_then(serde_json::Value::as_str);
    let kind = match code {
        Some(
            "rate_limit_exceeded" | "rate_limit_error" | "insufficient_quota" | "quota_exceeded",
        ) => Some(failure::FailureKind::RateLimit),
        Some("server_error" | "api_error" | "overloaded_error" | "internal_server_error") => {
            Some(failure::FailureKind::Transient)
        }
        Some("authentication_error" | "permission_error" | "invalid_api_key") => {
            Some(failure::FailureKind::Authorization)
        }
        Some("invalid_request_error") => Some(failure::FailureKind::Request),
        _ => None,
    };
    let message = format!("{label} error: {message}");
    Some(match kind {
        Some(kind) => crate::Error::Upstream(failure::ProviderFailure {
            kind,
            status: None,
            retry_after_ms: None,
            message,
        }),
        None => crate::Error::Provider(message),
    })
}

fn merge_extra_params(body: &mut serde_json::Value, extra: &serde_json::Value) -> Result<()> {
    let extra = extra
        .as_object()
        .ok_or_else(|| crate::Error::Config("model extra params must be an object".into()))?;
    let body = body
        .as_object_mut()
        .ok_or_else(|| crate::Error::Provider("provider request body must be an object".into()))?;
    for (name, value) in extra {
        if matches!(
            name.as_str(),
            "model"
                | "stream"
                | "messages"
                | "input"
                | "tools"
                | "system"
                | "instructions"
                | "prompt_cache_key"
        ) {
            return Err(crate::Error::Config(format!(
                "model extra params cannot replace {name}"
            )));
        }
        body.insert(name.clone(), value.clone());
    }
    Ok(())
}

fn apply_body_allowlist(
    body: &mut serde_json::Value,
    allowed: Option<&std::collections::HashSet<String>>,
) -> Result<()> {
    let Some(allowed) = allowed else {
        return Ok(());
    };
    body.as_object_mut()
        .ok_or_else(|| crate::Error::Provider("provider request body must be an object".into()))?
        .retain(|name, _| allowed.contains(name));
    Ok(())
}

fn apply_openai_prompt_cache_key(body: &mut serde_json::Value, model_id: &str) -> Result<()> {
    if !model_id.to_ascii_lowercase().contains("gpt") {
        return Ok(());
    }
    body.as_object_mut()
        .ok_or_else(|| crate::Error::Provider("provider request body must be an object".into()))?
        .insert(
            "prompt_cache_key".into(),
            serde_json::Value::String("cursor-byok".into()),
        );
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sse_transport_errors_are_not_relabelled_as_parse_errors() {
        let error = map_sse_error(
            "test provider",
            eventsource_stream::EventStreamError::Transport(crate::Error::Provider(
                "connection closed".into(),
            )),
        );

        let crate::Error::Provider(message) = error else {
            panic!("transport error category must be preserved");
        };
        assert_eq!(message, "connection closed");
    }

    #[test]
    fn provider_error_events_extract_flat_and_nested_messages() {
        assert_provider_error(
            "OpenAI Responses",
            serde_json::json!({
                "type": "error",
                "message": "Internal error during token generation"
            }),
            "OpenAI Responses error: Internal error during token generation",
        );
        assert_provider_error(
            "OpenAI Chat",
            serde_json::json!({
                "error": {"message": "quota exceeded", "type": "server_error"}
            }),
            "OpenAI Chat error: quota exceeded",
        );
        assert_provider_error(
            "Anthropic",
            serde_json::json!({
                "type": "error",
                "error": {"type": "overloaded_error", "message": "Overloaded"}
            }),
            "Anthropic error: Overloaded",
        );
        assert_provider_error(
            "OpenAI Responses",
            serde_json::json!({
                "type": "response.failed",
                "response": {"error": {"message": "generation failed"}}
            }),
            "OpenAI Responses error: generation failed",
        );
    }

    #[test]
    fn successful_provider_events_are_not_errors() {
        assert!(provider_event_error(
            "OpenAI Responses",
            &serde_json::json!({"type": "response.completed", "error": null})
        )
        .is_none());
    }

    fn assert_provider_error(label: &str, value: serde_json::Value, expected: &str) {
        let message = match provider_event_error(label, &value) {
            Some(crate::Error::Provider(message)) => message,
            Some(crate::Error::Upstream(failure)) => failure.message,
            _ => panic!("expected provider error"),
        };
        assert_eq!(message, expected);
    }
}
