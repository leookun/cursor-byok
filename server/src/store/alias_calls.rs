//! Records alias provenance on ordinary provider calls and checks run ownership.
use super::Store;
use crate::Result;

impl Store {
    pub async fn record_alias_call(
        &self,
        call_id: &str,
        alias_id: &str,
        alias_name: &str,
        target_id: &str,
        switches: usize,
    ) -> Result<()> {
        let _write = self.writes.lock().await;
        sqlx::query("UPDATE llm_calls SET alias_id = ?, alias_name = ?, alias_target_id = ?, alias_switch_count = ? WHERE call_id = ?")
            .bind(alias_id).bind(alias_name).bind(target_id).bind(switches.min(i64::MAX as usize) as i64).bind(call_id)
            .execute(&self.pool).await?;
        Ok(())
    }

    pub async fn finish_alias_failure(
        &self,
        call_id: &str,
        elapsed_ms: i64,
        message: &str,
    ) -> Result<()> {
        let _write = self.writes.lock().await;
        // Dropping the failed stream may have recorded cancellation first. The
        // resolver knows this was a failed attempt, not a user cancellation.
        sqlx::query("UPDATE llm_calls SET status = 'error', finished_at_ms = ?, duration_ms = ?, error_kind = 'provider', error_message = ? WHERE call_id = ? AND status IN ('running', 'cancelled')")
            .bind(super::now_ms()).bind(elapsed_ms).bind(message).bind(call_id).execute(&self.pool).await?;
        Ok(())
    }

    pub async fn alias_run_is_current(&self, conversation_id: &str, run_id: &str) -> Result<bool> {
        let active: Option<Option<String>> =
            sqlx::query_scalar("SELECT active_run_id FROM conversations WHERE conversation_id = ?")
                .bind(conversation_id)
                .fetch_optional(&self.pool)
                .await?;
        // Management connectivity tests do not create persisted conversations.
        Ok(active.is_none() || active.flatten().as_deref() == Some(run_id))
    }
}
