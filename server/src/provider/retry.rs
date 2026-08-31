//! Applies provider retry and backoff behavior.
use std::time::Duration;

use tokio_util::sync::CancellationToken;

use crate::{Error, Result};

use super::CallRecorder;

#[derive(Clone, Copy, Debug)]
pub(crate) struct RetryPolicy {
    pub retries: u32,
    pub delay: Duration,
}

impl Default for RetryPolicy {
    fn default() -> Self {
        Self {
            retries: 5,
            delay: Duration::from_secs(5),
        }
    }
}

#[derive(Debug)]
pub(crate) enum Attempt {
    Response(reqwest::Response),
    Cancelled,
}

pub(crate) async fn send_with_retry<F>(
    label: &str,
    build: F,
    policy: RetryPolicy,
    cancellation: &CancellationToken,
    recorder: Option<&CallRecorder>,
    request_headers: serde_json::Value,
    request_body: &serde_json::Value,
) -> Result<Attempt>
where
    F: Fn() -> reqwest::RequestBuilder,
{
    for attempt in 0..=policy.retries {
        let response = tokio::select! {
            _ = cancellation.cancelled() => return Ok(Attempt::Cancelled),
            response = build().send() => response,
        }?;
        if let Some(recorder) = recorder {
            recorder
                .response_headers(response.status().as_u16())
                .await?;
        }
        if response.status().is_success() {
            return Ok(Attempt::Response(response));
        }
        let status = response.status();
        let bytes = response.bytes().await?;
        let error = Error::Provider(format!(
            "{label} {status}: {}",
            String::from_utf8_lossy(&bytes)
        ));
        if attempt == policy.retries || !is_retryable(status) {
            return Err(error);
        }
        tracing::warn!(
            provider = label,
            status = status.as_u16(),
            attempt = attempt + 1,
            retries = policy.retries,
            delay_ms = policy.delay.as_millis(),
            "provider returned a non-success status, retrying"
        );
        if let Some(recorder) = recorder {
            recorder
                .retry(&error, request_headers.clone(), request_body)
                .await?;
        }
        tokio::select! {
            _ = cancellation.cancelled() => return Ok(Attempt::Cancelled),
            _ = tokio::time::sleep(policy.delay) => {}
        }
    }
    unreachable!("the retry loop returns on the final attempt")
}

/// Reports whether repeating the request could plausibly change the answer.
/// A rejected request - wrong key, unknown model, malformed or oversized body -
/// fails identically every time, so retrying it only delays the error the user
/// needs to see.
fn is_retryable(status: reqwest::StatusCode) -> bool {
    use reqwest::StatusCode;
    status.is_server_error()
        || matches!(
            status,
            StatusCode::REQUEST_TIMEOUT | StatusCode::TOO_EARLY | StatusCode::TOO_MANY_REQUESTS
        )
}

#[cfg(test)]
mod tests {
    use std::sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    };

    use super::*;

    /// Serves one fixed status line and counts the requests it received.
    async fn stub(status_line: &'static str) -> (String, Arc<AtomicUsize>) {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let hits = Arc::new(AtomicUsize::new(0));
        let counter = hits.clone();
        tokio::spawn(async move {
            while let Ok((mut socket, _)) = listener.accept().await {
                counter.fetch_add(1, Ordering::SeqCst);
                tokio::spawn(async move {
                    use tokio::io::{AsyncReadExt, AsyncWriteExt};
                    let mut buffer = [0_u8; 2048];
                    let _ = socket.read(&mut buffer).await;
                    let response = format!(
                        "HTTP/1.1 {status_line}\r\ncontent-length: 0\r\nconnection: close\r\n\r\n"
                    );
                    let _ = socket.write_all(response.as_bytes()).await;
                    let _ = socket.shutdown().await;
                });
            }
        });
        (format!("http://{address}/"), hits)
    }

    async fn attempts(status_line: &'static str, retries: u32) -> usize {
        let (url, hits) = stub(status_line).await;
        let client = reqwest::Client::builder().no_proxy().build().unwrap();
        let body = serde_json::json!({"model": "test"});
        let result = send_with_retry(
            "test",
            || client.post(&url).json(&body),
            RetryPolicy {
                retries,
                delay: Duration::from_millis(10),
            },
            &CancellationToken::new(),
            None,
            serde_json::Value::Null,
            &body,
        )
        .await;
        assert!(result.is_err(), "{status_line} must surface as an error");
        hits.load(Ordering::SeqCst)
    }

    #[tokio::test]
    async fn a_rejected_request_is_not_retried() {
        for status_line in [
            "400 Bad Request",
            "401 Unauthorized",
            "403 Forbidden",
            "404 Not Found",
            "413 Payload Too Large",
            "422 Unprocessable Entity",
        ] {
            assert_eq!(
                attempts(status_line, 5).await,
                1,
                "{status_line} must be reported immediately"
            );
        }
    }

    #[tokio::test]
    async fn a_transient_failure_still_uses_the_whole_policy() {
        for status_line in [
            "408 Request Timeout",
            "429 Too Many Requests",
            "500 Internal Server Error",
            "502 Bad Gateway",
            "503 Service Unavailable",
        ] {
            assert_eq!(
                attempts(status_line, 2).await,
                3,
                "{status_line} must be retried"
            );
        }
    }
}
