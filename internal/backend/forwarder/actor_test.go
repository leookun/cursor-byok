package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

// signatureLeakProbe is the secret Anthropic thinking signature carried in a
// ModelEvent. The invariant under test: this value is stored server-side
// (stream.ProviderAccumulatedReasoningSignature) but must NEVER appear in any
// client-visible text field of published InteractionUpdate messages.
const signatureLeakProbe = "sig-abc-123-secret"

// assertMessageExcludesSignature fails the test if the given AgentServerMessage
// contains signatureLeakProbe in any field when serialized. We proto-marshal
// the message and scan the wire bytes: the probe is plain ASCII, so a verbatim
// substring match against the entire serialized message is the most robust
// catch-all — it covers every text field (ThinkingDelta.Text,
// ThinkingCompleted, TextDelta.Text, etc.) without enumerating field paths,
// and would not match proto field tags (binary varints) for an ASCII probe.
func assertMessageExcludesSignature(t *testing.T, msg *agentv1.AgentServerMessage, label string) {
	t.Helper()
	if msg == nil {
		return
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("%s: proto.Marshal err=%v", label, err)
	}
	if idx := strings.Index(string(raw), signatureLeakProbe); idx >= 0 {
		t.Fatalf("%s: signature probe %q leaked into published message at byte offset %d (wire bytes=%q)",
			label, signatureLeakProbe, idx, string(raw))
	}
}

// encryptedBlobProbe is the secret OpenAI Responses encrypted_reasoning blob
// carried in a ModelEvent.ThinkingSignature when ThinkingSignatureSource ==
// "openai_responses". The invariant under test: this value is stored
// server-side (stream.ProviderAccumulatedReasoningSignature) but must NEVER
// appear in any client-visible text field of published InteractionUpdate
// messages. Only the synthetic placeholder "Thinking is encrypted. Please
// wait a moment." is published on this path.
const encryptedBlobProbe = "encrypted-blob-secret-data-xyz"

// summaryProbeText is the textual payload embedded in the ProviderSummary
// raw JSON. The invariant under test: the raw summary JSON (and its text
// substring) is stored server-side (stream.ProviderAccumulatedReasoningSummary)
// but must NEVER appear in any client-visible message field.
const summaryProbeText = "secret internal reasoning summary"

// summaryProbeJSON is the raw JSON the OpenAI Responses adapter feeds into
// ModelEvent.ProviderSummary. It is constructed verbatim here (not via
// json.Marshal) so the exact bytes can be scanned for in published message
// wire bytes — pinning that neither the full JSON nor any substring leaks.
var summaryProbeJSON = json.RawMessage(`{"type":"summary","text":"secret internal reasoning summary"}`)

// assertMessageExcludesProbes fails the test if the given AgentServerMessage
// contains EITHER the encrypted blob probe OR the summary text probe (or the
// raw summary JSON) when serialized. Same wire-byte scanning rationale as
// assertMessageExcludesSignature: proto-marshal + ASCII substring match is a
// catch-all across every text field without enumerating field paths, and the
// ASCII probes will not collide with proto field tags (binary varints).
func assertMessageExcludesProbes(t *testing.T, msg *agentv1.AgentServerMessage, label string) {
	t.Helper()
	if msg == nil {
		return
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("%s: proto.Marshal err=%v", label, err)
	}
	wire := string(raw)
	if idx := strings.Index(wire, encryptedBlobProbe); idx >= 0 {
		t.Fatalf("%s: encrypted blob probe %q leaked into published message at byte offset %d (wire bytes=%q)",
			label, encryptedBlobProbe, idx, wire)
	}
	if idx := strings.Index(wire, summaryProbeText); idx >= 0 {
		t.Fatalf("%s: summary text probe %q leaked into published message at byte offset %d (wire bytes=%q)",
			label, summaryProbeText, idx, wire)
	}
	// Also assert the full raw summary JSON (including its "type":"summary"
	// framing) never leaks — a regression could hypothetically thread the
	// raw json.RawMessage into a text field without the summaryProbeText
	// substring being present (e.g. if the JSON were re-serialized).
	if idx := strings.Index(wire, string(summaryProbeJSON)); idx >= 0 {
		t.Fatalf("%s: summary raw JSON probe leaked into published message at byte offset %d (wire bytes=%q)",
			label, idx, wire)
	}
}

// TestThinkingSignatureNotLeaked pins the invariant that the Anthropic thinking
// signature carried in a ModelEvent with Kind=ModelEventKindThinkingCompleted is
// stored server-side (stream.ProviderAccumulatedReasoningSignature) but NEVER
// published to the client in any visible text field.
//
// It exercises the REAL actor event handler (applyProviderModelEvent) and the
// REAL buildThinkingDeltaMessage / buildThinkingCompletedMessage functions —
// no mocks of the code under test. A regression that threads the signature into
// any client-visible message field will fail this test.
func TestThinkingSignatureNotLeaked(t *testing.T) {
	t.Run("actor_handler_anthropic_signature", func(t *testing.T) {
		// Minimal *Service: applyProviderModelEvent only touches service.broker
		// and service.emitModelActivity (nil-safe). No other field is read in
		// the ThinkingDelta / ThinkingCompleted branches.
		broker := NewStreamBroker()
		service := &Service{broker: broker}
		const requestID = "req-sig-leak-anthropic"
		if _, err := broker.OpenStream(requestID, "conv-1", 0, "claude", "Claude",
			agentv1.AgentMode_AGENT_MODE_AGENT, "hi"); err != nil {
			t.Fatalf("OpenStream err=%v", err)
		}
		stream, ok := broker.Get(requestID)
		if !ok || stream == nil {
			t.Fatalf("stream %q not found after OpenStream", requestID)
		}

		// 1. Feed a ThinkingDelta carrying reasoning text (no signature yet).
		//    Accumulated reasoning is non-empty so the ThinkingCompleted branch
		//    takes the non-synthetic path (signature stored, not emitted).
		thinkingDelta := modeladapter.ModelEvent{
			Kind:          modeladapter.ModelEventKindThinkingDelta,
			Text:          "Reasoning about the user's request.",
			ThinkingStyle: agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT,
		}
		if err := service.applyProviderModelEvent(stream, thinkingDelta); err != nil {
			t.Fatalf("applyProviderModelEvent(ThinkingDelta) err=%v", err)
		}

		// 2. Feed ThinkingCompleted carrying the secret Anthropic signature.
		completed := modeladapter.ModelEvent{
			Kind:                    modeladapter.ModelEventKindThinkingCompleted,
			ThinkingDurationMS:      500,
			ThinkingSignature:       signatureLeakProbe,
			ThinkingSignatureSource: modeladapter.ReasoningSignatureSourceAnthropic,
		}
		if err := service.applyProviderModelEvent(stream, completed); err != nil {
			t.Fatalf("applyProviderModelEvent(ThinkingCompleted) err=%v", err)
		}

		// 3. Capture every published StreamEvent.
		events, err := broker.ReadFromCursor(requestID, 0)
		if err != nil {
			t.Fatalf("ReadFromCursor err=%v", err)
		}
		if len(events) == 0 {
			t.Fatal("no StreamEvents published — handler did not emit thinking messages")
		}

		// 4. Assert NO published message contains the signature probe.
		for i, ev := range events {
			assertMessageExcludesSignature(t, ev.Message, "event["+string(rune('0'+i))+"]")
		}

		// 5. Confirm the signature WAS stored server-side — proving the probe
		//    reached the handler and the non-leak assertion is meaningful, not
		//    vacuous. (A test that never fed the signature would trivially pass.)
		stream.mu.Lock()
		stored := stream.ProviderAccumulatedReasoningSignature
		storedSource := stream.ProviderAccumulatedReasoningSignatureSource
		stream.mu.Unlock()
		if stored != signatureLeakProbe {
			t.Fatalf("signature not stored server-side: got %q, want %q (non-leak assertion is vacuous)",
				stored, signatureLeakProbe)
		}
		if storedSource != modeladapter.ReasoningSignatureSourceAnthropic {
			t.Fatalf("signature source not stored: got %q, want %q",
				storedSource, modeladapter.ReasoningSignatureSourceAnthropic)
		}
	})

	t.Run("actor_handler_openai_synthetic_signature", func(t *testing.T) {
		// OpenAI Responses path: signature with source=openai_responses AND
		// empty accumulated reasoning triggers the synthetic-thinking placeholder.
		// The synthetic text must still not contain the signature.
		broker := NewStreamBroker()
		service := &Service{broker: broker}
		const requestID = "req-sig-leak-openai-synthetic"
		if _, err := broker.OpenStream(requestID, "conv-2", 0, "gpt-5", "GPT-5",
			agentv1.AgentMode_AGENT_MODE_AGENT, "hi"); err != nil {
			t.Fatalf("OpenStream err=%v", err)
		}
		stream, ok := broker.Get(requestID)
		if !ok || stream == nil {
			t.Fatalf("stream %q not found after OpenStream", requestID)
		}

		completed := modeladapter.ModelEvent{
			Kind:                    modeladapter.ModelEventKindThinkingCompleted,
			ThinkingDurationMS:      0,
			ThinkingSignature:       signatureLeakProbe,
			ThinkingSignatureSource: modeladapter.ReasoningSignatureSourceOpenAIResponses,
			ThinkingStyle:           agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT,
		}
		if err := service.applyProviderModelEvent(stream, completed); err != nil {
			t.Fatalf("applyProviderModelEvent(synthetic) err=%v", err)
		}

		events, err := broker.ReadFromCursor(requestID, 0)
		if err != nil {
			t.Fatalf("ReadFromCursor err=%v", err)
		}
		if len(events) == 0 {
			t.Fatal("no StreamEvents published for synthetic-thinking path")
		}
		for i, ev := range events {
			assertMessageExcludesSignature(t, ev.Message, "synthetic-event["+string(rune('0'+i))+"]")
		}

		stream.mu.Lock()
		stored := stream.ProviderAccumulatedReasoningSignature
		stream.mu.Unlock()
		if stored != signatureLeakProbe {
			t.Fatalf("signature not stored server-side (synthetic path): got %q, want %q",
				stored, signatureLeakProbe)
		}
	})

	t.Run("events_builders_direct", func(t *testing.T) {
		// Tightest unit pin: call the builder functions directly with inputs
		// that are NOT the signature, and confirm the signature probe (had it
		// been threaded in by a future regression) would be detected. This also
		// documents that the builders accept only text/duration — no signature
		// parameter exists.
		cases := []struct {
			name string
			msg  *agentv1.AgentServerMessage
		}{
			{name: "ThinkingDelta", msg: buildThinkingDeltaMessage("reasoning text", agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT)},
			{name: "ThinkingCompleted", msg: buildThinkingCompletedMessage(500)},
			{name: "TextDelta", msg: buildTextDeltaMessage("assistant text")},
		}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Sanity: the builders do not embed the probe (they have no
			// signature input, so this should always pass — but it guards
			// against a future refactor that adds a leak path).
			assertMessageExcludesSignature(t, c.msg, c.name)
		})
	}
})
}

// TestEncryptedReasoningNotLeaked pins the invariant that OpenAI Responses
// encrypted reasoning content (the encrypted_content blob, carried in
// ModelEvent.ThinkingSignature when ThinkingSignatureSource ==
// "openai_responses") and the reasoning output item summary raw JSON
// (carried in ModelEvent.ProviderSummary) are stored server-side
// (stream.ProviderAccumulatedReasoningSignature /
// stream.ProviderAccumulatedReasoningSummary) but NEVER published to the
// client in any visible text field.
//
// It exercises the REAL actor event handler (applyProviderModelEvent) and the
// REAL buildThinkingDeltaMessage / buildThinkingCompletedMessage functions —
// no mocks of the code under test. The synthetic-thinking placeholder path
// (actor.go:529-552) is triggered by: non-empty ThinkingSignature +
// source=openai_responses + empty ProviderAccumulatedReasoning. On this path
// the actor publishes ONLY:
//   - buildThinkingDeltaMessage("Thinking is encrypted. Please wait a moment.", style)
//   - buildThinkingCompletedMessage(durationMS)
//
// Neither builder accepts the encrypted blob or the summary JSON — so neither
// can leak. A regression that threads event.ThinkingSignature or
// event.ProviderSummary into any client-visible message field will fail this
// test.
func TestEncryptedReasoningNotLeaked(t *testing.T) {
	t.Run("actor_handler_openai_responses_synthetic", func(t *testing.T) {
		// Minimal *Service: applyProviderModelEvent only touches service.broker
		// and service.emitModelActivity (nil-safe). No other field is read in
		// the ThinkingCompleted branch.
		broker := NewStreamBroker()
		service := &Service{broker: broker}
		const requestID = "req-encrypted-leak-openai-responses"
		if _, err := broker.OpenStream(requestID, "conv-enc", 0, "gpt-5", "GPT-5",
			agentv1.AgentMode_AGENT_MODE_AGENT, "hi"); err != nil {
			t.Fatalf("OpenStream err=%v", err)
		}
		stream, ok := broker.Get(requestID)
		if !ok || stream == nil {
			t.Fatalf("stream %q not found after OpenStream", requestID)
		}

		// Feed a ThinkingCompleted carrying the encrypted blob + raw summary
		// JSON. No prior ThinkingDelta has been emitted, so
		// ProviderAccumulatedReasoning is empty → synthetic-thinking path
		// triggers (actor.go:529-530).
		completed := modeladapter.ModelEvent{
			Kind:                    modeladapter.ModelEventKindThinkingCompleted,
			ThinkingDurationMS:      750,
			ThinkingSignature:       encryptedBlobProbe,
			ThinkingSignatureSource: modeladapter.ReasoningSignatureSourceOpenAIResponses,
			ThinkingStyle:           agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT,
			ProviderSummary:         append([]byte(nil), summaryProbeJSON...),
		}
		if err := service.applyProviderModelEvent(stream, completed); err != nil {
			t.Fatalf("applyProviderModelEvent(encrypted) err=%v", err)
		}

		// Capture every published StreamEvent.
		events, err := broker.ReadFromCursor(requestID, 0)
		if err != nil {
			t.Fatalf("ReadFromCursor err=%v", err)
		}
		if len(events) == 0 {
			t.Fatal("no StreamEvents published — synthetic-thinking path did not fire")
		}

		// Assert NO published message contains the encrypted blob, the summary
		// text, or the raw summary JSON.
		for i, ev := range events {
			assertMessageExcludesProbes(t, ev.Message, "event["+string(rune('0'+i))+"]")
		}

		// Confirm the encrypted blob AND the summary JSON were stored
		// server-side — proving the probes reached the handler and the
		// non-leak assertion is meaningful, not vacuous. (A test that never
		// fed the secrets would trivially pass.)
		stream.mu.Lock()
		storedSig := stream.ProviderAccumulatedReasoningSignature
		storedSigSource := stream.ProviderAccumulatedReasoningSignatureSource
		storedSummary := append([]byte(nil), stream.ProviderAccumulatedReasoningSummary...)
		stream.mu.Unlock()

		if storedSig != encryptedBlobProbe {
			t.Fatalf("encrypted blob not stored server-side: got %q, want %q (non-leak assertion is vacuous)",
				storedSig, encryptedBlobProbe)
		}
		if storedSigSource != modeladapter.ReasoningSignatureSourceOpenAIResponses {
			t.Fatalf("signature source not stored: got %q, want %q",
				storedSigSource, modeladapter.ReasoningSignatureSourceOpenAIResponses)
		}
		if string(storedSummary) != string(summaryProbeJSON) {
			t.Fatalf("summary JSON not stored server-side: got %q, want %q (non-leak assertion is vacuous)",
				string(storedSummary), string(summaryProbeJSON))
		}

		// Additionally assert the positive contract: at least one published
		// message IS the synthetic thinking placeholder, and at least one IS
		// the thinking-completed duration message. This pins that the
		// synthetic path actually fired (so the non-leak check above ran
		// against real output, not an empty publish list).
		var sawSyntheticPlaceholder, sawThinkingCompleted bool
		for _, ev := range events {
			if upd := ev.Message.GetInteractionUpdate(); upd != nil {
				if td := upd.GetThinkingDelta(); td != nil && td.Text == "Thinking is encrypted. Please wait a moment." {
					sawSyntheticPlaceholder = true
				}
				if tc := upd.GetThinkingCompleted(); tc != nil {
					sawThinkingCompleted = true
				}
			}
		}
		if !sawSyntheticPlaceholder {
			t.Fatal("synthetic 'Thinking is encrypted.' placeholder was NOT published — synthetic path did not fire (non-leak check ran against empty/wrong output)")
		}
		if !sawThinkingCompleted {
			t.Fatal("ThinkingCompleted message was NOT published — non-leak check did not cover the duration message")
		}
	})

	t.Run("events_builders_direct", func(t *testing.T) {
		// Tightest unit pin: call the builder functions directly and confirm
		// the encrypted blob and summary JSON (had they been threaded in by a
		// future regression) would be detected. This also documents that the
		// builders accept only text/duration — no encrypted blob or summary
		// JSON parameter exists.
		cases := []struct {
			name string
			msg  *agentv1.AgentServerMessage
		}{
			{name: "ThinkingDelta-synthetic", msg: buildThinkingDeltaMessage("Thinking is encrypted. Please wait a moment.", agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT)},
			{name: "ThinkingCompleted", msg: buildThinkingCompletedMessage(750)},
			{name: "TextDelta", msg: buildTextDeltaMessage("assistant text")},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				assertMessageExcludesProbes(t, c.msg, c.name)
			})
		}
	})
}
