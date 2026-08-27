mod anthropic;
mod event;
mod normalize;
mod openai_chat;
mod openai_responses;
mod recorder;
mod router;

use std::pin::Pin;

use base64::Engine;
use futures_util::Stream;
use tokio_util::sync::CancellationToken;

use crate::{model::ModelInvocation, Result};

pub use anthropic::AnthropicProvider;
pub use event::*;
pub use openai_chat::OpenAiChatProvider;
pub use openai_responses::OpenAiResponsesProvider;
pub use recorder::CallRecorder;
pub use router::{build as build_provider, ProviderRouter};

pub type ProviderStream = Pin<Box<dyn Stream<Item = Result<ModelEvent>> + Send>>;

pub trait Provider: Send + Sync {
    fn stream(
        &self,
        invocation: ModelInvocation,
        cancellation: CancellationToken,
    ) -> ProviderStream;
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

pub(crate) fn is_chatgpt_codex_url(url: &str) -> bool {
    url::Url::parse(url)
        .ok()
        .and_then(|parsed| {
            parsed
                .host_str()
                .map(|host| host.eq_ignore_ascii_case("chatgpt.com"))
        })
        .unwrap_or(false)
}

pub(crate) fn chatgpt_account_id(api_key: &str) -> Option<String> {
    let payload = api_key.split('.').nth(1)?;
    let decoded = base64::engine::general_purpose::URL_SAFE_NO_PAD
        .decode(payload)
        .or_else(|_| base64::engine::general_purpose::STANDARD.decode(payload))
        .ok()?;
    let body: serde_json::Value = serde_json::from_slice(&decoded).ok()?;
    body.get("https://api.openai.com/auth")
        .and_then(|auth| auth.get("chatgpt_account_id"))
        .or_else(|| body.get("chatgpt_account_id"))
        .and_then(serde_json::Value::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToString::to_string)
}

pub(crate) fn apply_chatgpt_codex_headers(
    mut request: reqwest::RequestBuilder,
    request_url: &str,
    api_key: &str,
) -> reqwest::RequestBuilder {
    if !is_chatgpt_codex_url(request_url) {
        return request;
    }
    request = request.header("originator", "codex_cli_rs");
    if let Some(account_id) = chatgpt_account_id(api_key) {
        request = request.header("ChatGPT-Account-Id", account_id);
    }
    request
}

fn apply_chatgpt_codex_body(body: &mut serde_json::Value, request_url: &str) -> Result<()> {
    if !is_chatgpt_codex_url(request_url) {
        return Ok(());
    }
    let object = body
        .as_object_mut()
        .ok_or_else(|| crate::Error::Provider("provider request body must be an object".into()))?;
    object.insert("store".into(), serde_json::Value::Bool(false));
    object.retain(|key, _| {
        matches!(
            key.as_str(),
            "model"
                | "input"
                | "instructions"
                | "stream"
                | "store"
                | "include"
                | "tools"
                | "tool_choice"
                | "reasoning"
                | "previous_response_id"
                | "truncation"
        )
    });
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::{apply_chatgpt_codex_body, chatgpt_account_id, is_chatgpt_codex_url};

    #[test]
    fn chatgpt_codex_url_matches_chatgpt_host() {
        assert!(is_chatgpt_codex_url(
            "https://chatgpt.com/backend-api/codex/responses"
        ));
        assert!(!is_chatgpt_codex_url("https://api.openai.com/v1/chat/completions"));
    }

    #[test]
    fn chatgpt_account_id_reads_jwt_claim() {
        use base64::Engine;
        let payload = base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(
            r#"{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-9"}}"#,
        );
        let token = format!("header.{payload}.sig");
        assert_eq!(chatgpt_account_id(&token).as_deref(), Some("acct-9"));
    }

    #[test]
    fn chatgpt_codex_body_drops_unsupported_platform_fields() {
        let mut body = serde_json::json!({
            "model": "gpt-5.6-luna",
            "input": [{"role": "user", "content": "hi"}],
            "instructions": "",
            "stream": true,
            "include": ["reasoning.encrypted_content"],
            "tools": [],
            "reasoning": {"effort": "high", "summary": "auto"},
            "max_output_tokens": 128000,
            "prompt_cache_key": "cursor-byok",
            "service_tier": "fast"
        });
        apply_chatgpt_codex_body(
            &mut body,
            "https://chatgpt.com/backend-api/codex/responses",
        )
        .unwrap();
        let object = body.as_object().unwrap();
        assert_eq!(object["store"], false);
        assert_eq!(object["model"], "gpt-5.6-luna");
        assert!(object.contains_key("reasoning"));
        assert!(object.contains_key("include"));
        assert!(!object.contains_key("max_output_tokens"));
        assert!(!object.contains_key("prompt_cache_key"));
        assert!(!object.contains_key("service_tier"));
    }

    #[test]
    fn chatgpt_codex_body_leaves_platform_responses_unchanged() {
        let mut body = serde_json::json!({
            "model": "gpt-5.4",
            "max_output_tokens": 4096,
            "prompt_cache_key": "cursor-byok"
        });
        apply_chatgpt_codex_body(&mut body, "https://api.openai.com/v1/responses").unwrap();
        assert_eq!(body["max_output_tokens"], 4096);
        assert!(body.get("store").is_none());
    }
}
