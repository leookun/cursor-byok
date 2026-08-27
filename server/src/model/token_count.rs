pub const GPT56_DEFAULT_CONTEXT_TOKENS: u64 = 272_000;
pub const GPT56_LONG_CONTEXT_TOKENS: u64 = 1_000_000;

pub(crate) fn is_gpt56_long_context_model(model_id: &str) -> bool {
    let id = model_id.to_ascii_lowercase().replace('_', "-");
    id.contains("gpt-5.6")
        && (id.contains("luna") || id.contains("sol") || id.contains("terra"))
}

pub(crate) fn is_grok_model(model_id: &str, base_url: &str, tooltip: &str) -> bool {
    let id = model_id.to_ascii_lowercase();
    base_url.contains("api.x.ai")
        || base_url.contains("cli-chat-proxy.grok.com")
        || id.contains("grok")
        || tooltip.contains("xAI")
        || tooltip.contains("Grok")
}

pub(crate) fn context_label(tokens: u64) -> (String, String) {
    let display = format_token_count(tokens);
    let value = if tokens.is_multiple_of(1_000_000) {
        format!("{}m", tokens / 1_000_000)
    } else if tokens.is_multiple_of(1_000) {
        format!("{}k", tokens / 1_000)
    } else {
        tokens.to_string()
    };
    (value, display)
}

pub(crate) fn parse_token_count(value: &str) -> Option<u64> {
    let value = value.trim().to_ascii_lowercase();
    let (number, multiplier) = match value.chars().last()? {
        'k' => (&value[..value.len() - 1], 1_000),
        'm' => (&value[..value.len() - 1], 1_000_000),
        _ => (value.as_str(), 1),
    };
    number.parse::<u64>().ok()?.checked_mul(multiplier)
}

pub(crate) fn format_token_count(tokens: u64) -> String {
    if tokens >= 1_000_000 && tokens.is_multiple_of(1_000_000) {
        format!("{}M", tokens / 1_000_000)
    } else if tokens >= 1_000 && tokens.is_multiple_of(1_000) {
        format!("{}K", tokens / 1_000)
    } else {
        tokens.to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn token_counts_parse_plain_and_abbreviated_values() {
        assert_eq!(parse_token_count("272000"), Some(272_000));
        assert_eq!(parse_token_count("272K"), Some(272_000));
        assert_eq!(parse_token_count("1m"), Some(1_000_000));
        assert_eq!(parse_token_count("invalid"), None);
    }

    #[test]
    fn token_counts_format_exact_thousands_and_millions() {
        assert_eq!(format_token_count(272_000), "272K");
        assert_eq!(format_token_count(1_000_000), "1M");
        assert_eq!(format_token_count(272_001), "272001");
    }

    #[test]
    fn gpt56_luna_sol_terra_are_long_context_models() {
        assert!(is_gpt56_long_context_model("gpt-5.6-sol"));
        assert!(is_gpt56_long_context_model("GPT-5.6-Luna"));
        assert!(is_gpt56_long_context_model("gpt-5.6-terra-high"));
        assert!(!is_gpt56_long_context_model("gpt-5.5"));
        assert!(!is_gpt56_long_context_model("grok-4.6"));
    }
}
