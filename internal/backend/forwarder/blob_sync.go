package forwarder

import (
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

const (
	checkpointBlobWriteTimeout          = 10 * time.Second
	checkpointBlobCacheIdleTTL          = 6 * time.Hour
	checkpointBlobCacheMaxConversations = 256
)

type checkpointBlobCacheEntry struct {
	Confirmed  map[string]struct{}
	LastAccess time.Time
}

func checkpointBlobKey(id []byte) string {
	return string(id)
}

func checkpointBlobHex(key string) string {
	return hex.EncodeToString([]byte(key))
}

func (service *Service) confirmedCheckpointBlob(conversationID string, key string) bool {
	if service == nil || key == "" {
		return false
	}
	service.checkpointBlobMu.Lock()
	defer service.checkpointBlobMu.Unlock()
	conversationID = strings.TrimSpace(conversationID)
	entry := service.checkpointBlobs[conversationID]
	if entry == nil {
		return false
	}
	entry.LastAccess = time.Now().UTC()
	_, ok := entry.Confirmed[key]
	return ok
}

func (service *Service) confirmCheckpointBlob(conversationID string, key string) {
	if service == nil || key == "" {
		return
	}
	service.checkpointBlobMu.Lock()
	defer service.checkpointBlobMu.Unlock()
	if service.checkpointBlobs == nil {
		service.checkpointBlobs = make(map[string]*checkpointBlobCacheEntry)
	}
	conversationID = strings.TrimSpace(conversationID)
	now := time.Now().UTC()
	entry := service.checkpointBlobs[conversationID]
	if entry == nil {
		entry = &checkpointBlobCacheEntry{Confirmed: make(map[string]struct{})}
		service.checkpointBlobs[conversationID] = entry
	}
	entry.Confirmed[key] = struct{}{}
	entry.LastAccess = now
	service.pruneCheckpointBlobCacheLocked(now)
}

func (service *Service) pruneCheckpointBlobCacheLocked(now time.Time) {
	if service == nil || len(service.checkpointBlobs) == 0 {
		return
	}
	cutoff := now.Add(-checkpointBlobCacheIdleTTL)
	for conversationID, entry := range service.checkpointBlobs {
		if entry == nil || entry.LastAccess.Before(cutoff) {
			delete(service.checkpointBlobs, conversationID)
		}
	}
	for len(service.checkpointBlobs) > checkpointBlobCacheMaxConversations {
		oldestConversationID := ""
		oldestAccess := now
		for conversationID, entry := range service.checkpointBlobs {
			if entry == nil || oldestConversationID == "" || entry.LastAccess.Before(oldestAccess) {
				oldestConversationID = conversationID
				if entry != nil {
					oldestAccess = entry.LastAccess
				}
			}
		}
		if oldestConversationID == "" {
			return
		}
		delete(service.checkpointBlobs, oldestConversationID)
	}
}

func checkpointCompletionAction(completion *pendingTurnCompletion) checkpointTerminalAction {
	if completion == nil {
		return checkpointTerminalAction{kind: checkpointTerminalActionNone}
	}
	return checkpointTerminalAction{
		kind:       checkpointTerminalActionComplete,
		completion: *clonePendingTurnCompletion(completion),
	}
}

func checkpointCancellationAction(message string) checkpointTerminalAction {
	return checkpointTerminalAction{
		kind:          checkpointTerminalActionCancel,
		cancelMessage: firstNonEmpty(strings.TrimSpace(message), "[canceled] User aborted request"),
	}
}

func mergeCheckpointTerminalAction(current checkpointTerminalAction, incoming checkpointTerminalAction) checkpointTerminalAction {
	switch {
	case incoming.kind == checkpointTerminalActionCancel:
		return incoming
	case current.kind == checkpointTerminalActionCancel:
		return current
	case incoming.kind == checkpointTerminalActionComplete:
		return incoming
	default:
		return current
	}
}

func (action checkpointTerminalAction) completionValue() *pendingTurnCompletion {
	if action.kind != checkpointTerminalActionComplete {
		return nil
	}
	return clonePendingTurnCompletion(&action.completion)
}

func (service *Service) queueCheckpointProjection(stream *ActiveStream, projection *CheckpointProjection, terminalAction checkpointTerminalAction) error {
	if service == nil || stream == nil || projection == nil || projection.State == nil {
		return nil
	}
	state, ok := proto.Clone(projection.State).(*agentv1.ConversationStateStructure)
	if !ok || state == nil {
		return fmt.Errorf("clone checkpoint state")
	}

	stream.mu.Lock()
	if stream.PendingCheckpointBlobWrites == nil {
		stream.PendingCheckpointBlobWrites = make(map[uint32]pendingCheckpointBlobWrite)
	}
	if stream.PendingCheckpointBlobRequests == nil {
		stream.PendingCheckpointBlobRequests = make(map[string]uint32)
	}
	stream.NextCheckpointRevision++
	if stream.NextCheckpointRevision == 0 {
		stream.NextCheckpointRevision++
	}
	revision := stream.NextCheckpointRevision
	if stream.PendingCheckpoint != nil {
		terminalAction = mergeCheckpointTerminalAction(stream.PendingCheckpoint.TerminalAction, terminalAction)
	}
	required := make(map[string]struct{}, len(projection.Blobs))
	toWrite := make([]struct {
		requestID uint32
		blob      CheckpointBlob
	}, 0, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		key := checkpointBlobKey(blob.ID)
		if key == "" {
			continue
		}
		required[key] = struct{}{}
		if service.confirmedCheckpointBlob(stream.ConversationID, key) {
			continue
		}
		if _, pending := stream.PendingCheckpointBlobRequests[key]; pending {
			continue
		}
		stream.NextCheckpointBlobRequestID++
		if stream.NextCheckpointBlobRequestID == 0 {
			stream.NextCheckpointBlobRequestID++
		}
		requestID := stream.NextCheckpointBlobRequestID
		stream.PendingCheckpointBlobWrites[requestID] = pendingCheckpointBlobWrite{
			Key:      key,
			Revision: revision,
		}
		stream.PendingCheckpointBlobRequests[key] = requestID
		toWrite = append(toWrite, struct {
			requestID uint32
			blob      CheckpointBlob
		}{requestID: requestID, blob: blob})
	}
	stream.PendingCheckpoint = &pendingCheckpointPublish{
		Revision:       revision,
		State:          state,
		Required:       required,
		TerminalAction: terminalAction,
	}
	if terminalAction.kind != checkpointTerminalActionNone {
		stream.Phase = TurnPhaseCheckpointing
	}
	for requestID, write := range stream.PendingCheckpointBlobWrites {
		if _, stillRequired := required[write.Key]; stillRequired {
			continue
		}
		delete(stream.PendingCheckpointBlobWrites, requestID)
		delete(stream.PendingCheckpointBlobRequests, write.Key)
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	for _, item := range toWrite {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildSetCheckpointBlobMessage(item.requestID, item.blob),
		}); err != nil {
			service.discardPendingCheckpoint(stream, fmt.Errorf("publish checkpoint blob write: %w", err))
			return err
		}
	}
	if len(toWrite) > 0 {
		service.scheduleStreamTimer(
			stream,
			providerTimerKey(streamTimerCheckpointBlobs, ""),
			checkpointBlobWriteTimeout,
			streamTimerCheckpointBlobs,
			"",
			0,
			"checkpoint blob write timeout",
		)
	}
	return service.publishReadyCheckpoint(stream)
}

func clonePendingTurnCompletion(completion *pendingTurnCompletion) *pendingTurnCompletion {
	if completion == nil {
		return nil
	}
	cloned := *completion
	return &cloned
}

func (service *Service) handleCheckpointBlobResult(stream *ActiveStream, message *agentv1.KvClientMessage) error {
	if service == nil || stream == nil || message == nil {
		return nil
	}
	result := message.GetSetBlobResult()
	if result == nil {
		return nil
	}
	stream.mu.Lock()
	write, ok := stream.PendingCheckpointBlobWrites[message.GetId()]
	pendingRequiresBlob := false
	if ok {
		delete(stream.PendingCheckpointBlobWrites, message.GetId())
		delete(stream.PendingCheckpointBlobRequests, write.Key)
		if stream.PendingCheckpoint != nil {
			_, pendingRequiresBlob = stream.PendingCheckpoint.Required[write.Key]
		}
	}
	conversationID := stream.ConversationID
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if !ok {
		return nil
	}
	if result.GetError() != nil {
		if !pendingRequiresBlob {
			return service.publishReadyCheckpoint(stream)
		}
		return service.abandonPendingCheckpoint(stream, fmt.Errorf(
			"write checkpoint blob %s: %s",
			checkpointBlobHex(write.Key),
			firstNonEmpty(result.GetError().GetMessage(), "client blob store rejected write"),
		))
	}
	service.confirmCheckpointBlob(conversationID, write.Key)
	return service.publishReadyCheckpoint(stream)
}

func (service *Service) publishReadyCheckpoint(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	if pending == nil {
		stream.mu.Unlock()
		return nil
	}
	for key := range pending.Required {
		if !service.confirmedCheckpointBlob(stream.ConversationID, key) {
			stream.mu.Unlock()
			return nil
		}
	}
	stream.PendingCheckpoint = nil
	state := pending.State
	terminalAction := pending.TerminalAction
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: buildCheckpointMessage(state)}); err != nil {
		return err
	}
	switch terminalAction.kind {
	case checkpointTerminalActionComplete:
		if completion := terminalAction.completionValue(); completion != nil {
			return service.finishSuccessfulTurnAfterCheckpoint(stream, *completion)
		}
	case checkpointTerminalActionCancel:
		return service.finishCanceledTurnAfterCheckpoint(stream, terminalAction.cancelMessage)
	}
	return nil
}

func (service *Service) discardPendingCheckpoint(stream *ActiveStream, cause error) {
	if service == nil || stream == nil {
		return
	}
	stream.mu.Lock()
	stream.PendingCheckpoint = nil
	stream.PendingCheckpointBlobWrites = make(map[uint32]pendingCheckpointBlobWrite)
	stream.PendingCheckpointBlobRequests = make(map[string]uint32)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if cause != nil {
		log.Printf("forwarder pending checkpoint discarded request_id=%s conversation_id=%s err=%v", stream.RequestID, stream.ConversationID, cause)
	}
}

func (service *Service) abandonPendingCheckpoint(stream *ActiveStream, cause error) error {
	if service == nil || stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	stream.PendingCheckpoint = nil
	stream.PendingCheckpointBlobWrites = make(map[uint32]pendingCheckpointBlobWrite)
	stream.PendingCheckpointBlobRequests = make(map[string]uint32)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if cause != nil {
		log.Printf("forwarder checkpoint blob sync abandoned request_id=%s conversation_id=%s err=%v", stream.RequestID, stream.ConversationID, cause)
	}
	if pending != nil {
		return service.failTerminalCheckpointSync(stream, cause)
	}
	return nil
}

func (service *Service) finishCanceledTurnAfterCheckpoint(stream *ActiveStream, message string) error {
	if stream == nil {
		return nil
	}
	service.setTurnPhase(stream, TurnPhaseCanceled)
	return service.broker.Cancel(stream.RequestID, firstNonEmpty(strings.TrimSpace(message), "[canceled] User aborted request"))
}

func (service *Service) failTerminalCheckpointSync(stream *ActiveStream, cause error) error {
	if stream == nil {
		return nil
	}
	message := "checkpoint synchronization failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = strings.TrimSpace(cause.Error())
	}
	service.setTurnPhase(stream, TurnPhaseFailed)
	return service.broker.Fail(stream.RequestID, "checkpoint_sync_error", message)
}

func (service *Service) handleCheckpointBlobTimeout(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	pendingCount := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	if pendingCount == 0 {
		return service.publishReadyCheckpoint(stream)
	}
	return service.abandonPendingCheckpoint(stream, fmt.Errorf("%d checkpoint blob writes timed out", pendingCount))
}
