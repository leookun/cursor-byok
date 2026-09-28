//! Owns core-side persistence of plugin resources and model catalogs.
use serde::{Deserialize, Serialize};

use super::data::PluginDataStore;
use crate::{Error, Result};

/// 核心理解的资源运行状态;插件只能通过 draft/patch/report 改变它。
#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum ResourceState {
    Ready,
    Cooling {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        retry_at_ms: Option<i64>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        message: Option<String>,
    },
    Invalid {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        message: Option<String>,
    },
}

impl ResourceState {
    /// 冷却到期后自动恢复可用。
    pub fn is_ready(&self, now_ms: i64) -> bool {
        match self {
            Self::Ready => true,
            Self::Cooling { retry_at_ms, .. } => retry_at_ms.is_some_and(|at| at <= now_ms),
            Self::Invalid { .. } => false,
        }
    }
}

/// 核心持久化的一条插件资源。`private_data` 只回传给插件。
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ResourceRecord {
    pub id: String,
    pub key: String,
    pub private_data: serde_json::Value,
    pub state: ResourceState,
    pub created_at_ms: i64,
    pub updated_at_ms: i64,
}

impl ResourceRecord {
    /// 传给插件的快照形状(SDK 的 ResourceSnapshot)。
    pub fn snapshot(&self, resource_type: &str) -> serde_json::Value {
        serde_json::json!({
            "id": self.id,
            "type": resource_type,
            "key": self.key,
            "privateData": self.private_data,
            "state": state_json(&self.state),
        })
    }
}

fn state_json(state: &ResourceState) -> serde_json::Value {
    match state {
        ResourceState::Ready => serde_json::json!({ "status": "ready" }),
        ResourceState::Cooling {
            retry_at_ms,
            message,
        } => serde_json::json!({
            "status": "cooling",
            "retryAtMs": retry_at_ms,
            "message": message,
        }),
        ResourceState::Invalid { message } => serde_json::json!({
            "status": "invalid",
            "message": message,
        }),
    }
}

/// 插件返回的新资源(SDK 的 ResourceDraft)。
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceDraft {
    pub key: String,
    pub private_data: serde_json::Value,
    #[serde(default)]
    pub state: Option<ResourceStateInput>,
}

/// 插件对单条资源的部分更新(SDK 的 ResourcePatch)。
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourcePatch {
    #[serde(default)]
    pub private_data: Option<serde_json::Value>,
    #[serde(default)]
    pub state: Option<ResourceStateInput>,
}

/// SDK 侧 camelCase 状态输入,转换成核心存储形状。
#[derive(Clone, Debug, Deserialize)]
#[serde(tag = "status", rename_all = "kebab-case", deny_unknown_fields)]
pub enum ResourceStateInput {
    Ready,
    Cooling {
        #[serde(default, rename = "retryAtMs")]
        retry_at_ms: Option<i64>,
        #[serde(default)]
        message: Option<String>,
    },
    Invalid {
        #[serde(default)]
        message: Option<String>,
    },
}

impl From<ResourceStateInput> for ResourceState {
    fn from(input: ResourceStateInput) -> Self {
        match input {
            ResourceStateInput::Ready => Self::Ready,
            ResourceStateInput::Cooling {
                retry_at_ms,
                message,
            } => Self::Cooling {
                retry_at_ms,
                message,
            },
            ResourceStateInput::Invalid { message } => Self::Invalid { message },
        }
    }
}

/// 插件发现的一个模型(SDK 的 ModelDefinition),由核心整体替换目录。
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct StoredModel {
    pub id: String,
    pub display_name: String,
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub max_output_tokens: Option<u64>,
    #[serde(default)]
    pub images: bool,
    #[serde(default = "default_model_enabled")]
    pub enabled: bool,
    #[serde(default)]
    pub private_data: serde_json::Value,
}

fn default_model_enabled() -> bool {
    true
}

impl StoredModel {
    pub fn from_definition(value: &serde_json::Value) -> Result<Self> {
        let object = value
            .as_object()
            .ok_or_else(|| Error::Protocol("plugin model definition must be an object".into()))?;
        let id = object
            .get("id")
            .and_then(serde_json::Value::as_str)
            .filter(|id| !id.trim().is_empty())
            .ok_or_else(|| Error::Protocol("plugin model definition requires id".into()))?;
        let display_name = object
            .get("displayName")
            .and_then(serde_json::Value::as_str)
            .filter(|name| !name.trim().is_empty())
            .ok_or_else(|| {
                Error::Protocol("plugin model definition requires displayName".into())
            })?;
        let capabilities = object
            .get("capabilities")
            .and_then(|value| value.as_object());
        let capability = |name: &str| {
            capabilities
                .and_then(|value| value.get(name))
                .and_then(serde_json::Value::as_bool)
                .unwrap_or(false)
        };
        Ok(Self {
            id: id.to_owned(),
            display_name: display_name.to_owned(),
            description: object
                .get("description")
                .and_then(serde_json::Value::as_str)
                .map(str::to_owned),
            max_output_tokens: object
                .get("maxOutputTokens")
                .and_then(serde_json::Value::as_u64),
            images: capability("images"),
            enabled: true,
            private_data: object
                .get("privateData")
                .cloned()
                .unwrap_or(serde_json::Value::Null),
        })
    }

    /// 传给插件的模型快照(SDK 的 ModelSnapshot)。
    pub fn snapshot(&self) -> serde_json::Value {
        serde_json::json!({
            "id": self.id,
            "displayName": self.display_name,
            "description": self.description,
            "maxOutputTokens": self.max_output_tokens,
            "capabilities": { "images": self.images },
            "privateData": self.private_data,
        })
    }
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct StoredAutomation {
    #[serde(default)]
    pub enabled: bool,
    #[serde(default)]
    pub last_run_at_ms: Option<i64>,
    /// Local calendar date of the last completed run. Kept beside the
    /// timestamp so daily due checks remain stable across restarts.
    #[serde(default)]
    pub last_run_date: Option<String>,
    #[serde(default)]
    pub last_run_succeeded: Option<bool>,
    #[serde(default)]
    pub last_run_failed: Option<bool>,
}

impl StoredAutomation {
    pub fn with_default(default_enabled: bool) -> Self {
        Self {
            enabled: default_enabled,
            last_run_at_ms: None,
            last_run_date: None,
            last_run_succeeded: None,
            last_run_failed: None,
        }
    }

    fn normalize(&mut self) {
        if self.last_run_date.is_none() {
            self.last_run_date = self.last_run_at_ms.and_then(local_date);
        }
        if self.last_run_succeeded.is_some() || self.last_run_failed.is_some() {
            let succeeded = self.last_run_succeeded.unwrap_or(false);
            self.last_run_succeeded = Some(succeeded);
            self.last_run_failed = Some(!succeeded);
        }
    }

    pub fn is_due(&self, now_ms: i64) -> bool {
        let last_run_date = self
            .last_run_date
            .clone()
            .or_else(|| self.last_run_at_ms.and_then(local_date));
        match (last_run_date, local_date(now_ms)) {
            (Some(last), Some(today)) => last != today,
            _ => true,
        }
    }
}

/// Return the host-local calendar date for a Unix millisecond timestamp.
pub fn local_date(timestamp_ms: i64) -> Option<String> {
    chrono::DateTime::<chrono::Utc>::from_timestamp_millis(timestamp_ms)
        .map(|value| value.with_timezone(&chrono::Local).date_naive().to_string())
}

/// 资源与模型目录的核心存储,构建在插件私有 JSON 文件之上。
#[derive(Clone)]
pub struct PluginStateStore {
    data: PluginDataStore,
}

#[derive(serde::Deserialize, serde::Serialize)]
pub struct UpsertOutcome {
    pub added: usize,
    pub updated: usize,
}

impl PluginStateStore {
    pub fn new(data: PluginDataStore) -> Self {
        Self { data }
    }

    pub async fn resources(
        &self,
        plugin_id: &str,
        resource_type: &str,
    ) -> Result<Vec<ResourceRecord>> {
        let value = self
            .data
            .read(plugin_id, &resource_key(resource_type))
            .await?;
        if value.is_null() {
            return Ok(Vec::new());
        }
        Ok(serde_json::from_value(value)?)
    }

    pub async fn upsert_resources(
        &self,
        plugin_id: &str,
        resource_type: &str,
        drafts: Vec<ResourceDraft>,
    ) -> Result<UpsertOutcome> {
        let now = now_ms();
        self.data
            .update_with_result(plugin_id, &resource_key(resource_type), move |value| {
                let mut records = decode_resources(value)?;
                let mut outcome = UpsertOutcome {
                    added: 0,
                    updated: 0,
                };
                for draft in drafts {
                    if draft.key.trim().is_empty() {
                        return Err(Error::Protocol("plugin resource draft requires key".into()));
                    }
                    let state = draft
                        .state
                        .map_or(ResourceState::Ready, ResourceState::from);
                    match records.iter_mut().find(|record| record.key == draft.key) {
                        Some(existing) => {
                            existing.private_data = draft.private_data;
                            existing.state = state;
                            existing.updated_at_ms = now;
                            outcome.updated += 1;
                        }
                        None => {
                            records.push(ResourceRecord {
                                id: uuid::Uuid::new_v4().to_string(),
                                key: draft.key,
                                private_data: draft.private_data,
                                state,
                                created_at_ms: now,
                                updated_at_ms: now,
                            });
                            outcome.added += 1;
                        }
                    }
                }
                Ok((serde_json::to_value(records)?, outcome))
            })
            .await
    }

    pub async fn apply_patch(
        &self,
        plugin_id: &str,
        resource_type: &str,
        resource_id: &str,
        patch: ResourcePatch,
    ) -> Result<()> {
        self.data
            .update_with(plugin_id, &resource_key(resource_type), move |value| {
                let mut records = decode_resources(value)?;
                let record = records
                    .iter_mut()
                    .find(|record| record.id == resource_id)
                    .ok_or_else(|| Error::RunNotFound(format!("plugin resource {resource_id}")))?;
                if let Some(private_data) = patch.private_data {
                    record.private_data = private_data;
                }
                if let Some(state) = patch.state {
                    record.state = state.into();
                }
                record.updated_at_ms = now_ms();
                serde_json::to_value(records).map_err(Into::into)
            })
            .await
            .map(|_| ())
    }

    pub async fn remove_resource(
        &self,
        plugin_id: &str,
        resource_type: &str,
        resource_id: &str,
    ) -> Result<ResourceRecord> {
        self.data
            .update_with_result(plugin_id, &resource_key(resource_type), |value| {
                let mut records = decode_resources(value)?;
                let index = records
                    .iter()
                    .position(|record| record.id == resource_id)
                    .ok_or_else(|| Error::RunNotFound(format!("plugin resource {resource_id}")))?;
                let removed = records.remove(index);
                Ok((serde_json::to_value(records)?, removed))
            })
            .await
    }

    pub async fn models(&self, plugin_id: &str, provider_id: &str) -> Result<Vec<StoredModel>> {
        let value = self.data.read(plugin_id, &model_key(provider_id)).await?;
        if value.is_null() {
            return Ok(Vec::new());
        }
        Ok(serde_json::from_value(value)?)
    }

    pub async fn replace_models(
        &self,
        plugin_id: &str,
        provider_id: &str,
        models: &[StoredModel],
    ) -> Result<()> {
        let previous = self.models(plugin_id, provider_id).await?;
        let models = models
            .iter()
            .cloned()
            .map(|mut model| {
                if let Some(old) = previous.iter().find(|old| old.id == model.id) {
                    model.enabled = old.enabled;
                }
                model
            })
            .collect::<Vec<_>>();
        self.data
            .update(
                plugin_id,
                &model_key(provider_id),
                &serde_json::to_value(models)?,
            )
            .await
    }

    pub async fn set_model_enabled(
        &self,
        plugin_id: &str,
        provider_id: &str,
        model_id: &str,
        enabled: bool,
    ) -> Result<()> {
        let mut models = self.models(plugin_id, provider_id).await?;
        let model = models
            .iter_mut()
            .find(|model| model.id == model_id)
            .ok_or_else(|| Error::RunNotFound(format!("plugin model {model_id}")))?;
        model.enabled = enabled;
        self.data
            .update(
                plugin_id,
                &model_key(provider_id),
                &serde_json::to_value(models)?,
            )
            .await
    }

    pub async fn automation(
        &self,
        plugin_id: &str,
        resource_type: &str,
        action_id: &str,
        default_enabled: bool,
    ) -> Result<StoredAutomation> {
        let value = self
            .data
            .read(plugin_id, &automation_key(resource_type, action_id))
            .await?;
        let mut state = if value.is_null() {
            StoredAutomation::with_default(default_enabled)
        } else {
            serde_json::from_value(value)?
        };
        state.normalize();
        Ok(state)
    }

    pub async fn set_automation_enabled(
        &self,
        plugin_id: &str,
        resource_type: &str,
        action_id: &str,
        enabled: bool,
        default_enabled: bool,
    ) -> Result<StoredAutomation> {
        let value = self
            .data
            .update_with(
                plugin_id,
                &automation_key(resource_type, action_id),
                |value| {
                    let mut state = if value.is_null() {
                        StoredAutomation::with_default(default_enabled)
                    } else {
                        serde_json::from_value(value)?
                    };
                    state.enabled = enabled;
                    state.normalize();
                    Ok(serde_json::to_value(&state)?)
                },
            )
            .await?;
        Ok(serde_json::from_value(value)?)
    }

    pub async fn record_automation_run(
        &self,
        plugin_id: &str,
        resource_type: &str,
        action_id: &str,
        succeeded: bool,
        ran_at_ms: i64,
        default_enabled: bool,
    ) -> Result<StoredAutomation> {
        let value = self
            .data
            .update_with(
                plugin_id,
                &automation_key(resource_type, action_id),
                |value| {
                    let mut state = if value.is_null() {
                        StoredAutomation::with_default(default_enabled)
                    } else {
                        serde_json::from_value(value)?
                    };
                    state.last_run_at_ms = Some(ran_at_ms);
                    state.last_run_date = local_date(ran_at_ms);
                    state.last_run_succeeded = Some(succeeded);
                    state.last_run_failed = Some(!succeeded);
                    Ok(serde_json::to_value(&state)?)
                },
            )
            .await?;
        Ok(serde_json::from_value(value)?)
    }

    pub async fn clear(&self, plugin_id: &str) -> Result<()> {
        self.data.clear(plugin_id).await
    }
}

pub fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|duration| duration.as_millis().min(i64::MAX as u128) as i64)
        .unwrap_or_default()
}

fn decode_resources(value: serde_json::Value) -> Result<Vec<ResourceRecord>> {
    if value.is_null() {
        Ok(Vec::new())
    } else {
        Ok(serde_json::from_value(value)?)
    }
}
fn resource_key(resource_type: &str) -> String {
    format!("resources-{resource_type}")
}

fn model_key(provider_id: &str) -> String {
    format!("models-{provider_id}")
}

fn automation_key(resource_type: &str, action_id: &str) -> String {
    format!("automation-{resource_type}-{action_id}")
}
#[cfg(test)]
mod tests {
    use super::*;

    fn store() -> (tempfile::TempDir, PluginStateStore) {
        let root = tempfile::tempdir().unwrap();
        let data = PluginDataStore::for_test(root.path().join("data")).unwrap();
        (root, PluginStateStore::new(data))
    }

    #[tokio::test]
    async fn upserts_resources_by_key_and_applies_patches() {
        let (_root, store) = store();
        let outcome = store
            .upsert_resources(
                "dev.example",
                "account",
                vec![ResourceDraft {
                    key: "acct-1".into(),
                    private_data: serde_json::json!({"token":"one"}),
                    state: None,
                }],
            )
            .await
            .unwrap();
        assert_eq!(outcome.added, 1);
        let outcome = store
            .upsert_resources(
                "dev.example",
                "account",
                vec![ResourceDraft {
                    key: "acct-1".into(),
                    private_data: serde_json::json!({"token":"two"}),
                    state: None,
                }],
            )
            .await
            .unwrap();
        assert_eq!(outcome.updated, 1);
        let records = store.resources("dev.example", "account").await.unwrap();
        assert_eq!(records.len(), 1);
        assert_eq!(records[0].private_data["token"], "two");

        store
            .apply_patch(
                "dev.example",
                "account",
                &records[0].id,
                ResourcePatch {
                    private_data: None,
                    state: Some(ResourceStateInput::Cooling {
                        retry_at_ms: Some(200),
                        message: None,
                    }),
                },
            )
            .await
            .unwrap();
        let records = store.resources("dev.example", "account").await.unwrap();
        assert!(!records[0].state.is_ready(100));
        assert!(records[0].state.is_ready(300), "cooling expires over time");
    }

    #[tokio::test]
    async fn concurrent_resource_mutations_do_not_lose_records_or_patches() {
        let (_root, store) = store();
        let first = store
            .upsert_resources(
                "dev.example",
                "account",
                vec![ResourceDraft {
                    key: "acct-1".into(),
                    private_data: serde_json::json!({"token":"one"}),
                    state: None,
                }],
            )
            .await
            .unwrap();
        assert_eq!(first.added, 1);
        let id = store.resources("dev.example", "account").await.unwrap()[0]
            .id
            .clone();
        let (patch, import) = tokio::join!(
            store.apply_patch(
                "dev.example",
                "account",
                &id,
                ResourcePatch {
                    private_data: None,
                    state: Some(ResourceStateInput::Invalid { message: None }),
                },
            ),
            store.upsert_resources(
                "dev.example",
                "account",
                vec![ResourceDraft {
                    key: "acct-2".into(),
                    private_data: serde_json::json!({"token":"two"}),
                    state: None,
                }],
            ),
        );
        patch.unwrap();
        import.unwrap();
        let records = store.resources("dev.example", "account").await.unwrap();
        assert_eq!(records.len(), 2);
        let patched = records.iter().find(|record| record.id == id).unwrap();
        assert!(matches!(patched.state, ResourceState::Invalid { .. }));
    }

    #[tokio::test]
    async fn replaces_model_catalogs() {
        let (_root, store) = store();
        let model = StoredModel::from_definition(&serde_json::json!({
            "id": "gpt-test",
            "displayName": "GPT Test",
            "capabilities": {"images": true},
            "privateData": {"reasoningEfforts": ["low"]},
        }))
        .unwrap();
        store
            .replace_models("dev.example", "codex", &[model])
            .await
            .unwrap();
        let models = store.models("dev.example", "codex").await.unwrap();
        assert_eq!(models.len(), 1);
        assert!(models[0].images);
        assert_eq!(models[0].private_data["reasoningEfforts"][0], "low");
    }

    #[tokio::test]
    async fn persists_automation_state_and_records_failures() {
        let (_root, store) = store();
        let initial = store
            .automation("dev.example", "account", "check-in", true)
            .await
            .unwrap();
        assert!(initial.enabled);
        assert_eq!(initial.last_run_at_ms, None);
        assert_eq!(initial.last_run_succeeded, None);
        assert_eq!(initial.last_run_failed, None);

        let state = store
            .set_automation_enabled("dev.example", "account", "check-in", false, true)
            .await
            .unwrap();
        assert!(!state.enabled);
        let state = store
            .record_automation_run("dev.example", "account", "check-in", false, 42, true)
            .await
            .unwrap();
        assert_eq!(state.last_run_at_ms, Some(42));
        assert_eq!(state.last_run_succeeded, Some(false));
        assert_eq!(state.last_run_failed, Some(true));

        let reloaded = store
            .automation("dev.example", "account", "check-in", true)
            .await
            .unwrap();
        assert_eq!(reloaded.last_run_at_ms, Some(42));
        assert_eq!(reloaded.last_run_succeeded, Some(false));
        assert_eq!(reloaded.last_run_failed, Some(true));
    }

    #[test]
    fn daily_due_uses_local_date_and_treats_failures_as_complete() {
        let today_ms = chrono::Local::now().timestamp_millis();
        assert!(StoredAutomation::with_default(false).is_due(today_ms));
        let today = StoredAutomation {
            enabled: true,
            last_run_at_ms: Some(today_ms),
            last_run_date: local_date(today_ms),
            last_run_succeeded: Some(false),
            last_run_failed: Some(true),
        };
        assert!(!today.is_due(today_ms + 60_000));
        let yesterday_ms = (chrono::Local::now() - chrono::Duration::days(1)).timestamp_millis();
        assert!(today.is_due(yesterday_ms));
    }
}
