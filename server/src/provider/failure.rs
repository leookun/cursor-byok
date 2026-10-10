//! Structured upstream failures used by alias routing; never classifies prompt text.
use serde::{Deserialize, Serialize};

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum FailureKind {
    RateLimit,
    Transient,
    Authorization,
    Request,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq, thiserror::Error)]
#[error("{message}")]
pub struct ProviderFailure {
    pub kind: FailureKind,
    pub status: Option<u16>,
    pub retry_after_ms: Option<u64>,
    pub message: String,
}

impl ProviderFailure {
    pub fn http(status: u16, headers: &reqwest::header::HeaderMap, message: String) -> Self {
        Self {
            kind: match status {
                429 => FailureKind::RateLimit,
                401 | 403 => FailureKind::Authorization,
                500..=599 => FailureKind::Transient,
                _ => FailureKind::Request,
            },
            status: Some(status),
            retry_after_ms: retry_after(headers, std::time::SystemTime::now()),
            message,
        }
    }

    pub fn transient(message: impl Into<String>) -> Self {
        Self {
            kind: FailureKind::Transient,
            status: None,
            retry_after_ms: None,
            message: message.into(),
        }
    }

    pub fn from_error(error: &crate::Error) -> Option<Self> {
        match error {
            crate::Error::Upstream(failure) => Some(failure.clone()),
            crate::Error::Http(error)
                if error.is_connect()
                    || error.is_timeout()
                    || error.is_body()
                    || error.is_request() =>
            {
                // reqwest errors can include URLs with credentials. Routing diagnostics do not.
                Some(Self::transient(if error.is_timeout() {
                    "upstream timed out"
                } else {
                    "upstream network failure"
                }))
            }
            _ => None,
        }
    }
}

fn retry_after(headers: &reqwest::header::HeaderMap, now: std::time::SystemTime) -> Option<u64> {
    if let Some(value) = headers
        .get(reqwest::header::RETRY_AFTER)
        .and_then(|value| value.to_str().ok())
    {
        if let Ok(seconds) = value.trim().parse::<u64>() {
            return Some(seconds.saturating_mul(1000));
        }
        if let Ok(date) = chrono::DateTime::parse_from_rfc2822(value) {
            let now_ms = now
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_millis();
            return Some(
                (i128::from(date.timestamp_millis()) - now_ms as i128)
                    .max(0)
                    .min(i128::from(u64::MAX)) as u64,
            );
        }
    }
    if let Some(seconds) = headers
        .get("ratelimit-reset")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse::<u64>().ok())
    {
        return Some(seconds.saturating_mul(1000));
    }
    // Absolute Unix reset times used by subscription and compatible API providers.
    let absolute = ["x-ratelimit-reset", "x-rate-limit-reset"]
        .into_iter()
        .filter_map(|name| headers.get(name)?.to_str().ok()?.parse::<u64>().ok())
        .map(|reset| {
            reset.saturating_mul(1000).saturating_sub(
                now.duration_since(std::time::UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_millis()
                    .min(u128::from(u64::MAX)) as u64,
            )
        })
        .max();
    absolute.or_else(|| {
        ["x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"]
            .into_iter()
            .filter_map(|name| duration_ms(headers.get(name)?.to_str().ok()?))
            .max()
    })
}

fn duration_ms(value: &str) -> Option<u64> {
    static PARTS: std::sync::OnceLock<regex::Regex> = std::sync::OnceLock::new();
    let parts = PARTS.get_or_init(|| {
        regex::Regex::new(r"(\d+(?:\.\d+)?)(ms|s|m|h)").expect("static duration regex")
    });
    let mut end = 0;
    let mut total = 0.0;
    for captures in parts.captures_iter(value) {
        let matched = captures.get(0)?;
        if matched.start() != end {
            return None;
        }
        end = matched.end();
        let unit = match &captures[2] {
            "ms" => 1.0,
            "s" => 1000.0,
            "m" => 60_000.0,
            "h" => 3_600_000.0,
            _ => return None,
        };
        total += captures[1].parse::<f64>().ok()? * unit;
    }
    (end == value.len() && end > 0 && total.is_finite() && total <= u64::MAX as f64)
        .then(|| total.ceil() as u64)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn retry_after_seconds_and_http_date() {
        let now = std::time::UNIX_EPOCH + std::time::Duration::from_secs(1_000_000_000);
        let mut headers = reqwest::header::HeaderMap::new();
        headers.insert("retry-after", "12".parse().unwrap());
        assert_eq!(retry_after(&headers, now), Some(12_000));
        headers.insert(
            "retry-after",
            "Sun, 09 Sep 2001 01:46:50 GMT".parse().unwrap(),
        );
        assert_eq!(retry_after(&headers, now), Some(10_000));
        headers.insert("retry-after", "invalid".parse().unwrap());
        assert_eq!(retry_after(&headers, now), None);
        headers.insert("x-ratelimit-reset", "1000000015".parse().unwrap());
        assert_eq!(retry_after(&headers, now), Some(15_000));
    }

    #[test]
    fn request_errors_are_not_transient() {
        let headers = reqwest::header::HeaderMap::new();
        assert_eq!(
            ProviderFailure::http(400, &headers, "bad request".into()).kind,
            FailureKind::Request
        );
        assert_eq!(
            ProviderFailure::http(401, &headers, "bad key".into()).kind,
            FailureKind::Authorization
        );
        assert_eq!(
            ProviderFailure::http(429, &headers, "limit".into()).kind,
            FailureKind::RateLimit
        );
        assert_eq!(
            ProviderFailure::http(503, &headers, "down".into()).kind,
            FailureKind::Transient
        );
    }
}
