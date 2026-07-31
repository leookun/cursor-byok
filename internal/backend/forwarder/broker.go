// broker.go 负责 request 维度活动流的订阅、广播、取消和终态收口。
package forwarder

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

const subscriberSignalBufferSize = 1
const orphanSubscriberGracePeriod = 30 * time.Second
const terminalStreamRetentionPeriod = 30 * time.Second

type StreamBroker struct {
	mu               sync.RWMutex
	streams          map[string]*ActiveStream
	nextSubscriberID atomic.Uint64
	nextStreamID     atomic.Uint64
}

// NewStreamBroker 创建活动流注册表。
func NewStreamBroker() *StreamBroker {
	return &StreamBroker{
		streams: make(map[string]*ActiveStream),
	}
}

// OpenStream 打开或复用指定 request 的活动流，并刷新其最新上下文。
func (broker *StreamBroker) OpenStream(requestID string, conversationID string, turnSeq int64, modelID string, modelName string, mode agentv1.AgentMode, latestUserText string) (*ActiveStream, error) {
	normalizedRequestID := strings.TrimSpace(requestID)
	if normalizedRequestID == "" {
		return nil, nil
	}
	normalizedMode, err := validateSupportedActiveMode(mode)
	if err != nil {
		return nil, err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if existing, ok := broker.streams[normalizedRequestID]; ok {
		existing.mu.Lock()
		if isTerminalStreamStatus(existing.Status) {
			broker.stopTerminalCleanupTimerLocked(existing)
			existing.mu.Unlock()
			delete(broker.streams, normalizedRequestID)
		} else {
			existing.ConversationID = strings.TrimSpace(conversationID)
			existing.TurnSeq = turnSeq
			existing.ModelID = strings.TrimSpace(modelID)
			existing.ModelName = strings.TrimSpace(modelName)
			existing.Mode = normalizedMode
			existing.LatestUserText = strings.TrimSpace(latestUserText)
			if existing.Status == "" {
				existing.Status = StreamStatusCreated
			}
			if existing.PendingExecs == nil {
				existing.PendingExecs = make(map[string]runtimecore.PendingExec)
			}
			if existing.PendingInteractions == nil {
				existing.PendingInteractions = make(map[string]runtimecore.PendingInteraction)
			}
			if existing.RecentCompletedInteractions == nil {
				existing.RecentCompletedInteractions = make(map[uint32]time.Time)
			}
			if existing.PartialToolCallIDs == nil {
				existing.PartialToolCallIDs = make(map[string]struct{})
			}
			if existing.PatchEditQueues == nil {
				existing.PatchEditQueues = make(map[string][]queuedPatchEditOperation)
			}
			if existing.BackgroundShells == nil {
				existing.BackgroundShells = make(map[string]*BackgroundShellState)
			}
			if existing.BackgroundShellsByMessageID == nil {
				existing.BackgroundShellsByMessageID = make(map[uint32]string)
			}
			if existing.BackgroundShellsByExecID == nil {
				existing.BackgroundShellsByExecID = make(map[string]string)
			}
			if existing.BackgroundShellActions == nil {
				existing.BackgroundShellActions = make(map[string]time.Time)
			}
			if existing.PendingKVAcks == nil {
				existing.PendingKVAcks = make(map[uint32]*pendingKVAck)
			}
			if existing.PendingTerminalDeliveries == nil {
				existing.PendingTerminalDeliveries = make(map[string]uint64)
			}
			existing.UpdatedAt = time.Now().UTC()
			existing.mu.Unlock()
			return existing, nil
		}
	}
	now := time.Now().UTC()
	stream := &ActiveStream{
		InstanceID:                  broker.nextStreamID.Add(1),
		RequestID:                   normalizedRequestID,
		ConversationID:              strings.TrimSpace(conversationID),
		TurnSeq:                     turnSeq,
		ModelID:                     strings.TrimSpace(modelID),
		ModelName:                   strings.TrimSpace(modelName),
		Mode:                        normalizedMode,
		LatestUserText:              strings.TrimSpace(latestUserText),
		Status:                      StreamStatusCreated,
		Backlog:                     make([]StreamEvent, 0, 64),
		Subscribers:                 make(map[string]*StreamSubscriber),
		PendingExecs:                make(map[string]runtimecore.PendingExec),
		PendingInteractions:         make(map[string]runtimecore.PendingInteraction),
		PartialToolCallIDs:          make(map[string]struct{}),
		PatchEditQueues:             make(map[string][]queuedPatchEditOperation),
		MCPToolServers:              make(map[string]string),
		RecentCompletedExecs:        make(map[uint32]time.Time),
		RecentCompletedInteractions: make(map[uint32]time.Time),
		BackgroundShells:            make(map[string]*BackgroundShellState),
		BackgroundShellsByMessageID: make(map[uint32]string),
		BackgroundShellsByExecID:    make(map[string]string),
		BackgroundShellActions:      make(map[string]time.Time),
		PendingKVAcks:               make(map[uint32]*pendingKVAck),
		PendingTerminalDeliveries:   make(map[string]uint64),
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}
	broker.streams[normalizedRequestID] = stream
	return stream, nil
}

// Get 返回指定 request 对应的活动流句柄。
func (broker *StreamBroker) Get(requestID string) (*ActiveStream, bool) {
	if broker == nil {
		return nil, false
	}
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	stream, ok := broker.streams[strings.TrimSpace(requestID)]
	return stream, ok
}

// Subscribe 为指定 request 注册一个新订阅者，并返回对应流与 backlog 唤醒信号。
func (broker *StreamBroker) Subscribe(requestID string) (*ActiveStream, string, <-chan struct{}, error) {
	normalizedRequestID := strings.TrimSpace(requestID)
	if normalizedRequestID == "" {
		return nil, "", nil, fmt.Errorf("request_id is required")
	}
	for {
		broker.mu.Lock()
		stream := broker.streams[normalizedRequestID]
		if stream == nil {
			broker.mu.Unlock()
			// RunSSE 可能先于 BidiAppend 到达。此时先创建一个占位活动流，
			// 等待后续上行把真实 conversation/model/mode 信息补齐。
			if _, err := broker.OpenStream(normalizedRequestID, "", 0, "", "", agentv1.AgentMode_AGENT_MODE_AGENT, ""); err != nil {
				return nil, "", nil, err
			}
			continue
		}
		subscriberID := fmt.Sprintf("sub-%d", broker.nextSubscriberID.Add(1))
		subscriber := &StreamSubscriber{Signal: make(chan struct{}, subscriberSignalBufferSize)}
		stream.mu.Lock()
		broker.stopTerminalCleanupTimerLocked(stream)
		stream.Subscribers[subscriberID] = subscriber
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		broker.mu.Unlock()
		return stream, subscriberID, subscriber.Signal, nil
	}
}

func (broker *StreamBroker) stopTerminalCleanupTimerLocked(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.TerminalCleanupSeq.Add(1)
	if stream.TerminalCleanupTimer != nil {
		stream.TerminalCleanupTimer.Stop()
		stream.TerminalCleanupTimer = nil
	}
}

type streamUnsubscribeResult struct {
	Remaining int
	Terminal  bool
	Retain    bool
}

// Unsubscribe 移除指定流实例上的订阅者，并登记该连接的终态交付结果。
func (broker *StreamBroker) Unsubscribe(stream *ActiveStream, subscriberID string, terminalObserved bool, completionRegistered bool, terminalEpoch uint64) streamUnsubscribeResult {
	if stream == nil {
		return streamUnsubscribeResult{}
	}
	result := streamUnsubscribeResult{}
	stream.mu.Lock()
	if _, ok := stream.Subscribers[strings.TrimSpace(subscriberID)]; ok {
		delete(stream.Subscribers, strings.TrimSpace(subscriberID))
	}
	result.Remaining = len(stream.Subscribers)
	result.Terminal = isTerminalStreamStatus(stream.Status)
	if result.Terminal {
		if terminalObserved && terminalEpoch == stream.TerminalEpoch && completionRegistered {
			if stream.PendingTerminalDeliveries == nil {
				stream.PendingTerminalDeliveries = make(map[string]uint64)
			}
			stream.PendingTerminalDeliveries[strings.TrimSpace(subscriberID)] = terminalEpoch
		} else {
			stream.TerminalReplayRequired = true
		}
	}
	result.Retain = stream.TerminalReplayRequired || len(stream.PendingTerminalDeliveries) > 0
	stream.mu.Unlock()
	return result
}

func (broker *StreamBroker) OtherConversationRequestIDs(conversationID string, keepRequestID string) []string {
	normalizedConversationID := strings.TrimSpace(conversationID)
	normalizedKeepRequestID := strings.TrimSpace(keepRequestID)
	if normalizedConversationID == "" {
		return nil
	}
	type requestStream struct {
		requestID string
		stream    *ActiveStream
	}
	candidates := make([]requestStream, 0, 2)
	broker.mu.RLock()
	for requestID, stream := range broker.streams {
		if stream == nil || strings.TrimSpace(requestID) == normalizedKeepRequestID {
			continue
		}
		candidates = append(candidates, requestStream{
			requestID: requestID,
			stream:    stream,
		})
	}
	broker.mu.RUnlock()
	requestIDs := make([]string, 0, 2)
	for _, candidate := range candidates {
		stream := candidate.stream
		stream.mu.Lock()
		sameConversation := strings.TrimSpace(stream.ConversationID) == normalizedConversationID
		status := stream.Status
		phase := stream.Phase
		stream.mu.Unlock()
		terminalPhase := phase == TurnPhaseCanceled || phase == TurnPhaseCompleted || phase == TurnPhaseFailed
		if !sameConversation || isTerminalStreamStatus(status) || terminalPhase {
			continue
		}
		requestIDs = append(requestIDs, candidate.requestID)
	}
	return requestIDs
}

func (broker *StreamBroker) scheduleTerminalCleanup(requestID string) bool {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return false
	}
	return broker.scheduleTerminalCleanupForStream(requestID, stream.InstanceID)
}

func (broker *StreamBroker) scheduleTerminalCleanupForStream(requestID string, instanceID uint64) bool {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil || stream.InstanceID != instanceID {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if len(stream.Subscribers) > 0 {
		broker.stopTerminalCleanupTimerLocked(stream)
		return false
	}
	if stream.Status != StreamStatusCompleted && stream.Status != StreamStatusCanceled && stream.Status != StreamStatusFailed {
		broker.stopTerminalCleanupTimerLocked(stream)
		return false
	}
	sequence := stream.TerminalCleanupSeq.Add(1)
	if stream.TerminalCleanupTimer != nil {
		stream.TerminalCleanupTimer.Stop()
	}
	stream.TerminalCleanupTimer = time.AfterFunc(terminalStreamRetentionPeriod, func() {
		broker.runScheduledTerminalCleanup(requestID, instanceID, sequence)
	})
	stream.UpdatedAt = time.Now().UTC()
	return true
}

func (broker *StreamBroker) runScheduledTerminalCleanup(requestID string, instanceID uint64, sequence uint64) {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil || stream.InstanceID != instanceID {
		return
	}
	stream.mu.Lock()
	if stream.TerminalCleanupSeq.Load() != sequence {
		stream.mu.Unlock()
		return
	}
	stream.TerminalCleanupTimer = nil
	if len(stream.Subscribers) > 0 {
		stream.mu.Unlock()
		return
	}
	if stream.Status != StreamStatusCompleted && stream.Status != StreamStatusCanceled && stream.Status != StreamStatusFailed {
		stream.mu.Unlock()
		return
	}
	stream.mu.Unlock()
	broker.removeIfIdleForStream(requestID, instanceID)
}

// RemoveIfIdle 在没有订阅者时移除终态流，或移除仍为空壳的占位流。
func (broker *StreamBroker) RemoveIfIdle(requestID string) bool {
	return broker.removeIfIdleForStream(requestID, 0)
}

func (broker *StreamBroker) removeIfIdleForStream(requestID string, instanceID uint64) bool {
	normalizedRequestID := strings.TrimSpace(requestID)
	if normalizedRequestID == "" {
		return false
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	stream, ok := broker.streams[normalizedRequestID]
	if !ok || stream == nil {
		return false
	}
	if instanceID != 0 && stream.InstanceID != instanceID {
		return false
	}
	stream.mu.Lock()
	subscriberCount := len(stream.Subscribers)
	isActive := stream.ProviderActive
	hasBacklog := len(stream.Backlog) > 0
	hasConversation := strings.TrimSpace(stream.ConversationID) != ""
	status := stream.Status
	if status == StreamStatusCompleted || status == StreamStatusCanceled || status == StreamStatusFailed {
		broker.stopTerminalCleanupTimerLocked(stream)
	}
	stream.mu.Unlock()
	if subscriberCount > 0 {
		return false
	}
	if status == StreamStatusCompleted || status == StreamStatusCanceled || status == StreamStatusFailed {
		delete(broker.streams, normalizedRequestID)
		return true
	}
	if isActive || hasBacklog || hasConversation {
		return false
	}
	delete(broker.streams, normalizedRequestID)
	return true
}

// Publish 把一个事件写入 backlog，并唤醒当前所有订阅者读取 backlog。
func (broker *StreamBroker) Publish(requestID string, event StreamEvent) error {
	return broker.publishToStream(requestID, 0, event)
}

func (broker *StreamBroker) publishToStream(requestID string, instanceID uint64, event StreamEvent) error {
	normalizedRequestID := strings.TrimSpace(requestID)
	broker.mu.RLock()
	stream, ok := broker.streams[normalizedRequestID]
	if !ok || stream == nil || (instanceID != 0 && stream.InstanceID != instanceID) {
		broker.mu.RUnlock()
		return fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	stream.mu.Lock()
	if !event.End && isTerminalStreamStatus(stream.Status) {
		stream.mu.Unlock()
		broker.mu.RUnlock()
		return nil
	}
	stream.Backlog = append(stream.Backlog, event)
	stream.UpdatedAt = time.Now().UTC()
	subscribers := make([]*StreamSubscriber, 0, len(stream.Subscribers))
	for _, subscriber := range stream.Subscribers {
		subscribers = append(subscribers, subscriber)
	}
	stream.mu.Unlock()
	broker.mu.RUnlock()

	for _, subscriber := range subscribers {
		if subscriber == nil {
			continue
		}
		select {
		case subscriber.Signal <- struct{}{}:
		default:
		}
	}
	return nil
}

// ReadFromCursor 返回从 cursor 开始尚未消费的 backlog 事件副本。
func (broker *StreamBroker) ReadFromCursor(requestID string, cursor int) ([]StreamEvent, error) {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return nil, fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	return broker.ReadFromStreamCursor(stream, cursor)
}

// ReadFromStreamCursor reads one immutable stream instance even when the same
// request ID has already started a later lifecycle.
func (broker *StreamBroker) ReadFromStreamCursor(stream *ActiveStream, cursor int) ([]StreamEvent, error) {
	if stream == nil {
		return nil, fmt.Errorf("request stream is not available")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(stream.Backlog) {
		return nil, nil
	}
	return append([]StreamEvent(nil), stream.Backlog[cursor:]...), nil
}

// CompleteTerminalDelivery resolves one subscriber's Connect endstream write.
func (broker *StreamBroker) CompleteTerminalDelivery(requestID string, instanceID uint64, subscriberID string, terminalEpoch uint64, delivered bool) {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil || stream.InstanceID != instanceID {
		return
	}
	stream.mu.Lock()
	pendingEpoch, pending := stream.PendingTerminalDeliveries[strings.TrimSpace(subscriberID)]
	if !pending || pendingEpoch != terminalEpoch || stream.TerminalEpoch != terminalEpoch {
		stream.mu.Unlock()
		return
	}
	delete(stream.PendingTerminalDeliveries, strings.TrimSpace(subscriberID))
	if !delivered {
		stream.TerminalReplayRequired = true
	}
	subscriberCount := len(stream.Subscribers)
	pendingCount := len(stream.PendingTerminalDeliveries)
	replayRequired := stream.TerminalReplayRequired
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if subscriberCount != 0 || pendingCount != 0 {
		return
	}
	if replayRequired {
		broker.scheduleTerminalCleanupForStream(requestID, instanceID)
	} else {
		broker.removeIfIdleForStream(requestID, instanceID)
	}
}

// Complete 把活动流标记为成功完成，并发布一个成功 endstream 事件。
func (broker *StreamBroker) Complete(requestID string, terminalCode string, terminalMessage string) error {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	return broker.CompleteStream(stream, terminalCode, terminalMessage)
}

func (broker *StreamBroker) CompleteStream(stream *ActiveStream, terminalCode string, terminalMessage string) error {
	return broker.finishStream(stream, StreamStatusCompleted, StreamEvent{
		End:                  true,
		TerminalErrorCode:    strings.TrimSpace(terminalCode),
		TerminalErrorMessage: strings.TrimSpace(terminalMessage),
	}, nil, false)
}

// Fail 把活动流标记为失败，并发布一个失败 endstream 事件。
func (broker *StreamBroker) Fail(requestID string, terminalCode string, terminalMessage string) error {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	return broker.FailStream(stream, terminalCode, terminalMessage)
}

func (broker *StreamBroker) FailStream(stream *ActiveStream, terminalCode string, terminalMessage string) error {
	cause := fmt.Errorf("stream failed: %s", firstNonEmpty(strings.TrimSpace(terminalMessage), strings.TrimSpace(terminalCode), "unknown"))
	return broker.finishStream(stream, StreamStatusFailed, StreamEvent{
		End:                  true,
		TerminalErrorCode:    strings.TrimSpace(terminalCode),
		TerminalErrorMessage: strings.TrimSpace(terminalMessage),
	}, cause, false)
}

// Cancel 主动取消活动流，并发布 canceled endstream。
func (broker *StreamBroker) Cancel(requestID string, terminalMessage string) error {
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", strings.TrimSpace(requestID))
	}
	return broker.CancelStream(stream, terminalMessage)
}

func (broker *StreamBroker) CancelStream(stream *ActiveStream, terminalMessage string) error {
	message := firstNonEmpty(strings.TrimSpace(terminalMessage), "[canceled] User aborted request")
	return broker.finishStream(stream, StreamStatusCanceled, StreamEvent{
		End:                  true,
		TerminalErrorCode:    "canceled",
		TerminalErrorMessage: message,
	}, fmt.Errorf("stream canceled: %s", message), true)
}

func (broker *StreamBroker) finishStream(stream *ActiveStream, status StreamStatus, event StreamEvent, waiterCause error, cancelProvider bool) error {
	if stream == nil {
		return fmt.Errorf("request stream is not available")
	}
	stream.mu.Lock()
	if isTerminalStreamStatus(stream.Status) {
		stream.mu.Unlock()
		return nil
	}
	broker.stopTerminalCleanupTimerLocked(stream)
	if cancelProvider && stream.ProviderCancel != nil {
		stream.ProviderCancel()
		stream.ProviderCancel = nil
	}
	if cancelProvider {
		stream.ProviderActive = false
	}
	stream.Status = status
	stream.TerminalEpoch++
	if stream.TerminalEpoch == 0 {
		stream.TerminalEpoch++
	}
	event.End = true
	event.TerminalEpoch = stream.TerminalEpoch
	stream.PendingTerminalDeliveries = make(map[string]uint64)
	stream.TerminalReplayRequired = false
	if waiterCause != nil {
		cancelStreamWaitersLocked(stream, waiterCause)
	}
	stream.Backlog = append(stream.Backlog, event)
	subscriberCount := len(stream.Subscribers)
	subscribers := make([]*StreamSubscriber, 0, subscriberCount)
	for _, subscriber := range stream.Subscribers {
		subscribers = append(subscribers, subscriber)
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	for _, subscriber := range subscribers {
		if subscriber == nil {
			continue
		}
		select {
		case subscriber.Signal <- struct{}{}:
		default:
		}
	}
	if subscriberCount == 0 {
		broker.scheduleTerminalCleanupForStream(stream.RequestID, stream.InstanceID)
	}
	return nil
}

// cancelStreamWaitersLocked releases setup and KV barriers after terminal state.
// The caller must hold stream.mu.
func cancelStreamWaitersLocked(stream *ActiveStream, cause error) {
	if stream == nil {
		return
	}
	if stream.RunSetupActive {
		stream.RunSetupCancelRequested = true
	}
	if stream.RunSetupCancel != nil {
		stream.RunSetupCancel()
	}
	for id, pending := range stream.PendingKVAcks {
		delete(stream.PendingKVAcks, id)
		pending.complete(cause)
	}
}

// firstNonEmpty 返回第一个非空白字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
