use std::collections::BTreeMap;

use chrono::{Offset, Utc};
use chrono_tz::Tz;

use crate::{
    cursor::{
        blob_sync::BlobSynchronizer,
        prompting::{Mode, PromptCompiler},
        proto::agent::v1 as pb,
    },
    model::{CanonicalMessage, MessageContent, Origin, Role},
    store::BlobId,
    Error, Result,
};

use super::{context, images};

const RESUME_PROMPT: &str = "<resume>\nContinue the unfinished task from the current conversation state. Use the latest workspace context, make concrete progress toward the user's request, and do not repeat work that is already complete.\n</resume>";

pub(crate) async fn compile_injection(
    injection: &pb::InjectContextAction,
    mode: i32,
    compiler: &PromptCompiler,
    blobs: &BlobSynchronizer,
) -> Result<CanonicalMessage> {
    if injection.injection_id.is_empty() {
        return Err(Error::Protocol(
            "InjectContextAction has no injection_id".into(),
        ));
    }
    let event_id = format!("inject-context:{}", injection.injection_id);
    match injection.payload.as_ref() {
        Some(pb::inject_context_action::Payload::UserContext(context)) => {
            let user = context.user_message.as_ref().ok_or_else(|| {
                Error::Protocol("InjectContextAction UserContext has no UserMessage".into())
            })?;
            if user.message_id.is_empty() {
                return Err(Error::Protocol(
                    "InjectContextAction UserMessage has no message_id".into(),
                ));
            }
            let empty_context = pb::RequestContext::default();
            compile(
                event_id,
                super::prepare::mode_from_proto(mode)?,
                user,
                context.request_context.as_ref().unwrap_or(&empty_context),
                "",
                compiler,
                blobs,
            )
            .await
        }
        Some(pb::inject_context_action::Payload::SystemContext(context)) => {
            Ok(CanonicalMessage {
                message_id: format!("runtime:{event_id}"),
                role: Role::User,
                origin: Origin::Runtime,
                content: MessageContent::Parts {
                    parts: vec![crate::model::ContentPart::Text {
                        text: format!(
                            "<system_context_injection>\n<producer>{}</producer>\n{}\n</system_context_injection>",
                            context.producer, context.content
                        ),
                    }],
                },
                runtime_event_id: Some(event_id),
            })
        }
        None => Err(Error::Protocol(
            "InjectContextAction has no payload".into(),
        )),
    }
}

pub async fn compile(
    event_id: String,
    mode: Mode,
    user: &pb::UserMessage,
    request_context: &pb::RequestContext,
    action_context: &str,
    compiler: &PromptCompiler,
    blobs: &BlobSynchronizer,
) -> Result<CanonicalMessage> {
    let timestamp = Time::now(
        request_context
            .env
            .as_ref()
            .map(|env| env.time_zone.as_str()),
    )?
    .timestamp;
    compile_with_timestamp(
        event_id,
        mode,
        user,
        request_context,
        action_context,
        timestamp,
        compiler,
        blobs,
    )
    .await
}

#[allow(clippy::too_many_arguments)]
pub(super) async fn user_event_id(
    input_id: &str,
    mode: Mode,
    user: &pb::UserMessage,
    request_context: &pb::RequestContext,
    action_context: &str,
    projected_request_context: Option<&MessageContent>,
    compiler: &PromptCompiler,
    blobs: &BlobSynchronizer,
) -> Result<String> {
    let runtime = compile_with_timestamp(
        "identity".into(),
        mode,
        user,
        request_context,
        action_context,
        String::new(),
        compiler,
        blobs,
    )
    .await?;
    let semantic = serde_json::to_vec(&(projected_request_context, runtime.content))?;
    Ok(format!(
        "{input_id}:{}",
        BlobId::digest(&semantic).to_base64()
    ))
}

#[allow(clippy::too_many_arguments)]
async fn compile_with_timestamp(
    event_id: String,
    mode: Mode,
    user: &pb::UserMessage,
    request_context: &pb::RequestContext,
    action_context: &str,
    timestamp: String,
    compiler: &PromptCompiler,
    blobs: &BlobSynchronizer,
) -> Result<CanonicalMessage> {
    let mut values = BTreeMap::from([
        ("OPEN_FILES", section(open_files(user))),
        (
            "SELECTED_CONTEXT",
            section(
                context::selected_context(user)
                    .filter(|value| !value.is_empty())
                    .map(|value| format!("<selected_context>\n{value}\n</selected_context>"))
                    .unwrap_or_default(),
            ),
        ),
        ("ACTION_CONTEXT", section(action_context.to_string())),
        ("TIMESTAMP", timestamp),
        ("USER_QUERY", user.text.clone()),
        ("DEBUG_SERVER_ENDPOINT", String::new()),
        ("DEBUG_LOG_PATH", String::new()),
        ("DEBUG_SESSION_ID", String::new()),
    ]);
    if let Some(debug) = &request_context.debug_mode_config {
        values.insert("DEBUG_SERVER_ENDPOINT", debug.server_endpoint.clone());
        values.insert("DEBUG_LOG_PATH", debug.log_path.clone());
        values.insert("DEBUG_SESSION_ID", debug.session_id.clone());
    }
    message(
        event_id,
        user,
        compiler.runtime_message(mode, &values)?,
        blobs,
    )
    .await
}

pub(super) fn compile_request_context(
    event_id: &str,
    request_context: &pb::RequestContext,
    history: &[CanonicalMessage],
) -> Result<Option<CanonicalMessage>> {
    let time = Time::now(
        request_context
            .env
            .as_ref()
            .map(|env| env.time_zone.as_str()),
    )?;
    let text = context::compile_context(request_context, &time.today);
    if text.is_empty() {
        return Ok(None);
    }
    let message = CanonicalMessage::text(
        format!("request-context:{event_id}"),
        Role::User,
        Origin::Prompt,
        text,
    );
    Ok(should_project_request_context(history, &message).then_some(message))
}

pub(super) fn compile_resume_messages(
    request_id: &str,
    request_context: &pb::RequestContext,
    history: &[CanonicalMessage],
    has_pending_tool_round: bool,
) -> Result<Vec<CanonicalMessage>> {
    let event_id = format!("resume:{request_id}");
    let mut messages = compile_request_context(&event_id, request_context, history)?
        .into_iter()
        .collect::<Vec<_>>();
    if !has_pending_tool_round {
        messages.push(CanonicalMessage::text(
            format!("resume-prompt:{request_id}"),
            Role::User,
            Origin::Prompt,
            RESUME_PROMPT,
        ));
    }
    Ok(messages)
}

fn should_project_request_context(
    history: &[CanonicalMessage],
    current: &CanonicalMessage,
) -> bool {
    history
        .iter()
        .rev()
        .find(|message| message.message_id.starts_with("request-context:"))
        .is_none_or(|previous| previous.content != current.content)
}

pub async fn compile_background(
    event_id: String,
    user: &pb::UserMessage,
    request_context: &pb::RequestContext,
    action_context: &str,
    blobs: &BlobSynchronizer,
) -> Result<(CanonicalMessage, String)> {
    let timestamp = Time::now(
        request_context
            .env
            .as_ref()
            .map(|env| env.time_zone.as_str()),
    )?
    .timestamp;
    let text = format!(
        "<timestamp>{timestamp}</timestamp>\n{}\n<user_query>{}</user_query>",
        action_context.trim(),
        user.text
    );
    let message = message(event_id, user, text.clone(), blobs).await?;
    Ok((message, text))
}

async fn message(
    event_id: String,
    user: &pb::UserMessage,
    text: String,
    blobs: &BlobSynchronizer,
) -> Result<CanonicalMessage> {
    Ok(CanonicalMessage {
        message_id: format!("runtime:{event_id}"),
        role: Role::User,
        origin: Origin::Runtime,
        content: MessageContent::Parts {
            parts: images::parts(user, text, blobs).await?,
        },
        runtime_event_id: Some(event_id),
    })
}

fn section(value: String) -> String {
    let value = value.trim();
    if value.is_empty() {
        String::new()
    } else {
        format!("{value}\n\n")
    }
}

fn open_files(user: &pb::UserMessage) -> String {
    let Some(ide) = user
        .selected_context
        .as_ref()
        .and_then(|selected| selected.invocation_context.as_ref())
        .and_then(|invocation| invocation.data.as_ref())
        .and_then(|data| match data {
            pb::invocation_context::Data::IdeState(ide) => Some(ide),
            _ => None,
        })
    else {
        return String::new();
    };
    if ide.visible_files.is_empty() && ide.recently_viewed_files.is_empty() {
        return String::new();
    }

    let mut output = String::from("<open_and_recently_viewed_files>\n");
    if !ide.recently_viewed_files.is_empty() {
        output.push_str("Recently viewed files (recent at the top, oldest at the bottom):\n");
        for file in &ide.recently_viewed_files {
            output.push_str(&format!(
                "- {} (total lines: {})\n",
                file.path, file.total_lines
            ));
        }
        output.push('\n');
    }
    if !ide.visible_files.is_empty() {
        output.push_str("Files that are currently open and visible in the user's IDE:\n");
        for (index, file) in ide.visible_files.iter().enumerate() {
            output.push_str(&format!("- {} (", file.path));
            if index == 0 {
                output.push_str("currently focused file");
                if let Some(cursor) = &file.cursor_position {
                    output.push_str(&format!(", cursor is on line {}", cursor.line));
                }
                output.push_str(&format!(", total lines: {}", file.total_lines));
            } else {
                output.push_str(&format!("total lines: {}", file.total_lines));
            }
            output.push_str(")\n");
        }
        output.push('\n');
    }
    output.push_str(
        "Note: these files may or may not be relevant to the current conversation. Use the read file tool if you need to get the contents of some of them.\n</open_and_recently_viewed_files>",
    );
    output
}

struct Time {
    timestamp: String,
    today: String,
}

impl Time {
    fn now(time_zone: Option<&str>) -> Result<Self> {
        let zone = match time_zone.filter(|value| !value.is_empty()) {
            Some(value) => value
                .parse::<Tz>()
                .map_err(|_| Error::Protocol(format!("invalid Cursor time zone: {value}")))?,
            None => chrono_tz::UTC,
        };
        let now = Utc::now().with_timezone(&zone);
        let offset = now.offset().fix().local_minus_utc();
        let sign = if offset < 0 { '-' } else { '+' };
        let offset = offset.unsigned_abs();
        let hours = offset / 3600;
        let minutes = (offset % 3600) / 60;
        let utc = if minutes == 0 {
            format!("UTC{sign}{hours}")
        } else {
            format!("UTC{sign}{hours}:{minutes:02}")
        };
        Ok(Self {
            timestamp: format!("{} ({utc})", now.format("%A, %b %-d, %Y, %-I:%M %p")),
            today: now.format("%A %b %-d,\n%Y").to_string(),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn request_context_is_only_projected_when_its_content_changes() {
        let first = CanonicalMessage::text(
            "request-context:first",
            Role::User,
            Origin::Prompt,
            "<rules>same</rules>",
        );
        let duplicate = CanonicalMessage::text(
            "request-context:second",
            Role::User,
            Origin::Prompt,
            "<rules>same</rules>",
        );
        let changed = CanonicalMessage::text(
            "request-context:third",
            Role::User,
            Origin::Prompt,
            "<rules>changed</rules>",
        );
        let runtime = CanonicalMessage::text(
            "runtime:turn",
            Role::User,
            Origin::Runtime,
            "<user_query>next</user_query>",
        );

        assert!(should_project_request_context(&[], &first));
        assert!(!should_project_request_context(
            &[first.clone(), runtime.clone()],
            &duplicate
        ));
        assert!(should_project_request_context(
            &[first.clone(), runtime.clone()],
            &changed
        ));
        assert!(should_project_request_context(
            &[first.clone(), changed, runtime],
            &CanonicalMessage::text(
                "request-context:fourth",
                Role::User,
                Origin::Prompt,
                "<rules>same</rules>",
            )
        ));
    }
}
