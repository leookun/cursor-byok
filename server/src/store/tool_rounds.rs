//! Persists Tool round calls, results, and settlement state.
use sqlx::Row;

use crate::{
    model::{
        CanonicalMessage, CheckpointId, ConversationId, MessageContent, Origin, Role, RunId,
        ToolCall, ToolCallContent, ToolResult, ToolResultContent, ToolRoundAssistant, ToolRoundId,
    },
    Error, Result,
};

use super::{now_ms, Store};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ToolRoundStatus {
    Pending,
    Settled,
}

#[derive(Clone, Debug, PartialEq)]
pub struct ToolRoundSnapshot {
    pub round_id: ToolRoundId,
    pub run_id: RunId,
    pub base_checkpoint_id: CheckpointId,
    pub assistant: ToolRoundAssistant,
    pub calls: Vec<ToolCall>,
    pub completed_call_ids: Vec<String>,
    pub status: ToolRoundStatus,
    pub version: u64,
    pub created_at_ms: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ToolCommit {
    pub checkpoint_id: CheckpointId,
    pub tool_round_version: u64,
    pub completion_seq: u64,
    pub settled: bool,
}

impl Store {
    pub async fn create_tool_round(
        &self,
        round_id: &ToolRoundId,
        run_id: &RunId,
        base_checkpoint_id: CheckpointId,
        assistant: &ToolRoundAssistant,
        calls: &[ToolCall],
        created_at_ms: Option<u64>,
    ) -> Result<()> {
        if calls.is_empty() {
            return Err(Error::Store("cannot persist an empty tool round".into()));
        }
        let assistant_json = serde_json::to_string(assistant)?;
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        let ownership: bool = sqlx::query_scalar(
            "SELECT EXISTS(
                SELECT 1 FROM runs r JOIN conversations c USING(conversation_id)
                WHERE r.run_id = ? AND r.head_checkpoint_id = ?
                  AND r.status = 'running' AND c.active_run_id = r.run_id
                  AND c.current_checkpoint_id = r.head_checkpoint_id
             )",
        )
        .bind(run_id.as_str())
        .bind(base_checkpoint_id.0)
        .fetch_one(&mut *tx)
        .await?;
        if !ownership {
            return Err(Error::Store(format!(
                "run {run_id} cannot start tool round at checkpoint {base_checkpoint_id}"
            )));
        }
        let now = now_ms();
        let created_at_ms = created_at_ms
            .map(i64::try_from)
            .transpose()
            .map_err(|_| Error::Protocol("tool round timestamp exceeds SQLite INTEGER".into()))?
            .unwrap_or(now);
        sqlx::query(
            "INSERT INTO tool_rounds
             (round_id, run_id, base_checkpoint_id, assistant_json, status, created_at_ms, updated_at_ms)
             VALUES (?, ?, ?, ?, 'pending', ?, ?)",
        )
        .bind(round_id.as_str())
        .bind(run_id.as_str())
        .bind(base_checkpoint_id.0)
        .bind(assistant_json)
        .bind(created_at_ms)
        .bind(now)
        .execute(&mut *tx)
        .await?;
        for call in calls {
            sqlx::query(
                "INSERT INTO tool_round_calls
                 (round_id, call_index, call_id, model_call_id, name, arguments_json, argument_error, status)
                 VALUES (?, ?, ?, ?, ?, ?, ?, 'pending')",
            )
            .bind(round_id.as_str())
            .bind(call.index as i64)
            .bind(&call.call_id)
            .bind(&call.model_call_id)
            .bind(&call.name)
            .bind(serde_json::to_string(&call.arguments)?)
            .bind(call.argument_error.as_deref())
            .execute(&mut *tx)
            .await?;
        }
        tx.commit().await?;
        session.committed();
        Ok(())
    }

    pub async fn commit_tool_result(
        &self,
        conversation_id: &ConversationId,
        run_id: &RunId,
        round_id: &ToolRoundId,
        result: &ToolResult,
    ) -> Result<ToolCommit> {
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        let round = sqlx::query(
            "SELECT assistant_json, status, version, next_completion_seq
             FROM tool_rounds WHERE round_id = ? AND run_id = ?",
        )
        .bind(round_id.as_str())
        .bind(run_id.as_str())
        .fetch_optional(&mut *tx)
        .await?
        .ok_or_else(|| Error::Store(format!("unknown tool round: {round_id}")))?;
        if round.get::<&str, _>(1) != "pending" {
            return Err(Error::Store(format!(
                "tool round is already settled: {round_id}"
            )));
        }
        let assistant: ToolRoundAssistant = serde_json::from_str(round.get(0))?;
        let version: i64 = round.get(2);
        let completion_seq: i64 = round.get(3);
        let call = sqlx::query(
            "SELECT call_index, name, arguments_json, status
             FROM tool_round_calls WHERE round_id = ? AND call_id = ?",
        )
        .bind(round_id.as_str())
        .bind(&result.call_id)
        .fetch_optional(&mut *tx)
        .await?;
        let Some(call) = call else {
            tracing::error!(
                run_id = %run_id,
                round_id = %round_id,
                call_id = result.call_id,
                "unknown tool result"
            );
            return Err(Error::Protocol(format!(
                "unknown tool result call_id: {}",
                result.call_id
            )));
        };
        if call.get::<&str, _>(3) != "pending" {
            return Err(Error::Protocol(format!(
                "duplicate tool result call_id: {}",
                result.call_id
            )));
        }

        let head: i64 = sqlx::query_scalar("SELECT head_checkpoint_id FROM runs WHERE run_id = ?")
            .bind(run_id.as_str())
            .fetch_one(&mut *tx)
            .await?;
        let call_index = call.get::<i64, _>(0) as usize;
        let name: String = call.get(1);
        let arguments_text: String = call.get(2);
        let arguments = serde_json::from_str(&arguments_text)?;
        let first = completion_seq == 0;
        let assistant_message = CanonicalMessage {
            message_id: format!("{}:{}:assistant", round_id, result.call_id),
            role: Role::Assistant,
            origin: Origin::Assistant,
            content: MessageContent::Assistant {
                text: if first { assistant.text } else { String::new() },
                thinking: if first {
                    assistant.thinking
                } else {
                    String::new()
                },
                tool_round_id: Some(round_id.clone()),
                replay_state: if first { assistant.replay_state } else { None },
                tool_calls: vec![ToolCallContent {
                    index: call_index,
                    call_id: result.call_id.clone(),
                    name: name.clone(),
                    arguments,
                }],
            },
            runtime_event_id: None,
        };
        let result_message = CanonicalMessage {
            message_id: format!("{}:{}:result", round_id, result.call_id),
            role: Role::Tool,
            origin: Origin::Tool,
            content: MessageContent::ToolResult(ToolResultContent {
                call_id: result.call_id.clone(),
                name,
                content: result.content.clone(),
                is_error: result.is_error,
                image: result.image.clone(),
                provider_parts: Vec::new(),
            }),
            runtime_event_id: None,
        };
        let checkpoint = Self::append_checkpoint_tx(
            &mut tx,
            conversation_id,
            run_id,
            CheckpointId(head),
            &[assistant_message, result_message],
        )
        .await?;

        sqlx::query(
            "UPDATE tool_round_calls SET status = 'completed', completion_seq = ?,
             result_content = ?, result_is_error = ?, committed_checkpoint_id = ?, completed_at_ms = ?
             WHERE round_id = ? AND call_id = ? AND status = 'pending'",
        )
        .bind(completion_seq)
        .bind(&result.content)
        .bind(result.is_error)
        .bind(checkpoint.0)
        .bind(now_ms())
        .bind(round_id.as_str())
        .bind(&result.call_id)
        .execute(&mut *tx)
        .await?;
        let pending: i64 = sqlx::query_scalar(
            "SELECT COUNT(*) FROM tool_round_calls WHERE round_id = ? AND status = 'pending'",
        )
        .bind(round_id.as_str())
        .fetch_one(&mut *tx)
        .await?;
        let settled = pending == 0;
        sqlx::query(
            "UPDATE tool_rounds SET status = ?, version = ?, next_completion_seq = ?, updated_at_ms = ?
             WHERE round_id = ?",
        )
        .bind(if settled { "settled" } else { "pending" })
        .bind(version + 1)
        .bind(completion_seq + 1)
        .bind(now_ms())
        .bind(round_id.as_str())
        .execute(&mut *tx)
        .await?;
        tx.commit().await?;
        session.committed();
        Ok(ToolCommit {
            checkpoint_id: checkpoint,
            tool_round_version: (version + 1) as u64,
            completion_seq: completion_seq as u64,
            settled,
        })
    }

    pub async fn tool_round(&self, round_id: &ToolRoundId) -> Result<Option<ToolRoundSnapshot>> {
        let Some(round) = sqlx::query(
            "SELECT run_id, base_checkpoint_id, assistant_json, status, version, created_at_ms
             FROM tool_rounds WHERE round_id = ?",
        )
        .bind(round_id.as_str())
        .fetch_optional(&self.pool)
        .await?
        else {
            return Ok(None);
        };
        let rows = sqlx::query(
            "SELECT call_index, call_id, model_call_id, name, arguments_json, argument_error, status
             FROM tool_round_calls WHERE round_id = ? ORDER BY call_index",
        )
        .bind(round_id.as_str())
        .fetch_all(&self.pool)
        .await?;
        let mut calls = Vec::with_capacity(rows.len());
        let mut completed = Vec::new();
        for row in rows {
            let arguments_text: String = row.get(4);
            let call_id: String = row.get(1);
            if row.get::<&str, _>(6) == "completed" {
                completed.push(call_id.clone());
            }
            calls.push(ToolCall {
                index: row.get::<i64, _>(0) as usize,
                call_id,
                model_call_id: row.get(2),
                name: row.get(3),
                arguments: serde_json::from_str(&arguments_text)?,
                arguments_text,
                argument_error: row.get(5),
            });
        }
        Ok(Some(ToolRoundSnapshot {
            round_id: round_id.clone(),
            run_id: RunId(round.get(0)),
            base_checkpoint_id: CheckpointId(round.get(1)),
            assistant: serde_json::from_str(round.get(2))?,
            calls,
            completed_call_ids: completed,
            status: if round.get::<&str, _>(3) == "settled" {
                ToolRoundStatus::Settled
            } else {
                ToolRoundStatus::Pending
            },
            version: round.get::<i64, _>(4) as u64,
            created_at_ms: round.get::<i64, _>(5) as u64,
        }))
    }
}
