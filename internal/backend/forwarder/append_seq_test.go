package forwarder

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAppendSequenceTrackerOrderedAcquire(t *testing.T) {
	tracker := newAppendSequenceTracker()
	ctx := context.Background()

	ticket1, stale, err := tracker.Acquire(ctx, "req-1", 1)
	if err != nil || stale {
		t.Fatalf("seq 1: stale=%v err=%v", stale, err)
	}
	ticket1.Release()

	ticket2, stale, err := tracker.Acquire(ctx, "req-1", 2)
	if err != nil || stale {
		t.Fatalf("seq 2: stale=%v err=%v", stale, err)
	}
	ticket2.Release()
}

func TestAppendSequenceTrackerRestartFromOne(t *testing.T) {
	tracker := newAppendSequenceTracker()
	ctx := context.Background()

	for seq := int64(1); seq <= 5; seq++ {
		ticket, stale, err := tracker.Acquire(ctx, "req-restart", seq)
		if err != nil || stale {
			t.Fatalf("seq %d: stale=%v err=%v", seq, stale, err)
		}
		ticket.Release()
	}

	// Same request_id, Cursor restarts append_seqno from 1 for a new turn.
	ticket, stale, err := tracker.Acquire(ctx, "req-restart", 1)
	if err != nil {
		t.Fatalf("restart seq 1 err=%v", err)
	}
	if stale {
		t.Fatal("expected sequence restart on append_seqno=1 after progress")
	}
	ticket.Release()

	ticket2, stale, err := tracker.Acquire(ctx, "req-restart", 2)
	if err != nil || stale {
		t.Fatalf("post-restart seq 2: stale=%v err=%v", stale, err)
	}
	ticket2.Release()
}

func TestAppendSequenceTrackerMidGapStillStale(t *testing.T) {
	tracker := newAppendSequenceTracker()
	ctx := context.Background()

	for seq := int64(1); seq <= 3; seq++ {
		ticket, stale, err := tracker.Acquire(ctx, "req-gap", seq)
		if err != nil || stale {
			t.Fatalf("seq %d: stale=%v err=%v", seq, stale, err)
		}
		ticket.Release()
	}

	_, stale, err := tracker.Acquire(ctx, "req-gap", 2)
	if err != nil {
		t.Fatalf("gap retransmit err=%v", err)
	}
	if !stale {
		t.Fatal("expected mid-sequence retransmit below next (but not 1) to stay stale")
	}
}

func TestAppendSequenceTrackerWaitsForInOrder(t *testing.T) {
	tracker := newAppendSequenceTracker()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ticket1, stale, err := tracker.Acquire(ctx, "req-wait", 1)
	if err != nil || stale {
		t.Fatalf("seq 1: stale=%v err=%v", stale, err)
	}

	done := make(chan error, 1)
	go func() {
		ticket2, stale, err := tracker.Acquire(ctx, "req-wait", 2)
		if err != nil {
			done <- err
			return
		}
		if stale {
			done <- errors.New("seq 2 unexpectedly stale")
			return
		}
		ticket2.Release()
		done <- nil
	}()

	time.Sleep(50 * time.Millisecond)
	ticket1.Release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waiting seq 2 failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for ordered seq 2")
	}
}

func TestAppendSequenceTrackerRestartWaitsUntilIdle(t *testing.T) {
	tracker := newAppendSequenceTracker()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Advance to next=3.
	for seq := int64(1); seq <= 2; seq++ {
		ticket, stale, err := tracker.Acquire(ctx, "req-idle", seq)
		if err != nil || stale {
			t.Fatalf("seq %d: stale=%v err=%v", seq, stale, err)
		}
		ticket.Release()
	}

	// Hold seq 3 so a concurrent restart must wait.
	hold, stale, err := tracker.Acquire(ctx, "req-idle", 3)
	if err != nil || stale {
		t.Fatalf("seq 3 hold: stale=%v err=%v", stale, err)
	}

	done := make(chan error, 1)
	go func() {
		ticket, stale, err := tracker.Acquire(ctx, "req-idle", 1)
		if err != nil {
			done <- err
			return
		}
		if stale {
			done <- errors.New("restart seq 1 unexpectedly stale")
			return
		}
		ticket.Release()
		done <- nil
	}()

	time.Sleep(50 * time.Millisecond)
	hold.Release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("restart while busy failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for restart after idle")
	}

	ticket2, stale, err := tracker.Acquire(ctx, "req-idle", 2)
	if err != nil || stale {
		t.Fatalf("post-restart seq 2: stale=%v err=%v", stale, err)
	}
	ticket2.Release()
}
