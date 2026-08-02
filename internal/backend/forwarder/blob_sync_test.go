package forwarder

import (
	"os"
	"path/filepath"
	"testing"

	"cursor/gen/agentv1"
)

func TestCheckpointBlobSyncPublishesCheckpointAfterAllWrites(t *testing.T) {
	service, stream := testCheckpointBlobService(t)
	projection, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "hello"),
		newAssistantTextEntry(1, "request-1", "hi", "", ""),
	}))
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}

	if err := service.queueCheckpointProjection(stream, projection, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	if len(events) != len(projection.Blobs) {
		t.Fatalf("events before ACK = %d, want %d blob writes", len(events), len(projection.Blobs))
	}
	for _, event := range events {
		if event.Message.GetKvServerMessage().GetSetBlobArgs() == nil {
			t.Fatalf("event before ACK = %#v, want set_blob_args", event.Message)
		}
	}

	for index, event := range events {
		requestID := event.Message.GetKvServerMessage().GetId()
		if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
			Id: requestID,
			Message: &agentv1.KvClientMessage_SetBlobResult{
				SetBlobResult: &agentv1.SetBlobResult{},
			},
		}); err != nil {
			t.Fatalf("handleCheckpointBlobResult(%d) error = %v", index, err)
		}
	}
	events, err = service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() after ACK error = %v", err)
	}
	if len(events) != len(projection.Blobs)+1 {
		t.Fatalf("events after ACK = %d, want %d", len(events), len(projection.Blobs)+1)
	}
	checkpoint := events[len(events)-1].Message.GetConversationCheckpointUpdate()
	if checkpoint == nil || len(checkpoint.GetTurns()) != 1 {
		t.Fatalf("last event checkpoint = %#v, want one Blob-backed turn", checkpoint)
	}
}

func TestCheckpointBlobSyncRejectDoesNotPublishDanglingCheckpoint(t *testing.T) {
	service, stream := testCheckpointBlobService(t)
	projection, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "hello"),
	}))
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if err := service.queueCheckpointProjection(stream, projection, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("blob write events = %d, err = %v", len(events), err)
	}
	requestID := events[0].Message.GetKvServerMessage().GetId()
	if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
		Id: requestID,
		Message: &agentv1.KvClientMessage_SetBlobResult{
			SetBlobResult: &agentv1.SetBlobResult{Error: &agentv1.Error{Message: "disk full"}},
		},
	}); err != nil {
		t.Fatalf("handleCheckpointBlobResult() error = %v", err)
	}
	events, err = service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() after rejection error = %v", err)
	}
	for _, event := range events {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			t.Fatal("rejected Blob write published a dangling checkpoint")
		}
	}
}

func TestTerminalCheckpointRejectionFailsInsteadOfCompleting(t *testing.T) {
	service, stream := testCheckpointBlobService(t)
	projection, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, stream.RequestID, "hello"),
	}))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	completion := &pendingTurnCompletion{RequestID: stream.RequestID}
	if err := service.queueCheckpointProjection(stream, projection, checkpointCompletionAction(completion)); err != nil {
		t.Fatalf("queue checkpoint: %v", err)
	}
	stream.mu.Lock()
	var requestID uint32
	for pendingID := range stream.PendingCheckpointBlobWrites {
		requestID = pendingID
		break
	}
	stream.mu.Unlock()
	if requestID == 0 {
		t.Fatal("test did not queue a Blob write")
	}
	if err := rejectCheckpointBlob(service, stream, requestID, "disk full"); err != nil {
		t.Fatalf("reject terminal checkpoint: %v", err)
	}
	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	var failed, completed bool
	for _, event := range events {
		if event.End && event.TerminalErrorCode == "checkpoint_sync_error" {
			failed = true
		}
		if event.Message.GetInteractionUpdate().GetTurnEnded() != nil {
			completed = true
		}
	}
	if !failed || completed {
		t.Fatalf("terminal checkpoint events failed=%v completed=%v", failed, completed)
	}
}

func TestCheckpointBlobSyncMergesRevisionsWithoutObsoleteFailure(t *testing.T) {
	service, stream := testCheckpointBlobService(t)
	first, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "hello"),
	}))
	if err != nil {
		t.Fatalf("first ProjectCheckpointProjection() error = %v", err)
	}
	latest, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "hello"),
		newAssistantTextEntry(1, "request-1", "latest answer", "", ""),
	}))
	if err != nil {
		t.Fatalf("latest ProjectCheckpointProjection() error = %v", err)
	}
	if err := service.queueCheckpointProjection(stream, first, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queue first projection: %v", err)
	}
	firstEvents, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("read first events: %v", err)
	}
	if err := service.queueCheckpointProjection(stream, latest, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queue latest projection: %v", err)
	}

	latestRequired := make(map[string]struct{}, len(latest.Blobs))
	for _, blob := range latest.Blobs {
		latestRequired[string(blob.ID)] = struct{}{}
	}
	var obsoleteRequestID uint32
	for _, event := range firstEvents {
		message := event.Message.GetKvServerMessage()
		if message == nil || message.GetSetBlobArgs() == nil {
			continue
		}
		if _, required := latestRequired[string(message.GetSetBlobArgs().GetBlobId())]; !required {
			obsoleteRequestID = message.GetId()
			break
		}
	}
	if obsoleteRequestID == 0 {
		t.Fatal("test did not find an obsolete first-revision Blob write")
	}
	if err := rejectCheckpointBlob(service, stream, obsoleteRequestID, "obsolete write rejected"); err != nil {
		t.Fatalf("reject obsolete Blob: %v", err)
	}
	if err := acknowledgePendingCheckpointBlobs(service, stream); err != nil {
		t.Fatalf("acknowledge latest Blob writes: %v", err)
	}

	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("read merged events: %v", err)
	}
	checkpoints := 0
	for _, event := range events {
		if checkpoint := event.Message.GetConversationCheckpointUpdate(); checkpoint != nil {
			checkpoints++
			if len(checkpoint.GetTurns()) != len(latest.State.GetTurns()) || string(checkpoint.GetTurns()[0]) != string(latest.State.GetTurns()[0]) {
				t.Fatalf("published checkpoint is not the latest revision: %#v", checkpoint)
			}
		}
	}
	if checkpoints != 1 {
		t.Fatalf("published checkpoints = %d, want exactly latest revision", checkpoints)
	}
}

func TestCheckpointBlobSyncCarriesCompletionIntoLatestRevision(t *testing.T) {
	service, stream := testCheckpointBlobService(t)
	first, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "hello"),
	}))
	if err != nil {
		t.Fatalf("first projection: %v", err)
	}
	latest, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "hello"),
		newAssistantTextEntry(1, "request-1", "done", "", ""),
	}))
	if err != nil {
		t.Fatalf("latest projection: %v", err)
	}
	completion := &pendingTurnCompletion{
		RequestID: stream.RequestID,
		Usage:     turnUsageSnapshot{InputTokens: 11, OutputTokens: 7},
	}
	if err := service.queueCheckpointProjection(stream, first, checkpointCompletionAction(completion)); err != nil {
		t.Fatalf("queue completion projection: %v", err)
	}
	if err := service.queueCheckpointProjection(stream, latest, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queue latest projection: %v", err)
	}
	if err := acknowledgePendingCheckpointBlobs(service, stream); err != nil {
		t.Fatalf("acknowledge latest Blob writes: %v", err)
	}

	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("read completion events: %v", err)
	}
	checkpointIndex, turnEndedIndex, endIndex := -1, -1, -1
	for index, event := range events {
		switch {
		case event.Message.GetConversationCheckpointUpdate() != nil:
			checkpointIndex = index
		case event.Message.GetInteractionUpdate().GetTurnEnded() != nil:
			turnEndedIndex = index
		case event.End:
			endIndex = index
		}
	}
	if checkpointIndex < 0 || turnEndedIndex <= checkpointIndex || endIndex <= turnEndedIndex {
		t.Fatalf("terminal order checkpoint=%d turn_ended=%d end=%d", checkpointIndex, turnEndedIndex, endIndex)
	}
}

func TestCheckpointBlobSyncTimeoutFailsStreamWithoutPublishingDanglingCheckpoint(t *testing.T) {
	service, stream := testCheckpointBlobService(t)
	projection, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "hello"),
	}))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if err := service.queueCheckpointProjection(stream, projection, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queue checkpoint: %v", err)
	}
	if err := service.handleCheckpointBlobTimeout(stream); err != nil {
		t.Fatalf("timeout checkpoint: %v", err)
	}
	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("read timeout events: %v", err)
	}
	var failed bool
	for _, event := range events {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			t.Fatal("timed-out Blob dependency published a dangling checkpoint")
		}
		if event.End && event.TerminalErrorCode == "checkpoint_sync_error" {
			failed = true
		}
	}
	if !failed {
		t.Fatal("timed-out checkpoint did not fail the stream explicitly")
	}
}

func TestCheckpointBlobSyncReusesConversationCacheAcrossRequests(t *testing.T) {
	service, firstStream := testCheckpointBlobService(t)
	projection, err := service.projector.ProjectCheckpointProjection(testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, "request-1", "hello"),
	}))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if err := service.queueCheckpointProjection(firstStream, projection, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queue first request: %v", err)
	}
	if err := acknowledgePendingCheckpointBlobs(service, firstStream); err != nil {
		t.Fatalf("acknowledge first request: %v", err)
	}

	secondStream, err := service.broker.OpenStream(
		"request-2", firstStream.ConversationID, 2, "default", "default",
		agentv1.AgentMode_AGENT_MODE_AGENT, "continue",
	)
	if err != nil {
		t.Fatalf("OpenStream() second request error = %v", err)
	}
	if err := service.queueCheckpointProjection(secondStream, projection, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queue second request: %v", err)
	}
	events, err := service.broker.ReadFromCursor(secondStream.RequestID, 0)
	if err != nil {
		t.Fatalf("read second request events: %v", err)
	}
	if len(events) != 1 || events[0].Message.GetConversationCheckpointUpdate() == nil {
		t.Fatalf("second request events = %#v, want cached immediate checkpoint", events)
	}
}

func TestCancellationReplacesUnconfirmedCheckpointBeforeEnding(t *testing.T) {
	service, stream := testCheckpointBlobService(t)
	conversation := testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, stream.RequestID, "hello"),
	})
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	projection, err := service.projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if err := service.queueCheckpointProjection(stream, projection, checkpointTerminalAction{kind: checkpointTerminalActionNone}); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	stream.mu.Lock()
	var staleRequestID uint32
	for requestID := range stream.PendingCheckpointBlobWrites {
		staleRequestID = requestID
		break
	}
	stream.mu.Unlock()
	if staleRequestID == 0 {
		t.Fatal("test did not queue an unconfirmed Blob write")
	}

	if err := service.handleCancelIntent(InboundIntent{
		Kind:         "cancel",
		RequestID:    stream.RequestID,
		CancelReason: "user stopped",
	}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}
	stream.mu.Lock()
	phase := stream.Phase
	status := stream.Status
	pendingCheckpoint := stream.PendingCheckpoint
	pendingWrites := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	if phase != TurnPhaseCheckpointing || status != StreamStatusCreated {
		t.Fatalf("before checkpoint ACK phase=%s status=%s, want checkpointing/created", phase, status)
	}
	if pendingCheckpoint == nil || pendingWrites == 0 {
		t.Fatalf("before checkpoint ACK pending_checkpoint=%v pending_writes=%d", pendingCheckpoint != nil, pendingWrites)
	}
	if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
		Id: staleRequestID,
		Message: &agentv1.KvClientMessage_SetBlobResult{
			SetBlobResult: &agentv1.SetBlobResult{},
		},
	}); err != nil {
		t.Fatalf("stale Blob ACK error = %v", err)
	}
	if err := acknowledgePendingCheckpointBlobs(service, stream); err != nil {
		t.Fatalf("acknowledge cancellation checkpoint: %v", err)
	}
	stream.mu.Lock()
	phase = stream.Phase
	status = stream.Status
	stream.mu.Unlock()
	if phase != TurnPhaseCanceled || status != StreamStatusCanceled {
		t.Fatalf("after checkpoint ACK phase=%s status=%s, want canceled", phase, status)
	}
	assertCanceledEndEvent(t, service, stream)
}

func TestCancellationMetadataFailureStillEndsStream(t *testing.T) {
	service, stream := testCheckpointBlobService(t)
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingPath, []byte("block child creation"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	service.store = NewConversationFileStore(blockingPath)
	if err := service.replaceCheckpointConversation(stream, testConversation([]HistoryEntry{
		testUserMessageEntry(t, 1, stream.RequestID, "hello"),
	})); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}

	if err := service.handleCancelIntent(InboundIntent{Kind: "cancel", RequestID: stream.RequestID}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}
	if err := acknowledgePendingCheckpointBlobs(service, stream); err != nil {
		t.Fatalf("acknowledge cancellation checkpoint: %v", err)
	}
	assertCanceledEndEvent(t, service, stream)
}

func TestCheckpointTerminalActionMergePriority(t *testing.T) {
	complete := checkpointCompletionAction(&pendingTurnCompletion{RequestID: "complete"})
	cancel := checkpointCancellationAction("user canceled")
	none := checkpointTerminalAction{kind: checkpointTerminalActionNone}

	tests := []struct {
		name     string
		current  checkpointTerminalAction
		incoming checkpointTerminalAction
		wantKind checkpointTerminalActionKind
		wantID   string
	}{
		{name: "none then complete", current: none, incoming: complete, wantKind: checkpointTerminalActionComplete, wantID: "complete"},
		{name: "complete then none", current: complete, incoming: none, wantKind: checkpointTerminalActionComplete, wantID: "complete"},
		{name: "complete then cancel", current: complete, incoming: cancel, wantKind: checkpointTerminalActionCancel},
		{name: "cancel then complete", current: cancel, incoming: complete, wantKind: checkpointTerminalActionCancel},
		{name: "cancel then none", current: cancel, incoming: none, wantKind: checkpointTerminalActionCancel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			merged := mergeCheckpointTerminalAction(test.current, test.incoming)
			if merged.kind != test.wantKind {
				t.Fatalf("merged kind = %d, want %d", merged.kind, test.wantKind)
			}
			if test.wantID != "" {
				completion := merged.completionValue()
				if completion == nil || completion.RequestID != test.wantID {
					t.Fatalf("merged completion = %#v, want request_id=%s", completion, test.wantID)
				}
			}
		})
	}
}

func TestCheckpointTerminalActionIsMutuallyExclusive(t *testing.T) {
	completion := &pendingTurnCompletion{RequestID: "request-1"}
	action := checkpointCompletionAction(completion)
	if action.kind != checkpointTerminalActionComplete || action.completionValue() == nil {
		t.Fatalf("completion action = %#v", action)
	}
	empty := checkpointCompletionAction(nil)
	if empty.kind != checkpointTerminalActionNone || empty.completionValue() != nil {
		t.Fatalf("empty action = %#v", empty)
	}
}

func assertCanceledEndEvent(t *testing.T, service *Service, stream *ActiveStream) {
	t.Helper()
	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	for _, event := range events {
		if event.End && event.TerminalErrorCode == "canceled" {
			return
		}
	}
	t.Fatal("cancellation did not publish canceled end event")
}

func acknowledgePendingCheckpointBlobs(service *Service, stream *ActiveStream) error {
	for {
		stream.mu.Lock()
		requestIDs := make([]uint32, 0, len(stream.PendingCheckpointBlobWrites))
		for requestID := range stream.PendingCheckpointBlobWrites {
			requestIDs = append(requestIDs, requestID)
		}
		stream.mu.Unlock()
		if len(requestIDs) == 0 {
			return nil
		}
		for _, requestID := range requestIDs {
			if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
				Id: requestID,
				Message: &agentv1.KvClientMessage_SetBlobResult{
					SetBlobResult: &agentv1.SetBlobResult{},
				},
			}); err != nil {
				return err
			}
		}
	}
}

func rejectCheckpointBlob(service *Service, stream *ActiveStream, requestID uint32, message string) error {
	return service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
		Id: requestID,
		Message: &agentv1.KvClientMessage_SetBlobResult{
			SetBlobResult: &agentv1.SetBlobResult{Error: &agentv1.Error{Message: message}},
		},
	})
}

func TestImportedTurnIDsRemainCheckpointPrefix(t *testing.T) {
	importedID := make([]byte, 32)
	for index := range importedID {
		importedID[index] = byte(index + 1)
	}
	conversation := testConversation([]HistoryEntry{
		testUserMessageEntry(t, 2, "request-2", "continued question"),
	})
	conversation.ImportedTurnIDs = [][]byte{importedID}
	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.State.GetTurns()) != 2 {
		t.Fatalf("turns = %d, want imported prefix plus projected turn", len(projection.State.GetTurns()))
	}
	if string(projection.State.GetTurns()[0]) != string(importedID) {
		t.Fatal("imported turn ID was not preserved as the checkpoint prefix")
	}
}

func testCheckpointBlobService(t *testing.T) (*Service, *ActiveStream) {
	t.Helper()
	broker := NewStreamBroker()
	service := &Service{
		projector:       NewHistoryProjector(),
		broker:          broker,
		checkpointBlobs: make(map[string]*checkpointBlobCacheEntry),
	}
	stream, err := broker.OpenStream(
		"request-1",
		"conversation-1",
		1,
		"default",
		"default",
		agentv1.AgentMode_AGENT_MODE_AGENT,
		"hello",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	return service, stream
}
