//! Deduplicates Cursor inputs against their Conversation base.
use crate::{
    model::{CheckpointId, ConversationId},
    Result,
};

use super::{now_ms, Store};

impl Store {
    pub async fn anchor_input(
        &self,
        conversation_id: &ConversationId,
        input_id: &str,
        base_checkpoint_id: CheckpointId,
    ) -> Result<CheckpointId> {
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        sqlx::query(
            "INSERT INTO input_anchors
             (conversation_id, input_id, base_checkpoint_id, created_at_ms)
             VALUES (?, ?, ?, ?)
             ON CONFLICT(conversation_id, input_id) DO NOTHING",
        )
        .bind(conversation_id.as_str())
        .bind(input_id)
        .bind(base_checkpoint_id.0)
        .bind(now_ms())
        .execute(&mut *tx)
        .await?;
        let anchored = sqlx::query_scalar::<_, i64>(
            "SELECT base_checkpoint_id FROM input_anchors
             WHERE conversation_id = ? AND input_id = ?",
        )
        .bind(conversation_id.as_str())
        .bind(input_id)
        .fetch_one(&mut *tx)
        .await?;
        tx.commit().await?;
        session.committed();
        Ok(CheckpointId(anchored))
    }
}
