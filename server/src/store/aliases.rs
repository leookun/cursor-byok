//! Transactional alias configuration storage; source references intentionally may dangle.
use sqlx::{Row, Sqlite, Transaction};

use crate::{
    alias::configuration::{normalize_alias_input, Alias, AliasInput, AliasSettings, ReturnMode},
    Error, Result,
};

use super::{now_ms, Store};

const ALIAS_COLUMNS: &str = "id, name, description, enabled, targets_json, sticky, return_mode, created_at_ms, updated_at_ms";
const SETTINGS_KEY: &str = "alias_settings";

impl Store {
    pub async fn aliases(&self) -> Result<Vec<Alias>> {
        sqlx::query(&format!(
            "SELECT {ALIAS_COLUMNS} FROM aliases ORDER BY name, id"
        ))
        .fetch_all(&self.pool)
        .await?
        .into_iter()
        .map(alias_from_row)
        .collect()
    }

    pub async fn alias(&self, id: &str) -> Result<Option<Alias>> {
        sqlx::query(&format!("SELECT {ALIAS_COLUMNS} FROM aliases WHERE id = ?"))
            .bind(id)
            .fetch_optional(&self.pool)
            .await?
            .map(alias_from_row)
            .transpose()
    }

    pub async fn alias_by_name(&self, name: &str) -> Result<Option<Alias>> {
        sqlx::query(&format!(
            "SELECT {ALIAS_COLUMNS} FROM aliases WHERE name = ? COLLATE NOCASE"
        ))
        .bind(name)
        .fetch_optional(&self.pool)
        .await?
        .map(alias_from_row)
        .transpose()
    }

    /// Includes retired names only to prevent stale requests falling through to Cursor.
    /// Use alias_by_name separately to resolve an active alias; retired names never resolve.
    pub async fn is_alias_name(&self, name: &str) -> Result<bool> {
        Ok(sqlx::query_scalar(
            "SELECT EXISTS(SELECT 1 FROM aliases WHERE name = ? COLLATE NOCASE)
             OR EXISTS(SELECT 1 FROM alias_retired_names WHERE name = ? COLLATE NOCASE)",
        )
        .bind(name)
        .bind(name)
        .fetch_one(&self.pool)
        .await?)
    }

    pub async fn save_alias(&self, id: Option<&str>, input: &AliasInput) -> Result<Alias> {
        let input = normalize_alias_input(input)?;
        let _write = self.writes.lock().await;
        let mut transaction = self.pool.begin().await?;
        let now = now_ms();
        let (id, created_at_ms) = match id {
            Some(id) => {
                let current = sqlx::query("SELECT name, created_at_ms FROM aliases WHERE id = ?")
                    .bind(id)
                    .fetch_optional(&mut *transaction)
                    .await?
                    .ok_or_else(|| Error::RunNotFound(format!("alias {id}")))?;
                let old_name: String = current.try_get("name")?;
                if old_name != input.name {
                    sqlx::query("INSERT INTO alias_retired_names (name) VALUES (?) ON CONFLICT(name) DO NOTHING")
                        .bind(&old_name)
                        .execute(&mut *transaction)
                        .await?;
                }
                (id.to_owned(), current.try_get("created_at_ms")?)
            }
            None => (uuid::Uuid::new_v4().to_string(), now),
        };
        let collision: bool = sqlx::query_scalar(
            "SELECT EXISTS(SELECT 1 FROM aliases WHERE name = ? COLLATE NOCASE AND id <> ?) OR EXISTS(SELECT 1 FROM model_configs WHERE model_id = ? COLLATE NOCASE OR model_hash = ? COLLATE NOCASE)",
        )
        .bind(&input.name)
        .bind(&id)
        .bind(&input.name)
        .bind(&input.name)
        .fetch_one(&mut *transaction)
        .await?;
        if collision {
            return Err(Error::Config(format!(
                "alias name '{}' is already used by an alias or API model",
                input.name
            )));
        }
        sqlx::query(
            "INSERT INTO aliases (id, name, description, enabled, targets_json, sticky, return_mode, created_at_ms, updated_at_ms)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description,
             enabled = excluded.enabled, targets_json = excluded.targets_json, sticky = excluded.sticky,
             return_mode = excluded.return_mode, updated_at_ms = excluded.updated_at_ms",
        )
        .bind(&id)
        .bind(&input.name)
        .bind(&input.description)
        .bind(input.enabled)
        .bind(serde_json::to_string(&input.targets)?)
        .bind(input.sticky)
        .bind(match input.return_mode {
            ReturnMode::NewSessions => "new_sessions",
            ReturnMode::Immediate => "immediate",
        })
        .bind(created_at_ms)
        .bind(now)
        .execute(&mut *transaction)
        .await?;
        transaction.commit().await?;
        Ok(Alias {
            id,
            config: input,
            created_at_ms,
            updated_at_ms: now,
        })
    }

    pub async fn delete_alias(&self, id: &str) -> Result<()> {
        let _write = self.writes.lock().await;
        let mut transaction = self.pool.begin().await?;
        sqlx::query("INSERT INTO alias_retired_names (name) SELECT name FROM aliases WHERE id = ? ON CONFLICT(name) DO NOTHING")
            .bind(id)
            .execute(&mut *transaction)
            .await?;
        let result = sqlx::query("DELETE FROM aliases WHERE id = ?")
            .bind(id)
            .execute(&mut *transaction)
            .await?;
        if result.rows_affected() != 1 {
            return Err(Error::RunNotFound(format!("alias {id}")));
        }
        transaction.commit().await?;
        Ok(())
    }

    pub async fn alias_settings(&self) -> Result<AliasSettings> {
        let json: Option<String> =
            sqlx::query_scalar("SELECT value_json FROM service_settings WHERE setting_key = ?")
                .bind(SETTINGS_KEY)
                .fetch_optional(&self.pool)
                .await?;
        let settings: AliasSettings = json
            .map(|json| serde_json::from_str(&json))
            .transpose()?
            .unwrap_or_default();
        settings.validate()?;
        Ok(settings)
    }

    pub async fn set_alias_settings(&self, settings: &AliasSettings) -> Result<AliasSettings> {
        settings.validate()?;
        let _write = self.writes.lock().await;
        let mut transaction = self.pool.begin().await?;
        sqlx::query(
            "INSERT INTO service_settings (setting_key, value_json, updated_at_ms) VALUES (?, ?, ?)
             ON CONFLICT(setting_key) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms",
        )
        .bind(SETTINGS_KEY)
        .bind(serde_json::to_string(settings)?)
        .bind(now_ms())
        .execute(&mut *transaction)
        .await?;
        transaction.commit().await?;
        Ok(settings.clone())
    }
}

/// Enforce the same namespace when API channels are created or renamed.
pub(super) async fn ensure_model_name_available(
    transaction: &mut Transaction<'_, Sqlite>,
    model_id: &str,
    model_hash: &str,
) -> Result<()> {
    let collision: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM aliases WHERE name = ? COLLATE NOCASE OR name = ? COLLATE NOCASE)
         OR EXISTS(SELECT 1 FROM alias_retired_names WHERE name = ? COLLATE NOCASE OR name = ? COLLATE NOCASE)",
    )
    .bind(model_id)
    .bind(model_hash)
    .bind(model_id)
    .bind(model_hash)
    .fetch_one(&mut **transaction)
    .await?;
    if collision {
        return Err(Error::Config(
            "API model id or hash is already used by an active or retired alias".into(),
        ));
    }
    Ok(())
}

fn alias_from_row(row: sqlx::sqlite::SqliteRow) -> Result<Alias> {
    let return_mode = match row.try_get::<&str, _>("return_mode")? {
        "new_sessions" => ReturnMode::NewSessions,
        "immediate" => ReturnMode::Immediate,
        value => return Err(Error::Config(format!("invalid alias return mode: {value}"))),
    };
    Ok(Alias {
        id: row.try_get("id")?,
        config: AliasInput {
            name: row.try_get("name")?,
            description: row.try_get("description")?,
            enabled: row.try_get("enabled")?,
            targets: serde_json::from_str(row.try_get::<&str, _>("targets_json")?)?,
            sticky: row.try_get("sticky")?,
            return_mode,
        },
        created_at_ms: row.try_get("created_at_ms")?,
        updated_at_ms: row.try_get("updated_at_ms")?,
    })
}

#[cfg(test)]
mod tests {
    use std::borrow::Cow;

    use super::*;
    use crate::{
        alias::configuration::{AliasTarget, SourceType},
        model::ModelConfigInput,
    };

    fn input(name: &str) -> AliasInput {
        AliasInput {
            name: name.into(),
            description: "Ordered channels".into(),
            enabled: true,
            sticky: true,
            return_mode: ReturnMode::NewSessions,
            targets: vec![
                AliasTarget {
                    source_type: SourceType::Plugin,
                    source_id: "example/provider".into(),
                    model_id: "first".into(),
                    enabled: true,
                },
                AliasTarget {
                    source_type: SourceType::Api,
                    source_id: uuid::Uuid::new_v4().to_string(),
                    model_id: String::new(),
                    enabled: false,
                },
            ],
        }
    }

    fn model_input(model_id: &str) -> ModelConfigInput {
        serde_json::from_value(serde_json::json!({
            "display_name": "API channel", "type": "openai", "base_url": "https://example.com",
            "api_key": "test", "tooltip_data": "Test", "model_id": model_id
        }))
        .unwrap()
    }

    async fn store() -> (tempfile::TempDir, Store) {
        let directory = tempfile::tempdir().unwrap();
        let store = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        (directory, store)
    }

    #[tokio::test]
    async fn ordered_targets_round_trip_and_empty_aliases_are_disabled() {
        let (_directory, store) = store().await;
        let original = input("MY-ALIAS");
        let saved = store.save_alias(None, &original).await.unwrap();
        assert_eq!(saved.config.name, "my-alias");
        assert_eq!(saved.config.targets, original.targets);
        assert_eq!(store.alias(&saved.id).await.unwrap(), Some(saved.clone()));
        assert_eq!(
            store.alias_by_name("MY-Alias").await.unwrap(),
            Some(saved.clone())
        );
        assert_eq!(store.aliases().await.unwrap(), vec![saved.clone()]);
        let mut reordered = saved.config.clone();
        reordered.targets.reverse();
        reordered.return_mode = ReturnMode::Immediate;
        reordered.sticky = false;
        let updated = store.save_alias(Some(&saved.id), &reordered).await.unwrap();
        assert_eq!(updated.created_at_ms, saved.created_at_ms);
        assert_eq!(
            store.alias(&saved.id).await.unwrap().unwrap().config,
            reordered
        );
        reordered.targets.clear();
        let empty = store.save_alias(Some(&saved.id), &reordered).await.unwrap();
        assert!(!empty.config.enabled);
        assert!(empty.config.targets.is_empty());
        store.delete_alias(&saved.id).await.unwrap();
        assert!(store.alias(&saved.id).await.unwrap().is_none());
        assert!(store.save_alias(Some(&saved.id), &original).await.is_err());
    }

    #[tokio::test]
    async fn case_insensitive_namespace_is_enforced_in_both_directions() {
        let (_directory, store) = store().await;
        let alias = store.save_alias(None, &input("route")).await.unwrap();
        assert!(store.save_alias(None, &input("ROUTE")).await.is_err());
        assert!(store.create_model(&model_input("Route")).await.is_err());
        let model = store.create_model(&model_input("Upstream")).await.unwrap();
        assert!(store.save_alias(None, &input("UPSTREAM")).await.is_err());
        assert!(store
            .save_alias(None, &input(&model.model_hash.to_uppercase()))
            .await
            .is_err());
        assert!(store
            .update_model(&model.model_hash, &model_input("ROUTE"))
            .await
            .is_err());
        assert!(store
            .save_alias(Some(&alias.id), &input("upstream"))
            .await
            .is_err());
        assert_eq!(
            store.alias(&alias.id).await.unwrap().unwrap().config.name,
            "route"
        );
        assert_eq!(
            store
                .model(&model.model_hash)
                .await
                .unwrap()
                .unwrap()
                .model_id,
            "Upstream"
        );
    }

    #[tokio::test]
    async fn source_identity_survives_edits_and_deleted_sources_remain_in_aliases() {
        let (_directory, store) = store().await;
        let mut config = model_input("upstream");
        let model = store.create_model(&config).await.unwrap();
        assert!(uuid::Uuid::parse_str(&model.source_id).is_ok());
        assert_eq!(model.supports_images, None);
        assert_eq!(model.supports_tools, None);
        let mut alias_input = input("route");
        alias_input.targets = vec![AliasTarget {
            source_type: SourceType::Api,
            source_id: model.source_id.clone(),
            model_id: String::new(),
            enabled: true,
        }];
        let alias = store.save_alias(None, &alias_input).await.unwrap();
        config.supports_images = Some(false);
        config.supports_tools = Some(true);
        let metadata_only = store
            .update_model(&model.model_hash, &config)
            .await
            .unwrap();
        assert_eq!(metadata_only.model_hash, model.model_hash);
        assert_eq!(metadata_only.source_id, model.source_id);
        config.model_id = "renamed-upstream".into();
        let updated = store
            .update_model(&model.model_hash, &config)
            .await
            .unwrap();
        assert_ne!(updated.model_hash, model.model_hash);
        assert_eq!(updated.source_id, model.source_id);
        assert_eq!(updated.supports_images, Some(false));
        assert_eq!(updated.supports_tools, Some(true));
        assert_eq!(
            store
                .model_by_source_id(&model.source_id)
                .await
                .unwrap()
                .unwrap()
                .model_hash,
            updated.model_hash
        );
        assert!(store.model(&model.model_hash).await.unwrap().is_none());
        store.delete_model(&updated.model_hash).await.unwrap();
        assert!(store
            .model_by_source_id(&model.source_id)
            .await
            .unwrap()
            .is_none());
        assert_eq!(store.alias(&alias.id).await.unwrap(), Some(alias.clone()));
        // Saving an existing dangling reference remains valid and visible.
        store
            .save_alias(Some(&alias.id), &alias.config)
            .await
            .unwrap();
    }

    #[tokio::test]
    async fn settings_defaults_validation_and_round_trip() {
        let (_directory, store) = store().await;
        assert_eq!(
            store.alias_settings().await.unwrap(),
            AliasSettings::default()
        );
        let settings = AliasSettings {
            authorization_seconds: 900,
            rate_limit_seconds: 1,
            ..AliasSettings::default()
        };
        assert_eq!(store.set_alias_settings(&settings).await.unwrap(), settings);
        assert_eq!(store.alias_settings().await.unwrap(), settings);
        let invalid = AliasSettings {
            authorization_seconds: 599,
            ..settings.clone()
        };
        assert!(store.set_alias_settings(&invalid).await.is_err());
        assert_eq!(store.alias_settings().await.unwrap(), settings);
    }

    #[tokio::test]
    async fn renamed_and_deleted_names_remain_local_without_resolving_old_targets() {
        let (directory, store) = store().await;
        assert!(!store.is_alias_name("unknown").await.unwrap());
        let original = store.save_alias(None, &input("original")).await.unwrap();
        assert!(store.is_alias_name("ORIGINAL").await.unwrap());
        let renamed = store
            .save_alias(Some(&original.id), &input("renamed"))
            .await
            .unwrap();
        assert_eq!(renamed.id, original.id);
        assert!(store.is_alias_name("ORIGINAL").await.unwrap());
        assert!(store.alias_by_name("original").await.unwrap().is_none());
        assert_eq!(
            store.alias_by_name("RENAMED").await.unwrap(),
            Some(renamed.clone())
        );
        assert!(store.create_model(&model_input("Original")).await.is_err());
        store.delete_alias(&renamed.id).await.unwrap();
        for name in ["original", "RENAMED"] {
            assert!(store.is_alias_name(name).await.unwrap());
            assert!(store.alias_by_name(name).await.unwrap().is_none());
        }
        assert!(store.aliases().await.unwrap().is_empty());
        assert!(store.delete_alias(&renamed.id).await.is_err());
        // Explicitly creating a new alias can reuse a retired name, but never the old identity.
        let recreated = store.save_alias(None, &input("original")).await.unwrap();
        assert_ne!(recreated.id, original.id);
        assert_eq!(
            store.alias_by_name("original").await.unwrap(),
            Some(recreated.clone())
        );
        store.delete_alias(&recreated.id).await.unwrap();
        store.pool.close().await;
        let reopened = Store::connect(&format!(
            "sqlite://{}",
            directory.path().join("test.db").display()
        ))
        .await
        .unwrap();
        for name in ["original", "renamed"] {
            assert!(reopened.is_alias_name(name).await.unwrap());
            assert!(reopened.alias_by_name(name).await.unwrap().is_none());
        }
    }

    #[tokio::test]
    async fn failed_rename_rolls_back_retired_name_and_unchanged_name_is_not_retired() {
        let (_directory, store) = store().await;
        let original = store.save_alias(None, &input("original")).await.unwrap();
        store.save_alias(None, &input("occupied")).await.unwrap();
        assert!(store
            .save_alias(Some(&original.id), &input("occupied"))
            .await
            .is_err());
        assert_eq!(
            store.alias_by_name("original").await.unwrap(),
            Some(original.clone())
        );
        store
            .save_alias(Some(&original.id), &input("ORIGINAL"))
            .await
            .unwrap();
        let retired: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM alias_retired_names")
            .fetch_one(store.pool())
            .await
            .unwrap();
        assert_eq!(retired, 0);
    }

    #[tokio::test]
    async fn version_eleven_upgrade_preserves_active_aliases_and_adds_retired_name_storage() {
        let directory = tempfile::tempdir().unwrap();
        let url = format!(
            "sqlite://{}?mode=rwc",
            directory.path().join("v11.db").display()
        );
        let pool = sqlx::SqlitePool::connect(&url).await.unwrap();
        let migrations = sqlx::migrate!("./migrations");
        let previous = sqlx::migrate::Migrator {
            migrations: Cow::Owned(
                migrations
                    .iter()
                    .filter(|migration| migration.version < 12)
                    .cloned()
                    .collect(),
            ),
            ..sqlx::migrate::Migrator::DEFAULT
        };
        previous.run(&pool).await.unwrap();
        sqlx::query("INSERT INTO aliases (id, name, description, enabled, targets_json, sticky, return_mode, created_at_ms, updated_at_ms) VALUES ('existing-id', 'existing', 'Keep description', 0, '[]', 1, 'new_sessions', 11, 22)")
            .execute(&pool).await.unwrap();
        pool.close().await;
        let store = Store::connect(&url).await.unwrap();
        let alias = store.alias_by_name("EXISTING").await.unwrap().unwrap();
        assert_eq!(alias.id, "existing-id");
        assert_eq!(alias.config.description, "Keep description");
        assert_eq!((alias.created_at_ms, alias.updated_at_ms), (11, 22));
        assert!(store.is_alias_name("existing").await.unwrap());
        let retired: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM alias_retired_names")
            .fetch_one(store.pool())
            .await
            .unwrap();
        assert_eq!(retired, 0);
        store
            .save_alias(Some(&alias.id), &input("new-name"))
            .await
            .unwrap();
        assert!(store.is_alias_name("EXISTING").await.unwrap());
        assert!(store.alias_by_name("existing").await.unwrap().is_none());
        assert!(
            sqlx::query("INSERT INTO alias_retired_names (name) VALUES ('existing')")
                .execute(store.pool())
                .await
                .is_err()
        );
        store.delete_alias(&alias.id).await.unwrap();
        assert!(store.is_alias_name("NEW-NAME").await.unwrap());
    }

    #[tokio::test]
    async fn previous_schema_upgrade_backfills_unique_v4_ids_and_preserves_data() {
        let directory = tempfile::tempdir().unwrap();
        let url = format!(
            "sqlite://{}?mode=rwc",
            directory.path().join("old.db").display()
        );
        let pool = sqlx::SqlitePool::connect(&url).await.unwrap();
        let migrations = sqlx::migrate!("./migrations");
        let previous = sqlx::migrate::Migrator {
            migrations: Cow::Owned(
                migrations
                    .iter()
                    .filter(|migration| migration.version < 10)
                    .cloned()
                    .collect(),
            ),
            ..sqlx::migrate::Migrator::DEFAULT
        };
        previous.run(&pool).await.unwrap();
        for (hash, name) in [("old-hash", "old-model"), ("other-hash", "other-model")] {
            sqlx::query("INSERT INTO model_configs (model_hash, display_name, group_name, model_type, base_url, api_key, tooltip_data, model_id, context_window_tokens, created_at_ms, updated_at_ms) VALUES (?, 'Old display', 'Old group', 'openai', 'https://example.com', 'secret', 'Old tooltip', ?, 12345, 11, 22)")
                .bind(hash).bind(name).execute(&pool).await.unwrap();
        }
        pool.close().await;
        let store = Store::connect(&url).await.unwrap();
        let model = store.model("old-hash").await.unwrap().unwrap();
        let other = store.model("other-hash").await.unwrap().unwrap();
        let uuid = uuid::Uuid::parse_str(&model.source_id).unwrap();
        assert_eq!(uuid.get_version_num(), 4);
        assert_ne!(model.source_id, other.source_id);
        assert_eq!(model.model_id, "old-model");
        assert_eq!(model.api_key, "secret");
        assert_eq!(model.group_name.as_deref(), Some("Old group"));
        assert_eq!(model.context_window_tokens, Some(12345));
        assert_eq!((model.created_at_ms, model.updated_at_ms), (11, 22));
        assert_eq!((model.supports_images, model.supports_tools), (None, None));
        assert!(store.aliases().await.unwrap().is_empty());
        let id = model.source_id;
        store.pool.close().await;
        let reopened = Store::connect(&url).await.unwrap();
        assert_eq!(
            reopened.model("old-hash").await.unwrap().unwrap().source_id,
            id
        );
    }
}
