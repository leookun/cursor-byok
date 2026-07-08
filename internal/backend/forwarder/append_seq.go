package forwarder

import (
	"context"
	"strings"
	"sync"
	"time"
)

const appendSequenceRetention = 10 * time.Minute

type appendSequenceTracker struct {
	mu     sync.Mutex
	states map[string]*appendSequenceState
}

type appendSequenceState struct {
	mu         sync.Mutex
	next       int64
	processing bool
	ready      chan struct{}
	updatedAt  time.Time
}

type appendSequenceTicket struct {
	state *appendSequenceState
	seq   int64
}

func newAppendSequenceTracker() *appendSequenceTracker {
	return &appendSequenceTracker{
		states: make(map[string]*appendSequenceState),
	}
}

func (tracker *appendSequenceTracker) Acquire(ctx context.Context, requestID string, appendSeq int64) (appendSequenceTicket, bool, error) {
	if tracker == nil || strings.TrimSpace(requestID) == "" || appendSeq <= 0 {
		return appendSequenceTicket{}, false, nil
	}
	state := tracker.state(strings.TrimSpace(requestID))
	stale, err := state.acquire(ctx, appendSeq)
	if err != nil || stale {
		return appendSequenceTicket{}, stale, err
	}
	return appendSequenceTicket{
		state: state,
		seq:   appendSeq,
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
		next:      1,
		ready:     make(chan struct{}),
		updatedAt: now,
	}
	tracker.states[requestID] = state
	return state
}

func (state *appendSequenceState) acquire(ctx context.Context, appendSeq int64) (bool, error) {
	for {
		state.mu.Lock()
		now := time.Now().UTC()
		if state.next <= 0 {
			state.next = 1
		}
		if state.ready == nil {
			state.ready = make(chan struct{})
		}
		state.updatedAt = now
		switch {
		case appendSeq < state.next:
			state.mu.Unlock()
			return true, nil
		case appendSeq == state.next && !state.processing:
			state.processing = true
			state.mu.Unlock()
			return false, nil
		default:
			ready := state.ready
			state.mu.Unlock()
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-ready:
			}
		}
	}
}

func (state *appendSequenceState) Release(seq int64) {
	if state == nil || seq <= 0 {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.processing && state.next == seq {
		state.processing = false
		state.next++
		close(state.ready)
		state.ready = make(chan struct{})
	}
	state.updatedAt = time.Now().UTC()
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
	if ticket.state == nil || ticket.seq <= 0 {
		return
	}
	ticket.state.Release(ticket.seq)
}
