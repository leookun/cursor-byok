# Thinking / Reasoning Support Reference

This document is the reference for how Cursor BYOK parses, normalizes, and forwards
model reasoning (chain-of-thought) streams across providers. It is anchored to the
source in `internal/backend/agent/model/` and `internal/backend/forwarder/`. Every
claim below is backed by a file and line range so a new contributor can find the
implementation quickly.

The pipeline is provider-agnostic and has three layers:

1. **Provider adapters** in this package parse provider-specific reasoning fields
   and emit a unified `ModelEvent` with `Kind = ModelEventKindThinkingDelta` or
   `ModelEventKindThinkingCompleted` (`types.go:160-162`, struct at `types.go:176`).
2. The **forwarder actor** consumes those events and translates them into the
   Cursor protocol messages `InteractionUpdate_ThinkingDelta` and
   `InteractionUpdate_ThinkingCompleted` via `buildThinkingDeltaMessage` /
   `buildThinkingCompletedMessage` (`forwarder/events.go:43-73`).
3. The Cursor protocol (`gen/agentv1`) has first-class thinking update types, so
   no extra translation is needed downstream.

The forwarder never re-parses provider JSON. It only consumes normalized
`ModelEvent`s, which keeps provider quirks contained to the adapter layer.

---

## Per-Provider Reasoning Coverage

| Provider | Model(s) | Reasoning Field(s) in API Response | Tag Wrapper in Content | Signature Handling | Display Behavior in Cursor |
|---|---|---|---|---|---|
| OpenAI (chat completions) | `gpt-*` reasoning variants, plus OpenAI-compatible third parties | `delta.reasoning_content` | `...` via `openAIThinkTagParser` | None | Thinking delta streamed live; `ThinkingDelta` shown until `ThinkingCompleted` closes the block |
| OpenAI (chat completions) | Qwen-family via OpenAI-compatible endpoint | `delta.reasoning_content` (when present) | `` | None | **GAP**: `` tag is not recognized by `openAIThinkTagParser` (it only matches ``). If a Qwen model emits reasoning only inside ``, it leaks as plain `TextDelta`. |
| OpenAI (chat completions) | some proxies / gateways | `delta.reasoning` (alias) | none | None | **GAP**: `delta.reasoning` is not parsed. Only `delta.reasoning_content` is read (`openai.go:552`). Reasoning on these proxies leaks as text or is dropped. |
| OpenAI (Responses API) | `gpt-5` reasoning family | `response.reasoning_text.delta` and `response.reasoning_summary_text.delta` | none | `reasoning.encrypted_content` on the `reasoning` output item is captured as `ThinkingSignature` with source `openai_responses` | Live reasoning text is streamed; the encrypted content is never shown to the client. If the only reasoning the model produced is encrypted (no visible text), the forwarder emits a synthetic placeholder. |
| Anthropic | Claude `claude-*` extended-thinking models | `content_block_delta` with `delta.type = "thinking_delta"` and `delta.thinking` | Anthropic also supports `` in content blocks; the same `openAIThinkTagParser`-style scanner (`anthropic.go` `thinkParser`) splits tagged text | `delta.signature` is stored on the stream as `currentThinkingSignature` and emitted on `ThinkingCompleted` with source `anthropic`. The signature is never sent to the client as text. | Thinking delta streamed live; on completion, `ThinkingCompleted` carries the duration and the signature is stashed server-side for the next request. |
| DeepSeek | `deepseek-reasoner` / R1, V3 reasoning variants | `delta.reasoning_content` | DeepSeek also emits `` in content for some variants | None | Live reasoning delta; `ThinkingCompleted` closes the block when `` is seen or the stream ends. |
| Qwen (Alibaba) | `qwq-*`, `qwen-*` reasoning variants via OpenAI-compatible endpoint | `delta.reasoning_content` | Qwen sometimes uses `` for reasoning inside content | None | `delta.reasoning_content` is streamed. **GAP**: `` tag is not parsed, so any reasoning wrapped only in that tag leaks as text. |
| GLM / ChatGLM (Zhipu) | `glm-4` reasoning variants via OpenAI-compatible endpoint | `delta.reasoning_content` | none | None | Live reasoning delta, closed by `ThinkingCompleted`. |
| Moonshot / Kimi | `kimi-*` reasoning variants via OpenAI-compatible endpoint | `delta.reasoning_content` | none | None | Live reasoning delta, closed by `ThinkingCompleted`. |
| Doubao (ByteDance) | `doubao-*` reasoning variants via OpenAI-compatible endpoint | `delta.reasoning_content` | none | None | Live reasoning delta, closed by `ThinkingCompleted`. |
| Google Gemini | `gemini-*` thinking models | `parts[].thought: true` | none | None | **GAP**: no Gemini adapter exists in this package. Gemini reasoning is out of scope until an adapter is added. |

Notes on the table:

- "OpenAI-compatible endpoint" means the provider speaks the OpenAI chat
  completions streaming format, so it is served by `OpenAIAdapter` and benefits
  from the shared `reasoning_content` parsing and the `openAIThinkTagParser`.
- "Signature handling" describes what the adapter does with a provider-signed
  reasoning blob. The two providers that sign reasoning today are Anthropic and
  OpenAI Responses, and in both cases the signature is retained server-side and
  never echoed to the client. See the invariant section below.
- "Display behavior" is what Cursor actually renders. Reasoning text reaches
  the client only via `ThinkingDelta` messages; raw reasoning never lands in
  `TextDelta`.

---

## How `openAIThinkTagParser` Works

Source: `internal/backend/agent/model/openai.go:76-173`.

Some OpenAI-compatible providers (notably DeepSeek and some Qwen variants) put
their reasoning inside the `content` field wrapped in `` / ``
tags instead of using the dedicated `reasoning_content` field. The
`openAIThinkTagParser` is a small, allocation-light state machine that splits a
streaming `content` string back into three kinds of parts:

- `openAIContentPartText` (normal assistant text)
- `openAIContentPartReasoning` (text that came from inside ``)
- `openAIContentPartThinkingCompleted` (sentinel emitted when `` is seen)

Tag constants (`openai.go:76-80`):

```
openAIThinkOpenTag  = ""
openAIThinkCloseTag = ""
```

Key behaviors:

1. **Streaming-safe.** `Consume(text)` is called per chunk. If a chunk ends in
   the middle of a tag (for example the stream delivered just ``), the
   incomplete suffix is held in `parser.carry` and prepended to the next chunk.
   This prevents the tag from being split across SSE frames and leaking as
   text.
2. **Stateful.** `parser.inThink` tracks whether the parser is currently inside
   a `` block. While inside, all consumed text becomes
   `openAIContentPartReasoning`. On `` it emits the
   `ThinkingCompleted` sentinel and flips back to text mode.
3. **Flush.** `Flush()` returns any leftover `carry` and classifies it as
   reasoning if the stream ended while still inside a think block, otherwise as
   text. The chat loop calls `Flush` at `[DONE]`, on tool-call boundaries, and
   on stream end.
4. **Only `` is recognized.** Any other reasoning-tag variant (notably Qwen's
   ``) is treated as ordinary text and will leak as a `TextDelta`.

The chat completions loop wires the parser at `openai.go:741-814`: every
`choice.Delta.Content` chunk is fed through `thinkParser.Consume`, the resulting
parts are emitted via `emitTaggedContentParts`, and `delta.reasoning_content`
is emitted directly as a `ThinkingDelta` (`openai.go:801-805`). The Anthropic
adapter reuses the same shape through its own `thinkParser` for
`` blocks that appear inside Anthropic text deltas
(`anthropic.go:446-467`, `565-578`).

---

## `reasoning_content` vs `reasoning` vs `reasoning_text`

Three field names show up across providers and they are not synonyms:

- **`reasoning_content`** is the canonical field for OpenAI chat-completions
  style streaming. It is parsed by the chat adapter at `openai.go:552` and
  emitted as a live `ThinkingDelta`. This is the field DeepSeek, Qwen, GLM,
  Moonshot, and Doubao use. It is also the JSON tag on the unified `Message`
  struct (`types.go:29`), so once reasoning enters the internal `Message` model
  it always lives under `reasoning_content`.
- **`reasoning`** (the bare `delta.reasoning` alias used by some proxies) is
  **not** parsed. This is a known gap. If a proxy emits only `delta.reasoning`,
  its reasoning is invisible to the adapter.
- **`reasoning_text`** and **`reasoning_summary_text`** are OpenAI Responses
  API streaming event types, not chat fields. They are dispatched at
  `openai.go:1564` as `response.reasoning_text.delta` and
  `response.reasoning_summary_text.delta` events, and both are emitted as
  `ThinkingDelta`.

So a contributor adding support for a new provider should first check which of
these three surfaces the provider uses, then wire it into the matching branch.

---

## Signature Non-Leak Invariant

Reasoning signatures exist so a provider can prove reasoning continuity across
stateless multi-turn calls without re-sending the plaintext chain of thought.
Two providers sign reasoning today:

- **Anthropic**: `delta.signature` arrives alongside `thinking_delta`
  (`anthropic.go:576-578`). It is stored on the stream as
  `currentThinkingSignature` and emitted on the `ThinkingCompleted` event with
  `ThinkingSignatureSource = "anthropic"` (`anthropic.go:393-405`).
- **OpenAI Responses**: the `reasoning` output item carries an
  `encrypted_content` field. The Responses adapter calls
  `emitReasoningSignature` (`openai.go:1350-1377`) which emits a
  `ThinkingCompleted` event carrying the encrypted content as
  `ThinkingSignature` with source `"openai_responses"`, plus the item `id`,
  `status`, and `summary` for stateless replay.

The invariant is enforced in the forwarder actor at
`internal/backend/forwarder/actor.go:518-559`:

```
case ModelEventKindThinkingCompleted:
    if strings.TrimSpace(event.ThinkingSignature) != "" {
        stream.ProviderAccumulatedReasoningSignature = ...
        stream.ProviderAccumulatedReasoningSignatureSource = ...
        stream.ProviderAccumulatedReasoningItemID = ...
        stream.ProviderAccumulatedReasoningStatus = ...
        stream.ProviderAccumulatedReasoningSummary = ...
        shouldEmitSyntheticThinking = ...
    }
    if shouldEmitSyntheticThinking { ... }
    return service.broker.Publish(..., buildThinkingCompletedMessage(completedDuration))
```

Note what is **not** there: `event.ThinkingSignature` is never copied into a
`TextDelta` or `ThinkingDelta` message. It is only written into the server-side
stream accumulator fields (`ProviderAccumulatedReasoningSignature*`), which feed
the next outbound request body. The only messages published to the broker are
the synthetic placeholder (when applicable) and the `ThinkingCompleted`
duration message. The signature itself never crosses the client boundary.

This invariant should be pinned with a regression test: any event that carries
a non-empty `ThinkingSignature` must not produce a `ThinkingDelta` or
`TextDelta` whose text contains the signature bytes.

---

## Synthetic "Thinking is encrypted" Placeholder

Source: `internal/backend/forwarder/actor.go:519-555`.

OpenAI Responses can return reasoning only in encrypted form. In that case the
adapter never emits a `ThinkingDelta` (there is no plaintext to stream), so the
forwarder would otherwise show the user nothing while the model is "thinking".
To keep the UX honest, the actor synthesizes a single placeholder delta when
all three conditions hold:

1. The event carries a non-empty `ThinkingSignature`.
2. The signature source is
   `ReasoningSignatureSourceOpenAIResponses` (i.e. it is an OpenAI Responses
   encrypted reasoning item, not an Anthropic signature).
3. The accumulated reasoning text so far is empty
   (`stream.ProviderAccumulatedReasoning == ""`), meaning no real
   `ThinkingDelta` was ever sent.

The placeholder text is the fixed string:

```
Thinking is encrypted. Please wait a moment.
```

It is published exactly once per request via a `shouldEmitSyntheticThinking`
guard (`actor.go:529-547`). If the same request later produces another
`ThinkingCompleted` with a signature, `ProviderSyntheticThinkingPublished` is
already `true`, so the second one is suppressed (`suppressThinkingCompleted` at
`actor.go:543-546`) to avoid a duplicate "completed" pulse. The placeholder is
also given a synthesized duration derived from
`ProviderSyntheticThinkingStartedAt` if the event did not carry one
(`actor.go:535-540`).

This placeholder is display-only. It is never written into
`ProviderAccumulatedReasoning` and it does not round-trip into the next
request.

---

## Known Gaps and Limitations

These are the open items as of this writing. Each has a concrete failure mode.

1. **Qwen `` tag is not parsed.** `openAIThinkTagParser` only matches
   `` / ``. Qwen variants that wrap reasoning only in ``
   will leak that reasoning as ordinary `TextDelta` to the client. Fix: extend
   the tag constants at `openai.go:76-80` to include the Qwen variant, or add a
   second parser pass.
2. **`delta.reasoning` alias is not parsed.** The chat adapter struct only
   decodes `reasoning_content` (`openai.go:552`). Proxies that emit
   `delta.reasoning` instead will silently drop reasoning. Fix: add a
   `Reasoning string` field to the delta struct and merge it with
   `ReasoningContent` before emitting.
3. **Gemini has no adapter.** There is no `gemini.go` in this package. Gemini's
   `parts[].thought: true` reasoning surface is unsupported, and any Gemini
   channel configured by the user is served through whatever adapter the router
   falls back to, which will not parse `thought` parts.
4. **Raw reasoning in content without tags leaks as text.** If a provider puts
   plaintext reasoning into `delta.content` with no tag wrapper and no
   `reasoning_content` field, the adapter has no signal to reclassify it. This
   is the root cause of items 1 and 2, and it also affects any future provider
   that invents a new tag spelling.
5. **Signature non-leak is enforced by code shape, not by a test.** The
   `actor.go:518-559` handler simply never copies the signature into a client
   message. That is correct today, but without a regression test a future edit
   could break the invariant silently. A test should assert: given a
   `ThinkingCompleted` event with a non-empty `ThinkingSignature`, the only
   messages published to the broker are (optionally) the synthetic placeholder
   and the `ThinkingCompleted` duration message, and neither contains the
   signature bytes.

---

## Key Code Locations

Quick index for navigating the implementation.

| Concern | Location |
|---|---|
| Unified `Message` struct with `ReasoningContent` / `ReasoningSignature` / `ReasoningSignatureSource` | `internal/backend/agent/model/types.go:21-46` |
| `ReasoningSignatureSource*` constants | `internal/backend/agent/model/types.go:13-18` |
| `ModelEvent` struct | `internal/backend/agent/model/types.go:175-199` |
| `ModelEventKindThinkingDelta` / `ThinkingCompleted` | `internal/backend/agent/model/types.go:156-162` |
| `openAIThinkTagParser` (tag constants, `Consume`, `Flush`) | `internal/backend/agent/model/openai.go:76-173` |
| Chat-completions chunk struct with `ReasoningContent` | `internal/backend/agent/model/openai.go:541-565` |
| Chat-completions reasoning emit | `internal/backend/agent/model/openai.go:796-814` |
| Responses API `Include = ["reasoning.encrypted_content"]` | `internal/backend/agent/model/openai.go:942-945` |
| Responses `openAIResponsesOutputItem.EncryptedContent` | `internal/backend/agent/model/openai.go:1028` |
| `emitReasoningSignature` (Responses encrypted reasoning) | `internal/backend/agent/model/openai.go:1350-1377` |
| `applyOutputItem` dispatch on `reasoning` type | `internal/backend/agent/model/openai.go:1420-1423` |
| Responses reasoning text/summary delta dispatch | `internal/backend/agent/model/openai.go:1564-1567` |
| Anthropic `flushThinkingCompleted` with signature | `internal/backend/agent/model/anthropic.go:385-412` |
| Anthropic `emitThinkingDelta` | `internal/backend/agent/model/anthropic.go:429-445` |
| Anthropic `thinking_delta` / `signature` parsing | `internal/backend/agent/model/anthropic.go:565-578` |
| Forwarder actor thinking event handler (signature stashing, synthetic placeholder) | `internal/backend/forwarder/actor.go:510-559` |
| `buildThinkingDeltaMessage` | `internal/backend/forwarder/events.go:43-58` |
| `buildThinkingCompletedMessage` | `internal/backend/forwarder/events.go:60-73` |

---

## When to Update This Doc

This is a reference, not a design log. Update it when:

- A new provider adapter is added (add a row, note its reasoning field and tag).
- A gap is closed (move the row out of the "gaps" framing and update the
  parsing location).
- The synthetic placeholder text or its trigger conditions change.
- The signature non-leak invariant gains (or loses) a regression test.

Do not edit this file for unrelated refactors that do not change observable
reasoning behavior.
