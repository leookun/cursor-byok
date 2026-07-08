---
name: prefix-cache-stability
description: Use when changing prompt compilation, history replay, persisted conversation state, model request construction, or dynamic reminders in this repo; protects prefix-cache hit rate by keeping model-visible history append-only and dynamic attention scoped to the latest request.
---

# Prefix Cache Stability

Use this skill before editing prompt, history replay, persisted conversation state, or provider request code.

## Hard Constraints

- Model-visible history is append-only. If a message was sent to the model and is meant to remain historical context, persist it and replay it at the same relative position.
- Do not move previously sent model-visible messages to a new position in later requests.
- Keep the largest stable prefix first: system prompt, imported replay, persisted user/request/tool history, then current-turn suffix context.
- Truly dynamic attention is latest-only. Current state blocks, latest edit guards, and other volatile reminders should be appended near the end of the current request and should not become long-lived prefix content unless they are intentionally persisted as historical facts.
- Persisted prompt context must be worded so it is safe as history. Avoid stale wording like "currently" unless the context is only latest-only.
- Never optimize cache by dropping correctness-critical context.
- Never remove, strip, reorder, or suppress historical `reasoning_content` replay merely to reduce repetitive thinking. Some providers need prior reasoning for valid continuation; optimize the latest tool guidance or current-turn prompt behavior instead.

## Implementation Pattern

1. Classify each prompt addition:
   - Stable system policy: belongs in the fixed system prompt.
   - Historical model-visible context: persist as replayable history.
   - Latest-only attention: append as current suffix, do not persist.
2. For persisted context, store enough metadata to dedupe the same turn, usually `source` plus a content hash.
3. Replay persisted context from history/projector, not by regenerating and inserting it into old positions.
4. On provider retries or same-turn follow-up passes, do not duplicate an already persisted prompt context.
5. Persisted conversation state must include replayable prompt context so a restarted conversation preserves the same prefix. In this repo, use `context.json.items` for replayable semantic history and `state.json` for mutable latest state.

## Verification

- Compare adjacent provider request artifacts or captured canonical request bodies and compute the longest common prefix.
- Check final raw SSE usage fields before blaming local metrics. Some OpenAI-compatible providers do not return cached-token fields.
- A healthy change should make old request prefixes stable while allowing only the newest suffix to vary.
