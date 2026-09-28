//! Defines serializable plugin capability definitions and desktop descriptors.
use serde::{Deserialize, Serialize};

use super::state::{ResourceRecord, ResourceState, StoredModel};

/// 由 collect.ts 输出的能力摘要;不含任何可执行内容。
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct PluginModuleDefinition {
    pub providers: Vec<ProviderDefinition>,
    #[serde(default)]
    pub resources: Vec<ResourceDefinition>,
}

/// 插件提供的显示文本:纯字符串或 locale → 文本映射;核心原样透传,由前端解析。
pub type LocalizedText = serde_json::Value;

#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ProviderDefinition {
    pub id: String,
    pub display_name: LocalizedText,
    #[serde(default)]
    pub description: LocalizedText,
    pub provider_type: String,
    #[serde(default)]
    pub resource_type: Option<String>,
    pub has_models: bool,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceDefinition {
    #[serde(rename = "type")]
    pub resource_type: String,
    pub display_name: LocalizedText,
    #[serde(default)]
    pub add: Vec<AddMethodDefinition>,
    #[serde(default)]
    pub import: Option<ImportDefinition>,
    #[serde(default)]
    pub actions: Vec<ResourceActionDefinition>,
    pub can_refresh: bool,
    pub can_remove: bool,
    #[serde(default)]
    pub export: ResourceExportDefinition,
}

impl Default for ResourceExportDefinition {
    fn default() -> Self {
        // A resource that never declared `export` keeps the pre-policy
        // behavior: the management export menu stays available.
        Self { enabled: true }
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceExportDefinition {
    /// Export stays available unless a plugin opts out, which keeps the
    /// behavior of plugins written before the policy existed.
    #[serde(default = "default_export_enabled")]
    pub enabled: bool,
}

fn default_export_enabled() -> bool {
    true
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceActionDefinition {
    pub id: String,
    pub display_name: LocalizedText,
    #[serde(default)]
    pub description: LocalizedText,
    #[serde(default = "default_action_target")]
    pub target: String,
    #[serde(default)]
    pub automation: Option<ResourceAutomationDefinition>,
    #[serde(default)]
    pub destructive: bool,
}

fn default_action_target() -> String {
    "resource".into()
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceAutomationDefinition {
    pub kind: String,
    #[serde(default)]
    pub default_enabled: bool,
    /// 后台行为：不投影到桌面端，用户无需配置。
    #[serde(default)]
    pub hidden: bool,
}

/// The persisted, user-visible state of one declared resource automation.
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PluginAutomationDescriptor {
    pub action_id: String,
    pub kind: String,
    pub enabled: bool,
    pub last_run_at_ms: Option<i64>,
    pub last_run_succeeded: Option<bool>,
    pub last_run_failed: Option<bool>,
}

impl PluginAutomationDescriptor {
    pub fn from_state(action_id: &str, kind: &str, state: &super::state::StoredAutomation) -> Self {
        Self {
            action_id: action_id.to_owned(),
            kind: kind.to_owned(),
            enabled: state.enabled,
            last_run_at_ms: state.last_run_at_ms,
            last_run_succeeded: state.last_run_succeeded,
            last_run_failed: state.last_run_failed,
        }
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct AddMethodDefinition {
    #[serde(rename = "type")]
    pub method_type: String,
    pub id: String,
    pub display_name: LocalizedText,
    #[serde(default)]
    pub description: LocalizedText,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub callback: Option<OAuthCallbackDefinition>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct OAuthCallbackDefinition {
    #[serde(default)]
    pub port: Option<u16>,
    #[serde(default)]
    pub path: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ImportDefinition {
    pub display_name: LocalizedText,
    #[serde(default)]
    pub description: LocalizedText,
    pub accept: Vec<String>,
    pub multiple: bool,
}

pub const OAUTH2_ADD_METHOD: &str = "oauth2.0";
pub const OAUTH2_AUTHORIZATION_CODE_ADD_METHOD: &str = "oauth2.authorization-code";

/// 桌面端看到的插件全貌。
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PluginDescriptor {
    pub id: String,
    pub name: String,
    pub version: String,
    pub author: Option<String>,
    pub icon: String,
    pub providers: Vec<PluginProviderDescriptor>,
    pub resources: Vec<PluginResourceDescriptor>,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PluginProviderDescriptor {
    pub id: String,
    pub plugin_id: String,
    pub display_name: LocalizedText,
    pub description: LocalizedText,
    pub provider_type: String,
    pub resource_type: Option<String>,
    pub has_models: bool,
    /// 已满足调用条件:模型目录非空,且需要资源时至少有一条资源。
    pub configured: bool,
    pub models: Vec<PluginModelDescriptor>,
}

/// 一个可直接被 Cursor 调用的插件模型。
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PluginModelDescriptor {
    /// 稳定模型 ID:`plugin:<plugin>/<provider>/<model>`。
    pub id: String,
    pub plugin_id: String,
    pub plugin_name: String,
    pub provider_id: String,
    pub model_id: String,
    pub display_name: String,
    pub description: Option<String>,
    pub icon: String,
    pub provider_type: String,
    pub max_output_tokens: Option<u64>,
    pub images: bool,
    pub enabled: bool,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PluginResourceDescriptor {
    #[serde(rename = "type")]
    pub resource_type: String,
    pub display_name: LocalizedText,
    pub add: Vec<AddMethodDefinition>,
    pub import: Option<ImportDefinition>,
    pub actions: Vec<ResourceActionDefinition>,
    pub automations: Vec<PluginAutomationDescriptor>,
    pub can_refresh: bool,
    pub can_remove: bool,
    pub export: ResourceExportDefinition,
    pub resources: Vec<PluginResourceView>,
}

/// 单条资源的对外投影;凭证保留在核心存储,不进入该结构。
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PluginResourceView {
    pub id: String,
    pub state: ResourceState,
    pub display_name: String,
    pub description: LocalizedText,
    pub metrics: Vec<ResourceMetric>,
    pub created_at_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceMetric {
    pub id: String,
    pub label: LocalizedText,
    pub unit: String,
    pub value: f64,
    #[serde(default)]
    pub reset_at_ms: Option<i64>,
}

/// 插件对一条资源的展示投影(resource.present 的返回值)。
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourcePresentation {
    pub display_name: String,
    #[serde(default)]
    pub description: LocalizedText,
    #[serde(default)]
    pub metrics: Vec<ResourceMetric>,
}

/// 插件资源操作返回的安全详情;patch 只在核心内部应用,不会回传给桌面端。
#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceActionResult {
    pub title: LocalizedText,
    #[serde(default)]
    pub description: Option<LocalizedText>,
    #[serde(default)]
    pub cards: Vec<ResourceActionCard>,
    #[serde(default = "default_action_succeeded")]
    pub succeeded: bool,
    #[serde(default, skip_serializing)]
    pub patch: Option<super::state::ResourcePatch>,
}

fn default_action_succeeded() -> bool {
    true
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceActionCard {
    pub id: String,
    pub title: LocalizedText,
    #[serde(default)]
    pub status: Option<LocalizedText>,
    #[serde(default)]
    pub granted_at_ms: Option<i64>,
    #[serde(default)]
    pub expires_at_ms: Option<i64>,
    #[serde(default)]
    pub fields: Vec<ResourceActionField>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ResourceActionField {
    pub id: String,
    pub label: LocalizedText,
    pub value: String,
}

/// 返回给桌面端的资源操作结果,明确排除插件私有 patch。
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ResourceActionResponse {
    pub title: LocalizedText,
    pub description: Option<LocalizedText>,
    pub cards: Vec<ResourceActionCard>,
    pub succeeded: bool,
}

impl From<ResourceActionResult> for ResourceActionResponse {
    fn from(result: ResourceActionResult) -> Self {
        Self {
            title: result.title,
            description: result.description,
            cards: result.cards,
            succeeded: result.succeeded,
        }
    }
}

impl PluginResourceView {
    pub fn from_record(record: &ResourceRecord, presentation: ResourcePresentation) -> Self {
        Self {
            id: record.id.clone(),
            state: record.state.clone(),
            display_name: presentation.display_name,
            description: presentation.description,
            metrics: presentation.metrics,
            created_at_ms: record.created_at_ms,
        }
    }
}

pub const ADAPTER_ID_PREFIX: &str = "plugin:";

pub fn model_id(plugin_id: &str, provider_id: &str, model_id: &str) -> String {
    format!("{ADAPTER_ID_PREFIX}{plugin_id}/{provider_id}/{model_id}")
}

/// 解析稳定模型 ID;上游模型段允许包含 `/`。
pub fn parse_model_id(value: &str) -> Option<(&str, &str, &str)> {
    let rest = value.strip_prefix(ADAPTER_ID_PREFIX)?;
    let (plugin_id, rest) = rest.split_once('/')?;
    let (provider_id, model_id) = rest.split_once('/')?;
    (!plugin_id.is_empty() && !provider_id.is_empty() && !model_id.is_empty()).then_some((
        plugin_id,
        provider_id,
        model_id,
    ))
}

impl PluginModelDescriptor {
    pub fn new(
        plugin_id: &str,
        plugin_name: &str,
        icon: &str,
        provider: &ProviderDefinition,
        model: &StoredModel,
    ) -> Self {
        Self {
            id: model_id(plugin_id, &provider.id, &model.id),
            plugin_id: plugin_id.to_owned(),
            plugin_name: plugin_name.to_owned(),
            provider_id: provider.id.clone(),
            model_id: model.id.clone(),
            display_name: model.display_name.clone(),
            description: model.description.clone(),
            icon: icon.to_owned(),
            provider_type: provider.provider_type.clone(),
            max_output_tokens: model.max_output_tokens,
            images: model.images,
            enabled: model.enabled,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_stable_model_ids_with_slashes() {
        let id = model_id("dev.example", "codex", "org/gpt-5");
        assert_eq!(
            parse_model_id(&id),
            Some(("dev.example", "codex", "org/gpt-5"))
        );
        assert_eq!(parse_model_id("plugin:only/one"), None);
        assert_eq!(parse_model_id("model-hash"), None);
    }

    #[test]
    fn projects_automation_state_without_mutating_action_metadata() {
        let action: ResourceActionDefinition = serde_json::from_value(serde_json::json!({
            "id": "check-in",
            "displayName": "Daily check-in",
            "automation": {"kind": "daily", "defaultEnabled": true}
        }))
        .unwrap();
        assert_eq!(action.target, "resource");
        let metadata = action.automation.unwrap();
        assert_eq!(metadata.kind, "daily");
        assert!(metadata.default_enabled);

        let state = super::super::state::StoredAutomation::with_default(metadata.default_enabled);
        let descriptor = PluginAutomationDescriptor::from_state(&action.id, &metadata.kind, &state);
        let value = serde_json::to_value(descriptor).unwrap();
        assert_eq!(value["actionId"], "check-in");
        assert_eq!(value["kind"], "daily");
        assert_eq!(value["enabled"], true);
        assert!(value["lastRunAtMs"].is_null());
        assert!(value["lastRunSucceeded"].is_null());
        assert!(value["lastRunFailed"].is_null());
    }

    #[test]
    fn hidden_automations_stay_off_the_desktop_projection() {
        // A background automation must still run on schedule while the host
        // hides it from the resource panel.
        let action: ResourceActionDefinition = serde_json::from_value(serde_json::json!({
            "id": "check-in",
            "displayName": "Daily check-in",
            "automation": {"kind": "daily", "defaultEnabled": true, "hidden": true}
        }))
        .unwrap();
        let metadata = action.automation.unwrap();
        assert_eq!(metadata.kind, "daily");
        assert!(metadata.default_enabled);
        assert!(metadata.hidden);

        let visible: ResourceActionDefinition = serde_json::from_value(serde_json::json!({
            "id": "check-in",
            "displayName": "Daily check-in",
            "automation": {"kind": "daily"}
        }))
        .unwrap();
        assert!(!visible.automation.unwrap().hidden);
    }

    #[test]
    fn parses_credential_export_policy() {
        let definition: PluginModuleDefinition = serde_json::from_value(serde_json::json!({
            "providers": [{
                "id": "codebuddy",
                "displayName": "CodeBuddy",
                "providerType": "tencent",
                "resourceType": "account",
                "hasModels": true
            }],
            "resources": [{
                "type": "account",
                "displayName": "Accounts",
                "canRefresh": true,
                "canRemove": false,
                "export": {"enabled": true}
            }]
        }))
        .unwrap();
        assert!(definition.resources[0].export.enabled);
    }

    #[test]
    fn credential_export_is_opt_out() {
        // A plugin that does not declare `export` must keep the pre-policy
        // behavior, so the Codex/Grok/Antigravity trees need no changes.
        let opted_out: PluginModuleDefinition = serde_json::from_value(serde_json::json!({
            "providers": [{
                "id": "codebuddy",
                "displayName": "CodeBuddy",
                "providerType": "tencent",
                "hasModels": true
            }],
            "resources": [{
                "type": "account",
                "displayName": "Accounts",
                "canRefresh": true,
                "canRemove": true,
                "export": {"enabled": false}
            }]
        }))
        .unwrap();
        assert!(!opted_out.resources[0].export.enabled);

        let undeclared: PluginModuleDefinition = serde_json::from_value(serde_json::json!({
            "providers": [{
                "id": "grok",
                "displayName": "Grok",
                "providerType": "xai",
                "hasModels": true
            }],
            "resources": [{
                "type": "account",
                "displayName": "Accounts",
                "canRefresh": true,
                "canRemove": true
            }]
        }))
        .unwrap();
        assert!(undeclared.resources[0].export.enabled);
    }

    #[test]
    fn action_failure_is_explicit_and_default_remains_success() {
        let failed: ResourceActionResult = serde_json::from_value(serde_json::json!({
            "title": "Failed",
            "succeeded": false
        }))
        .unwrap();
        assert!(!failed.succeeded);
        let legacy: ResourceActionResult = serde_json::from_value(serde_json::json!({
            "title": "Completed"
        }))
        .unwrap();
        assert!(legacy.succeeded);
    }
}
