use serde::Serialize;

use super::{CanonicalMessage, PromptSpec};

pub(crate) fn estimate_context_tokens(prompt: &PromptSpec, messages: &[CanonicalMessage]) -> u64 {
    estimate_serialized_tokens(&(prompt, messages))
}

pub(crate) fn estimate_message_tokens<T: Serialize>(messages: &[T]) -> u64 {
    estimate_serialized_tokens(messages)
}

fn estimate_serialized_tokens(value: &(impl Serialize + ?Sized)) -> u64 {
    let serialized = serde_json::to_string(value).unwrap_or_default();
    serialized
        .chars()
        .fold(0_u64, |units, character| {
            units.saturating_add(if character.is_ascii() { 273 } else { 550 })
        })
        .div_ceil(1_000)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{CanonicalMessage, Origin, Role};

    #[test]
    fn estimate_grows_with_serialized_context() {
        let prompt = PromptSpec {
            instructions: "system".into(),
            tools: Vec::new(),
        };
        let short = vec![CanonicalMessage::text(
            "short",
            Role::User,
            Origin::User,
            "hello",
        )];
        let long = vec![CanonicalMessage::text(
            "long",
            Role::User,
            Origin::User,
            "x".repeat(100_000),
        )];

        assert!(estimate_context_tokens(&prompt, &long) > 25_000);
        assert!(estimate_context_tokens(&prompt, &long) > estimate_context_tokens(&prompt, &short));
    }
}
