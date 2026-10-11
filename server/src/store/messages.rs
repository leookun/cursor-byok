//! Persists canonical Messages and enforces event idempotency.
use sqlx::{Row, SqliteConnection};

use crate::{
    model::{CanonicalMessage, ConversationId},
    Error, Result,
};

use super::{now_ms, Store};

impl Store {
    pub async fn message(
        &self,
        conversation_id: &ConversationId,
        message_id: &str,
    ) -> Result<Option<CanonicalMessage>> {
        let payload: Option<String> = sqlx::query_scalar(
            "SELECT payload_json FROM messages WHERE conversation_id = ? AND message_id = ?",
        )
        .bind(conversation_id.as_str())
        .bind(message_id)
        .fetch_optional(&self.pool)
        .await?;
        payload
            .map(|payload| serde_json::from_str(&payload).map_err(Into::into))
            .transpose()
    }

    pub(crate) async fn put_message_tx(
        tx: &mut SqliteConnection,
        conversation_id: &ConversationId,
        message: &CanonicalMessage,
    ) -> Result<()> {
        let payload = serde_json::to_string(message)?;
        let inserted = sqlx::query(
            "INSERT OR IGNORE INTO messages
             (conversation_id, message_id, role, origin, payload_json, runtime_event_id, created_at_ms)
             VALUES (?, ?, ?, ?, ?, ?, ?)",
        )
        .bind(conversation_id.as_str())
        .bind(&message.message_id)
        .bind(role_name(&message.role))
        .bind(origin_name(&message.origin))
        .bind(&payload)
        .bind(&message.runtime_event_id)
        .bind(now_ms())
        .execute(&mut *tx)
        .await?
        .rows_affected()
            == 1;
        if inserted {
            return Ok(());
        }

        let existing: Option<String> = sqlx::query_scalar(
            "SELECT payload_json FROM messages
             WHERE conversation_id = ? AND (message_id = ? OR runtime_event_id = ?)",
        )
        .bind(conversation_id.as_str())
        .bind(&message.message_id)
        .bind(&message.runtime_event_id)
        .fetch_optional(&mut *tx)
        .await?;
        match existing {
            Some(existing) if existing == payload => Ok(()),
            Some(_) => Err(Error::Store(format!(
                "message id or runtime event reused with different content: {}",
                message.message_id
            ))),
            None => Err(Error::Store(format!(
                "message insert was ignored without an existing object: {}",
                message.message_id
            ))),
        }
    }

    pub(crate) async fn load_checkpoint_messages_tx(
        tx: &mut SqliteConnection,
        checkpoint_id: i64,
    ) -> Result<Vec<CanonicalMessage>> {
        let rows = sqlx::query(
            "WITH RECURSIVE lineage(checkpoint_id, parent_checkpoint_id, depth) AS (
                 SELECT checkpoint_id, parent_checkpoint_id, 0
                 FROM conversation_checkpoints WHERE checkpoint_id = ?
                 UNION ALL
                 SELECT r.checkpoint_id, r.parent_checkpoint_id, lineage.depth + 1
                 FROM conversation_checkpoints r
                 JOIN lineage ON r.checkpoint_id = lineage.parent_checkpoint_id
             )
             SELECT m.payload_json
             FROM lineage
             JOIN checkpoint_messages rm ON rm.checkpoint_id = lineage.checkpoint_id
             JOIN messages m
               ON m.conversation_id = rm.conversation_id AND m.message_id = rm.message_id
             ORDER BY lineage.depth DESC, rm.ordinal ASC",
        )
        .bind(checkpoint_id)
        .fetch_all(&mut *tx)
        .await?;
        rows.into_iter()
            .map(|row| serde_json::from_str(row.get::<&str, _>(0)).map_err(Into::into))
            .collect()
    }
}

fn role_name(role: &crate::model::Role) -> &'static str {
    match role {
        crate::model::Role::System => "system",
        crate::model::Role::User => "user",
        crate::model::Role::Assistant => "assistant",
        crate::model::Role::Tool => "tool",
    }
}

fn origin_name(origin: &crate::model::Origin) -> &'static str {
    match origin {
        crate::model::Origin::Prompt => "prompt",
        crate::model::Origin::User => "user",
        crate::model::Origin::Runtime => "runtime",
        crate::model::Origin::Assistant => "assistant",
        crate::model::Origin::Tool => "tool",
    }
}
