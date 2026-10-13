//! Defines provider-independent model requests and streaming responses.
use serde::{Deserialize, Serialize};

use super::{ModelSpec, ProjectedContent, ProjectedMessage, ToolDefinition};

const PROVIDER_TOOL_CALL_ID_MAX_CHARS: usize = 64;

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct PromptSpec {
    pub instructions: String,
    pub tools: Vec<ToolDefinition>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct ModelRequest {
    pub prompt: PromptSpec,
    pub model: ModelSpec,
    pub history: Vec<ProjectedMessage>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct ModelInvocation {
    pub call_id: String,
    pub run_id: String,
    pub conversation_id: String,
    pub prompt_cache_enabled: bool,
    pub provider_call_index: u64,
    pub request: ModelRequest,
}

impl ModelInvocation {
    /// Stable across turns, tool rounds, and retries within one conversation.
    pub(crate) fn cache_key(&self) -> Option<String> {
        self.prompt_cache_enabled
            .then(|| format!("cursor:{}", self.conversation_id))
    }
}

pub(crate) fn normalize_provider_tool_call_ids(history: &mut [ProjectedMessage]) {
    for message in history {
        match &mut message.content {
            ProjectedContent::Assistant { calls, .. } => {
                for call in calls {
                    truncate_tool_call_id(&mut call.call_id);
                }
            }
            ProjectedContent::ToolResult(result) => {
                truncate_tool_call_id(&mut result.call_id);
            }
            ProjectedContent::Parts(_) => {}
        }
    }
}

fn truncate_tool_call_id(call_id: &mut String) {
    if let Some((end, _)) = call_id.char_indices().nth(PROVIDER_TOOL_CALL_ID_MAX_CHARS) {
        call_id.truncate(end);
    }
}
