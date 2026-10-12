//! Public alias configuration and validation, independent of runtime routing.
use std::collections::HashSet;

use serde::{Deserialize, Serialize};

use crate::{Error, Result};

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq, Hash)]
#[serde(rename_all = "snake_case")]
pub enum SourceType {
    Api,
    Plugin,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq, Hash)]
pub struct AliasTarget {
    pub source_type: SourceType,
    pub source_id: String,
    pub model_id: String,
    pub enabled: bool,
}

impl AliasTarget {
    /// JSON tuple encoding is unambiguous even when identifiers contain delimiters.
    pub fn key(&self) -> String {
        serde_json::to_string(&(self.source_type, &self.source_id, &self.model_id))
            .expect("strings and source types are JSON serializable")
    }
}

#[derive(Clone, Copy, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum ReturnMode {
    #[default]
    NewSessions,
    Immediate,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct AliasInput {
    pub name: String,
    #[serde(default)]
    pub description: String,
    pub enabled: bool,
    pub targets: Vec<AliasTarget>,
    #[serde(default = "default_true")]
    pub sticky: bool,
    #[serde(default)]
    pub return_mode: ReturnMode,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct Alias {
    pub id: String,
    #[serde(flatten)]
    pub config: AliasInput,
    pub created_at_ms: i64,
    pub updated_at_ms: i64,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
pub struct Capabilities {
    pub context_window_tokens: Option<u64>,
    pub max_output_tokens: Option<u64>,
    pub images: Option<bool>,
    pub tools: Option<bool>,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(default)]
pub struct AliasSettings {
    pub rate_limit_seconds: u64,
    pub transient_seconds: u64,
    pub authorization_seconds: u64,
    pub connect_timeout_seconds: u64,
    pub first_token_timeout_seconds: u64,
}

impl Default for AliasSettings {
    fn default() -> Self {
        Self {
            rate_limit_seconds: 60,
            transient_seconds: 30,
            authorization_seconds: 600,
            connect_timeout_seconds: 10,
            first_token_timeout_seconds: 60,
        }
    }
}

impl AliasSettings {
    pub fn validate(&self) -> Result<()> {
        const MAX_COOLDOWN_SECONDS: u64 = 30 * 24 * 60 * 60;
        const MAX_TIMEOUT_SECONDS: u64 = 24 * 60 * 60;
        if !(600..=MAX_COOLDOWN_SECONDS).contains(&self.authorization_seconds) {
            return Err(Error::Config(
                "alias authorization cooldown must be between 600 seconds and 30 days".into(),
            ));
        }
        if !(1..=MAX_COOLDOWN_SECONDS).contains(&self.rate_limit_seconds)
            || !(1..=MAX_COOLDOWN_SECONDS).contains(&self.transient_seconds)
        {
            return Err(Error::Config(
                "alias rate-limit and transient cooldowns must be between 1 second and 30 days"
                    .into(),
            ));
        }
        if !(1..=MAX_TIMEOUT_SECONDS).contains(&self.connect_timeout_seconds)
            || !(1..=MAX_TIMEOUT_SECONDS).contains(&self.first_token_timeout_seconds)
        {
            return Err(Error::Config(
                "alias timeouts must be between 1 and 86400 seconds".into(),
            ));
        }
        Ok(())
    }
}

pub fn normalize_alias_input(input: &AliasInput) -> Result<AliasInput> {
    let mut input = input.clone();
    input.name = input.name.to_ascii_lowercase();
    if input.name.is_empty()
        || input.name.len() > 64
        || !input.name.bytes().all(|byte| {
            byte.is_ascii_lowercase() || byte.is_ascii_digit() || matches!(byte, b'.' | b'_' | b'-')
        })
    {
        return Err(Error::Config(
            "alias name must contain 1–64 ASCII letters, digits, dots, underscores or hyphens"
                .into(),
        ));
    }
    let mut identities = HashSet::new();
    for target in &mut input.targets {
        match target.source_type {
            SourceType::Api => {
                if !target.model_id.is_empty() {
                    return Err(Error::Config(
                        "API alias targets must have an empty model_id".into(),
                    ));
                }
                target.source_id = uuid::Uuid::parse_str(&target.source_id)
                    .map_err(|_| Error::Config("API alias target source_id must be a UUID".into()))?
                    .to_string();
            }
            SourceType::Plugin => {
                let parts = target.source_id.split('/').collect::<Vec<_>>();
                if parts.len() != 2
                    || parts
                        .iter()
                        .any(|part| part.is_empty() || part.trim() != *part)
                {
                    return Err(Error::Config(
                        "plugin alias target source_id must be <plugin_id>/<provider_id>".into(),
                    ));
                }
                if target.model_id.trim().is_empty() {
                    return Err(Error::Config(
                        "plugin alias target model_id cannot be empty".into(),
                    ));
                }
            }
        }
        if !identities.insert(target.key()) {
            return Err(Error::Config(
                "alias targets must be unique, including disabled targets".into(),
            ));
        }
    }
    if input.targets.is_empty() {
        input.enabled = false;
    }
    Ok(input)
}

/// Reserved provider prefixes remain usable, but can affect Cursor's model heuristics.
pub fn name_warning(name: &str) -> Option<String> {
    let name = name.to_ascii_lowercase();
    let reserved = [
        "gpt-",
        "claude-",
        "gemini-",
        "glm-",
        "kimi-",
        "grok-",
        "deepseek-",
    ]
    .into_iter()
    .any(|prefix| name.starts_with(prefix))
        || (name.starts_with('o') && name.as_bytes().get(1).is_some_and(u8::is_ascii_digit))
        || matches!(name.as_str(), "auto" | "composer" | "default");
    reserved.then(|| "reserved_name".into())
}

fn default_true() -> bool {
    true
}

#[cfg(test)]
mod tests {
    use super::*;

    fn input() -> AliasInput {
        serde_json::from_value(serde_json::json!({
            "name": "MY-alias.1", "enabled": true, "targets": []
        }))
        .unwrap()
    }

    #[test]
    fn defaults_normalization_and_name_validation() {
        let normalized = normalize_alias_input(&input()).unwrap();
        assert_eq!(normalized.name, "my-alias.1");
        assert!(normalized.sticky);
        assert_eq!(normalized.return_mode, ReturnMode::NewSessions);
        assert!(!normalized.enabled);
        for name in ["", "a b", "a/b", "é", " trailing ", &"a".repeat(65)] {
            assert!(normalize_alias_input(&AliasInput {
                name: name.into(),
                ..input()
            })
            .is_err());
        }
        assert!(name_warning("GPT-personal").is_some());
        assert!(name_warning("work").is_none());
        assert_eq!(
            serde_json::from_str::<AliasSettings>("{}").unwrap(),
            AliasSettings::default()
        );
        assert_eq!(Capabilities::default().images, None);
    }

    #[test]
    fn reserved_names_match_only_the_specified_prefixes_pattern_and_exact_names() {
        for prefix in [
            "gpt-",
            "claude-",
            "gemini-",
            "glm-",
            "kimi-",
            "grok-",
            "deepseek-",
        ] {
            for name in [
                prefix.to_owned(),
                format!("{prefix}personal"),
                prefix.to_uppercase(),
            ] {
                assert_eq!(
                    name_warning(&name).as_deref(),
                    Some("reserved_name"),
                    "{name}"
                );
            }
            assert_eq!(name_warning(prefix.trim_end_matches('-')), None);
        }
        for name in [
            "auto", "composer", "default", "AUTO", "Composer", "DEFAULT", "o0", "o2-test", "o9foo",
            "o123", "O7",
        ] {
            assert_eq!(
                name_warning(name).as_deref(),
                Some("reserved_name"),
                "{name}"
            );
        }
        for name in [
            "",
            "gptpersonal",
            "claudette",
            "auto-route",
            "automatic",
            "composer-route",
            "default-route",
            "cursor",
            "cursor-test",
            "plugin",
            "plugin-test",
            "o",
            "openai",
            "o-test",
            "xo3",
            "oé",
            "o１",
            "work",
        ] {
            assert_eq!(name_warning(name), None, "{name}");
        }
    }

    #[test]
    fn settings_enforce_finite_nonzero_cooldown_and_timeout_ranges() {
        let defaults = AliasSettings::default();
        assert!(defaults.validate().is_ok());
        assert!(AliasSettings {
            rate_limit_seconds: 1,
            transient_seconds: 1,
            authorization_seconds: 600,
            connect_timeout_seconds: 1,
            first_token_timeout_seconds: 1,
        }
        .validate()
        .is_ok());
        assert!(AliasSettings {
            rate_limit_seconds: 2_592_000,
            transient_seconds: 2_592_000,
            authorization_seconds: 2_592_000,
            connect_timeout_seconds: 86_400,
            first_token_timeout_seconds: 86_400,
        }
        .validate()
        .is_ok());
        for value in [0, 2_592_001, u64::MAX] {
            assert!(AliasSettings {
                rate_limit_seconds: value,
                ..defaults.clone()
            }
            .validate()
            .is_err());
            assert!(AliasSettings {
                transient_seconds: value,
                ..defaults.clone()
            }
            .validate()
            .is_err());
        }
        for value in [0, 599, 2_592_001, u64::MAX] {
            assert!(AliasSettings {
                authorization_seconds: value,
                ..defaults.clone()
            }
            .validate()
            .is_err());
        }
        for value in [0, 86_401, u64::MAX] {
            assert!(AliasSettings {
                connect_timeout_seconds: value,
                ..defaults.clone()
            }
            .validate()
            .is_err());
            assert!(AliasSettings {
                first_token_timeout_seconds: value,
                ..defaults.clone()
            }
            .validate()
            .is_err());
        }
    }

    #[test]
    fn validates_api_ids_and_plugin_target_shape() {
        let id = uuid::Uuid::new_v4().to_string();
        let mut value = input();
        value.targets = vec![AliasTarget {
            source_type: SourceType::Api,
            source_id: id.to_uppercase(),
            model_id: String::new(),
            enabled: false,
        }];
        assert_eq!(
            normalize_alias_input(&value).unwrap().targets[0].source_id,
            id
        );
        value.targets[0].model_id = "not-empty".into();
        assert!(normalize_alias_input(&value).is_err());
        value.targets[0].model_id.clear();
        value.targets[0].source_id = "mutable-model-hash".into();
        assert!(normalize_alias_input(&value).is_err());
        value.targets[0].source_type = SourceType::Plugin;
        value.targets[0].model_id = "model".into();
        for source_id in ["plugin", "/provider", "plugin/", "plugin/provider/extra"] {
            value.targets[0].source_id = source_id.into();
            assert!(normalize_alias_input(&value).is_err());
        }
        value.targets[0].source_id = "plugin/provider".into();
        value.targets[0].model_id.clear();
        assert!(normalize_alias_input(&value).is_err());
    }

    #[test]
    fn identities_ignore_enabled_and_reject_aliases() {
        let target = AliasTarget {
            source_type: SourceType::Plugin,
            source_id: "plugin/provider".into(),
            model_id: "upstream/model".into(),
            enabled: true,
        };
        let disabled = AliasTarget {
            enabled: false,
            ..target.clone()
        };
        assert_eq!(target.key(), disabled.key());
        let mut value = input();
        value.targets = vec![target, disabled];
        assert!(normalize_alias_input(&value).is_err());
        assert!(serde_json::from_str::<SourceType>("\"alias\"").is_err());
        value.targets.truncate(1);
        value.targets[0].source_id = "plugin/provider/extra".into();
        assert!(normalize_alias_input(&value).is_err());
    }
}
