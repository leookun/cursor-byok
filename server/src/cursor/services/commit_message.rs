//! Generates Git commit messages locally for Cursor's SCM action.
//!
//! Cursor sends `aiserver.v1.AiService/WriteGitCommitMessage` with the staged
//! diffs. Empty commit-settings `model_id` keeps the original behaviour and
//! forwards the RPC unchanged (直连). A configured local model identifier
//! answers the request locally: truncated diffs + previous commits form the user
//! message, the customizable commit prompt is the system prompt, and the raw
//! completion is cleaned before being returned.
use std::{
    sync::Arc,
    time::{Duration, Instant},
};

use axum::{
    body::{to_bytes, Body},
    extract::{Extension, State},
    http::{header, HeaderValue, Request, Response, StatusCode},
};
use futures_util::StreamExt;
use prost::Message;
use tokio_util::sync::CancellationToken;

use crate::{
    api::cursor::proxy::{self, CursorProxy},
    cursor::{
        protocol::{connect, proto::aiserver::v1 as ai},
        transport::TransportRegistry,
    },
    model::{
        ContentPart, ModelInvocation, ModelRequest, ModelSpec, ProjectedContent, ProjectedMessage,
        PromptSpec, Role,
    },
    plugin::ADAPTER_ID_PREFIX,
    provider::{ModelEvent, Provider},
    store::CommitSettings,
    Error, Result,
};

const DIFF_TOTAL_LIMIT: usize = 40_000;
const DIFF_SINGLE_LIMIT: usize = 16_000;
const PREVIOUS_COMMIT_LIMIT: usize = 12;
const EXPLICIT_CONTEXT_LIMIT: usize = 20_000;
const GENERATION_TIMEOUT: Duration = Duration::from_secs(180);
const COMMIT_MAX_OUTPUT_TOKENS: u64 = 30_000;

pub async fn write_git_commit_message(
    State(registry): State<TransportRegistry>,
    Extension(upstream): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let settings = registry.store().commit_settings().await?;
    if settings.is_direct() {
        return forward_direct(&registry, upstream, request).await;
    }
    generate_local(&registry, request, settings).await
}

async fn forward_direct(
    registry: &TransportRegistry,
    upstream: CursorProxy,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let settings = registry.store().tab_settings().await?;
    match settings.service_url() {
        Some(service_url) => proxy::forward_to_service(&upstream, request, service_url).await,
        None => proxy::forward(Extension(upstream), request).await,
    }
}

async fn generate_local(
    registry: &TransportRegistry,
    request: Request<Body>,
    settings: CommitSettings,
) -> Result<Response<Body>> {
    let (parts, body) = request.into_parts();
    let connect_timeout_ms = parts
        .headers
        .get("connect-timeout-ms")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse::<u64>().ok());
    tracing::info!(?connect_timeout_ms, "write git commit message received");
    let body = to_bytes(body, usize::MAX)
        .await
        .map_err(|error| Error::Protocol(format!("cannot read request body: {error}")))?;
    let request: ai::WriteGitCommitMessageRequest = connect::decode_unary(&body)?;
    let diffs = truncate_diffs(&request.diffs, DIFF_TOTAL_LIMIT, DIFF_SINGLE_LIMIT);
    if diffs.is_empty() {
        return Err(Error::Protocol("diffs are required".into()));
    }
    let model_id = settings.model_id.trim();
    ensure_configured_model(registry, model_id).await?;
    let invocation = build_invocation(&settings, model_id, build_user_content(&request, &diffs));
    let provider = registry.conversations().dependencies().provider.clone();
    let generated = generate(
        provider,
        invocation,
        connect_timeout_ms.map(Duration::from_millis),
    )
    .await?;
    let commit_message = clean_generated_commit_message(&generated);
    if commit_message.is_empty() {
        return Err(Error::Provider("generated commit message is empty".into()));
    }
    let payload = ai::WriteGitCommitMessageResponse { commit_message }.encode_to_vec();
    let mut response = Response::new(Body::from(payload));
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/proto"),
    );
    Ok(response)
}

async fn ensure_configured_model(registry: &TransportRegistry, model_id: &str) -> Result<()> {
    if model_id.starts_with(ADAPTER_ID_PREFIX) {
        let plugins = registry.plugins().ok_or_else(|| {
            Error::Provider(format!("commit plugin model {model_id} is unavailable"))
        })?;
        plugins.model_descriptor(model_id).await?;
        return Ok(());
    }
    if registry.store().model(model_id).await?.is_some() {
        return Ok(());
    }
    Err(Error::Provider(format!(
        "commit model {model_id} is not configured; select a configured model in the commit settings"
    )))
}

fn build_invocation(
    settings: &CommitSettings,
    model_id: &str,
    user_content: String,
) -> ModelInvocation {
    let call_id = format!("commit-message-{}", uuid::Uuid::new_v4());
    ModelInvocation {
        call_id: call_id.clone(),
        run_id: call_id.clone(),
        conversation_id: call_id,
        prompt_cache_enabled: true,
        provider_call_index: 0,
        request: ModelRequest {
            prompt: PromptSpec {
                instructions: settings.effective_prompt().to_owned(),
                tools: Vec::new(),
            },
            model: ModelSpec {
                max_output_tokens: Some(COMMIT_MAX_OUTPUT_TOKENS),
                ..ModelSpec::new(model_id.to_owned())
            },
            history: vec![ProjectedMessage {
                message_id: "commit-message".into(),
                role: Role::User,
                content: ProjectedContent::Parts(vec![ContentPart::Text { text: user_content }]),
            }],
        },
    }
}

async fn generate(
    provider: Arc<dyn Provider>,
    invocation: ModelInvocation,
    client_timeout: Option<Duration>,
) -> Result<String> {
    let cancellation = CancellationToken::new();
    let stream = provider.stream(invocation, cancellation.clone());
    let mut accumulated = String::new();
    let soft_deadline = client_timeout
        .map(|timeout| timeout.saturating_sub(Duration::from_millis(700)))
        .filter(|deadline| !deadline.is_zero());
    let deadline = Instant::now()
        + soft_deadline
            .unwrap_or(GENERATION_TIMEOUT)
            .min(GENERATION_TIMEOUT);
    let mut deadline_hit = false;
    let completed = tokio::time::timeout(GENERATION_TIMEOUT, async {
        futures_util::pin_mut!(stream);
        let mut finished = false;
        loop {
            let wait = deadline.saturating_duration_since(Instant::now());
            if wait.is_zero() {
                deadline_hit = true;
                break;
            }
            match tokio::time::timeout(wait, stream.next()).await {
                Err(_) => {
                    deadline_hit = true;
                    break;
                }
                Ok(None) => break,
                Ok(Some(event)) => match event? {
                    ModelEvent::TextDelta(delta) => accumulated.push_str(&delta),
                    ModelEvent::ToolCallStart { .. } => {
                        return Err(Error::Provider(
                            "commit message generation must not invoke tools".into(),
                        ));
                    }
                    ModelEvent::Done(_) => {
                        finished = true;
                        break;
                    }
                    _ => {}
                },
            }
        }
        if !finished && !deadline_hit {
            return Err(Error::Provider(
                "provider stream ended without Done during commit message generation".into(),
            ));
        }
        Ok(())
    })
    .await;
    match completed {
        Ok(result) => {
            result?;
            if accumulated.trim().is_empty() {
                return Err(Error::Provider("generated commit message is empty".into()));
            }
            Ok(accumulated)
        }
        Err(_) => {
            cancellation.cancel();
            Err(Error::Provider(
                "commit message generation timed out".into(),
            ))
        }
    }
}

fn build_user_content(request: &ai::WriteGitCommitMessageRequest, diffs: &[String]) -> String {
    let mut sections = vec!["Generate a Git commit message for the following changes.".to_owned()];
    let previous =
        truncate_previous_commits(&request.previous_commit_messages, PREVIOUS_COMMIT_LIMIT);
    if !previous.is_empty() {
        sections.push(format!("Recent commit messages:\n{}", previous.join("\n")));
    }
    if let Some(context) = &request.explicit_context {
        let context_json = explicit_context_json(context);
        if !context_json.is_empty() {
            sections.push(format!("Explicit context:\n{context_json}"));
        }
    }
    let diff_sections: Vec<String> = diffs
        .iter()
        .enumerate()
        .map(|(index, diff)| format!("--- Diff {} ---\n{}", index + 1, diff))
        .collect();
    sections.push(format!("Diffs:\n{}", diff_sections.join("\n\n")));
    sections.join("\n\n")
}

fn explicit_context_json(context: &ai::ExplicitContext) -> String {
    let context_text = context.context.trim();
    let repo_context = context
        .repo_context
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty());
    if context_text.is_empty() && repo_context.is_none() {
        return String::new();
    }
    let mut fields = serde_json::Map::new();
    if !context_text.is_empty() {
        fields.insert(
            "context".into(),
            serde_json::Value::String(context_text.to_owned()),
        );
    }
    if let Some(repo_context) = repo_context {
        fields.insert(
            "repo_context".into(),
            serde_json::Value::String(repo_context.to_owned()),
        );
    }
    truncate_text(
        &serde_json::to_string(&serde_json::Value::Object(fields)).unwrap_or_default(),
        EXPLICIT_CONTEXT_LIMIT,
    )
}

fn truncate_diffs(input: &[String], total_limit: usize, single_limit: usize) -> Vec<String> {
    let mut result = Vec::new();
    let mut remaining = total_limit;
    for raw in input {
        let diff = raw.trim();
        if diff.is_empty() || remaining == 0 {
            continue;
        }
        let truncated = truncate_text(diff, remaining.min(single_limit));
        if truncated.is_empty() {
            continue;
        }
        remaining = remaining.saturating_sub(truncated.chars().count());
        result.push(truncated);
    }
    result
}

fn truncate_previous_commits(input: &[String], limit: usize) -> Vec<String> {
    let mut result = Vec::new();
    for raw in input {
        let value = raw.trim();
        if value.is_empty() {
            continue;
        }
        result.push(format!("- {value}"));
        if result.len() >= limit {
            break;
        }
    }
    result
}

fn truncate_text(value: &str, limit: usize) -> String {
    let trimmed = value.trim();
    if trimmed.is_empty() || limit == 0 {
        return String::new();
    }
    if trimmed.chars().count() <= limit {
        return trimmed.to_owned();
    }
    let truncated: String = trimmed.chars().take(limit).collect();
    format!("{}\n...[truncated]", truncated.trim_end())
}

fn clean_generated_commit_message(value: &str) -> String {
    let mut result = strip_code_fence(value.trim());
    const PREFIXES: [&str; 3] = ["commit message:", "git commit message:", "message:"];
    loop {
        let lower = result.trim().to_ascii_lowercase();
        let Some(prefix) = PREFIXES.iter().find(|prefix| lower.starts_with(*prefix)) else {
            break;
        };
        result = result.trim()[prefix.len()..].to_owned();
    }
    let result = result.trim().to_owned();
    if result.lines().all(|line| line.trim().is_empty()) {
        String::new()
    } else {
        result
    }
}

fn strip_code_fence(value: &str) -> String {
    let trimmed = value.trim();
    if !trimmed.starts_with("```") {
        return trimmed.to_owned();
    }
    let mut lines = trimmed.lines();
    if lines.next().is_none() {
        return trimmed.to_owned();
    }
    let mut body: Vec<&str> = lines.collect();
    if body
        .last()
        .is_some_and(|line| line.trim_start().starts_with("```"))
    {
        body.pop();
    }
    body.join("\n").trim().to_owned()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn empty_model_id_is_direct() {
        assert!(CommitSettings::default().is_direct());
        assert!(CommitSettings {
            model_id: "  ".into(),
            ..CommitSettings::default()
        }
        .is_direct());
        assert!(!CommitSettings {
            model_id: "abc".into(),
            ..CommitSettings::default()
        }
        .is_direct());
    }

    #[test]
    fn truncation_limits_apply_per_diff_and_in_total() {
        let diffs = vec!["a".repeat(20_000), "b".repeat(20_000), "c".repeat(20_000)];
        let truncated = truncate_diffs(&diffs, DIFF_TOTAL_LIMIT, DIFF_SINGLE_LIMIT);
        assert_eq!(truncated.len(), 3);
        let total: usize = truncated.iter().map(|diff| diff.chars().count()).sum();
        assert!(total <= DIFF_TOTAL_LIMIT + 3 * "\n...[truncated]".len());
    }

    #[test]
    fn cleaning_strips_fences_and_prefixes() {
        let raw = "```\nCommit message: fix: 修复登录超时问题\n```";
        assert_eq!(clean_generated_commit_message(raw), "fix: 修复登录超时问题");
    }

    #[test]
    fn cleaning_returns_empty_for_blank_output() {
        assert_eq!(clean_generated_commit_message("  \n "), "");
    }

    #[test]
    fn explicit_context_drops_empty_fields() {
        let empty = explicit_context_json(&ai::ExplicitContext {
            context: "  ".into(),
            repo_context: None,
        });
        assert_eq!(empty, "");
        let filled = explicit_context_json(&ai::ExplicitContext {
            context: "背景".into(),
            repo_context: Some("repo".into()),
        });
        assert_eq!(filled, "{\"context\":\"背景\",\"repo_context\":\"repo\"}");
    }
}
