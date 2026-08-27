use sqlx::Row;

use crate::{
    subscription::{account_exhausted, account_identity, SubscriptionKind, CODEX_SESSION_WINDOW_MS},
    Error, Result,
};

use super::{now_ms, Store};

const ACCOUNT_COLUMNS: &str = r#"
    account_id, provider, access_token, refresh_token, display_name, plan_label,
    remaining_percent, used_percent, reset_at_ms, session_remaining_percent, session_reset_at_ms,
    limit_reached, active, created_at_ms, updated_at_ms
"#;

#[derive(Clone, Debug, PartialEq)]
pub struct SubscriptionAccount {
    pub account_id: String,
    pub provider: SubscriptionKind,
    pub access_token: String,
    pub refresh_token: Option<String>,
    pub display_name: String,
    pub plan_label: Option<String>,
    pub remaining_percent: Option<f64>,
    pub used_percent: Option<f64>,
    pub reset_at_ms: Option<i64>,
    pub session_remaining_percent: Option<f64>,
    pub session_reset_at_ms: Option<i64>,
    pub limit_reached: bool,
    pub active: bool,
    pub created_at_ms: i64,
    pub updated_at_ms: i64,
}

pub struct SubscriptionUsageUpdate {
    pub plan_label: Option<String>,
    pub remaining_percent: Option<f64>,
    pub used_percent: Option<f64>,
    pub reset_at_ms: Option<i64>,
    pub session_remaining_percent: Option<f64>,
    pub session_reset_at_ms: Option<i64>,
    pub limit_reached: bool,
}

impl Store {
    pub async fn subscription_accounts(
        &self,
        provider: SubscriptionKind,
    ) -> Result<Vec<SubscriptionAccount>> {
        let query = format!(
            "SELECT {ACCOUNT_COLUMNS} FROM subscription_accounts WHERE provider = ? ORDER BY active DESC, updated_at_ms DESC"
        );
        sqlx::query(&query)
            .bind(provider.as_str())
            .fetch_all(&self.pool)
            .await?
            .into_iter()
            .map(account_from_row)
            .collect()
    }

    pub async fn subscription_account(&self, account_id: &str) -> Result<Option<SubscriptionAccount>> {
        let query = format!("SELECT {ACCOUNT_COLUMNS} FROM subscription_accounts WHERE account_id = ?");
        sqlx::query(&query)
            .bind(account_id)
            .fetch_optional(&self.pool)
            .await?
            .map(account_from_row)
            .transpose()
    }

    pub async fn active_subscription_account(
        &self,
        provider: SubscriptionKind,
    ) -> Result<Option<SubscriptionAccount>> {
        let query = format!(
            "SELECT {ACCOUNT_COLUMNS} FROM subscription_accounts WHERE provider = ? AND active = 1 LIMIT 1"
        );
        sqlx::query(&query)
            .bind(provider.as_str())
            .fetch_optional(&self.pool)
            .await?
            .map(account_from_row)
            .transpose()
    }

    pub async fn upsert_subscription_account(
        &self,
        provider: SubscriptionKind,
        account_id: &str,
        access_token: &str,
        refresh_token: Option<&str>,
        display_name: &str,
        make_active: bool,
    ) -> Result<SubscriptionAccount> {
        let now = now_ms();
        let _write = self.writes.lock().await;
        let mut activate = make_active;
        let mut stale_ids = Vec::new();
        for account in self.subscription_accounts(provider).await? {
            if account.account_id == account_id {
                continue;
            }
            let same_token = account.access_token == access_token;
            let same_identity = account_identity(provider, &account.access_token).0 == account_id;
            if same_token || same_identity {
                if account.active {
                    activate = true;
                }
                stale_ids.push(account.account_id);
            }
        }
        let mut transaction = self.pool.begin().await?;
        for stale_id in &stale_ids {
            sqlx::query("DELETE FROM subscription_accounts WHERE account_id = ?")
                .bind(stale_id)
                .execute(&mut *transaction)
                .await?;
        }
        if activate {
            sqlx::query(
                "UPDATE subscription_accounts SET active = 0, updated_at_ms = ? WHERE provider = ? AND active = 1",
            )
            .bind(now)
            .bind(provider.as_str())
            .execute(&mut *transaction)
            .await?;
        }
        sqlx::query(
            r#"INSERT INTO subscription_accounts (
                account_id, provider, access_token, refresh_token, display_name,
                plan_label, remaining_percent, used_percent, reset_at_ms,
                session_remaining_percent, session_reset_at_ms, limit_reached,
                active, created_at_ms, updated_at_ms
            ) VALUES (?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, 0, ?, ?, ?)
            ON CONFLICT(account_id) DO UPDATE SET
                access_token = excluded.access_token,
                refresh_token = excluded.refresh_token,
                display_name = excluded.display_name,
                session_remaining_percent = NULL,
                session_reset_at_ms = NULL,
                limit_reached = 0,
                active = CASE WHEN excluded.active = 1 THEN 1 ELSE subscription_accounts.active END,
                updated_at_ms = excluded.updated_at_ms"#,
        )
        .bind(account_id)
        .bind(provider.as_str())
        .bind(access_token)
        .bind(refresh_token)
        .bind(display_name)
        .bind(i64::from(activate))
        .bind(now)
        .bind(now)
        .execute(&mut *transaction)
        .await?;
        transaction.commit().await?;
        Ok(self
            .subscription_account(account_id)
            .await?
            .expect("upserted subscription account must exist"))
    }

    pub async fn activate_subscription_account(&self, account_id: &str) -> Result<SubscriptionAccount> {
        let account = self
            .subscription_account(account_id)
            .await?
            .ok_or_else(|| Error::RunNotFound(format!("subscription account {account_id}")))?;
        let now = now_ms();
        let _write = self.writes.lock().await;
        let mut transaction = self.pool.begin().await?;
        sqlx::query(
            "UPDATE subscription_accounts SET active = 0, updated_at_ms = ? WHERE provider = ? AND active = 1",
        )
        .bind(now)
        .bind(account.provider.as_str())
        .execute(&mut *transaction)
        .await?;
        sqlx::query(
            "UPDATE subscription_accounts SET active = 1, updated_at_ms = ? WHERE account_id = ?",
        )
        .bind(now)
        .bind(account_id)
        .execute(&mut *transaction)
        .await?;
        transaction.commit().await?;
        Ok(self
            .subscription_account(account_id)
            .await?
            .expect("activated subscription account must exist"))
    }

    pub async fn delete_subscription_account(&self, account_id: &str) -> Result<Option<SubscriptionAccount>> {
        let account = self
            .subscription_account(account_id)
            .await?
            .ok_or_else(|| Error::RunNotFound(format!("subscription account {account_id}")))?;
        let now = now_ms();
        let _write = self.writes.lock().await;
        let mut transaction = self.pool.begin().await?;
        sqlx::query("DELETE FROM subscription_accounts WHERE account_id = ?")
            .bind(account_id)
            .execute(&mut *transaction)
            .await?;
        let replacement = if account.active {
            sqlx::query_scalar::<_, String>(
                "SELECT account_id FROM subscription_accounts WHERE provider = ? ORDER BY updated_at_ms DESC LIMIT 1",
            )
            .bind(account.provider.as_str())
            .fetch_optional(&mut *transaction)
            .await?
        } else {
            None
        };
        if let Some(next_id) = &replacement {
            sqlx::query(
                "UPDATE subscription_accounts SET active = 1, updated_at_ms = ? WHERE account_id = ?",
            )
            .bind(now)
            .bind(next_id)
            .execute(&mut *transaction)
            .await?;
        }
        transaction.commit().await?;
        match replacement {
            Some(next_id) => self.subscription_account(&next_id).await,
            None => Ok(None),
        }
    }

    pub async fn update_subscription_usage(
        &self,
        account_id: &str,
        usage: &SubscriptionUsageUpdate,
    ) -> Result<SubscriptionAccount> {
        let now = now_ms();
        let _write = self.writes.lock().await;
        let result = sqlx::query(
            r#"UPDATE subscription_accounts SET
                plan_label = ?, remaining_percent = ?, used_percent = ?, reset_at_ms = ?,
                session_remaining_percent = ?, session_reset_at_ms = ?,
                limit_reached = ?, updated_at_ms = ?
            WHERE account_id = ?"#,
        )
        .bind(&usage.plan_label)
        .bind(usage.remaining_percent)
        .bind(usage.used_percent)
        .bind(usage.reset_at_ms)
        .bind(usage.session_remaining_percent)
        .bind(usage.session_reset_at_ms)
        .bind(i64::from(usage.limit_reached))
        .bind(now)
        .bind(account_id)
        .execute(&self.pool)
        .await?;
        if result.rows_affected() != 1 {
            return Err(Error::RunNotFound(format!("subscription account {account_id}")));
        }
        Ok(self
            .subscription_account(account_id)
            .await?
            .expect("updated subscription account must exist"))
    }

    pub async fn mark_subscription_exhausted(&self, account_id: &str) -> Result<SubscriptionAccount> {
        let account = self
            .subscription_account(account_id)
            .await?
            .ok_or_else(|| Error::RunNotFound(format!("subscription account {account_id}")))?;
        let now = now_ms();
        let session_reset = account
            .session_reset_at_ms
            .filter(|reset| *reset > now)
            .or(Some(now + CODEX_SESSION_WINDOW_MS));
        let (remaining_percent, used_percent, reset_at_ms, session_remaining_percent, session_reset_at_ms, limit_reached) =
            if account.provider == SubscriptionKind::Codex {
                (
                    account.remaining_percent,
                    account.used_percent,
                    account.reset_at_ms,
                    Some(0.0),
                    session_reset,
                    false,
                )
            } else {
                (Some(0.0), Some(100.0), account.reset_at_ms, None, None, true)
            };
        self.update_subscription_usage(
            account_id,
            &SubscriptionUsageUpdate {
                plan_label: account.plan_label,
                remaining_percent,
                used_percent,
                reset_at_ms,
                session_remaining_percent,
                session_reset_at_ms,
                limit_reached,
            },
        )
        .await
    }

    pub async fn rotate_subscription_account(
        &self,
        provider: SubscriptionKind,
        current_id: Option<&str>,
    ) -> Result<Option<SubscriptionAccount>> {
        let now = now_ms();
        let accounts = self.subscription_accounts(provider).await?;
        let mut available = accounts
            .iter()
            .filter(|account| !account.is_exhausted(now))
            .collect::<Vec<_>>();
        available.sort_by(|left, right| {
            remaining_score(right.session_remaining_percent)
                .cmp(&remaining_score(left.session_remaining_percent))
                .then(
                    remaining_score(right.remaining_percent)
                        .cmp(&remaining_score(left.remaining_percent)),
                )
        });
        let next = available
            .iter()
            .copied()
            .find(|account| Some(account.account_id.as_str()) != current_id)
            .or_else(|| available.first().copied());
        match next {
            Some(account) if Some(account.account_id.as_str()) != current_id || !account.active => {
                Ok(Some(self.activate_subscription_account(&account.account_id).await?))
            }
            Some(account) => Ok(Some(account.clone())),
            None => Ok(None),
        }
    }
}

impl SubscriptionAccount {
    fn is_exhausted(&self, now_ms: i64) -> bool {
        account_exhausted(
            self.remaining_percent,
            self.reset_at_ms,
            self.session_remaining_percent,
            self.session_reset_at_ms,
            now_ms,
        )
    }
}

fn remaining_score(value: Option<f64>) -> i64 {
    value.unwrap_or(100.0).clamp(0.0, 100.0).round() as i64
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::subscription::SubscriptionKind;

    #[tokio::test]
    async fn rotate_skips_exhausted_accounts() {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("accounts.db").display()
        ))
        .await
        .unwrap();
        store
            .upsert_subscription_account(SubscriptionKind::Grok, "grok:a", "token-a", None, "A", true)
            .await
            .unwrap();
        store
            .upsert_subscription_account(SubscriptionKind::Grok, "grok:b", "token-b", None, "B", true)
            .await
            .unwrap();
        store
            .update_subscription_usage(
                "grok:b",
                &SubscriptionUsageUpdate {
                    plan_label: None,
                    remaining_percent: Some(0.0),
                    used_percent: Some(100.0),
                    reset_at_ms: Some(now_ms() + 60_000),
                    session_remaining_percent: None,
                    session_reset_at_ms: None,
                    limit_reached: true,
                },
            )
            .await
            .unwrap();
        let next = store
            .rotate_subscription_account(SubscriptionKind::Grok, Some("grok:b"))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(next.account_id, "grok:a");
        assert!(next.active);
        assert_eq!(next.access_token, "token-a");
    }

    #[tokio::test]
    async fn rotate_skips_codex_accounts_with_empty_five_hour_window() {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("codex-session.db").display()
        ))
        .await
        .unwrap();
        store
            .upsert_subscription_account(SubscriptionKind::Codex, "codex:a", "token-a", None, "A", true)
            .await
            .unwrap();
        store
            .upsert_subscription_account(SubscriptionKind::Codex, "codex:b", "token-b", None, "B", true)
            .await
            .unwrap();
        store
            .update_subscription_usage(
                "codex:b",
                &SubscriptionUsageUpdate {
                    plan_label: Some("ChatGPT Plus".into()),
                    remaining_percent: Some(70.0),
                    used_percent: Some(30.0),
                    reset_at_ms: Some(now_ms() + 86_400_000),
                    session_remaining_percent: Some(0.0),
                    session_reset_at_ms: Some(now_ms() + 3_600_000),
                    limit_reached: true,
                },
            )
            .await
            .unwrap();
        store
            .update_subscription_usage(
                "codex:a",
                &SubscriptionUsageUpdate {
                    plan_label: Some("ChatGPT Plus".into()),
                    remaining_percent: Some(40.0),
                    used_percent: Some(60.0),
                    reset_at_ms: Some(now_ms() + 86_400_000),
                    session_remaining_percent: Some(80.0),
                    session_reset_at_ms: Some(now_ms() + 3_600_000),
                    limit_reached: false,
                },
            )
            .await
            .unwrap();
        let next = store
            .rotate_subscription_account(SubscriptionKind::Codex, Some("codex:b"))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(next.account_id, "codex:a");
        assert!(next.active);
    }
}

fn account_from_row(row: sqlx::sqlite::SqliteRow) -> Result<SubscriptionAccount> {
    Ok(SubscriptionAccount {
        account_id: row.get("account_id"),
        provider: SubscriptionKind::parse(&row.get::<String, _>("provider"))?,
        access_token: row.get("access_token"),
        refresh_token: row.get("refresh_token"),
        display_name: row.get("display_name"),
        plan_label: row.get("plan_label"),
        remaining_percent: row.get("remaining_percent"),
        used_percent: row.get("used_percent"),
        reset_at_ms: row.get("reset_at_ms"),
        session_remaining_percent: row.get("session_remaining_percent"),
        session_reset_at_ms: row.get("session_reset_at_ms"),
        limit_reached: row.get::<i64, _>("limit_reached") != 0,
        active: row.get::<i64, _>("active") != 0,
        created_at_ms: row.get("created_at_ms"),
        updated_at_ms: row.get("updated_at_ms"),
    })
}
