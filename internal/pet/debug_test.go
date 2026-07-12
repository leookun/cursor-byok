package pet

import (
	"testing"
)

func TestDebugger_RecordsEvents(t *testing.T) {
	d := NewDebugger()
	bus := NewEventBus()
	d.Attach(bus)
	bus.Publish(Event{Type: EventStateChanged, Data: map[string]interface{}{"from": "idle", "to": "walking"}})
	bus.Publish(Event{Type: EventAnimationFinished, Data: map[string]interface{}{"anim": "walk"}})

	events := d.RecentEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	// 第一条应是 state change，且 summary 正确。
	if events[0].Summary != "idle->walking" {
		t.Fatalf("unexpected summary: %q", events[0].Summary)
	}
	snap := d.Snapshot()
	counters := snap["counters"].(map[string]int64)
	if counters["state_changes"] != 1 {
		t.Fatalf("expected 1 state change, got %d", counters["state_changes"])
	}
}

func TestDebugger_RingBufferOverflow(t *testing.T) {
	d := NewDebugger()
	bus := NewEventBus()
	d.Attach(bus)
	// 发布超过容量（默认 200）的事件。
	n := 250
	for i := 0; i < n; i++ {
		bus.Publish(Event{Type: EventBehaviorFinished})
	}
	events := d.RecentEvents()
	if len(events) != d.ringCap {
		t.Fatalf("ring buffer should cap at %d, got %d", d.ringCap, len(events))
	}
	// 应保留最近的 200 条（最后一条是 EventBehaviorFinished）。
	if events[len(events)-1].Type != string(EventBehaviorFinished) {
		t.Fatal("last event should be behavior.finished")
	}
}

func TestDebugger_RecordIntent(t *testing.T) {
	d := NewDebugger()
	d.RecordIntent(IntentJump)
	d.RecordIntent(IntentJump)
	d.RecordIntent(IntentWave)
	snap := d.Snapshot()
	intents := snap["intent_counts"].(map[string]int64)
	if intents["jump"] != 2 || intents["wave"] != 1 {
		t.Fatalf("unexpected intent counts: %v", intents)
	}
}
