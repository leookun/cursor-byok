package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

func TestStreamBrokerTrimsAcknowledgedBacklog(t *testing.T) {
	broker := NewStreamBrokerWithBacklogLimit(3)
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	subscriberID, _, err := broker.Subscribe("request")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	for index := 0; index < 10; index++ {
		if err := broker.Publish("request", StreamEvent{Message: &agentv1.AgentServerMessage{}}); err != nil {
			t.Fatalf("publish %d: %v", index, err)
		}
	}

	if err := broker.AcknowledgeCursor("request", subscriberID, 7); err != nil {
		t.Fatalf("acknowledge cursor: %v", err)
	}
	backlog, err := broker.ReadFromCursor("request", 7)
	if err != nil {
		t.Fatalf("read acknowledged cursor: %v", err)
	}
	if len(backlog) != 3 {
		t.Fatalf("backlog after acknowledgement = %d, want 3", len(backlog))
	}
	if len(stream.Backlog) != 3 {
		t.Fatalf("stored backlog = %d, want 3", len(stream.Backlog))
	}

	if err := broker.AcknowledgeCursor("request", subscriberID, 10); err != nil {
		t.Fatalf("acknowledge terminal cursor: %v", err)
	}
	backlog, err = broker.ReadFromCursor("request", 10)
	if err != nil {
		t.Fatalf("read terminal cursor: %v", err)
	}
	if len(backlog) != 0 {
		t.Fatalf("backlog after terminal acknowledgement = %d, want 0", len(backlog))
	}
}
