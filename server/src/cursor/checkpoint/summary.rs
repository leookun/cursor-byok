use prost::Message;

use crate::{
    cursor::{presentation::PresentationDelta, proto::agent::v1 as pb},
    model::{estimate_context_tokens, CanonicalMessage, PromptSpec},
    store::{BlobEdge, BlobId},
    Error, Result,
};

use super::CheckpointBuilder;

impl CheckpointBuilder {
    pub async fn compacted(
        &mut self,
        messages: &[CanonicalMessage],
        mode: i32,
        summary: &str,
        presentation: &PresentationDelta,
    ) -> Result<pb::ConversationStateStructure> {
        let summarized = self
            .base
            .root_prompt_messages_json
            .iter()
            .skip(1)
            .map(|id| BlobId::from_bytes(id))
            .collect::<Result<Vec<_>>>()?;
        let root_ids = self.replace_roots(messages).await?;
        let summary_message = root_ids
            .last()
            .filter(|_| root_ids.len() >= 2)
            .ok_or_else(|| Error::Protocol("compaction produced no summary root".into()))?
            .clone();

        let summary_id = self
            .sync
            .persist(
                &pb::ConversationSummary {
                    summary: summary.into(),
                }
                .encode_to_vec(),
                &[],
            )
            .await?;
        let archive = pb::ConversationSummaryArchive {
            summarized_messages: summarized.iter().map(|id| id.as_bytes().to_vec()).collect(),
            summary: summary.into(),
            window_tail: 0,
            summary_message: summary_message.as_bytes().to_vec(),
        };
        let mut edges = summarized
            .iter()
            .enumerate()
            .map(|(index, child)| BlobEdge {
                child: child.clone(),
                field_name: format!("summarized_messages[{index}]"),
            })
            .collect::<Vec<_>>();
        edges.push(BlobEdge {
            child: summary_message,
            field_name: "summary_message".into(),
        });
        let archive_id = self.sync.persist(&archive.encode_to_vec(), &edges).await?;
        let turn_ids = self.project_turns(mode, presentation).await?;

        for path in &presentation.read_paths {
            if !self.base.read_paths.contains(path) {
                self.base.read_paths.push(path.clone());
            }
        }
        self.base.root_prompt_messages_json =
            root_ids.iter().map(|id| id.as_bytes().to_vec()).collect();
        self.base.turns = turn_ids.iter().map(|id| id.as_bytes().to_vec()).collect();
        self.base.pending_tool_calls.clear();
        self.base.mode = Some(mode);
        self.base.summary = Some(summary_id.as_bytes().to_vec());
        self.base.summary_archive = Some(archive_id.as_bytes().to_vec());
        if !self
            .base
            .summary_archives
            .contains(&archive_id.as_bytes().to_vec())
        {
            self.base
                .summary_archives
                .push(archive_id.as_bytes().to_vec());
        }
        self.base.self_summary_count = self.base.self_summary_count.saturating_add(1);
        let compacted_prompt = PromptSpec {
            instructions: self.instructions.clone(),
            tools: self.tool_definitions.clone(),
        };
        self.record_context_tokens(Some(estimate_context_tokens(&compacted_prompt, messages)));
        if let Some(details) = self.base.token_details.as_mut() {
            details.breakdown = Some(crate::cursor::usage::breakdown(
                details.used_tokens,
                details.max_tokens,
                details.breakdown.as_ref(),
                &self.instructions,
                &self.tool_definitions,
                &self.dynamic_tools,
                messages,
            )?);
        }
        Ok(self.base.clone())
    }
}
