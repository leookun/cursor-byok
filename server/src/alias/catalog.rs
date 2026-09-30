//! Resolves saved source references and intersects declared capabilities.
use serde::Serialize;

use super::{
    configuration::{Alias, AliasTarget, Capabilities, SourceType},
    state::TargetStatus,
    AliasResolver,
};
use crate::{model::ModelSpec, Result};

#[derive(Clone, Debug, Serialize)]
pub struct AliasSource {
    pub target: AliasTarget,
    pub label: String,
    pub source_name: String,
    pub request_model_id: String,
    pub model_id: String,
    pub available: bool,
    pub reason: Option<String>,
    pub parameters: Capabilities,
}

#[derive(Clone, Debug, Serialize)]
pub struct AliasView {
    #[serde(flatten)]
    pub alias: Alias,
    pub status: String,
    pub active_target: Option<String>,
    pub target_statuses: Vec<TargetStatus>,
    pub parameters: Capabilities,
    pub warnings: Vec<String>,
}

impl AliasResolver {
    pub async fn sources(&self) -> Result<Vec<AliasSource>> {
        let mut sources = Vec::new();
        for model in self.store.models().await? {
            sources.push(AliasSource {
                target: AliasTarget {
                    source_type: SourceType::Api,
                    source_id: model.source_id.clone(),
                    model_id: String::new(),
                    enabled: true,
                },
                label: model.display_name.clone(),
                source_name: model.group_name.clone().unwrap_or_else(|| {
                    url::Url::parse(&model.base_url)
                        .ok()
                        .and_then(|url| url.host_str().map(str::to_owned))
                        .unwrap_or_else(|| "API".into())
                }),
                request_model_id: model.model_hash.clone(),
                model_id: model.model_id.clone(),
                available: true,
                reason: None,
                parameters: Capabilities {
                    context_window_tokens: model.context_window_tokens,
                    max_output_tokens: model.max_output_tokens(),
                    images: model.supports_images,
                    tools: model.supports_tools,
                },
            });
        }
        for plugin in self.plugins.plugins().await {
            for provider in plugin.providers {
                for model in provider.models {
                    let available = provider.configured
                        && model.enabled
                        && self
                            .plugins
                            .model_available(&model.id)
                            .await
                            .unwrap_or(false);
                    sources.push(AliasSource {
                        target: AliasTarget {
                            source_type: SourceType::Plugin,
                            source_id: format!("{}/{}", model.plugin_id, model.provider_id),
                            model_id: model.model_id.clone(),
                            enabled: true,
                        },
                        label: model.display_name,
                        source_name: provider
                            .display_name
                            .as_str()
                            .or_else(|| {
                                provider
                                    .display_name
                                    .get("en-US")
                                    .and_then(serde_json::Value::as_str)
                            })
                            .unwrap_or(&plugin.name)
                            .to_owned(),
                        request_model_id: model.id,
                        model_id: model.model_id,
                        available,
                        reason: (!available).then(|| {
                            if !model.enabled {
                                "source_disabled"
                            } else {
                                "source_unavailable"
                            }
                            .into()
                        }),
                        parameters: Capabilities {
                            context_window_tokens: model.context_window_tokens,
                            max_output_tokens: model.max_output_tokens,
                            images: model.supports_images,
                            tools: model.supports_tools,
                        },
                    });
                }
            }
        }
        Ok(sources)
    }

    pub async fn views(&self) -> Result<Vec<AliasView>> {
        let sources = self.sources().await?;
        Ok(self
            .store
            .aliases()
            .await?
            .into_iter()
            .map(|alias| self.view(alias, &sources))
            .collect())
    }

    pub fn view(&self, alias: Alias, sources: &[AliasSource]) -> AliasView {
        let now = crate::store::now_ms();
        let last_active = self.state.active_target(&alias.id);
        let mut parameters = Vec::new();
        let mut target_statuses = Vec::new();
        for target in &alias.config.targets {
            let key = target.key();
            let source = sources.iter().find(|source| source.target.key() == key);
            if target.enabled {
                parameters.push(
                    source
                        .map(|source| source.parameters.clone())
                        .unwrap_or_default(),
                );
            }
            let (status, reason) = if !target.enabled {
                ("disabled", None)
            } else if source.is_none() {
                ("broken", Some("source_missing".into()))
            } else if !source.expect("checked").available {
                ("unavailable", source.expect("checked").reason.clone())
            } else if let Some(health) = self.state.health(&key, now) {
                target_statuses.push(health);
                continue;
            } else if last_active.as_deref() == Some(key.as_str()) {
                ("active", None)
            } else {
                ("available", None)
            };
            target_statuses.push(TargetStatus {
                key,
                status: status.into(),
                reason,
                retry_at_ms: None,
            });
        }
        let available = target_statuses
            .iter()
            .filter(|target| matches!(target.status.as_str(), "active" | "available"))
            .count();
        let enabled = alias
            .config
            .targets
            .iter()
            .filter(|target| target.enabled)
            .count();
        let parameters = intersect(&parameters);
        let mut warnings = Vec::new();
        if reserved_name(&alias.config.name) {
            warnings.push("reserved_name".into());
        }
        if alias.config.targets.is_empty() {
            warnings.push("no_targets".into());
        }
        if available == 0 {
            warnings.push("no_available_targets".into());
        }
        if parameters.context_window_tokens.is_none() {
            warnings.push("unknown_context".into());
        }
        if parameters.max_output_tokens.is_none() {
            warnings.push("unknown_output".into());
        }
        if parameters.images.is_none() {
            warnings.push("unknown_images".into());
        }
        match parameters.tools {
            None => warnings.push("unknown_tools".into()),
            Some(false) => warnings.push("tools_unsupported".into()),
            _ => {}
        }
        let status = if !alias.config.enabled {
            "disabled"
        } else if available == 0 {
            "unavailable"
        } else if available < enabled {
            "partial"
        } else {
            "working"
        }
        .into();
        let active_target = target_statuses
            .iter()
            .find(|target| target.status == "active")
            .or_else(|| {
                target_statuses
                    .iter()
                    .find(|target| target.status == "available")
            })
            .map(|target| target.key.clone());
        AliasView {
            alias,
            status,
            active_target,
            target_statuses,
            parameters,
            warnings,
        }
    }

    pub async fn configure(&self, model: &mut ModelSpec) -> Result<bool> {
        let Some(alias) = self.store.alias_by_name(&model.model_id).await? else {
            return Ok(false);
        };
        let sources = self.sources().await?;
        let view = self.view(alias, &sources);
        model.model_id = view.alias.config.name.clone();
        model.display_name = Some(view.alias.config.name);
        // Clamp before compaction. A Cursor variant must not enlarge the backup budget.
        model.context_window_tokens = clamp(
            model.context_window_tokens,
            view.parameters.context_window_tokens,
        );
        model.max_output_tokens = clamp(model.max_output_tokens, view.parameters.max_output_tokens);
        // Each target, rather than the alias or previous attempt, owns reasoning defaults.
        model.reasoning = Default::default();
        Ok(true)
    }

    pub async fn tested(&self, request_model_id: &str) -> Result<()> {
        for source in self.sources().await? {
            if source.request_model_id == request_model_id {
                self.state.tested(&source.target.key());
            }
        }
        Ok(())
    }
}

pub fn intersect(values: &[Capabilities]) -> Capabilities {
    if values.is_empty() {
        return Capabilities::default();
    }
    fn minimum(values: impl Iterator<Item = Option<u64>>) -> Option<u64> {
        values.collect::<Option<Vec<_>>>()?.into_iter().min()
    }
    fn all(values: impl Iterator<Item = Option<bool>>) -> Option<bool> {
        let values = values.collect::<Vec<_>>();
        if values.contains(&Some(false)) {
            Some(false)
        } else if values.contains(&None) {
            None
        } else {
            Some(true)
        }
    }
    Capabilities {
        context_window_tokens: minimum(values.iter().map(|value| value.context_window_tokens)),
        max_output_tokens: minimum(values.iter().map(|value| value.max_output_tokens)),
        images: all(values.iter().map(|value| value.images)),
        tools: all(values.iter().map(|value| value.tools)),
    }
}

pub fn clamp(requested: Option<u64>, limit: Option<u64>) -> Option<u64> {
    match (requested, limit) {
        (Some(requested), Some(limit)) => Some(requested.min(limit)),
        (_, Some(limit)) => Some(limit),
        (requested, None) => requested,
    }
}

fn reserved_name(name: &str) -> bool {
    super::configuration::name_warning(name).is_some()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn intersection_uses_every_enabled_target_and_keeps_unknowns_unknown() {
        let a = Capabilities {
            context_window_tokens: Some(200_000),
            max_output_tokens: Some(32_000),
            images: Some(true),
            tools: Some(true),
        };
        let b = Capabilities {
            context_window_tokens: Some(128_000),
            max_output_tokens: Some(8_000),
            images: Some(false),
            tools: Some(true),
        };
        let combined = intersect(&[a.clone(), b]);
        assert_eq!(combined.context_window_tokens, Some(128_000));
        assert_eq!(combined.max_output_tokens, Some(8_000));
        assert_eq!(combined.images, Some(false));
        assert_eq!(combined.tools, Some(true));
        assert_eq!(
            intersect(&[a, Capabilities::default()]).context_window_tokens,
            None
        );
        assert_eq!(clamp(Some(1_000_000), Some(128_000)), Some(128_000));
    }
}
