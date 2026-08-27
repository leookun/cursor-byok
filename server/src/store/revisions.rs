use sha2::{Digest, Sha256};
use sqlx::{Sqlite, Transaction};

use crate::{
    model::{CanonicalMessage, ConversationId, RevisionId, RunId},
    Error, Result,
};

use super::{now_ms, Store};

impl Store {
    pub async fn ensure_conversation(
        &self,
        conversation_id: &ConversationId,
    ) -> Result<RevisionId> {
        let _write = self.writes.lock().await;
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        let revision = Self::ensure_conversation_tx(&mut tx, conversation_id).await?;
        tx.commit().await?;
        Ok(revision)
    }

    pub async fn load_revision_messages(
        &self,
        revision_id: RevisionId,
    ) -> Result<Vec<CanonicalMessage>> {
        let mut tx = self.pool.begin().await?;
        let messages = Self::load_revision_messages_tx(&mut tx, revision_id.0).await?;
        tx.commit().await?;
        Ok(messages)
    }

    pub async fn revision_parent(&self, revision_id: RevisionId) -> Result<Option<RevisionId>> {
        let parent = sqlx::query_scalar::<_, Option<i64>>(
            "SELECT parent_revision_id FROM conversation_revisions WHERE revision_id = ?",
        )
        .bind(revision_id.0)
        .fetch_optional(&self.pool)
        .await?
        .flatten()
        .map(RevisionId);
        Ok(parent)
    }

    pub async fn load_current_messages(
        &self,
        conversation_id: &ConversationId,
    ) -> Result<Vec<CanonicalMessage>> {
        let Some(revision_id) = sqlx::query_scalar::<_, i64>(
            "SELECT current_revision_id FROM conversations WHERE conversation_id = ?",
        )
        .bind(conversation_id.as_str())
        .fetch_optional(&self.pool)
        .await?
        else {
            return Ok(Vec::new());
        };
        self.load_revision_messages(RevisionId(revision_id)).await
    }

    pub async fn match_revision_prefix(
        &self,
        conversation_id: &ConversationId,
        base_revision_id: RevisionId,
        additions: &[CanonicalMessage],
    ) -> Result<(RevisionId, usize)> {
        let mut revision = base_revision_id;
        let mut messages = self.load_revision_messages(revision).await?;
        for (index, addition) in additions.iter().enumerate() {
            messages.push(addition.clone());
            let digest = message_digest(&messages)?;
            let child = sqlx::query_scalar::<_, i64>(
                "SELECT revision_id FROM conversation_revisions
                 WHERE conversation_id = ? AND parent_revision_id = ? AND state_digest = ?",
            )
            .bind(conversation_id.as_str())
            .bind(revision.0)
            .bind(digest.as_slice())
            .fetch_optional(&self.pool)
            .await?
            .map(RevisionId);
            let Some(child) = child else {
                return Ok((revision, index));
            };
            if self.load_revision_messages(child).await? != messages {
                return Ok((revision, index));
            }
            revision = child;
        }
        Ok((revision, additions.len()))
    }

    pub async fn import_revision(
        &self,
        conversation_id: &ConversationId,
        messages: &[CanonicalMessage],
    ) -> Result<RevisionId> {
        let digest = message_digest(messages)?;
        let _write = self.writes.lock().await;
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        let current = Self::ensure_conversation_tx(&mut tx, conversation_id).await?;
        if let Some(existing) = sqlx::query_scalar::<_, i64>(
            "SELECT revision_id FROM conversation_revisions
             WHERE conversation_id = ? AND state_digest = ?",
        )
        .bind(conversation_id.as_str())
        .bind(digest.as_slice())
        .fetch_optional(&mut *tx)
        .await?
        {
            tx.commit().await?;
            return Ok(RevisionId(existing));
        }

        let current_messages = Self::load_revision_messages_tx(&mut tx, current.0).await?;
        let (parent, additions) = if messages.starts_with(&current_messages) {
            (current, &messages[current_messages.len()..])
        } else {
            let root: i64 = sqlx::query_scalar(
                "SELECT revision_id FROM conversation_revisions
                 WHERE conversation_id = ? AND parent_revision_id IS NULL",
            )
            .bind(conversation_id.as_str())
            .fetch_one(&mut *tx)
            .await?;
            (RevisionId(root), messages)
        };
        let revision =
            Self::insert_revision_tx(&mut tx, conversation_id, parent, additions, digest).await?;
        tx.commit().await?;
        Ok(revision)
    }

    pub async fn append_revision(
        &self,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: RevisionId,
        additions: &[CanonicalMessage],
    ) -> Result<RevisionId> {
        if additions.is_empty() {
            return Ok(expected);
        }
        let mut full = self.load_revision_messages(expected).await?;
        full.extend_from_slice(additions);
        let digest = message_digest(&full)?;
        let _write = self.writes.lock().await;
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        let revision = Self::append_revision_with_digest_tx(
            &mut tx,
            conversation_id,
            run_id,
            expected,
            additions,
            digest,
        )
        .await?;
        tx.commit().await?;
        Ok(revision)
    }

    pub async fn replace_revision(
        &self,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: RevisionId,
        messages: &[CanonicalMessage],
    ) -> Result<RevisionId> {
        let digest = message_digest(messages)?;
        let _write = self.writes.lock().await;
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        Self::require_active_head_tx(&mut tx, conversation_id, run_id, expected).await?;
        let root: i64 = sqlx::query_scalar(
            "SELECT revision_id FROM conversation_revisions
             WHERE conversation_id = ? AND parent_revision_id IS NULL",
        )
        .bind(conversation_id.as_str())
        .fetch_one(&mut *tx)
        .await?;
        let revision =
            Self::insert_revision_tx(&mut tx, conversation_id, RevisionId(root), messages, digest)
                .await?;
        let updated = sqlx::query(
            "UPDATE conversations SET current_revision_id = ?, updated_at_ms = ?
             WHERE conversation_id = ? AND current_revision_id = ? AND active_run_id = ?",
        )
        .bind(revision.0)
        .bind(now_ms())
        .bind(conversation_id.as_str())
        .bind(expected.0)
        .bind(run_id.as_str())
        .execute(&mut *tx)
        .await?
        .rows_affected();
        if updated != 1 {
            return Err(Error::Store(format!(
                "lost active ownership while replacing revision for run {run_id}"
            )));
        }
        sqlx::query("UPDATE runs SET head_revision_id = ?, updated_at_ms = ? WHERE run_id = ?")
            .bind(revision.0)
            .bind(now_ms())
            .bind(run_id.as_str())
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        Ok(revision)
    }

    pub async fn append_message_once(
        &self,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: RevisionId,
        message: &CanonicalMessage,
    ) -> Result<(RevisionId, bool)> {
        let existing = self.load_revision_messages(expected).await?;
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
            self.append_revision(
                conversation_id,
                run_id,
                expected,
                std::slice::from_ref(message),
            )
            .await?,
            true,
        ))
    }

    pub(crate) async fn append_revision_tx(
        tx: &mut Transaction<'_, Sqlite>,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: RevisionId,
        additions: &[CanonicalMessage],
    ) -> Result<RevisionId> {
        let mut full = Self::load_revision_messages_tx(tx, expected.0).await?;
        full.extend_from_slice(additions);
        let digest = message_digest(&full)?;
        Self::append_revision_with_digest_tx(
            tx,
            conversation_id,
            run_id,
            expected,
            additions,
            digest,
        )
        .await
    }

    async fn append_revision_with_digest_tx(
        tx: &mut Transaction<'_, Sqlite>,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: RevisionId,
        additions: &[CanonicalMessage],
        digest: [u8; 32],
    ) -> Result<RevisionId> {
        Self::require_active_head_tx(tx, conversation_id, run_id, expected).await?;
        if let Some(existing) = sqlx::query_scalar::<_, i64>(
            "SELECT revision_id FROM conversation_revisions
             WHERE conversation_id = ? AND state_digest = ?",
        )
        .bind(conversation_id.as_str())
        .bind(digest.as_slice())
        .fetch_optional(&mut **tx)
        .await?
        {
            // A retried turn can rebuild the same prefix from the same base.
            // Reuse that revision instead of treating identical state as a conflict.
            Self::advance_active_head_tx(tx, conversation_id, run_id, expected, RevisionId(existing))
                .await?;
            return Ok(RevisionId(existing));
        }
        let revision =
            Self::insert_revision_tx(tx, conversation_id, expected, additions, digest).await?;
        Self::advance_active_head_tx(tx, conversation_id, run_id, expected, revision).await?;
        Ok(revision)
    }

    async fn advance_active_head_tx(
        tx: &mut Transaction<'_, Sqlite>,
        conversation_id: &ConversationId,
        run_id: &RunId,
        expected: RevisionId,
        revision: RevisionId,
    ) -> Result<()> {
        if revision == expected {
            return Ok(());
        }
        let updated = sqlx::query(
            "UPDATE conversations SET current_revision_id = ?, updated_at_ms = ?
             WHERE conversation_id = ? AND current_revision_id = ? AND active_run_id = ?",
        )
        .bind(revision.0)
        .bind(now_ms())
        .bind(conversation_id.as_str())
        .bind(expected.0)
        .bind(run_id.as_str())
        .execute(&mut **tx)
        .await?
        .rows_affected();
        if updated != 1 {
            return Err(Error::Store(format!(
                "lost active ownership while appending revision for run {run_id}"
            )));
        }
        sqlx::query("UPDATE runs SET head_revision_id = ?, updated_at_ms = ? WHERE run_id = ?")
            .bind(revision.0)
            .bind(now_ms())
            .bind(run_id.as_str())
            .execute(&mut **tx)
            .await?;
        Ok(())
    }

    async fn insert_revision_tx(
        tx: &mut Transaction<'_, Sqlite>,
        conversation_id: &ConversationId,
        parent: RevisionId,
        additions: &[CanonicalMessage],
        digest: [u8; 32],
    ) -> Result<RevisionId> {
        for message in additions {
            Self::put_message_tx(tx, conversation_id, message).await?;
        }
        let revision = sqlx::query(
            "INSERT INTO conversation_revisions
             (conversation_id, parent_revision_id, state_digest, created_at_ms)
             VALUES (?, ?, ?, ?)",
        )
        .bind(conversation_id.as_str())
        .bind(parent.0)
        .bind(digest.as_slice())
        .bind(now_ms())
        .execute(&mut **tx)
        .await?
        .last_insert_rowid();
        for (ordinal, message) in additions.iter().enumerate() {
            sqlx::query(
                "INSERT INTO revision_messages(revision_id, ordinal, conversation_id, message_id)
                 VALUES (?, ?, ?, ?)",
            )
            .bind(revision)
            .bind(ordinal as i64)
            .bind(conversation_id.as_str())
            .bind(&message.message_id)
            .execute(&mut **tx)
            .await?;
        }
        Ok(RevisionId(revision))
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
