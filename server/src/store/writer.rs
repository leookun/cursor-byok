//! Serializes write transactions that mutate Conversation state.
use std::sync::Arc;

use sqlx::{pool::PoolConnection, Connection, Sqlite, Transaction};
use tokio::sync::{Mutex, MutexGuard};

use crate::Result;

use super::Store;

#[derive(Clone, Default)]
pub(crate) struct WriteCoordinator {
    lock: Arc<Mutex<()>>,
}

impl WriteCoordinator {
    pub(crate) async fn lock(&self) -> MutexGuard<'_, ()> {
        self.lock.lock().await
    }
}

/// Holds the pooled connection and write lock for one `BEGIN IMMEDIATE` transaction.
///
/// The connection returns to the pool only when the transaction committed cleanly.
/// sqlx keeps its transaction depth set when a `COMMIT` or rollback fails, so a
/// connection that did not finish cleanly is closed instead: reusing it would make
/// every later write fail with `attempted to call begin_with at non-zero transaction
/// depth` until the process restarts.
pub(crate) struct WriteSession<'a> {
    _write: MutexGuard<'a, ()>,
    connection: PoolConnection<Sqlite>,
    committed: bool,
}

impl Store {
    pub(crate) async fn write_session(&self) -> Result<WriteSession<'_>> {
        Ok(WriteSession {
            _write: self.writes.lock().await,
            connection: self.pool.acquire().await?,
            committed: false,
        })
    }
}

impl WriteSession<'_> {
    pub(crate) async fn begin(&mut self) -> Result<Transaction<'_, Sqlite>> {
        Ok(self.connection.begin_with("BEGIN IMMEDIATE").await?)
    }

    pub(crate) fn committed(&mut self) {
        self.committed = true;
    }
}

impl Drop for WriteSession<'_> {
    fn drop(&mut self) {
        if !self.committed {
            self.connection.close_on_drop();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::ConversationId;

    async fn store() -> (tempfile::TempDir, Store) {
        let directory = tempfile::tempdir().unwrap();
        let database_url = format!("sqlite://{}", directory.path().join("test.db").display());
        let store = Store::connect(&database_url).await.unwrap();
        (directory, store)
    }

    async fn assert_writes_still_work(store: &Store) {
        for _ in 0..4 {
            store
                .ensure_conversation(&ConversationId::new("poison-check"))
                .await
                .unwrap();
        }
    }

    #[tokio::test]
    async fn failed_commit_reports_the_error() {
        let (_directory, store) = store().await;
        let mut session = store.write_session().await.unwrap();
        let mut tx = session.begin().await.unwrap();
        // Deferring foreign keys moves the violation from the INSERT to the COMMIT.
        sqlx::query("PRAGMA defer_foreign_keys = ON")
            .execute(&mut *tx)
            .await
            .unwrap();
        sqlx::query(
            "INSERT INTO checkpoint_messages(checkpoint_id, ordinal, conversation_id, message_id)
             VALUES (999999, 0, 'missing-conversation', 'missing-message')",
        )
        .execute(&mut *tx)
        .await
        .unwrap();
        assert!(tx.commit().await.is_err());
        drop(session);
        assert_writes_still_work(&store).await;
    }

    #[tokio::test]
    async fn unfinished_transaction_discards_the_connection() {
        let (_directory, store) = store().await;
        {
            let mut session = store.write_session().await.unwrap();
            let tx = session.begin().await.unwrap();
            // Leaving the transaction open keeps sqlx's transaction depth set on the
            // connection. Returning that connection to the pool would make every later
            // write fail with `attempted to call begin_with at non-zero transaction depth`.
            std::mem::forget(tx);
            drop(session);
        }
        assert_writes_still_work(&store).await;
    }
}
