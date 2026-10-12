//! Process-local routing health and conversation affinity. No credentials or prompts.
use std::collections::HashMap;

use parking_lot::Mutex;
use serde::Serialize;

use super::configuration::AliasSettings;
use crate::provider::failure::{FailureKind, ProviderFailure};

#[derive(Clone, Debug, Serialize)]
pub struct TargetStatus {
    pub key: String,
    pub status: String,
    pub reason: Option<String>,
    pub retry_at_ms: Option<i64>,
}

#[derive(Clone)]
struct Health {
    until_ms: i64,
    authorization: bool,
    tested: bool,
    reason: String,
}

#[derive(Default)]
struct Conversation {
    run_id: String,
    target: Option<String>,
    committed: bool,
    sticky: Option<String>,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct AttemptReport {
    pub target_id: String,
    pub error: Option<String>,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct RouteReport {
    pub target_id: Option<String>,
    pub switches: usize,
    pub attempts: Vec<AttemptReport>,
}

#[derive(Default)]
struct Inner {
    health: HashMap<String, Health>,
    conversations: HashMap<(String, String), Conversation>,
    active_targets: HashMap<String, String>,
    tests: HashMap<String, RouteReport>,
}

#[derive(Default)]
pub struct RoutingState(Mutex<Inner>);

impl RoutingState {
    pub fn health(&self, key: &str, now_ms: i64) -> Option<TargetStatus> {
        let mut inner = self.0.lock();
        let health = inner.health.get(key)?;
        if now_ms >= health.until_ms && (!health.authorization || health.tested) {
            inner.health.remove(key);
            return None;
        }
        Some(TargetStatus {
            key: key.into(),
            status: if health.authorization {
                "authorization"
            } else {
                "cooldown"
            }
            .into(),
            reason: Some(health.reason.clone()),
            retry_at_ms: Some(health.until_ms),
        })
    }

    pub fn failed(
        &self,
        key: &str,
        failure: &ProviderFailure,
        settings: &AliasSettings,
        now_ms: i64,
    ) {
        let seconds = match failure.kind {
            FailureKind::RateLimit => settings.rate_limit_seconds,
            FailureKind::Transient => settings.transient_seconds,
            FailureKind::Authorization => settings.authorization_seconds.max(600),
            FailureKind::Request => return,
        };
        let duration_ms = if failure.kind == FailureKind::RateLimit {
            failure
                .retry_after_ms
                .unwrap_or(seconds.saturating_mul(1000))
        } else {
            seconds.saturating_mul(1000)
        };
        let reason = match failure.kind {
            FailureKind::RateLimit => "rate_limit",
            FailureKind::Transient => "upstream_failure",
            FailureKind::Authorization => "authorization",
            FailureKind::Request => unreachable!(),
        };
        self.0.lock().health.insert(
            key.into(),
            Health {
                until_ms: now_ms.saturating_add(duration_ms.min(i64::MAX as u64) as i64),
                authorization: failure.kind == FailureKind::Authorization,
                tested: false,
                reason: reason.into(),
            },
        );
    }

    pub fn tested(&self, key: &str) {
        let mut inner = self.0.lock();
        if let Some(health) = inner.health.get_mut(key) {
            if health.authorization {
                health.tested = true;
            } else {
                inner.health.remove(key);
            }
        }
    }

    /// One request owns the output boundary, including all its tool rounds.
    pub fn begin(&self, conversation: &str, alias: &str, run: &str) -> (Option<String>, bool) {
        let mut inner = self.0.lock();
        let entry = inner
            .conversations
            .entry((conversation.into(), alias.into()))
            .or_default();
        if entry.run_id != run {
            entry.run_id = run.into();
            entry.committed = false;
            entry.target = None;
        }
        (
            if entry.committed {
                entry.target.clone()
            } else {
                entry.sticky.clone()
            },
            entry.committed,
        )
    }

    pub fn commit(&self, conversation: &str, alias: &str, run: &str, target: &str, sticky: bool) {
        let mut inner = self.0.lock();
        let Some(entry) = inner
            .conversations
            .get_mut(&(conversation.into(), alias.into()))
        else {
            return;
        };
        if entry.run_id != run {
            return;
        }
        entry.committed = true;
        entry.target = Some(target.into());
        entry.sticky = sticky.then(|| target.into());
        inner.active_targets.insert(alias.into(), target.into());
    }

    pub fn active_target(&self, alias: &str) -> Option<String> {
        self.0.lock().active_targets.get(alias).cloned()
    }

    pub fn watch_test(&self, run: &str) {
        self.0
            .lock()
            .tests
            .insert(run.into(), RouteReport::default());
    }
    pub fn take_test(&self, run: &str) -> RouteReport {
        let mut inner = self.0.lock();
        inner
            .conversations
            .retain(|(conversation, _), _| conversation != run);
        inner.tests.remove(run).unwrap_or_default()
    }
    pub fn attempt(&self, run: &str, target: &str, error: Option<String>) {
        if let Some(report) = self.0.lock().tests.get_mut(run) {
            report.attempts.push(AttemptReport {
                target_id: target.into(),
                error,
            });
            report.switches = report.attempts.len().saturating_sub(1);
        }
    }
    pub fn answered(&self, run: &str, target: &str) {
        if let Some(report) = self.0.lock().tests.get_mut(run) {
            report.target_id = Some(target.into());
        }
    }

    pub fn remove_alias(&self, alias: &str) {
        let mut inner = self.0.lock();
        inner.conversations.retain(|(_, id), _| id != alias);
        inner.active_targets.remove(alias);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn rate_limit_expires_but_auth_requires_a_successful_test() {
        let state = RoutingState::default();
        let settings = AliasSettings::default();
        let mut failure = ProviderFailure {
            kind: FailureKind::RateLimit,
            status: Some(429),
            retry_after_ms: Some(5000),
            message: "limited".into(),
        };
        state.failed("target", &failure, &settings, 1000);
        assert!(state.health("target", 5999).is_some());
        assert!(state.health("target", 6000).is_none());
        failure.kind = FailureKind::Authorization;
        state.failed("target", &failure, &settings, 1000);
        assert!(state.health("target", 601_000).is_some());
        state.tested("target");
        assert!(state.health("target", 601_000).is_none());
        state.failed("target", &failure, &settings, 1000);
        state.tested("target");
        assert!(state.health("target", 600_999).is_some());
    }
    #[test]
    fn affinity_outlives_a_run_but_output_commit_does_not() {
        let state = RoutingState::default();
        assert_eq!(state.begin("c", "a", "run1"), (None, false));
        state.commit("c", "a", "run1", "backup", true);
        assert_eq!(state.begin("c", "a", "run1"), (Some("backup".into()), true));
        assert_eq!(
            state.begin("c", "a", "run2"),
            (Some("backup".into()), false)
        );
        state.commit("c", "a", "run1", "obsolete", true);
        assert_eq!(
            state.begin("c", "a", "run2"),
            (Some("backup".into()), false)
        );
        assert_eq!(state.begin("new-conversation", "a", "run3"), (None, false));
    }
}
