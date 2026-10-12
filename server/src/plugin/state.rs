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

pub(super) fn state_json(state: &ResourceState) -> serde_json::Value {
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

/// Global selection for one plugin/resource type, independent of conversations.
/// Revision also invalidates discovery results when the active credentials change.
#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct ResourceSelection {
    pub active_resource_id: Option<String>,
    pub automatic_switching: bool,
    pub revision: u64,
}

impl ResourceSelection {
    fn advance(&mut self) -> Result<()> {
        self.revision = self
            .revision
            .checked_add(1)
            .ok_or_else(|| Error::Config("plugin selection revision exhausted".into()))?;
        Ok(())
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
        decode(
            self.data
                .read(plugin_id, &resource_key(resource_type))
                .await?,
        )
    }

    pub async fn selection(
        &self,
        plugin_id: &str,
        resource_type: &str,
    ) -> Result<ResourceSelection> {
        decode(
            self.data
                .read(plugin_id, &selection_key(resource_type))
                .await?,
        )
    }

    /// Explicit user edits always advance revision, including selecting the same ID.
    pub async fn set_selection(
        &self,
        plugin_id: &str,
        resource_type: &str,
        active_resource_id: Option<String>,
        automatic_switching: bool,
    ) -> Result<ResourceSelection> {
        self.modify_resources(plugin_id, resource_type, |records, selection| {
            if let Some(id) = &active_resource_id {
                if !records.iter().any(|record| &record.id == id) {
                    return Err(Error::RunNotFound(format!("plugin resource {id}")));
                }
            }
            selection.active_resource_id = active_resource_id;
            selection.automatic_switching = automatic_switching;
            selection.advance()?;
            Ok(selection.clone())
        })
        .await
    }

    /// Selects only usable resources. Discovery may use a cooling active resource,
    /// but neither discovery nor execution ever falls back to an invalid resource.
    pub async fn select_resource(
        &self,
        plugin_id: &str,
        resource_type: &str,
        require_ready: bool,
    ) -> Result<(ResourceRecord, ResourceSelection)> {
        self.modify_resources(plugin_id, resource_type, |records, selection| {
            let now = now_ms();
            let active = active_index(records, selection);
            if let Some(index) = active {
                let record = &records[index];
                if record.state.is_ready(now)
                    || (!require_ready && !matches!(record.state, ResourceState::Invalid { .. }))
                {
                    return Ok((record.clone(), selection.clone()));
                }
            }
            if selection.automatic_switching {
                let start = active.map_or(0, |index| index + 1);
                if let Some(index) = next_ready(records, start, now) {
                    selection.active_resource_id = Some(records[index].id.clone());
                    selection.advance()?;
                    return Ok((records[index].clone(), selection.clone()));
                }
            }
            let mut reason = match active.map(|index| &records[index].state) {
                Some(ResourceState::Cooling {
                    retry_at_ms,
                    message,
                }) => {
                    let mut reason = "selected plugin resource is cooling down".to_owned();
                    if let Some(message) = message
                        .as_deref()
                        .filter(|message| !message.trim().is_empty())
                    {
                        reason.push_str(&format!(": {message}"));
                    }
                    if let Some(at) = retry_at_ms {
                        let retry_at = chrono::DateTime::from_timestamp_millis(*at)
                            .map(|date| date.to_rfc3339_opts(chrono::SecondsFormat::Secs, true))
                            .unwrap_or_else(|| format!("{at} milliseconds since Unix epoch"));
                        reason.push_str(&format!("; retry at {retry_at}"));
                    }
                    reason
                }
                Some(ResourceState::Invalid { message }) => {
                    let mut reason = "selected plugin resource is invalid".to_owned();
                    if let Some(message) = message
                        .as_deref()
                        .filter(|message| !message.trim().is_empty())
                    {
                        reason.push_str(&format!(": {message}"));
                    }
                    reason.push_str("; refresh or reimport the resource");
                    reason
                }
                _ if records.is_empty() => {
                    "no plugin resources are configured; add a resource".into()
                }
                _ if selection.active_resource_id.is_none() => {
                    "no plugin resource is selected; select a resource".into()
                }
                _ => "selected plugin resource no longer exists; select a resource".into(),
            };
            if selection.automatic_switching {
                reason.push_str("; no ready alternative resource is available");
            }
            Err(Error::Config(format!(
                "{plugin_id}/{resource_type}: {reason}"
            )))
        })
        .await
    }

    pub async fn upsert_resources(
        &self,
        plugin_id: &str,
        resource_type: &str,
        drafts: Vec<ResourceDraft>,
    ) -> Result<UpsertOutcome> {
        self.modify_resources(plugin_id, resource_type, |records, selection| {
            let was_empty = records.is_empty();
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
                        advance_resource(existing)?;
                        if selection.active_resource_id.as_deref() == Some(existing.id.as_str()) {
                            selection.advance()?;
                        }
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
            // Import order is stable. Later additions/reimports never choose an account.
            if was_empty && !records.is_empty() {
                selection.active_resource_id = Some(records[0].id.clone());
                selection.advance()?;
            }
            Ok(outcome)
        })
        .await
    }

    /// Compare-and-swap on the resource snapshot, including imported credentials.
    pub async fn apply_patch_if_current(
        &self,
        plugin_id: &str,
        resource_type: &str,
        expected: &ResourceRecord,
        patch: ResourcePatch,
    ) -> Result<bool> {
        self.modify_resources(plugin_id, resource_type, |records, selection| {
            let Some(record) = records.iter_mut().find(|record| current(record, expected)) else {
                return Ok(false);
            };
            patch_record(record, patch)?;
            if selection.active_resource_id.as_deref() == Some(record.id.as_str()) {
                selection.advance()?;
            }
            Ok(true)
        })
        .await
    }

    /// Applies a failure only to its original resource snapshot. Selection may be
    /// advanced only while its snapshot is unchanged; later user edits take priority.
    /// Returns retry permission, not whether the patch was written.
    pub async fn fail_resource(
        &self,
        plugin_id: &str,
        resource_type: &str,
        expected_resource: &ResourceRecord,
        expected_selection: &ResourceSelection,
        patch: ResourcePatch,
    ) -> Result<bool> {
        self.modify_resources(plugin_id, resource_type, |records, selection| {
            let may_switch = &*selection == expected_selection;
            let matched = records
                .iter()
                .position(|record| current(record, expected_resource));
            if let Some(index) = matched {
                patch_record(&mut records[index], patch)?;
                if selection.active_resource_id.as_deref() == Some(expected_resource.id.as_str()) {
                    selection.advance()?;
                }
                if may_switch
                    && selection.automatic_switching
                    && selection.active_resource_id.as_deref()
                        == Some(expected_resource.id.as_str())
                {
                    if let Some(next) = next_ready(records, index + 1, now_ms()) {
                        if records[next].id != expected_resource.id {
                            selection.active_resource_id = Some(records[next].id.clone());
                            selection.advance()?;
                        }
                    }
                }
            }
            Ok(selection.automatic_switching
                && active_index(records, selection).is_some_and(|index| {
                    records[index].id != expected_resource.id
                        && records[index].state.is_ready(now_ms())
                }))
        })
        .await
    }

    pub async fn remove_resource(
        &self,
        plugin_id: &str,
        resource_type: &str,
        resource_id: &str,
    ) -> Result<ResourceRecord> {
        self.modify_resources(plugin_id, resource_type, |records, selection| {
            let index = records
                .iter()
                .position(|record| record.id == resource_id)
                .ok_or_else(|| Error::RunNotFound(format!("plugin resource {resource_id}")))?;
            let removed = records.remove(index);
            if selection.active_resource_id.as_deref() == Some(resource_id) {
                selection.active_resource_id = if selection.automatic_switching {
                    next_ready(records, index, now_ms()).map(|next| records[next].id.clone())
                } else {
                    None
                };
                selection.advance()?;
            }
            Ok(removed)
        })
        .await
    }

    pub async fn models(&self, plugin_id: &str, provider_id: &str) -> Result<Vec<StoredModel>> {
        decode(self.data.read(plugin_id, &model_key(provider_id)).await?)
    }

    pub async fn replace_models(
        &self,
        plugin_id: &str,
        provider_id: &str,
        models: &[StoredModel],
    ) -> Result<()> {
        self.data
            .modify(plugin_id, &[model_key(provider_id)], |values| {
                values[0] = merged_models(&values[0], models)?;
                Ok(())
            })
            .await
    }

    pub async fn replace_models_if_selected(
        &self,
        plugin_id: &str,
        resource_type: &str,
        expected_selection: &ResourceSelection,
        provider_id: &str,
        models: &[StoredModel],
    ) -> Result<bool> {
        self.data
            .modify(
                plugin_id,
                &[
                    resource_key(resource_type),
                    selection_key(resource_type),
                    model_key(provider_id),
                ],
                |values| {
                    let records: Vec<ResourceRecord> = decode(values[0].clone())?;
                    let selection: ResourceSelection = decode(values[1].clone())?;
                    if &selection != expected_selection
                        || active_index(&records, &selection).is_none_or(|index| {
                            matches!(records[index].state, ResourceState::Invalid { .. })
                        })
                    {
                        return Ok(false);
                    }
                    values[2] = merged_models(&values[2], models)?;
                    Ok(true)
                },
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
        self.data
            .modify(plugin_id, &[model_key(provider_id)], |values| {
                let mut models: Vec<StoredModel> = decode(values[0].clone())?;
                let model = models
                    .iter_mut()
                    .find(|model| model.id == model_id)
                    .ok_or_else(|| Error::RunNotFound(format!("plugin model {model_id}")))?;
                model.enabled = enabled;
                values[0] = serde_json::to_value(models)?;
                Ok(())
            })
            .await
    }

    pub async fn clear(&self, plugin_id: &str) -> Result<()> {
        self.data.clear(plugin_id).await
    }

    async fn modify_resources<T>(
        &self,
        plugin_id: &str,
        resource_type: &str,
        change: impl FnOnce(&mut Vec<ResourceRecord>, &mut ResourceSelection) -> Result<T>,
    ) -> Result<T> {
        self.data
            .modify(
                plugin_id,
                &[resource_key(resource_type), selection_key(resource_type)],
                |values| {
                    let mut records = decode(values[0].clone())?;
                    let mut selection = decode(values[1].clone())?;
                    let result = change(&mut records, &mut selection)?;
                    values[0] = serde_json::to_value(records)?;
                    values[1] = serde_json::to_value(selection)?;
                    Ok(result)
                },
            )
            .await
    }
}

fn decode<T: serde::de::DeserializeOwned + Default>(value: serde_json::Value) -> Result<T> {
    if value.is_null() {
        Ok(T::default())
    } else {
        Ok(serde_json::from_value(value)?)
    }
}

fn advance_resource(record: &mut ResourceRecord) -> Result<()> {
    record.updated_at_ms = now_ms().max(
        record
            .updated_at_ms
            .checked_add(1)
            .ok_or_else(|| Error::Config("plugin resource revision exhausted".into()))?,
    );
    Ok(())
}

fn patch_record(record: &mut ResourceRecord, patch: ResourcePatch) -> Result<()> {
    if let Some(private_data) = patch.private_data {
        record.private_data = private_data;
    }
    if let Some(state) = patch.state {
        record.state = state.into();
    }
    advance_resource(record)
}

fn current(record: &ResourceRecord, expected: &ResourceRecord) -> bool {
    record.id == expected.id && record.updated_at_ms == expected.updated_at_ms
}

fn active_index(records: &[ResourceRecord], selection: &ResourceSelection) -> Option<usize> {
    records
        .iter()
        .position(|record| selection.active_resource_id.as_deref() == Some(record.id.as_str()))
}

/// Insertion order, starting just after the previous active resource and wrapping.
fn next_ready(records: &[ResourceRecord], start: usize, now: i64) -> Option<usize> {
    (0..records.len())
        .map(|offset| (start + offset) % records.len())
        .find(|&index| records[index].state.is_ready(now))
}

fn merged_models(
    previous: &serde_json::Value,
    models: &[StoredModel],
) -> Result<serde_json::Value> {
    let previous: Vec<StoredModel> = decode(previous.clone())?;
    let models: Vec<_> = models
        .iter()
        .cloned()
        .map(|mut model| {
            if let Some(old) = previous.iter().find(|old| old.id == model.id) {
                model.enabled = old.enabled;
            }
            model
        })
        .collect();
    Ok(serde_json::to_value(models)?)
}

fn selection_key(resource_type: &str) -> String {
    format!("selection-{resource_type}")
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
#[path = "state_tests.rs"]
mod selection_tests;

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

        assert!(store
            .apply_patch_if_current(
                "dev.example",
                "account",
                &records[0],
                ResourcePatch {
                    private_data: None,
                    state: Some(ResourceStateInput::Cooling {
                        retry_at_ms: Some(200),
                        message: None,
                    }),
                },
            )
            .await
            .unwrap());
        let records = store.resources("dev.example", "account").await.unwrap();
        assert!(!records[0].state.is_ready(100));
        assert!(records[0].state.is_ready(300), "cooling expires over time");
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
