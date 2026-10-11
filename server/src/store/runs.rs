//! Persists Run ownership, status, and provider call progress.
use sqlx::Row;

use crate::{
    model::{CheckpointId, ConversationId, PreparedRun, RunId, RunKind, Usage},
    Error, Result,
};

use super::{now_ms, Store};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RunStatus {
    Running,
    Completed,
    Cancelled,
    Failed,
}

impl RunStatus {
    fn as_str(self) -> &'static str {
        match self {
            Self::Running => "running",
            Self::Completed => "completed",
            Self::Cancelled => "cancelled",
            Self::Failed => "failed",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ClaimedRun {
    pub run_id: RunId,
    pub conversation_id: ConversationId,
    pub head_checkpoint_id: CheckpointId,
    pub replaced_run_id: Option<RunId>,
}

impl Store {
    pub async fn claim_run(&self, prepared: &PreparedRun) -> Result<ClaimedRun> {
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        let now = now_ms();
        Self::ensure_conversation_tx(&mut tx, &prepared.conversation_id).await?;
        let belongs: bool = sqlx::query_scalar(
            "SELECT EXISTS(
                SELECT 1 FROM conversation_checkpoints
                WHERE checkpoint_id = ? AND conversation_id = ?
             )",
        )
        .bind(prepared.base_checkpoint_id.0)
        .bind(prepared.conversation_id.as_str())
        .fetch_one(&mut *tx)
        .await?;
        if !belongs {
            return Err(Error::Store(format!(
                "base checkpoint {} does not belong to conversation {}",
                prepared.base_checkpoint_id, prepared.conversation_id
            )));
        }

        let replaced: Option<String> =
            sqlx::query_scalar("SELECT active_run_id FROM conversations WHERE conversation_id = ?")
                .bind(prepared.conversation_id.as_str())
                .fetch_one(&mut *tx)
                .await?;
        if let Some(replaced) = replaced.as_deref() {
            if replaced != prepared.run_id.as_str() {
                sqlx::query(
                    "UPDATE runs SET status = 'cancelled', updated_at_ms = ?
                     WHERE run_id = ? AND status = 'running'",
                )
                .bind(now)
                .bind(replaced)
                .execute(&mut *tx)
                .await?;
                sqlx::query(
                    "UPDATE llm_calls SET status = 'cancelled', finished_at_ms = ?,
                     duration_ms = MAX(0, ? - created_at_ms)
                     WHERE run_id = ? AND status = 'running'",
                )
                .bind(now)
                .bind(now)
                .bind(replaced)
                .execute(&mut *tx)
                .await?;
            }
        }

        let (parent_run_id, parent_tool_call_id, run_kind, subagent_kind) =
            run_kind_columns(&prepared.kind);
        sqlx::query(
            "INSERT INTO runs
             (run_id, cursor_request_id, conversation_id, base_checkpoint_id, head_checkpoint_id,
              parent_run_id, parent_tool_call_id, run_kind, subagent_kind,
              status, created_at_ms, updated_at_ms)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?)",
        )
        .bind(prepared.run_id.as_str())
        .bind(prepared.cursor_request_id.as_deref())
        .bind(prepared.conversation_id.as_str())
        .bind(prepared.base_checkpoint_id.0)
        .bind(prepared.base_checkpoint_id.0)
        .bind(parent_run_id)
        .bind(parent_tool_call_id)
        .bind(run_kind)
        .bind(subagent_kind)
        .bind(now)
        .bind(now)
        .execute(&mut *tx)
        .await?;

        sqlx::query(
            "UPDATE conversations
             SET current_checkpoint_id = ?, active_run_id = ?, updated_at_ms = ?
             WHERE conversation_id = ?",
        )
        .bind(prepared.base_checkpoint_id.0)
        .bind(prepared.run_id.as_str())
        .bind(now)
        .bind(prepared.conversation_id.as_str())
        .execute(&mut *tx)
        .await?;
        tx.commit().await?;
        session.committed();
        Ok(ClaimedRun {
            run_id: prepared.run_id.clone(),
            conversation_id: prepared.conversation_id.clone(),
            head_checkpoint_id: prepared.base_checkpoint_id,
            replaced_run_id: replaced
                .filter(|run| run != prepared.run_id.as_str())
                .map(RunId),
        })
    }

    pub async fn active_run_for_cursor_request(
        &self,
        cursor_request_id: &str,
    ) -> Result<Option<RunId>> {
        let run_id: Option<String> = sqlx::query_scalar(
            "SELECT run_id FROM runs
             WHERE cursor_request_id = ? AND status = 'running'
             ORDER BY created_at_ms DESC
             LIMIT 1",
        )
        .bind(cursor_request_id)
        .fetch_optional(&self.pool)
        .await?;
        Ok(run_id.map(RunId))
    }

    pub async fn begin_provider_call(&self, run_id: &RunId) -> Result<u64> {
        let _write = self.writes.lock().await;
        let index: Option<i64> = sqlx::query_scalar(
            "UPDATE runs SET provider_call_index = provider_call_index + 1, updated_at_ms = ?
             WHERE run_id = ? AND status = 'running'
             RETURNING provider_call_index",
        )
        .bind(now_ms())
        .bind(run_id.as_str())
        .fetch_optional(&self.pool)
        .await?;
        index
            .map(|index| index as u64)
            .ok_or_else(|| Error::Store(format!("run is not active: {run_id}")))
    }

    pub async fn finish_run(
        &self,
        run_id: &RunId,
        status: RunStatus,
        usage: Option<Usage>,
        failure: Option<(&str, &str)>,
    ) -> Result<bool> {
        let usage_json = serde_json::to_string(&usage)?;
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        let row = sqlx::query(
            "SELECT conversation_id, status, failure_category, failure_summary
             FROM runs WHERE run_id = ?",
        )
        .bind(run_id.as_str())
        .fetch_optional(&mut *tx)
        .await?;
        let Some(row) = row else {
            return Err(Error::RunNotFound(run_id.to_string()));
        };
        let conversation_id: String = row.get("conversation_id");
        let current_status: String = row.get("status");
        let (requested_category, requested_summary) = failure.unzip();
        let terminal_status = if current_status == "running" {
            status.as_str()
        } else {
            current_status.as_str()
        };
        let stored_category: Option<String> = row.get("failure_category");
        let stored_summary: Option<String> = row.get("failure_summary");
        let (category, summary) = if current_status == "running" {
            (requested_category, requested_summary)
        } else {
            (stored_category.as_deref(), stored_summary.as_deref())
        };
        let now = now_ms();
        sqlx::query(
            "UPDATE runs SET status = ?, turn_usage_json = ?, failure_category = ?,
             failure_summary = ?, updated_at_ms = ?
             WHERE run_id = ? AND status = 'running'",
        )
        .bind(status.as_str())
        .bind(usage_json)
        .bind(category)
        .bind(summary)
        .bind(now)
        .bind(run_id.as_str())
        .execute(&mut *tx)
        .await?;
        let (call_status, call_error_kind, call_error_message) = match terminal_status {
            "cancelled" => ("cancelled", None, None),
            "failed" => ("error", category, summary),
            "completed" => (
                "error",
                Some("internal"),
                Some("Run completed before LLM call reached a terminal state"),
            ),
            value => {
                return Err(Error::Store(format!(
                    "cannot finish LLM calls for non-terminal Run status: {value}"
                )))
            }
        };
        sqlx::query(
            "UPDATE llm_calls SET status = ?, finished_at_ms = ?,
             duration_ms = MAX(0, ? - created_at_ms), error_kind = ?, error_message = ?
             WHERE run_id = ? AND status = 'running'",
        )
        .bind(call_status)
        .bind(now)
        .bind(now)
        .bind(call_error_kind)
        .bind(call_error_message)
        .bind(run_id.as_str())
        .execute(&mut *tx)
        .await?;
        let released = sqlx::query(
            "UPDATE conversations SET active_run_id = NULL, updated_at_ms = ?
             WHERE conversation_id = ? AND active_run_id = ?",
        )
        .bind(now)
        .bind(conversation_id)
        .bind(run_id.as_str())
        .execute(&mut *tx)
        .await?
        .rows_affected()
            == 1;
        tx.commit().await?;
        session.committed();
        Ok(released)
    }
}

fn run_kind_columns(kind: &RunKind) -> (Option<&str>, Option<&str>, &'static str, Option<String>) {
    match kind {
        RunKind::Root => (None, None, "root", None),
        RunKind::Subagent {
            parent_run_id,
            parent_tool_call_id,
            kind,
            ..
        } => (
            Some(parent_run_id.as_str()),
            Some(parent_tool_call_id.as_str()),
            "subagent",
            Some(match kind {
                crate::model::SubagentKind::GeneralPurpose => "generalPurpose".into(),
                crate::model::SubagentKind::Named(name) => name.clone(),
            }),
        ),
    }
}
