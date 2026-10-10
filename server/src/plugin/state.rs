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
    pub context_window_tokens: Option<u64>,
    #[serde(default)]
    pub supports_tools: Option<bool>,
    #[serde(default)]
    pub supports_images: Option<bool>,
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
            context_window_tokens: object
                .get("contextWindowTokens")
                .and_then(serde_json::Value::as_u64),
            supports_tools: capability("tools"),
            supports_images: capability("images"),
            images: object
                .get("images")
                .and_then(serde_json::Value::as_bool)
                .unwrap_or_else(|| capability("images").unwrap_or(false)),
            enabled: true,
            private_data: object
                .get("privateData")
                .cloned()
                .unwrap_or(serde_json::Value::Null),
        })
    }

    /// 传给插件的模型快照(SDK 的 ModelSnapshot)。
    pub fn snapshot(&self) -> serde_json::Value {
        let mut capabilities = serde_json::Map::new();
        if let Some(images) = self.supports_images {
            capabilities.insert("images".into(), images.into());
        }
        if let Some(tools) = self.supports_tools {
            capabilities.insert("tools".into(), tools.into());
        }
        let mut snapshot = serde_json::json!({
            "id": self.id,
            "displayName": self.display_name,
            "description": self.description,
            "maxOutputTokens": self.max_output_tokens,
            "images": self.images,
            "capabilities": capabilities,
            "privateData": self.private_data,
        });
        if let Some(tokens) = self.context_window_tokens {
            snapshot["contextWindowTokens"] = tokens.into();
        }
        snapshot
    }
}

/// 资源与模型目录的核心存储,构建在插件私有 JSON 文件之上。
#[derive(Clone)]
pub struct PluginStateStore {
    data: PluginDataStore,
}

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
        let mut records = self.resources(plugin_id, resource_type).await?;
        let now = now_ms();
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
        self.save_resources(plugin_id, resource_type, &records)
            .await?;
        Ok(outcome)
    }

    pub async fn apply_patch(
        &self,
        plugin_id: &str,
        resource_type: &str,
        resource_id: &str,
        patch: ResourcePatch,
    ) -> Result<()> {
        let mut records = self.resources(plugin_id, resource_type).await?;
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
        self.save_resources(plugin_id, resource_type, &records)
            .await
    }

    pub async fn remove_resource(
        &self,
        plugin_id: &str,
        resource_type: &str,
        resource_id: &str,
    ) -> Result<ResourceRecord> {
        let mut records = self.resources(plugin_id, resource_type).await?;
        let index = records
            .iter()
            .position(|record| record.id == resource_id)
            .ok_or_else(|| Error::RunNotFound(format!("plugin resource {resource_id}")))?;
        let removed = records.remove(index);
        self.save_resources(plugin_id, resource_type, &records)
            .await?;
        Ok(removed)
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

    pub async fn clear(&self, plugin_id: &str) -> Result<()> {
        self.data.clear(plugin_id).await
    }

    async fn save_resources(
        &self,
        plugin_id: &str,
        resource_type: &str,
        records: &[ResourceRecord],
    ) -> Result<()> {
        self.data
            .update(
                plugin_id,
                &resource_key(resource_type),
                &serde_json::to_value(records)?,
            )
            .await
    }
}

pub fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|duration| duration.as_millis().min(i64::MAX as u128) as i64)
        .unwrap_or_default()
}

fn resource_key(resource_type: &str) -> String {
    format!("resources-{resource_type}")
}

fn model_key(provider_id: &str) -> String {
    format!("models-{provider_id}")
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

    #[test]
    fn model_metadata_roundtrips_known_false_and_unknown() {
        let known = StoredModel::from_definition(&serde_json::json!({
            "id": "known", "displayName": "Known",
            "contextWindowTokens": 128000, "maxOutputTokens": 16000,
            "capabilities": {"tools": true, "images": false},
        }))
        .unwrap();
        assert_eq!(known.context_window_tokens, Some(128000));
        assert_eq!(known.supports_tools, Some(true));
        assert_eq!(known.supports_images, Some(false));
        assert!(!known.images);
        let stored: StoredModel =
            serde_json::from_value(serde_json::to_value(&known).unwrap()).unwrap();
        let snapshot = stored.snapshot();
        assert_eq!(snapshot["contextWindowTokens"], 128000);
        assert_eq!(
            snapshot["capabilities"],
            serde_json::json!({"tools": true, "images": false})
        );
        let restored = StoredModel::from_definition(&snapshot).unwrap();
        assert_eq!(restored.supports_images, Some(false));

        let unknown = StoredModel::from_definition(&serde_json::json!({
            "id": "unknown", "displayName": "Unknown",
        }))
        .unwrap();
        assert_eq!(unknown.supports_images, None);
        assert_eq!(unknown.supports_tools, None);
        assert_eq!(unknown.context_window_tokens, None);
        assert!(
            !unknown.images,
            "ordinary image flag retains its false default"
        );
        assert_eq!(unknown.snapshot()["capabilities"], serde_json::json!({}));
        assert!(unknown.snapshot().get("contextWindowTokens").is_none());
        assert_eq!(
            StoredModel::from_definition(&unknown.snapshot())
                .unwrap()
                .supports_images,
            None
        );
        let catalog_image = StoredModel::from_definition(&serde_json::json!({
            "id": "catalog", "displayName": "Catalog", "images": true,
        }))
        .unwrap();
        assert!(
            catalog_image.images,
            "ordinary catalog image behavior is independent of verification"
        );
        assert_eq!(catalog_image.supports_images, None);
        let restored = StoredModel::from_definition(&catalog_image.snapshot()).unwrap();
        assert!(restored.images);
        assert_eq!(restored.supports_images, None);
    }

    #[test]
    fn only_ready_or_expired_cooling_resources_are_available() {
        assert!(ResourceState::Ready.is_ready(100));
        assert!(ResourceState::Cooling {
            retry_at_ms: Some(100),
            message: None
        }
        .is_ready(100));
        assert!(!ResourceState::Cooling {
            retry_at_ms: Some(101),
            message: None
        }
        .is_ready(100));
        assert!(!ResourceState::Cooling {
            retry_at_ms: None,
            message: None
        }
        .is_ready(100));
        assert!(!ResourceState::Invalid { message: None }.is_ready(100));
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
}
