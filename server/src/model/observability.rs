//! Defines provider call and usage observability records.
use super::ProviderType;

mod usage {
    use std::ops::AddAssign;

    use serde::{Deserialize, Serialize};

    #[derive(Clone, Copy, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
    pub struct Usage {
        pub input_tokens: Option<u64>,
        pub context_input_tokens: Option<u64>,
        pub output_tokens: Option<u64>,
        pub total_tokens: Option<u64>,
        pub cache_read_tokens: Option<u64>,
        pub cache_write_tokens: Option<u64>,
        pub reasoning_tokens: Option<u64>,
    }

    impl AddAssign for Usage {
        fn add_assign(&mut self, rhs: Self) {
            self.input_tokens = sum(self.input_tokens, rhs.input_tokens);
            self.context_input_tokens = sum(self.context_input_tokens, rhs.context_input_tokens);
            self.output_tokens = sum(self.output_tokens, rhs.output_tokens);
            self.total_tokens = sum(self.total_tokens, rhs.total_tokens);
            self.cache_read_tokens = sum(self.cache_read_tokens, rhs.cache_read_tokens);
            self.cache_write_tokens = sum(self.cache_write_tokens, rhs.cache_write_tokens);
            self.reasoning_tokens = sum(self.reasoning_tokens, rhs.reasoning_tokens);
        }
    }

    /// Adds two optional counts, treating an unreported count as zero.
    /// The result is only unknown when neither side reported the field.
    fn sum(left: Option<u64>, right: Option<u64>) -> Option<u64> {
        if left.is_none() && right.is_none() {
            return None;
        }
        Some(
            left.unwrap_or_default()
                .saturating_add(right.unwrap_or_default()),
        )
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn a_record_without_cache_details_keeps_the_counts_already_accumulated() {
            let mut total = Usage {
                input_tokens: Some(1_000),
                output_tokens: Some(20),
                total_tokens: Some(1_020),
                cache_read_tokens: Some(900),
                ..Default::default()
            };
            // A second provider call that omits `prompt_tokens_details`.
            total += Usage {
                input_tokens: Some(1_200),
                output_tokens: Some(30),
                total_tokens: Some(1_230),
                ..Default::default()
            };
            assert_eq!(total.input_tokens, Some(2_200));
            assert_eq!(total.output_tokens, Some(50));
            assert_eq!(total.total_tokens, Some(2_250));
            assert_eq!(total.cache_read_tokens, Some(900));
        }

        #[test]
        fn a_late_reported_count_is_not_discarded() {
            let mut total = Usage {
                input_tokens: Some(1_000),
                ..Default::default()
            };
            total += Usage {
                input_tokens: Some(1_200),
                cache_read_tokens: Some(900),
                reasoning_tokens: Some(64),
                ..Default::default()
            };
            assert_eq!(total.cache_read_tokens, Some(900));
            assert_eq!(total.reasoning_tokens, Some(64));
        }

        #[test]
        fn a_field_no_call_reported_stays_unknown() {
            let mut total = Usage {
                input_tokens: Some(1_000),
                ..Default::default()
            };
            total += Usage {
                input_tokens: Some(1_200),
                ..Default::default()
            };
            assert_eq!(total.cache_write_tokens, None);
        }
    }
}
pub use usage::*;

mod llm_call {
    use serde::Serialize;

    use super::ProviderType;

    #[derive(Clone, Debug)]
    pub struct NewLlmCall {
        pub call_id: String,
        pub run_id: String,
        pub conversation_id: String,
        pub provider_call_index: i64,
        pub model_hash: String,
        pub provider_type: ProviderType,
        pub provider_url: String,
        pub request_type: ProviderType,
        pub request_url: String,
        pub model_id: String,
        pub display_name: String,
        pub reasoning_effort: Option<String>,
        pub fast: bool,
        pub message_count: usize,
        pub tool_count: usize,
        pub detailed: bool,
    }

    #[derive(Clone, Debug, Serialize)]
    pub struct LlmCallSummary {
        pub alias_id: Option<String>,
        pub alias_name: Option<String>,
        pub alias_target_id: Option<String>,
        pub alias_switch_count: i64,
        pub call_id: String,
        pub run_id: String,
        pub conversation_id: String,
        pub provider_call_index: i64,
        pub model_hash: Option<String>,
        pub provider_type: String,
        pub provider_url: String,
        pub request_type: String,
        pub request_url: String,
        pub model_id: String,
        pub display_name: String,
        pub reasoning_effort: Option<String>,
        pub fast: Option<bool>,
        pub status: String,
        pub finish_reason: Option<String>,
        pub created_at_ms: i64,
        pub request_started_at_ms: Option<i64>,
        pub response_headers_at_ms: Option<i64>,
        pub first_event_at_ms: Option<i64>,
        pub first_text_at_ms: Option<i64>,
        pub first_valid_response_at_ms: Option<i64>,
        pub finished_at_ms: Option<i64>,
        pub queue_ms: Option<i64>,
        pub ttfb_ms: Option<i64>,
        pub ttft_ms: Option<i64>,
        pub ttfr_ms: Option<i64>,
        pub duration_ms: Option<i64>,
        pub input_tokens: Option<i64>,
        pub output_tokens: Option<i64>,
        pub total_tokens: Option<i64>,
        pub cache_read_tokens: Option<i64>,
        pub cache_write_tokens: Option<i64>,
        pub reasoning_tokens: Option<i64>,
        pub usage: Option<serde_json::Value>,
        pub message_count: i64,
        pub tool_count: i64,
        pub request_bytes: Option<i64>,
        pub response_bytes: i64,
        pub stream_event_count: i64,
        pub http_status: Option<i64>,
        pub error_kind: Option<String>,
        pub error_message: Option<String>,
        pub detailed: bool,
    }

    #[derive(Clone, Debug, Serialize)]
    pub struct LlmCallRequest {
        pub headers: serde_json::Value,
        pub body: serde_json::Value,
        pub byte_count: i64,
    }

    #[derive(Clone, Debug, Serialize)]
    pub struct LlmCallResponseChunk {
        pub seq: i64,
        pub received_offset_ms: i64,
        pub data: String,
        pub byte_count: i64,
    }
}
pub use llm_call::*;

mod cursor_trace {
    use serde::Serialize;

    #[derive(Clone, Debug, Serialize)]
    pub struct CursorRunTraceSummary {
        pub request_id: String,
        pub conversation_id: Option<String>,
        pub route: String,
        pub model_id: Option<String>,
        pub status: String,
        pub request_bytes: i64,
        pub response_bytes: i64,
        pub response_event_count: i64,
        pub http_status: Option<i64>,
        pub received_at_ms: i64,
        pub first_response_at_ms: Option<i64>,
        pub finished_at_ms: Option<i64>,
        pub error_message: Option<String>,
    }

    #[derive(Clone, Debug)]
    pub struct CursorRunTraceArtifact {
        pub seq: i64,
        pub artifact_type: String,
        pub source: String,
        pub metadata: serde_json::Value,
        pub created_at_ms: i64,
        pub data: Vec<u8>,
    }
}
pub use cursor_trace::*;

mod overview {
    //! Read-only usage aggregates rendered by the desktop overview page.

    use serde::Serialize;

    #[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
    pub struct OverviewMetrics {
        pub alias_switches: i64,
        pub llm_calls: i64,
        pub successful_calls: i64,
        pub failed_calls: i64,
        pub token_usage: i64,
        pub prompt_tokens: i64,
        pub input_tokens: i64,
        pub cache_read_tokens: i64,
        pub cache_write_tokens: i64,
        pub output_tokens: i64,
    }

    #[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
    #[serde(rename_all = "snake_case")]
    pub enum TokenUsageGranularity {
        Minute,
        Hour,
        #[default]
        Day,
    }

    #[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
    pub struct TokenUsageBucket {
        pub bucket_start_ms: i64,
        pub input_tokens: i64,
        pub cache_read_tokens: i64,
        pub cache_write_tokens: i64,
        pub output_tokens: i64,
    }

    impl TokenUsageBucket {
        pub fn total_tokens(&self) -> i64 {
            self.input_tokens
                .saturating_add(self.cache_read_tokens)
                .saturating_add(self.cache_write_tokens)
                .saturating_add(self.output_tokens)
        }
    }

    #[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
    pub struct Overview {
        pub metrics: OverviewMetrics,
        pub token_usage_granularity: TokenUsageGranularity,
        pub token_usage_series: Vec<TokenUsageBucket>,
    }
}
pub use overview::*;
