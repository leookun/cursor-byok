package forwarder

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	appendSequenceRetention = 10 * time.Minute
	maxAppendSequence       = int64(1<<63 - 1)
)

type appendSequenceTracker struct {
	mu     sync.Mutex
	states map[string]*appendSequenceState
}

type appendSequenceState struct {
	mu                               sync.Mutex
	next                             int64
	processing                       bool
	ready                            chan struct{}
	completed                        map[int64]struct{}
	restartGeneration                uint64
	acceptedFingerprints             map[int64][32]byte
	sequenceZeroReplayPending        bool
	sequenceZeroReplayGeneration     uint64
	sequenceZeroReplayBaseNext       int64
	sequenceZeroReplayChangeObserved bool
	replayDuplicateFingerprints      map[int64][32]byte
	replayBaselineFingerprints       map[int64][32]byte
	replayBaselineExclusiveSequence  int64
	updatedAt                        time.Time
}

type appendSequenceGeneration struct {
	state             *appendSequenceState
	restartGeneration uint64
}

type appendSequenceTicket struct {
	state           *appendSequenceState
	seq             int64
	generation      appendSequenceGeneration
	replayRestarted bool
}

type appendSequenceCompletion struct {
	stale           bool
	retry           bool
	generation      appendSequenceGeneration
	replayRestarted bool
}

func newAppendSequenceTracker() *appendSequenceTracker {
	return &appendSequenceTracker{
		states: make(map[string]*appendSequenceState),
	}
}

// Acquire preserves the original one-based tracker API used by existing
// callers. Production Bidi traffic uses AcquireForTransport because the
// current client transport is zero-based.
func (tracker *appendSequenceTracker) Acquire(ctx context.Context, requestID string, appendSeq int64) (appendSequenceTicket, bool, error) {
	requestID = strings.TrimSpace(requestID)
	if tracker == nil || requestID == "" || appendSeq <= 0 {
		return appendSequenceTicket{}, false, nil
	}
	state := tracker.state(requestID)
	allowRestart := false
	if appendSeq == 1 {
		state.mu.Lock()
		next := state.next
		state.mu.Unlock()
		if next > 0 {
			allowRestart = true
		}
	}
	return tracker.acquireState(ctx, requestID, appendSeq-1, allowRestart)
}

func (tracker *appendSequenceTracker) AcquireForTransport(ctx context.Context, requestID string, appendSeq int64, allowRestart bool) (appendSequenceTicket, bool, error) {
	return tracker.acquireForTransport(ctx, requestID, appendSeq, allowRestart, nil)
}

func (tracker *appendSequenceTracker) AcquireForTransportWithFingerprint(ctx context.Context, requestID string, appendSeq int64, allowRestart bool, fingerprint [32]byte) (appendSequenceTicket, bool, error) {
	return tracker.acquireForTransport(ctx, requestID, appendSeq, allowRestart, &fingerprint)
}

func (tracker *appendSequenceTracker) acquireForTransport(ctx context.Context, requestID string, appendSeq int64, allowRestart bool, fingerprint *[32]byte) (appendSequenceTicket, bool, error) {
	if tracker == nil || strings.TrimSpace(requestID) == "" {
		return appendSequenceTicket{}, false, nil
	}
	if appendSeq < 0 {
		return appendSequenceTicket{}, false, nil
	}
	requestID = strings.TrimSpace(requestID)
	return tracker.acquireStateWithFingerprint(ctx, requestID, appendSeq, allowRestart, fingerprint)
}

func (tracker *appendSequenceTracker) acquireState(ctx context.Context, requestID string, appendSeq int64, allowRestart bool) (appendSequenceTicket, bool, error) {
	return tracker.acquireStateWithFingerprint(ctx, requestID, appendSeq, allowRestart, nil)
}

func (tracker *appendSequenceTracker) acquireStateWithFingerprint(ctx context.Context, requestID string, appendSeq int64, allowRestart bool, fingerprint *[32]byte) (appendSequenceTicket, bool, error) {
	if tracker == nil || requestID == "" {
		return appendSequenceTicket{}, false, nil
	}
	state := tracker.state(requestID)
	stale, generation, replayRestarted, err := state.acquire(ctx, requestID, appendSeq, allowRestart, fingerprint)
	if err != nil || stale {
		return appendSequenceTicket{}, stale, err
	}
	return appendSequenceTicket{
		state:           state,
		seq:             appendSeq,
		generation:      generation,
		replayRestarted: replayRestarted,
	}, false, nil
}

func (tracker *appendSequenceTracker) state(requestID string) *appendSequenceState {
	now := time.Now().UTC()
	cutoff := now.Add(-appendSequenceRetention)

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for key, state := range tracker.states {
		if state == nil || state.expired(cutoff) {
			delete(tracker.states, key)
		}
	}
	if state, ok := tracker.states[requestID]; ok && state != nil {
		state.touch(now)
		return state
	}
	state := &appendSequenceState{
		next:      0,
		ready:     make(chan struct{}),
		updatedAt: now,
	}
	tracker.states[requestID] = state
	return state
}

// IsDefinitelyStale is a non-blocking filter for early control signals.
func (tracker *appendSequenceTracker) IsDefinitelyStale(requestID string, appendSeq int64, allowRestart bool) bool {
	if tracker == nil || strings.TrimSpace(requestID) == "" || appendSeq < 0 {
		return false
	}
	requestID = strings.TrimSpace(requestID)
	tracker.mu.Lock()
	state := tracker.states[requestID]
	tracker.mu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.canRestartLocked(appendSeq, allowRestart) {
		return false
	}
	return appendSeq < state.next
}

func (tracker *appendSequenceTracker) ObserveSequenceZeroFingerprint(requestID string, fingerprint [32]byte) bool {
	if tracker == nil || strings.TrimSpace(requestID) == "" {
		return true
	}
	requestID = strings.TrimSpace(requestID)
	state := tracker.state(requestID)
	now := time.Now().UTC()
	state.mu.Lock()
	defer state.mu.Unlock()
	currentFingerprint, exists := state.acceptedFingerprints[0]
	if exists && currentFingerprint == fingerprint {
		if !state.sequenceZeroReplayPendingLocked() {
			state.beginSequenceZeroReplayLocked()
		}
		state.sequenceZeroReplayPending = true
		state.sequenceZeroReplayGeneration = state.restartGeneration
		state.updatedAt = now
		return false
	}
	return true
}

func (tracker *appendSequenceTracker) ReplayRestartCandidate(requestID string, appendSeq int64, fingerprint [32]byte) (appendSequenceGeneration, bool) {
	requestID = strings.TrimSpace(requestID)
	if tracker == nil || requestID == "" || appendSeq <= 0 {
		return appendSequenceGeneration{}, false
	}
	tracker.mu.Lock()
	state := tracker.states[requestID]
	tracker.mu.Unlock()
	if state == nil {
		return appendSequenceGeneration{}, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.sequenceZeroReplayPendingLocked() || appendSeq >= state.sequenceZeroReplayBaseNext {
		return appendSequenceGeneration{}, false
	}
	if state.fingerprintMatchesLocked(appendSeq, &fingerprint) {
		return appendSequenceGeneration{}, false
	}
	state.sequenceZeroReplayChangeObserved = true
	return state.generationLocked(), true
}

// CompleteAhead records a control-plane append without making its RPC wait
// behind a long-running earlier append. Once the gap closes, Release advances
// across these completed sequence numbers before waking ordered waiters.
func (tracker *appendSequenceTracker) CompleteAhead(requestID string, appendSeq int64) bool {
	requestID = strings.TrimSpace(requestID)
	if tracker == nil || requestID == "" || appendSeq <= 0 {
		return false
	}
	return tracker.state(requestID).completeAhead(appendSeq, nil).stale
}

func (tracker *appendSequenceTracker) CaptureGeneration(requestID string) appendSequenceGeneration {
	requestID = strings.TrimSpace(requestID)
	if tracker == nil || requestID == "" {
		return appendSequenceGeneration{}
	}
	state := tracker.state(requestID)
	state.mu.Lock()
	generation := appendSequenceGeneration{
		state:             state,
		restartGeneration: state.restartGeneration,
	}
	state.mu.Unlock()
	return generation
}

func (tracker *appendSequenceTracker) CompleteAheadForGeneration(requestID string, appendSeq int64, generation appendSequenceGeneration) (bool, bool) {
	matched, completion := tracker.CompleteAheadForGenerationWithFingerprint(requestID, appendSeq, generation, nil)
	return matched, completion.stale
}

func (tracker *appendSequenceTracker) CompleteAheadForGenerationWithFingerprint(requestID string, appendSeq int64, generation appendSequenceGeneration, fingerprint *[32]byte) (bool, appendSequenceCompletion) {
	requestID = strings.TrimSpace(requestID)
	if tracker == nil || requestID == "" || appendSeq <= 0 || generation.state == nil {
		return false, appendSequenceCompletion{}
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	state := tracker.states[requestID]
	if state == nil || state != generation.state {
		return false, appendSequenceCompletion{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.restartGeneration != generation.restartGeneration {
		return false, appendSequenceCompletion{}
	}
	return true, state.completeAheadLocked(appendSeq, fingerprint)
}

func (state *appendSequenceState) completeAhead(appendSeq int64, fingerprint *[32]byte) appendSequenceCompletion {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.completeAheadLocked(appendSeq, fingerprint)
}

func (state *appendSequenceState) completeAheadLocked(appendSeq int64, fingerprint *[32]byte) appendSequenceCompletion {
	state.updatedAt = time.Now().UTC()
	completion := appendSequenceCompletion{generation: state.generationLocked()}
	if state.sequenceZeroReplayPendingLocked() && appendSeq > 0 {
		baseNext := state.sequenceZeroReplayBaseNext
		switch {
		case appendSeq < baseNext && state.fingerprintMatchesLocked(appendSeq, fingerprint):
			state.rememberReplayDuplicateLocked(appendSeq, fingerprint)
			state.notifyReadyLocked()
			completion.stale = true
			return completion
		case appendSeq < baseNext && !state.replayPrefixConfirmedLocked(appendSeq):
			state.sequenceZeroReplayChangeObserved = true
			completion.retry = true
			return completion
		case appendSeq < baseNext:
			state.sequenceZeroReplayChangeObserved = true
			completion.generation = state.resetForSequenceZeroReplayLocked(appendSeq)
			completion.replayRestarted = true
		case appendSeq == state.next && !state.processing && state.next >= baseNext:
			if state.sequenceZeroReplayChangeObserved {
				completion.retry = true
				return completion
			}
			state.clearSequenceZeroReplayLocked()
		}
	}
	if state.consumeReplayBaselineLocked(appendSeq, fingerprint) {
		completion.stale = true
		completion.generation = state.generationLocked()
		return completion
	}
	if appendSeq < state.next || (appendSeq == state.next && state.processing) {
		completion.stale = true
		completion.generation = state.generationLocked()
		return completion
	}
	if state.completed == nil {
		state.completed = make(map[int64]struct{})
	}
	if _, exists := state.completed[appendSeq]; exists {
		completion.stale = true
		completion.generation = state.generationLocked()
		return completion
	}
	state.completed[appendSeq] = struct{}{}
	state.rememberAcceptedFingerprintLocked(appendSeq, fingerprint)
	if !state.processing && appendSeq == state.next {
		state.advanceCompletedLocked()
		state.notifyReadyLocked()
	}
	completion.generation = state.generationLocked()
	return completion
}

func (state *appendSequenceState) acquire(ctx context.Context, requestID string, appendSeq int64, allowRestart bool, fingerprint *[32]byte) (bool, appendSequenceGeneration, bool, error) {
	restartDecisionMade := false
	restartEligible := false
	var observedRestartGeneration uint64
	for {
		state.mu.Lock()
		now := time.Now().UTC()
		if state.next < 0 {
			state.next = 0
		}
		if state.ready == nil {
			state.ready = make(chan struct{})
		}
		state.updatedAt = now
		if fingerprint != nil && appendSeq == 0 {
			if state.fingerprintMatchesLocked(0, fingerprint) {
				if !state.sequenceZeroReplayPendingLocked() {
					state.beginSequenceZeroReplayLocked()
				}
				state.sequenceZeroReplayPending = true
				state.sequenceZeroReplayGeneration = state.restartGeneration
				state.mu.Unlock()
				return true, appendSequenceGeneration{}, false, nil
			}
		}

		// A new Bidi transport restarts at seqno 0 only after the caller has
		// validated the active stream or pending-operation lifecycle.
		if !restartDecisionMade {
			restartEligible = state.canRestartLocked(appendSeq, allowRestart)
			observedRestartGeneration = state.restartGeneration
			restartDecisionMade = true
		}
		// Another seqno 0 waiter already claimed this lifecycle. Permanently
		// revoke this waiter's restart eligibility so it becomes stale after
		// the winner releases instead of resetting the sequence a second time.
		if restartEligible && state.restartGeneration != observedRestartGeneration {
			restartEligible = false
		}
		if restartEligible {
			if state.processing {
				ready := state.ready
				state.mu.Unlock()
				select {
				case <-ctx.Done():
					return false, appendSequenceGeneration{}, false, ctx.Err()
				case <-ready:
				}
				continue
			}
			prevNext := state.next
			state.next = 0
			state.processing = true
			state.completed = nil
			state.restartGeneration = nextAppendSequenceRestartGeneration(state.restartGeneration)
			state.clearSequenceZeroReplayLocked()
			state.clearReplayBaselineLocked()
			state.acceptedFingerprints = nil
			state.rememberAcceptedFingerprintLocked(appendSeq, fingerprint)
			generation := state.generationLocked()
			state.mu.Unlock()
			log.Printf("forwarder reset append sequence request_id=%s previous_next=%d append_seqno=0", requestID, prevNext)
			return false, generation, false, nil
		}
		if state.sequenceZeroReplayPendingLocked() && appendSeq > 0 {
			baseNext := state.sequenceZeroReplayBaseNext
			switch {
			case appendSeq < baseNext && state.fingerprintMatchesLocked(appendSeq, fingerprint):
				state.rememberReplayDuplicateLocked(appendSeq, fingerprint)
				state.notifyReadyLocked()
				state.mu.Unlock()
				return true, appendSequenceGeneration{}, false, nil
			case appendSeq < baseNext:
				state.sequenceZeroReplayChangeObserved = true
				if state.processing || !state.replayPrefixConfirmedLocked(appendSeq) {
					ready := state.ready
					state.mu.Unlock()
					select {
					case <-ctx.Done():
						return false, appendSequenceGeneration{}, false, ctx.Err()
					case <-ready:
					}
					continue
				}
				prevNext := state.next
				generation := state.resetForSequenceZeroReplayLocked(appendSeq)
				state.processing = true
				state.rememberAcceptedFingerprintLocked(appendSeq, fingerprint)
				state.mu.Unlock()
				log.Printf("forwarder confirmed append sequence replay request_id=%s previous_next=%d append_seqno=%d", requestID, prevNext, appendSeq)
				return false, generation, true, nil
			case appendSeq == state.next && !state.processing && state.next >= baseNext:
				if state.sequenceZeroReplayChangeObserved {
					ready := state.ready
					state.mu.Unlock()
					select {
					case <-ctx.Done():
						return false, appendSequenceGeneration{}, false, ctx.Err()
					case <-ready:
					}
					continue
				}
				state.clearSequenceZeroReplayLocked()
			}
		}
		if state.consumeReplayBaselineLocked(appendSeq, fingerprint) {
			generation := state.generationLocked()
			state.mu.Unlock()
			return true, generation, false, nil
		}

		switch {
		case appendSeq < state.next:
			state.mu.Unlock()
			return true, appendSequenceGeneration{}, false, nil
		case appendSeq == state.next && !state.processing:
			state.processing = true
			if appendSeq == 0 && fingerprint != nil {
				state.clearSequenceZeroReplayLocked()
				state.clearReplayBaselineLocked()
				state.acceptedFingerprints = nil
			}
			state.rememberAcceptedFingerprintLocked(appendSeq, fingerprint)
			generation := state.generationLocked()
			state.mu.Unlock()
			return false, generation, false, nil
		default:
			ready := state.ready
			state.mu.Unlock()
			select {
			case <-ctx.Done():
				return false, appendSequenceGeneration{}, false, ctx.Err()
			case <-ready:
			}
		}
	}
}

func nextAppendSequenceRestartGeneration(current uint64) uint64 {
	next := current + 1
	if next == 0 {
		next++
	}
	return next
}

func (state *appendSequenceState) canRestartLocked(appendSeq int64, allowRestart bool) bool {
	return state != nil && allowRestart && appendSeq == 0 && (state.next > 0 || state.processing)
}

func (state *appendSequenceState) Release(seq int64, generation appendSequenceGeneration) {
	if state == nil || seq < 0 || generation.state != state {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.restartGeneration != generation.restartGeneration {
		return
	}
	if state.processing && state.next == seq {
		state.processing = false
		state.next++
		state.advanceCompletedLocked()
		state.notifyReadyLocked()
	}
	state.updatedAt = time.Now().UTC()
}

func (state *appendSequenceState) resetForSequenceZeroReplayLocked(appendSeq int64) appendSequenceGeneration {
	previousFingerprints := state.acceptedFingerprints
	baseNext := state.sequenceZeroReplayBaseNext
	replayDuplicates := state.replayDuplicateFingerprints

	state.next = appendSeq
	state.processing = false
	state.completed = make(map[int64]struct{})
	state.restartGeneration = nextAppendSequenceRestartGeneration(state.restartGeneration)
	state.acceptedFingerprints = make(map[int64][32]byte)
	if fingerprint, ok := previousFingerprints[0]; ok {
		state.acceptedFingerprints[0] = fingerprint
	}
	for seq, fingerprint := range replayDuplicates {
		state.acceptedFingerprints[seq] = fingerprint
		if seq >= appendSeq {
			state.completed[seq] = struct{}{}
		}
	}
	state.replayBaselineFingerprints = previousFingerprints
	state.replayBaselineExclusiveSequence = baseNext
	state.clearSequenceZeroReplayLocked()
	state.notifyReadyLocked()
	return state.generationLocked()
}

func (state *appendSequenceState) sequenceZeroReplayPendingLocked() bool {
	if !state.sequenceZeroReplayPending {
		return false
	}
	if state.sequenceZeroReplayGeneration == state.restartGeneration {
		return true
	}
	state.clearSequenceZeroReplayLocked()
	return false
}

func (state *appendSequenceState) beginSequenceZeroReplayLocked() {
	state.replayDuplicateFingerprints = nil
	state.sequenceZeroReplayChangeObserved = false
	state.sequenceZeroReplayBaseNext = state.next
	if state.processing && state.sequenceZeroReplayBaseNext < maxAppendSequence {
		state.sequenceZeroReplayBaseNext++
	}
}

func (state *appendSequenceState) clearSequenceZeroReplayLocked() {
	state.sequenceZeroReplayPending = false
	state.sequenceZeroReplayGeneration = 0
	state.sequenceZeroReplayBaseNext = 0
	state.sequenceZeroReplayChangeObserved = false
	state.replayDuplicateFingerprints = nil
}

func (state *appendSequenceState) clearReplayBaselineLocked() {
	state.replayBaselineFingerprints = nil
	state.replayBaselineExclusiveSequence = 0
}

func (state *appendSequenceState) generationLocked() appendSequenceGeneration {
	return appendSequenceGeneration{
		state:             state,
		restartGeneration: state.restartGeneration,
	}
}

func (state *appendSequenceState) fingerprintMatchesLocked(appendSeq int64, fingerprint *[32]byte) bool {
	if state == nil || fingerprint == nil {
		return false
	}
	expected, ok := state.acceptedFingerprints[appendSeq]
	return ok && expected == *fingerprint
}

func (state *appendSequenceState) rememberAcceptedFingerprintLocked(appendSeq int64, fingerprint *[32]byte) {
	if state == nil || appendSeq < 0 || fingerprint == nil {
		return
	}
	if state.acceptedFingerprints == nil {
		state.acceptedFingerprints = make(map[int64][32]byte)
	}
	state.acceptedFingerprints[appendSeq] = *fingerprint
}

func (state *appendSequenceState) rememberReplayDuplicateLocked(appendSeq int64, fingerprint *[32]byte) {
	if state == nil || appendSeq <= 0 || fingerprint == nil {
		return
	}
	if state.replayDuplicateFingerprints == nil {
		state.replayDuplicateFingerprints = make(map[int64][32]byte)
	}
	state.replayDuplicateFingerprints[appendSeq] = *fingerprint
}

func (state *appendSequenceState) replayPrefixConfirmedLocked(appendSeq int64) bool {
	if state == nil || appendSeq <= 1 {
		return true
	}
	for seq := int64(1); seq < appendSeq; seq++ {
		if _, ok := state.replayDuplicateFingerprints[seq]; !ok {
			return false
		}
	}
	return true
}

func (state *appendSequenceState) consumeReplayBaselineLocked(appendSeq int64, fingerprint *[32]byte) bool {
	if state == nil || fingerprint == nil || appendSeq <= 0 || appendSeq >= state.replayBaselineExclusiveSequence {
		return false
	}
	expected, ok := state.replayBaselineFingerprints[appendSeq]
	if !ok || expected != *fingerprint {
		return false
	}
	state.rememberAcceptedFingerprintLocked(appendSeq, fingerprint)
	if appendSeq < state.next {
		return true
	}
	if state.completed == nil {
		state.completed = make(map[int64]struct{})
	}
	state.completed[appendSeq] = struct{}{}
	if !state.processing && appendSeq == state.next {
		state.advanceCompletedLocked()
		state.notifyReadyLocked()
	}
	return true
}

func (state *appendSequenceState) advanceCompletedLocked() {
	for {
		_, ok := state.completed[state.next]
		if !ok {
			break
		}
		delete(state.completed, state.next)
		state.next++
	}
	if state.replayBaselineExclusiveSequence > 0 && state.next >= state.replayBaselineExclusiveSequence {
		state.clearReplayBaselineLocked()
	}
	// Advancing can be caused by work accepted before a duplicate seqno 0.
	// Only an explicit expected append can prove that no replay is in progress.
}

func (state *appendSequenceState) notifyReadyLocked() {
	if state.ready == nil {
		state.ready = make(chan struct{})
		return
	}
	close(state.ready)
	state.ready = make(chan struct{})
}

func (state *appendSequenceState) expired(cutoff time.Time) bool {
	if state == nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.processing {
		return false
	}
	return !state.updatedAt.IsZero() && state.updatedAt.Before(cutoff)
}

func (state *appendSequenceState) touch(now time.Time) {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.updatedAt = now
	state.mu.Unlock()
}

func (ticket appendSequenceTicket) Release() {
	if ticket.state == nil || ticket.seq < 0 {
		return
	}
	ticket.state.Release(ticket.seq, ticket.generation)
}

func (ticket appendSequenceTicket) CompleteAhead(appendSeq int64) {
	ticket.CompleteAheadWithFingerprint(appendSeq, nil)
}

func (ticket appendSequenceTicket) CompleteAheadWithFingerprint(appendSeq int64, fingerprint *[32]byte) appendSequenceCompletion {
	if ticket.state == nil || ticket.seq != 0 || appendSeq <= 0 {
		return appendSequenceCompletion{}
	}
	ticket.state.mu.Lock()
	defer ticket.state.mu.Unlock()
	if ticket.state.restartGeneration != ticket.generation.restartGeneration {
		return appendSequenceCompletion{retry: true}
	}
	return ticket.state.completeAheadLocked(appendSeq, fingerprint)
}

func (ticket appendSequenceTicket) Generation() appendSequenceGeneration {
	return ticket.generation
}

func (ticket appendSequenceTicket) ReplayRestarted() bool {
	return ticket.replayRestarted
}
