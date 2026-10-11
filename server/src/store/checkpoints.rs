//! Persists immutable internal Conversation checkpoints.
use sha2::{Digest, Sha256};
use sqlx::SqliteConnection;

use crate::{
    model::{CanonicalMessage, CheckpointId, ConversationId, RunId},
    Error, Result,
};

use super::{now_ms, Store};

impl Store {
    pub async fn ensure_conversation(
        &self,
        conversation_id: &ConversationId,
    ) -> Result<CheckpointId> {
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        let checkpoint = Self::ensure_conversation_tx(&mut tx, conversation_id).await?;
        tx.commit().await?;
        session.committed();
        Ok(checkpoint)
    }

    pub async fn load_checkpoint_messages(
        &self,
        checkpoint_id: CheckpointId,
    ) -> Result<Vec<CanonicalMessage>> {
        let mut connection = self.pool.acquire().await?;
        Self::load_checkpoint_messages_tx(&mut connection, checkpoint_id.0).await
    }

    pub async fn checkpoint_parent(
        &self,
        checkpoint_id: CheckpointId,
    ) -> Result<Option<CheckpointId>> {
        let parent = sqlx::query_scalar::<_, Option<i64>>(
            "SELECT parent_checkpoint_id FROM conversation_checkpoints WHERE checkpoint_id = ?",
        )
        .bind(checkpoint_id.0)
        .fetch_optional(&self.pool)
        .await?
        .flatten()
        .map(CheckpointId);
        Ok(parent)
    }

    pub async fn load_current_messages(
        &self,
        conversation_id: &ConversationId,
    ) -> Result<Vec<CanonicalMessage>> {
        let Some(checkpoint_id) = sqlx::query_scalar::<_, i64>(
            "SELECT current_checkpoint_id FROM conversations WHERE conversation_id = ?",
        )
        .bind(conversation_id.as_str())
        .fetch_optional(&self.pool)
        .await?
        else {
            return Ok(Vec::new());
        };
        self.load_checkpoint_messages(CheckpointId(checkpoint_id))
            .await
    }

    pub async fn match_checkpoint_prefix(
        &self,
        conversation_id: &ConversationId,
        base_checkpoint_id: CheckpointId,
        additions: &[CanonicalMessage],
    ) -> Result<(CheckpointId, usize)> {
        let mut checkpoint = base_checkpoint_id;
        let mut messages = self.load_checkpoint_messages(checkpoint).await?;
        for (index, addition) in additions.iter().enumerate() {
            messages.push(addition.clone());
            let digest = message_digest(&messages)?;
            let child = sqlx::query_scalar::<_, i64>(
                "SELECT checkpoint_id FROM conversation_checkpoints
                 WHERE conversation_id = ? AND parent_checkpoint_id = ? AND state_digest = ?",
            )
            .bind(conversation_id.as_str())
            .bind(checkpoint.0)
            .bind(digest.as_slice())
            .fetch_optional(&self.pool)
            .await?
            .map(CheckpointId);
            let Some(child) = child else {
                return Ok((checkpoint, index));
            };
            if self.load_checkpoint_messages(child).await? != messages {
                return Ok((checkpoint, index));
            }
            checkpoint = child;
        }
        Ok((checkpoint, additions.len()))
    }

    pub async fn import_checkpoint(
        &self,
        conversation_id: &ConversationId,
        messages: &[CanonicalMessage],
    ) -> Result<CheckpointId> {
        let digest = message_digest(messages)?;
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        let current = Self::ensure_conversation_tx(&mut tx, conversation_id).await?;
        if let Some(existing) = sqlx::query_scalar::<_, i64>(
            "SELECT checkpoint_id FROM conversation_checkpoints
             WHERE conversation_id = ? AND state_digest = ?",
        )
        .bind(conversation_id.as_str())
        .bind(digest.as_slice())
        .fetch_optional(&mut *tx)
        .await?
        {
            tx.commit().await?;
            session.committed();
            return Ok(CheckpointId(existing));
        }

        let current_messages = Self::load_checkpoint_messages_tx(&mut tx, current.0).await?;
        let (parent, additions) = if messages.starts_with(&current_messages) {
            (current, &messages[current_messages.len()..])
        } else {
            let root: i64 = sqlx::query_scalar(
                "SELECT checkpoint_id FROM conversation_checkpoints
                 WHERE conversation_id = ? AND parent_checkpoint_id IS NULL",
            )
            .bind(conversation_id.as_str())
            .fetch_one(&mut *tx)
            .await?;
            (CheckpointId(root), messages)
        };
        let checkpoint =
            Self::insert_checkpoint_tx(&mut tx, conversation_id, parent, additions, digest).await?;
        tx.commit().await?;
        session.committed();
        Ok(checkpoint)
    }

    pub async fn append_checkpoint(
        &self,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: CheckpointId,
        additions: &[CanonicalMessage],
    ) -> Result<CheckpointId> {
        if additions.is_empty() {
            return Ok(expected);
        }
        let mut full = self.load_checkpoint_messages(expected).await?;
        full.extend_from_slice(additions);
        let digest = message_digest(&full)?;
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        let checkpoint = Self::append_checkpoint_with_digest_tx(
            &mut tx,
            conversation_id,
            run_id,
            expected,
            additions,
            digest,
        )
        .await?;
        tx.commit().await?;
        session.committed();
        Ok(checkpoint)
    }

    pub async fn replace_checkpoint(
        &self,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: CheckpointId,
        messages: &[CanonicalMessage],
    ) -> Result<CheckpointId> {
        let digest = message_digest(messages)?;
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        Self::require_active_head_tx(&mut tx, conversation_id, run_id, expected).await?;
        let root: i64 = sqlx::query_scalar(
            "SELECT checkpoint_id FROM conversation_checkpoints
             WHERE conversation_id = ? AND parent_checkpoint_id IS NULL",
        )
        .bind(conversation_id.as_str())
        .fetch_one(&mut *tx)
        .await?;
        let checkpoint = Self::insert_checkpoint_tx(
            &mut tx,
            conversation_id,
            CheckpointId(root),
            messages,
            digest,
        )
        .await?;
        let updated = sqlx::query(
            "UPDATE conversations SET current_checkpoint_id = ?, updated_at_ms = ?
             WHERE conversation_id = ? AND current_checkpoint_id = ? AND active_run_id = ?",
        )
        .bind(checkpoint.0)
        .bind(now_ms())
        .bind(conversation_id.as_str())
        .bind(expected.0)
        .bind(run_id.as_str())
        .execute(&mut *tx)
        .await?
        .rows_affected();
        if updated != 1 {
            return Err(Error::Store(format!(
                "lost active ownership while replacing checkpoint for run {run_id}"
            )));
        }
        sqlx::query("UPDATE runs SET head_checkpoint_id = ?, updated_at_ms = ? WHERE run_id = ?")
            .bind(checkpoint.0)
            .bind(now_ms())
            .bind(run_id.as_str())
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        session.committed();
        Ok(checkpoint)
    }

    pub async fn append_message_once(
        &self,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: CheckpointId,
        message: &CanonicalMessage,
    ) -> Result<(CheckpointId, bool)> {
        let existing = self.load_checkpoint_messages(expected).await?;
        if let Some(existing) = existing.iter().find(|existing| {
            existing.message_id == message.message_id
                || message.runtime_event_id.is_some()
                    && existing.runtime_event_id == message.runtime_event_id
        }) {
            return if existing == message {
                Ok((expected, false))
            } else {
                Err(Error::Store(format!(
                    "message id or runtime event reused with different content: {}",
                    message.message_id
                )))
            };
        }
        Ok((
            self.append_checkpoint(
                conversation_id,
                run_id,
                expected,
                std::slice::from_ref(message),
            )
            .await?,
            true,
        ))
    }

    pub(crate) async fn append_checkpoint_tx(
        tx: &mut SqliteConnection,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: CheckpointId,
        additions: &[CanonicalMessage],
    ) -> Result<CheckpointId> {
        let mut full = Self::load_checkpoint_messages_tx(tx, expected.0).await?;
        full.extend_from_slice(additions);
        let digest = message_digest(&full)?;
        Self::append_checkpoint_with_digest_tx(
            tx,
            conversation_id,
            run_id,
            expected,
            additions,
            digest,
        )
        .await
    }

    async fn append_checkpoint_with_digest_tx(
        tx: &mut SqliteConnection,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: CheckpointId,
        additions: &[CanonicalMessage],
        digest: [u8; 32],
    ) -> Result<CheckpointId> {
        Self::require_active_head_tx(tx, conversation_id, run_id, expected).await?;
        if sqlx::query_scalar::<_, i64>(
            "SELECT checkpoint_id FROM conversation_checkpoints
             WHERE conversation_id = ? AND state_digest = ?",
        )
        .bind(conversation_id.as_str())
        .bind(digest.as_slice())
        .fetch_optional(&mut *tx)
        .await?
        .is_some()
        {
            return Err(Error::Store(
                "active append would reuse an existing checkpoint instead of creating a child"
                    .into(),
            ));
        }
        let checkpoint =
            Self::insert_checkpoint_tx(tx, conversation_id, expected, additions, digest).await?;
        let updated = sqlx::query(
            "UPDATE conversations SET current_checkpoint_id = ?, updated_at_ms = ?
             WHERE conversation_id = ? AND current_checkpoint_id = ? AND active_run_id = ?",
        )
        .bind(checkpoint.0)
        .bind(now_ms())
        .bind(conversation_id.as_str())
        .bind(expected.0)
        .bind(run_id.as_str())
        .execute(&mut *tx)
        .await?
        .rows_affected();
        if updated != 1 {
            return Err(Error::Store(format!(
                "lost active ownership while appending checkpoint for run {run_id}"
            )));
        }
        sqlx::query("UPDATE runs SET head_checkpoint_id = ?, updated_at_ms = ? WHERE run_id = ?")
            .bind(checkpoint.0)
            .bind(now_ms())
            .bind(run_id.as_str())
            .execute(&mut *tx)
            .await?;
        Ok(checkpoint)
    }

    async fn insert_checkpoint_tx(
        tx: &mut SqliteConnection,
        conversation_id: &ConversationId,
        parent: CheckpointId,
        additions: &[CanonicalMessage],
        digest: [u8; 32],
    ) -> Result<CheckpointId> {
        for message in additions {
            Self::put_message_tx(tx, conversation_id, message).await?;
        }
        let checkpoint = sqlx::query(
            "INSERT INTO conversation_checkpoints
             (conversation_id, parent_checkpoint_id, state_digest, created_at_ms)
             VALUES (?, ?, ?, ?)",
        )
        .bind(conversation_id.as_str())
        .bind(parent.0)
        .bind(digest.as_slice())
        .bind(now_ms())
        .execute(&mut *tx)
        .await?
        .last_insert_rowid();
        for (ordinal, message) in additions.iter().enumerate() {
            sqlx::query(
                "INSERT INTO checkpoint_messages(checkpoint_id, ordinal, conversation_id, message_id)
                 VALUES (?, ?, ?, ?)",
            )
            .bind(checkpoint)
            .bind(ordinal as i64)
            .bind(conversation_id.as_str())
            .bind(&message.message_id)
            .execute(&mut *tx)
            .await?;
        }
        Ok(CheckpointId(checkpoint))
    }
}

pub(crate) fn message_digest(messages: &[CanonicalMessage]) -> Result<[u8; 32]> {
    let mut hasher = Sha256::new();
    for message in messages {
        let bytes = serde_json::to_vec(message)?;
        hasher.update((bytes.len() as u64).to_be_bytes());
        hasher.update(bytes);
    }
    Ok(hasher.finalize().into())
}
