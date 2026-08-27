use axum::{extract::State, Json};
use serde::{Deserialize, Serialize};

use crate::{
    store::{Store, SubscriptionUsageUpdate},
    subscription::{account_exhausted, SubscriptionKind},
    Error, Result,
};
use super::ControlService;

const GROK_CREDITS_URL: &str = "https://cli-chat-proxy.grok.com/v1/billing?format=credits";
const CODEX_USAGE_URL: &str = "https://chatgpt.com/backend-api/wham/usage";

#[derive(Debug, Deserialize, Serialize)]
pub struct SubscriptionUsageInput {
    pub api_key: Option<String>,
}

#[derive(Debug, Deserialize, Serialize, PartialEq)]
pub struct SubscriptionUsageResponse {
    pub plan_label: Option<String>,
    pub remaining_percent: Option<f64>,
    pub used_percent: Option<f64>,
    pub reset_at_ms: Option<i64>,
    pub limit_reached: bool,
    pub session_remaining_percent: Option<f64>,
    pub session_reset_at_ms: Option<i64>,
}

pub async fn grok_usage(
    State(service): State<ControlService>,
    Json(input): Json<SubscriptionUsageInput>,
) -> Result<Json<SubscriptionUsageResponse>> {
    Ok(Json(
        refresh_usage(service.store(), SubscriptionKind::Grok, input.api_key.as_deref()).await?,
    ))
}

pub async fn codex_usage(
    State(service): State<ControlService>,
    Json(input): Json<SubscriptionUsageInput>,
) -> Result<Json<SubscriptionUsageResponse>> {
    Ok(Json(
        refresh_usage(service.store(), SubscriptionKind::Codex, input.api_key.as_deref()).await?,
    ))
}

pub(crate) async fn refresh_usage(
    store: &Store,
    kind: SubscriptionKind,
    api_key: Option<&str>,
) -> Result<SubscriptionUsageResponse> {
    let explicit = api_key.map(str::trim).filter(|value| !value.is_empty());
    let mut account = if explicit.is_none() {
        store.active_subscription_account(kind).await?
    } else {
        None
    };
    let token = explicit
        .map(ToString::to_string)
        .or_else(|| account.as_ref().map(|item| item.access_token.clone()))
        .ok_or_else(|| Error::Config(format!("missing {} access token", kind.label())))?;
    let client = crate::network::client(store).await?;
    let mut usage = fetch_usage(&client, kind, &token).await?;
    if let Some(current) = account.as_ref() {
        store
            .update_subscription_usage(&current.account_id, &usage_update(&usage))
            .await?;
        if account_exhausted(
            usage.remaining_percent,
            usage.reset_at_ms,
            usage.session_remaining_percent,
            usage.session_reset_at_ms,
            crate::store::now_ms(),
        ) {
            if let Some(next) = store
                .rotate_subscription_account(kind, Some(&current.account_id))
                .await?
            {
                usage = fetch_usage(&client, kind, &next.access_token).await?;
                store
                    .update_subscription_usage(&next.account_id, &usage_update(&usage))
                    .await?;
                account = Some(next);
            }
        }
    }
    let _ = account;
    Ok(usage)
}

async fn fetch_usage(
    client: &reqwest::Client,
    kind: SubscriptionKind,
    api_key: &str,
) -> Result<SubscriptionUsageResponse> {
    match kind {
        SubscriptionKind::Grok => fetch_grok_usage(client, api_key).await,
        SubscriptionKind::Codex => fetch_codex_usage(client, api_key).await,
    }
}

async fn fetch_grok_usage(client: &reqwest::Client, api_key: &str) -> Result<SubscriptionUsageResponse> {
    let response = client
        .get(GROK_CREDITS_URL)
        .bearer_auth(api_key)
        .header("accept", "application/json")
        .header("x-xai-token-auth", "xai-grok-cli")
        .send()
        .await?;
    let status = response.status();
    let body: serde_json::Value = response.json().await.unwrap_or_default();
    if !status.is_success() {
        return Err(Error::Provider(format!(
            "Grok usage lookup failed (HTTP {status}): {body}"
        )));
    }
    Ok(parse_grok_usage(&body))
}

async fn fetch_codex_usage(client: &reqwest::Client, api_key: &str) -> Result<SubscriptionUsageResponse> {
    let request = crate::provider::apply_chatgpt_codex_headers(
        client
            .get(CODEX_USAGE_URL)
            .bearer_auth(api_key)
            .header("accept", "application/json"),
        CODEX_USAGE_URL,
        api_key,
    );
    let response = request.send().await?;
    let status = response.status();
    let body: serde_json::Value = response.json().await.unwrap_or_default();
    if !status.is_success() {
        return Err(Error::Provider(format!(
            "Codex usage lookup failed (HTTP {status}): {body}"
        )));
    }
    Ok(parse_codex_usage(&body))
}

fn usage_update(usage: &SubscriptionUsageResponse) -> SubscriptionUsageUpdate {
    SubscriptionUsageUpdate {
        plan_label: usage.plan_label.clone(),
        remaining_percent: usage.remaining_percent,
        used_percent: usage.used_percent,
        reset_at_ms: usage.reset_at_ms,
        session_remaining_percent: usage.session_remaining_percent,
        session_reset_at_ms: usage.session_reset_at_ms,
        limit_reached: usage.limit_reached,
    }
}

fn parse_grok_usage(body: &serde_json::Value) -> SubscriptionUsageResponse {
    let config = body.get("config").unwrap_or(body);
    let used = json_f64(config.get("creditUsagePercent"))
        .or_else(|| json_f64(config.get("credit_usage_percent")))
        .or_else(|| {
            let used = json_amount(config.get("onDemandUsed").or_else(|| config.get("on_demand_used")));
            let cap = json_amount(config.get("onDemandCap").or_else(|| config.get("on_demand_cap")));
            match (used, cap) {
                (Some(used), Some(cap)) if cap > 0.0 => Some((used / cap) * 100.0),
                _ => None,
            }
        })
        .or_else(|| {
            config
                .get("currentPeriod")
                .or_else(|| config.get("current_period"))
                .map(|_| 0.0)
        });
    let remaining = used.map(|value| clamp_percent(100.0 - value));
    let reset_at_ms = parse_reset_ms(
        config
            .pointer("/currentPeriod/end")
            .or_else(|| config.pointer("/current_period/end"))
            .or_else(|| config.get("billingPeriodEnd"))
            .or_else(|| config.get("billing_period_end")),
    );
    let plan_label = json_string(config.get("subscriptionTierDisplay"))
        .or_else(|| json_string(config.get("subscription_tier_display")))
        .or_else(|| json_string(config.get("subscriptionTier")))
        .or_else(|| json_string(config.get("product")));
    SubscriptionUsageResponse {
        plan_label,
        remaining_percent: remaining,
        used_percent: used.map(clamp_percent),
        reset_at_ms,
        limit_reached: remaining.is_some_and(|value| value <= 0.0),
        session_remaining_percent: None,
        session_reset_at_ms: None,
    }
}

fn parse_codex_usage(body: &serde_json::Value) -> SubscriptionUsageResponse {
    let rate_limit = body.get("rate_limit").unwrap_or(body);
    let weekly = rate_limit
        .get("secondary_window")
        .or_else(|| rate_limit.get("primary_window"));
    let session = rate_limit
        .get("secondary_window")
        .and_then(|_| rate_limit.get("primary_window"));
    let used = window_used_percent(weekly);
    let remaining = used.map(|value| clamp_percent(100.0 - value));
    let plan_label = json_string(body.get("plan_type")).map(codex_plan_label);
    SubscriptionUsageResponse {
        plan_label,
        remaining_percent: remaining,
        used_percent: used.map(clamp_percent),
        reset_at_ms: window_reset_ms(weekly),
        limit_reached: rate_limit
            .get("limit_reached")
            .and_then(serde_json::Value::as_bool)
            .unwrap_or(remaining.is_some_and(|value| value <= 0.0)),
        session_remaining_percent: window_used_percent(session).map(|value| clamp_percent(100.0 - value)),
        session_reset_at_ms: window_reset_ms(session),
    }
}

fn window_used_percent(window: Option<&serde_json::Value>) -> Option<f64> {
    json_f64(window.and_then(|value| value.get("used_percent")))
}

fn window_reset_ms(window: Option<&serde_json::Value>) -> Option<i64> {
    let window = window?;
    parse_reset_ms(window.get("reset_at")).or_else(|| {
        json_f64(window.get("reset_after_seconds")).map(|seconds| {
            chrono::Utc::now().timestamp_millis() + (seconds * 1000.0) as i64
        })
    })
}

fn json_string(value: Option<&serde_json::Value>) -> Option<String> {
    value
        .and_then(serde_json::Value::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToString::to_string)
}

fn json_f64(value: Option<&serde_json::Value>) -> Option<f64> {
    let value = value?;
    value
        .as_f64()
        .or_else(|| value.as_i64().map(|number| number as f64))
        .or_else(|| value.as_u64().map(|number| number as f64))
        .or_else(|| value.as_str()?.trim().parse().ok())
}

fn json_amount(value: Option<&serde_json::Value>) -> Option<f64> {
    json_f64(value).or_else(|| json_f64(value.and_then(|item| item.get("val"))))
}

fn parse_reset_ms(value: Option<&serde_json::Value>) -> Option<i64> {
    let value = value?;
    if let Some(number) = value.as_i64().or_else(|| value.as_u64().map(|number| number as i64)) {
        return Some(if number > 10_000_000_000 {
            number
        } else {
            number.saturating_mul(1000)
        });
    }
    value.as_str().and_then(|text| {
        chrono::DateTime::parse_from_rfc3339(text)
            .ok()
            .map(|time| time.timestamp_millis())
    })
}

fn clamp_percent(value: f64) -> f64 {
    value.clamp(0.0, 100.0)
}

fn codex_plan_label(plan: String) -> String {
    match plan.to_ascii_lowercase().as_str() {
        "plus" => "ChatGPT Plus".into(),
        "pro" => "ChatGPT Pro".into(),
        "team" => "ChatGPT Team".into(),
        "business" => "ChatGPT Business".into(),
        "enterprise" => "ChatGPT Enterprise".into(),
        "free" => "ChatGPT Free".into(),
        "go" => "ChatGPT Go".into(),
        _ => plan,
    }
}

#[cfg(test)]
mod tests {
    use super::{parse_codex_usage, parse_grok_usage};
    use serde_json::json;

    #[test]
    fn grok_credits_percent_is_inverted_to_remaining() {
        let usage = parse_grok_usage(&json!({
            "config": {
                "creditUsagePercent": 34.0,
                "subscriptionTierDisplay": "SuperGrok",
                "currentPeriod": { "end": "2026-06-08T00:00:00Z" }
            }
        }));
        assert_eq!(usage.remaining_percent, Some(66.0));
        assert_eq!(usage.used_percent, Some(34.0));
        assert_eq!(usage.plan_label.as_deref(), Some("SuperGrok"));
        assert_eq!(usage.reset_at_ms, Some(1_780_876_800_000));
        assert!(!usage.limit_reached);
    }

    #[test]
    fn grok_missing_percent_with_period_is_unused() {
        let usage = parse_grok_usage(&json!({
            "config": { "currentPeriod": { "type": "USAGE_PERIOD_TYPE_WEEKLY" } }
        }));
        assert_eq!(usage.used_percent, Some(0.0));
        assert_eq!(usage.remaining_percent, Some(100.0));
    }

    #[test]
    fn codex_weekly_window_is_the_displayed_quota() {
        let usage = parse_codex_usage(&json!({
            "plan_type": "plus",
            "rate_limit": {
                "limit_reached": false,
                "primary_window": { "used_percent": 80.0, "reset_at": 1780000000 },
                "secondary_window": { "used_percent": 25.0, "reset_at": 1780500000 }
            }
        }));
        assert_eq!(usage.plan_label.as_deref(), Some("ChatGPT Plus"));
        assert_eq!(usage.remaining_percent, Some(75.0));
        assert_eq!(usage.session_remaining_percent, Some(20.0));
        assert_eq!(usage.reset_at_ms, Some(1_780_500_000_000));
        assert!(!usage.limit_reached);
    }
}
