//! Explicit Cursor selection IDs routed to configured BYOK models.
use std::collections::BTreeMap;

use crate::{Error, Result};

use super::{now_ms, Store};

const KEY: &str = "cursor_model_aliases";

impl Store {
    pub async fn cursor_model_aliases(&self) -> Result<BTreeMap<String, String>> {
        let value = sqlx::query_scalar::<_, String>(
            "SELECT value_json FROM service_settings WHERE setting_key = ?",
        )
        .bind(KEY)
        .fetch_optional(&self.pool)
        .await?;
        value
            .map(|value| serde_json::from_str(&value).map_err(Into::into))
            .unwrap_or_else(|| Ok(BTreeMap::new()))
    }

    pub async fn set_cursor_model_aliases(
        &self,
        aliases: BTreeMap<String, String>,
    ) -> Result<BTreeMap<String, String>> {
        let _write = self.writes.lock().await;
        for (alias, target) in &aliases {
            if alias == "default"
                || alias.trim().is_empty()
                || alias.trim() != alias
                || alias.starts_with(crate::plugin::ADAPTER_ID_PREFIX)
                || self.model(alias).await?.is_some()
            {
                return Err(Error::Config(format!(
                    "invalid Cursor model alias: {alias}"
                )));
            }
            if self.model(target).await?.is_none() {
                return Err(Error::Config(format!(
                    "Cursor model alias {alias} must target a configured BYOK model hash"
                )));
            }
        }
        sqlx::query(
            "INSERT INTO service_settings(setting_key, value_json, updated_at_ms) VALUES (?, ?, ?) ON CONFLICT(setting_key) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms",
        )
        .bind(KEY)
        .bind(serde_json::to_string(&aliases)?)
        .bind(now_ms())
        .execute(&self.pool)
        .await?;
        Ok(aliases)
    }
}
