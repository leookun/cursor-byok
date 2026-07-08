// file_store.go 负责 conversation 的两份持久化事实：state.json 与 context.json。
package forwarder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cursor/gen/agentv1"
)

const (
	conversationStateFileName          = "state.json"
	conversationContextFileName        = "context.json"
	conversationSchemaVersion          = 1
	conversationLockStaleAfter         = 30 * time.Minute
	legacyConversationLockStaleAfter   = 30 * time.Second
	conversationLockAcquireTimeout     = 30 * time.Second
	staleConversationLockRemoveTimeout = 30 * time.Second
	conversationLockRetryInterval      = 10 * time.Millisecond
)

var (
	conversationLockProcessStartedAt = time.Now()
	conversationProcessLocksMu       sync.Mutex
	conversationProcessLocks         = make(map[string]*conversationProcessLock)
)

type conversationProcessLock struct {
	mu   sync.Mutex
	refs int
}

type ConversationFileStore struct {
	root string
}

type conversationContextFile struct {
	SchemaVersion  int            `json:"schema_version"`
	ConversationID string         `json:"conversation_id"`
	Version        int64          `json:"version"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Items          []HistoryEntry `json:"items"`
}

// NewConversationFileStore 创建 JSON history 文件存储。
func NewConversationFileStore(historyRoot string) *ConversationFileStore {
	return &ConversationFileStore{root: strings.TrimSpace(historyRoot)}
}

// HistoryDir 返回 history 根路径。
func (store *ConversationFileStore) HistoryDir() string {
	if store == nil {
		return ""
	}
	return store.root
}

// CreateConversation 确保指定会话对应的 state/context 文件存在并完成元数据初始化。
func (store *ConversationFileStore) CreateConversation(conversationID string, mode agentv1.AgentMode, parentConversationID string, parentToolCallID string, rootConversationID string) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	return store.mutateConversation(conversationID, true, func(conversation *ConversationFile) error {
		if strings.TrimSpace(conversation.ConversationID) != "" {
			if strings.TrimSpace(conversation.Mode) == "" {
				alias, err := modeAlias(mode)
				if err != nil {
					return err
				}
				conversation.Mode = alias
			}
			return nil
		}
		now := time.Now().UTC()
		normalizedConversationID := strings.TrimSpace(conversationID)
		if normalizedConversationID == "" {
			return fmt.Errorf("conversation_id is required")
		}
		conversation.SchemaVersion = conversationSchemaVersion
		conversation.ConversationID = normalizedConversationID
		conversation.RootConversationID = strings.TrimSpace(rootConversationID)
		if conversation.RootConversationID == "" {
			conversation.RootConversationID = normalizedConversationID
		}
		conversation.ParentConversationID = strings.TrimSpace(parentConversationID)
		conversation.ParentToolCallID = strings.TrimSpace(parentToolCallID)
		alias, err := modeAlias(mode)
		if err != nil {
			return err
		}
		conversation.Mode = alias
		conversation.CreatedAt = now
		conversation.UpdatedAt = now
		conversation.NextTurnSeq = 1
		conversation.NextEntrySeq = 1
		conversation.ContextVersion = 0
		conversation.CurrentLoopStatus = "idle"
		conversation.Entries = make([]HistoryEntry, 0, 16)
		return nil
	})
}

// LoadConversation 读取 state.json + context.json。
func (store *ConversationFileStore) LoadConversation(conversationID string) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	return store.mutateConversation(conversationID, false, nil)
}

// AppendEntries 把已经发生的语义事件追加到 context.json，并同步 state.json。
func (store *ConversationFileStore) AppendEntries(conversationID string, entries []HistoryEntry) (*ConversationFile, []HistoryEntry, error) {
	if store == nil {
		return nil, nil, fmt.Errorf("conversation file store is nil")
	}
	if len(entries) == 0 {
		conversation, err := store.LoadConversation(conversationID)
		return conversation, nil, err
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, nil, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, nil, err
	}
	if conversation == nil {
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, 16),
			CreatedAt:          time.Now().UTC(),
		}
		alias, err := modeAlias(agentv1.AgentMode_AGENT_MODE_AGENT)
		if err != nil {
			return nil, nil, err
		}
		conversation.Mode = alias
	}
	assigned := appendEntriesInPlace(conversation, entries)
	deriveConversationLoopState(conversation)
	if err := store.writeConversationLocked(normalizedConversationID, conversation); err != nil {
		return nil, nil, err
	}
	return cloneConversationFile(conversation), assigned, nil
}

func (store *ConversationFileStore) SaveConversationWithEntries(conversationID string, source *ConversationFile, entries []HistoryEntry) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, len(entries)),
			CreatedAt:          time.Now().UTC(),
		}
	}
	mergeConversationMetadata(conversation, source)
	appendEntriesInPlace(conversation, resetEntrySequences(entries))
	deriveConversationLoopState(conversation)
	if err := store.writeConversationLocked(normalizedConversationID, conversation); err != nil {
		return nil, err
	}
	return cloneConversationFile(conversation), nil
}

// UpdateConversationMeta 更新 state.json；context.json 保持不变。
func (store *ConversationFileStore) UpdateConversationMeta(conversationID string, update func(*ConversationFile) error) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, 16),
			CreatedAt:          time.Now().UTC(),
		}
	}
	if update != nil {
		if err := update(conversation); err != nil {
			return nil, err
		}
	}
	if err := store.writeConversationMetaLocked(normalizedConversationID, conversation); err != nil {
		return nil, err
	}
	return cloneConversationFile(conversation), nil
}

// ReplaceEntries 原子替换 context.json，并同步 state.json 中的 sequence/version 状态。
func (store *ConversationFileStore) ReplaceEntries(conversationID string, entries []HistoryEntry, update func(*ConversationFile) error) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, len(entries)),
			CreatedAt:          time.Now().UTC(),
		}
	}
	conversation.Entries = nil
	conversation.NextEntrySeq = 1
	conversation.NextTurnSeq = 1
	appendEntriesInPlace(conversation, resetEntrySequences(entries))
	if update != nil {
		if err := update(conversation); err != nil {
			return nil, err
		}
	}
	deriveConversationLoopState(conversation)
	if err := store.writeConversationLocked(normalizedConversationID, conversation); err != nil {
		return nil, err
	}
	return cloneConversationFile(conversation), nil
}

// GetConversationSummary 返回轻量会话摘要。
func (store *ConversationFileStore) GetConversationSummary(conversationID string) (ConversationSummary, error) {
	conversation, err := store.LoadConversation(conversationID)
	if err != nil || conversation == nil {
		return ConversationSummary{}, err
	}
	return ConversationSummary{
		ConversationID: conversation.ConversationID,
		Mode:           conversation.Mode,
		EntriesCount:   len(conversation.Entries),
		NextTurnSeq:    conversation.NextTurnSeq,
		NextEntrySeq:   conversation.NextEntrySeq,
		UpdatedAt:      conversation.UpdatedAt,
	}, nil
}

// ListConversationIDs 返回 history 根目录下包含 state/context 的 conversation id。
func (store *ConversationFileStore) ListConversationIDs() ([]string, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan history directory: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		conversationID := strings.TrimSpace(entry.Name())
		if conversationID == "" {
			continue
		}
		if ok, err := fileExists(store.statePath(conversationID)); err != nil {
			return nil, err
		} else if ok {
			ids = append(ids, conversationID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (store *ConversationFileStore) mutateConversation(conversationID string, createIfMissing bool, update func(*ConversationFile) error) (*ConversationFile, error) {
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		if !createIfMissing {
			return nil, nil
		}
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, 16),
			CreatedAt:          time.Now().UTC(),
		}
	}
	if update == nil {
		return cloneConversationFile(conversation), nil
	}
	if err := update(conversation); err != nil {
		return nil, err
	}
	deriveConversationLoopState(conversation)
	if err := store.writeConversationLocked(normalizedConversationID, conversation); err != nil {
		return nil, err
	}
	return cloneConversationFile(conversation), nil
}

func (store *ConversationFileStore) readConversationLocked(conversationID string) (*ConversationFile, error) {
	stateBody, err := os.ReadFile(store.statePath(conversationID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read conversation state: %w", err)
	}
	var conversation ConversationFile
	if err := json.Unmarshal(stateBody, &conversation); err != nil {
		return nil, fmt.Errorf("decode conversation state %q: %w", conversationID, err)
	}
	context, err := store.readContextLocked(conversationID)
	if err != nil {
		return nil, err
	}
	conversation.Entries = context
	normalizeLoadedConversation(conversationID, &conversation)
	return &conversation, nil
}

func (store *ConversationFileStore) readContextLocked(conversationID string) ([]HistoryEntry, error) {
	body, err := os.ReadFile(store.contextPath(conversationID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make([]HistoryEntry, 0, 16), nil
		}
		return nil, fmt.Errorf("read conversation context: %w", err)
	}
	var context conversationContextFile
	if err := json.Unmarshal(body, &context); err != nil {
		return nil, fmt.Errorf("decode conversation context %q: %w", conversationID, err)
	}
	return append([]HistoryEntry(nil), context.Items...), nil
}

func (store *ConversationFileStore) writeConversationLocked(conversationID string, conversation *ConversationFile) error {
	if conversation == nil {
		return fmt.Errorf("conversation is nil")
	}
	normalizeLoadedConversation(conversationID, conversation)
	if err := store.writeContextLocked(conversationID, conversation); err != nil {
		return err
	}
	return store.writeConversationMetaLocked(conversationID, conversation)
}

func (store *ConversationFileStore) writeConversationMetaLocked(conversationID string, conversation *ConversationFile) error {
	if conversation == nil {
		return fmt.Errorf("conversation is nil")
	}
	currentLoopID := conversation.CurrentLoopID
	currentLoopStatus := conversation.CurrentLoopStatus
	currentRequestID := conversation.CurrentRequestID
	currentTurnSeq := conversation.CurrentTurnSeq
	normalizeLoadedConversation(conversationID, conversation)
	if strings.TrimSpace(currentLoopStatus) != "" && (strings.TrimSpace(currentRequestID) == "" || conversationHasRequestEntry(conversation.Entries, currentRequestID, currentTurnSeq)) {
		conversation.CurrentLoopID = currentLoopID
		conversation.CurrentLoopStatus = currentLoopStatus
		conversation.CurrentRequestID = currentRequestID
		conversation.CurrentTurnSeq = currentTurnSeq
	}
	if err := refreshConversationRuntimeState(conversation); err != nil {
		return err
	}
	metadata := cloneConversationFile(conversation)
	metadata.SchemaVersion = conversationSchemaVersion
	metadata.ContextVersion = contextVersionForEntries(conversation.Entries)
	metadata.Entries = nil
	return writeJSONFileAtomic(store.statePath(conversationID), metadata)
}

func (store *ConversationFileStore) writeContextLocked(conversationID string, conversation *ConversationFile) error {
	context := conversationContextFile{
		SchemaVersion:  conversationSchemaVersion,
		ConversationID: strings.TrimSpace(conversationID),
		Version:        contextVersionForEntries(conversation.Entries),
		UpdatedAt:      time.Now().UTC(),
		Items:          append([]HistoryEntry(nil), conversation.Entries...),
	}
	return writeJSONFileAtomic(store.contextPath(conversationID), context)
}

func contextVersionForEntries(entries []HistoryEntry) int64 {
	var version int64
	for _, entry := range entries {
		if entry.Seq > version {
			version = entry.Seq
		}
	}
	return version
}

func deriveConversationLoopState(conversation *ConversationFile) {
	if conversation == nil {
		return
	}
	conversation.SchemaVersion = conversationSchemaVersion
	conversation.ContextVersion = contextVersionForEntries(conversation.Entries)
	fallbackStatus := firstNonEmpty(strings.TrimSpace(conversation.CurrentLoopStatus), "idle")
	requestID := strings.TrimSpace(conversation.CurrentRequestID)
	turnSeq := conversation.CurrentTurnSeq
	if requestID != "" && !conversationHasRequestEntry(conversation.Entries, requestID, turnSeq) {
		requestID = ""
		turnSeq = 0
		fallbackStatus = "idle"
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if strings.TrimSpace(entry.RequestID) == "" {
			continue
		}
		if requestID == "" {
			requestID = strings.TrimSpace(entry.RequestID)
			turnSeq = entry.TurnSeq
		}
		break
	}
	status := deriveRequestLoopStatus(conversation.Entries, requestID, turnSeq, fallbackStatus)
	conversation.CurrentRequestID = requestID
	conversation.CurrentTurnSeq = turnSeq
	if requestID != "" {
		conversation.CurrentLoopID = fmt.Sprintf("%d:%s", turnSeq, requestID)
	}
	conversation.CurrentLoopStatus = status
}

func conversationHasRequestEntry(entries []HistoryEntry, requestID string, turnSeq int64) bool {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.RequestID) != requestID {
			continue
		}
		if turnSeq > 0 && entry.TurnSeq > 0 && entry.TurnSeq != turnSeq {
			continue
		}
		return true
	}
	return false
}

func deriveRequestLoopStatus(entries []HistoryEntry, requestID string, turnSeq int64, fallbackStatus string) string {
	if strings.TrimSpace(requestID) == "" {
		return firstNonEmpty(strings.TrimSpace(fallbackStatus), "idle")
	}
	openToolCalls := make(map[string]struct{})
	terminalStatus := ""
	seenActivity := false
	for _, entry := range entries {
		if strings.TrimSpace(entry.RequestID) != strings.TrimSpace(requestID) {
			continue
		}
		if turnSeq > 0 && entry.TurnSeq > 0 && entry.TurnSeq != turnSeq {
			continue
		}
		switch strings.TrimSpace(entry.Kind) {
		case "tool_call":
			seenActivity = true
			toolCallID := historyEntryToolCallID(entry)
			if toolCallID == "" {
				toolCallID = fmt.Sprintf("entry:%d", entry.Seq)
			}
			openToolCalls[toolCallID] = struct{}{}
		case "tool_result", "assistant_text", "prompt_context", "request_context", "user_message":
			seenActivity = true
			if strings.TrimSpace(entry.Kind) == "tool_result" {
				if toolCallID := historyEntryToolCallID(entry); toolCallID != "" {
					delete(openToolCalls, toolCallID)
				}
			}
		case "metadata":
			var payload metadataPayload
			if err := json.Unmarshal(entry.Payload, &payload); err == nil {
				switch strings.TrimSpace(payload.Type) {
				case "turn_completed":
					terminalStatus = "completed"
				case "provider_error":
					terminalStatus = "provider_error"
				case "failed":
					terminalStatus = "failed"
				case "control":
					if strings.TrimSpace(readStringValue(payload.Value["status"])) == "canceled" {
						terminalStatus = "canceled"
					}
				case "run_request":
					seenActivity = true
				}
			}
		}
	}
	if terminalStatus != "" {
		return terminalStatus
	}
	if len(openToolCalls) > 0 {
		return "waiting_tool"
	}
	if seenActivity {
		return "running"
	}
	return firstNonEmpty(strings.TrimSpace(fallbackStatus), "idle")
}

func (store *ConversationFileStore) conversationDir(conversationID string) string {
	return filepath.Join(store.root, conversationID)
}

func (store *ConversationFileStore) statePath(conversationID string) string {
	return filepath.Join(store.conversationDir(conversationID), conversationStateFileName)
}

func (store *ConversationFileStore) contextPath(conversationID string) string {
	return filepath.Join(store.conversationDir(conversationID), conversationContextFileName)
}

func (store *ConversationFileStore) lockPath(conversationID string) string {
	return filepath.Join(store.conversationDir(conversationID), "conversation.lock")
}

func appendEntriesInPlace(conversation *ConversationFile, entries []HistoryEntry) []HistoryEntry {
	if conversation == nil || len(entries) == 0 {
		return nil
	}
	now := time.Now().UTC()
	assigned := make([]HistoryEntry, 0, len(entries))
	maxTurnSeq := conversation.NextTurnSeq - 1
	for _, entry := range entries {
		next := entry
		if next.CreatedAt.IsZero() {
			next.CreatedAt = now
		}
		if next.Seq <= 0 {
			next.Seq = conversation.NextEntrySeq
			conversation.NextEntrySeq++
		} else if next.Seq >= conversation.NextEntrySeq {
			conversation.NextEntrySeq = next.Seq + 1
		}
		if next.TurnSeq > maxTurnSeq {
			maxTurnSeq = next.TurnSeq
		}
		conversation.Entries = append(conversation.Entries, next)
		assigned = append(assigned, next)
	}
	if maxTurnSeq+1 > conversation.NextTurnSeq {
		conversation.NextTurnSeq = maxTurnSeq + 1
	}
	if conversation.CreatedAt.IsZero() {
		conversation.CreatedAt = now
	}
	conversation.UpdatedAt = now
	conversation.ContextVersion = contextVersionForEntries(conversation.Entries)
	return assigned
}

func mergeConversationMetadata(target *ConversationFile, source *ConversationFile) {
	if target == nil || source == nil {
		return
	}
	if strings.TrimSpace(source.ConversationID) != "" {
		target.ConversationID = strings.TrimSpace(source.ConversationID)
	}
	if strings.TrimSpace(source.RootConversationID) != "" {
		target.RootConversationID = strings.TrimSpace(source.RootConversationID)
	}
	target.ParentConversationID = strings.TrimSpace(source.ParentConversationID)
	target.ParentToolCallID = strings.TrimSpace(source.ParentToolCallID)
	target.SubagentTypeName = strings.TrimSpace(source.SubagentTypeName)
	if strings.TrimSpace(source.Mode) != "" {
		target.Mode = strings.TrimSpace(source.Mode)
	}
	target.TokenDetailsUsedTokens = source.TokenDetailsUsedTokens
	if source.TokenDetailsMaxTokens > 0 {
		target.TokenDetailsMaxTokens = source.TokenDetailsMaxTokens
	}
	target.AutoCompactionPending = source.AutoCompactionPending
	target.AutoCompactionPromptTokens = source.AutoCompactionPromptTokens
	target.AutoCompactionReserveTokens = source.AutoCompactionReserveTokens
	target.AutoCompactionTriggeredAt = source.AutoCompactionTriggeredAt
	target.AutoCompactionSourceModelCallID = source.AutoCompactionSourceModelCallID
	target.CurrentPlanText = source.CurrentPlanText
	target.CurrentPlans = clonePlanRegistryEntries(source.CurrentPlans)
	target.CurrentTodos = cloneTodoItems(source.CurrentTodos)
	target.LatestRequestPrefix = cloneConversationRequestPrefix(source.LatestRequestPrefix)
	target.LastProviderCall = cloneConversationProviderCall(source.LastProviderCall)
	if !source.CreatedAt.IsZero() && (target.CreatedAt.IsZero() || source.CreatedAt.Before(target.CreatedAt)) {
		target.CreatedAt = source.CreatedAt
	}
	if !source.UpdatedAt.IsZero() && source.UpdatedAt.After(target.UpdatedAt) {
		target.UpdatedAt = source.UpdatedAt
	}
	if source.NextTurnSeq > target.NextTurnSeq {
		target.NextTurnSeq = source.NextTurnSeq
	}
	if source.NextEntrySeq > target.NextEntrySeq {
		target.NextEntrySeq = source.NextEntrySeq
	}
	target.CurrentLoopID = strings.TrimSpace(source.CurrentLoopID)
	target.CurrentLoopStatus = strings.TrimSpace(source.CurrentLoopStatus)
	target.CurrentRequestID = strings.TrimSpace(source.CurrentRequestID)
	target.CurrentTurnSeq = source.CurrentTurnSeq
}

func normalizeLoadedConversation(conversationID string, conversation *ConversationFile) {
	if conversation == nil {
		return
	}
	conversation.SchemaVersion = conversationSchemaVersion
	if strings.TrimSpace(conversation.ConversationID) == "" {
		conversation.ConversationID = conversationID
	}
	if strings.TrimSpace(conversation.RootConversationID) == "" {
		conversation.RootConversationID = conversation.ConversationID
	}
	if conversation.NextTurnSeq <= 0 {
		conversation.NextTurnSeq = 1
	}
	if conversation.NextEntrySeq <= 0 {
		conversation.NextEntrySeq = 1
	}
	if conversation.Entries == nil {
		conversation.Entries = make([]HistoryEntry, 0, 16)
	}
	for _, entry := range conversation.Entries {
		if entry.Seq >= conversation.NextEntrySeq {
			conversation.NextEntrySeq = entry.Seq + 1
		}
		if entry.TurnSeq >= conversation.NextTurnSeq {
			conversation.NextTurnSeq = entry.TurnSeq + 1
		}
		if conversation.CreatedAt.IsZero() || (!entry.CreatedAt.IsZero() && entry.CreatedAt.Before(conversation.CreatedAt)) {
			conversation.CreatedAt = entry.CreatedAt
		}
		if !entry.CreatedAt.IsZero() && entry.CreatedAt.After(conversation.UpdatedAt) {
			conversation.UpdatedAt = entry.CreatedAt
		}
	}
	if conversation.CreatedAt.IsZero() {
		conversation.CreatedAt = time.Now().UTC()
	}
	if conversation.UpdatedAt.IsZero() {
		conversation.UpdatedAt = conversation.CreatedAt
	}
	deriveConversationLoopState(conversation)
}

func validateConversationID(conversationID string) (string, error) {
	normalized := strings.TrimSpace(conversationID)
	if normalized == "" {
		return "", fmt.Errorf("conversation_id is required")
	}
	if strings.Contains(normalized, "/") || strings.Contains(normalized, string(os.PathSeparator)) {
		return "", fmt.Errorf("conversation_id must not contain path separators")
	}
	return normalized, nil
}

func writeJSONFileAtomic(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err == nil {
		data = pretty.Bytes()
	}
	file, tempPath, err := openUniqueArtifactTempFile(path)
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := renameArtifactTempFile(tempPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	renamed = true
	return syncDirectory(filepath.Dir(path))
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func cloneConversationFile(conversation *ConversationFile) *ConversationFile {
	if conversation == nil {
		return nil
	}
	cloned := *conversation
	cloned.CurrentPlans = clonePlanRegistryEntries(conversation.CurrentPlans)
	cloned.CurrentTodos = cloneTodoItems(conversation.CurrentTodos)
	cloned.LatestRequestPrefix = cloneConversationRequestPrefix(conversation.LatestRequestPrefix)
	cloned.LastProviderCall = cloneConversationProviderCall(conversation.LastProviderCall)
	cloned.Entries = append([]HistoryEntry(nil), conversation.Entries...)
	return &cloned
}

func cloneConversationRequestPrefix(prefix *ConversationRequestPrefix) *ConversationRequestPrefix {
	if prefix == nil {
		return nil
	}
	cloned := *prefix
	return &cloned
}

func cloneConversationProviderCall(call *ConversationProviderCall) *ConversationProviderCall {
	if call == nil {
		return nil
	}
	cloned := *call
	return &cloned
}

func cloneByteSlices(items [][]byte) [][]byte {
	if len(items) == 0 {
		return nil
	}
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func cloneStringSlice(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	return append([]string(nil), items...)
}

func acquireConversationLock(lockPath string) (func(), error) {
	releaseProcessLock := acquireConversationProcessLock(lockPath)
	releaseFileLock, err := acquireConversationFileLock(lockPath)
	if err != nil {
		releaseProcessLock()
		return nil, err
	}
	return func() {
		releaseFileLock()
		releaseProcessLock()
	}, nil
}

func acquireConversationProcessLock(lockPath string) func() {
	key := filepath.Clean(lockPath)
	conversationProcessLocksMu.Lock()
	lock := conversationProcessLocks[key]
	if lock == nil {
		lock = &conversationProcessLock{}
		conversationProcessLocks[key] = lock
	}
	lock.refs++
	conversationProcessLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		conversationProcessLocksMu.Lock()
		lock.refs--
		if lock.refs <= 0 {
			delete(conversationProcessLocks, key)
		}
		conversationProcessLocksMu.Unlock()
	}
}

func acquireConversationFileLock(lockPath string) (func(), error) {
	deadline := time.Now().Add(conversationLockAcquireTimeout)
	var staleRemoveDeadline time.Time
	var lastStaleRemoveErr error
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			owner := conversationLockOwnerToken()
			_, _ = file.WriteString(fmt.Sprintf("pid=%d\nowner=%s\ncreated_at=%s\n", os.Getpid(), owner, time.Now().UTC().Format(time.RFC3339Nano)))
			_ = file.Close()
			return func() {
				removeConversationLockIfOwner(lockPath, owner)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create history lock: %w", err)
		}
		if stale, staleErr := conversationLockIsStale(lockPath); staleErr != nil {
			return nil, staleErr
		} else if stale {
			removeErr := os.Remove(lockPath)
			if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				lastStaleRemoveErr = nil
				staleRemoveDeadline = time.Time{}
				continue
			}
			if staleRemoveDeadline.IsZero() {
				staleRemoveDeadline = time.Now().Add(staleConversationLockRemoveTimeout)
			}
			lastStaleRemoveErr = removeErr
		} else {
			lastStaleRemoveErr = nil
			staleRemoveDeadline = time.Time{}
		}
		waitDeadline := deadline
		if lastStaleRemoveErr != nil && staleRemoveDeadline.After(waitDeadline) {
			waitDeadline = staleRemoveDeadline
		}
		remaining := time.Until(waitDeadline)
		if remaining <= 0 {
			break
		}
		if remaining > conversationLockRetryInterval {
			remaining = conversationLockRetryInterval
		}
		time.Sleep(remaining)
	}
	if lastStaleRemoveErr != nil {
		return nil, fmt.Errorf("timeout acquiring history lock %q (stale lock remove failed: %w)", lockPath, lastStaleRemoveErr)
	}
	return nil, fmt.Errorf("timeout acquiring history lock %q", lockPath)
}

func conversationLockIsStale(lockPath string) (bool, error) {
	info, err := os.Stat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if time.Since(info.ModTime()) > conversationLockStaleAfter {
		return true, nil
	}
	pid := readConversationLockPID(lockPath)
	if pid <= 0 {
		return time.Since(info.ModTime()) > legacyConversationLockStaleAfter, nil
	}
	if pid == os.Getpid() {
		if lockCreatedBeforeCurrentProcess(lockPath, info.ModTime()) {
			return true, nil
		}
		return false, nil
	}
	return !processLooksAlive(pid), nil
}

func lockCreatedBeforeCurrentProcess(lockPath string, modTime time.Time) bool {
	startedAt := conversationLockProcessStartedAt.Add(-time.Second)
	if createdAt := readConversationLockCreatedAt(lockPath); !createdAt.IsZero() {
		return createdAt.Before(startedAt)
	}
	return !modTime.IsZero() && modTime.Before(startedAt)
}

func readConversationLockPID(lockPath string) int {
	pid, _, _ := readConversationLockMetadata(lockPath)
	return pid
}

func readConversationLockCreatedAt(lockPath string) time.Time {
	_, createdAt, _ := readConversationLockMetadata(lockPath)
	return createdAt
}

func readConversationLockOwner(lockPath string) string {
	_, _, owner := readConversationLockMetadata(lockPath)
	return owner
}

func readConversationLockMetadata(lockPath string) (int, time.Time, string) {
	body, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, time.Time{}, ""
	}
	var pid int
	var createdAt time.Time
	var owner string
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "pid":
			parsedPID, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil && parsedPID > 0 {
				pid = parsedPID
			}
		case "created_at":
			parsedCreatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
			if err == nil {
				createdAt = parsedCreatedAt
			}
		case "owner":
			owner = strings.TrimSpace(value)
		}
	}
	return pid, createdAt, owner
}

func conversationLockOwnerToken() string {
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func removeConversationLockIfOwner(lockPath string, owner string) {
	if strings.TrimSpace(owner) != "" {
		currentOwner := readConversationLockOwner(lockPath)
		if currentOwner != "" && currentOwner != owner {
			return
		}
	}
	_ = os.Remove(lockPath)
}

func processLooksAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	return processExists(pid)
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
