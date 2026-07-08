package forwarder

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

const todoSectionReminderMessage = "<system_reminder>\nYou are currently under the todo section, be sure to track tasks and do not forget to update.\n</system_reminder>"

const (
	promptContextSourceStructuredCurrentPlan  = "structured_state/current_plan"
	promptContextSourceStructuredTodoList     = "structured_state/todo_list"
	promptContextSourceStructuredTodoReminder = "structured_state/todo_reminder"
)

const todoUnsafeReplaceBaseError = "todo update rejected: merge=false may only omit completed or cancelled todos; use merge=true for incremental updates or include every active todo"

type structuredConversationState struct {
	PlanText string
	HasPlan  bool
	Plans    map[string]*agentv1.PlanRegistryEntry
	Todos    []*agentv1.TodoItem
	HasTodos bool
}

func projectConversationStructuredState(conversation *ConversationFile) (structuredConversationState, error) {
	state := structuredConversationState{}
	if conversation == nil {
		return state, nil
	}
	for _, entry := range checkpointProjectionEntries(conversation.Entries) {
		switch strings.TrimSpace(entry.Kind) {
		case "runtime_state":
			var payload runtimeStateEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return structuredConversationState{}, fmt.Errorf("decode runtime_state entry: %w", err)
			}
			if strings.TrimSpace(payload.PlanText) != "" {
				state.PlanText = strings.TrimSpace(payload.PlanText)
				state.HasPlan = true
			}
			if len(payload.Plans) > 0 {
				state.Plans = clonePlanRegistryEntries(payload.Plans)
			}
			if len(payload.Todos) > 0 {
				state.Todos = cloneTodoItems(payload.Todos)
				state.HasTodos = true
			}
			continue
		case "tool_result":
		default:
			continue
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return structuredConversationState{}, fmt.Errorf("decode structured state tool_result: %w", err)
		}
		if len(payload.ToolCall) == 0 {
			continue
		}
		toolCall := &agentv1.ToolCall{}
		if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
			return structuredConversationState{}, fmt.Errorf("decode structured state tool_call: %w", err)
		}
		entryTime := entry.CreatedAt
		switch item := toolCall.GetTool().(type) {
		case *agentv1.ToolCall_CreatePlanToolCall:
			createPlanToolCall := item.CreatePlanToolCall
			if createPlanToolCall == nil || createPlanToolCall.GetResult().GetSuccess() == nil {
				continue
			}
			planURI := strings.TrimSpace(createPlanToolCall.GetResult().GetPlanUri())
			if planURI == "" {
				continue
			}
			state.PlanText = strings.TrimSpace(createPlanToolCall.GetArgs().GetPlan())
			state.HasPlan = true
			state.Plans = upsertCurrentPlanRegistryEntry(state.Plans, planURI)
			todos, err := normalizeTodoItems(flattenCreatePlanTodos(createPlanToolCall.GetArgs()), entryTime, false)
			if err != nil {
				return structuredConversationState{}, err
			}
			state.Todos = todos
			state.HasTodos = true
		case *agentv1.ToolCall_UpdateTodosToolCall:
			updateTodosToolCall := item.UpdateTodosToolCall
			if updateTodosToolCall == nil || updateTodosToolCall.GetResult().GetSuccess() == nil {
				continue
			}
			success := updateTodosToolCall.GetResult().GetSuccess()
			todos, err := normalizeTodoItems(success.GetTodos(), entryTime, false)
			if err != nil {
				return structuredConversationState{}, err
			}
			if !success.GetWasMerge() && len(missingActiveTodoReplacementIDs(state.Todos, todos)) > 0 {
				todos, err = mergeTodoItems(state.Todos, todos, entryTime)
				if err != nil {
					return structuredConversationState{}, err
				}
			}
			state.Todos = todos
			state.HasTodos = true
		}
	}
	return state, nil
}

func refreshConversationRuntimeState(conversation *ConversationFile) error {
	if conversation == nil {
		return nil
	}
	state, err := projectConversationStructuredState(conversation)
	if err != nil {
		return err
	}
	conversation.CurrentPlanText = ""
	conversation.CurrentPlans = nil
	conversation.CurrentTodos = nil
	if state.HasPlan && strings.TrimSpace(state.PlanText) != "" {
		conversation.CurrentPlanText = strings.TrimSpace(state.PlanText)
	}
	if len(state.Plans) > 0 {
		conversation.CurrentPlans = clonePlanRegistryEntries(state.Plans)
	}
	if state.HasTodos && len(state.Todos) > 0 {
		conversation.CurrentTodos = cloneTodoItems(state.Todos)
	}
	return nil
}

func buildStructuredStatePromptContexts(conversation *ConversationFile) ([]PromptContextMessage, []modeladapter.Message, error) {
	state, err := projectConversationStructuredState(conversation)
	if err != nil {
		return nil, nil, err
	}
	contexts := make([]PromptContextMessage, 0, 2)
	tailMessages := make([]modeladapter.Message, 0, 1)
	if state.HasPlan && strings.TrimSpace(state.PlanText) != "" {
		contexts = append(contexts, newPromptContextMessage(
			promptContextSourceStructuredCurrentPlan,
			modeladapter.Message{
				Role:    "user",
				Content: "<current_plan>\n" + strings.TrimSpace(state.PlanText) + "\n</current_plan>",
			},
			false,
		))
	}
	if state.HasTodos {
		if todoText := renderTodoList(state.Todos); todoText != "" {
			contexts = append(contexts, newPromptContextMessage(
				promptContextSourceStructuredTodoList,
				modeladapter.Message{
					Role:    "user",
					Content: "<todo_list>\n" + todoText + "\n</todo_list>",
				},
				false,
			))
			tailMessages = append(tailMessages, modeladapter.Message{
				Role:    "user",
				Content: todoSectionReminderMessage,
			})
		}
	}
	return contexts, tailMessages, nil
}

func upsertCurrentPlanRegistryEntry(plans map[string]*agentv1.PlanRegistryEntry, planURI string) map[string]*agentv1.PlanRegistryEntry {
	trimmedURI := strings.TrimSpace(planURI)
	if trimmedURI == "" {
		return clonePlanRegistryEntries(plans)
	}
	next := clonePlanRegistryEntries(plans)
	if next == nil {
		next = make(map[string]*agentv1.PlanRegistryEntry, 1)
	}
	key := "current"
	if _, ok := next[key]; !ok && len(next) == 1 {
		for existingKey := range next {
			key = existingKey
		}
	}
	next[key] = &agentv1.PlanRegistryEntry{
		Id:   key,
		Path: trimmedURI,
	}
	return next
}

func clonePlanRegistryEntries(items map[string]*agentv1.PlanRegistryEntry) map[string]*agentv1.PlanRegistryEntry {
	if len(items) == 0 {
		return nil
	}
	cloned := make(map[string]*agentv1.PlanRegistryEntry, len(items))
	for key, item := range items {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" || item == nil {
			continue
		}
		cloned[trimmedKey] = &agentv1.PlanRegistryEntry{
			Id:   strings.TrimSpace(item.GetId()),
			Path: strings.TrimSpace(item.GetPath()),
		}
		if cloned[trimmedKey].GetId() == "" {
			cloned[trimmedKey].Id = trimmedKey
		}
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func encodeConversationPlanBytes(plan string) []byte {
	text := strings.TrimSpace(plan)
	if text == "" {
		return nil
	}
	payload, err := proto.Marshal(&agentv1.ConversationPlan{Plan: text})
	if err != nil {
		return nil
	}
	return payload
}

func encodeConversationTodoBytes(items []*agentv1.TodoItem) [][]byte {
	if len(items) == 0 {
		return nil
	}
	encoded := make([][]byte, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		payload, err := proto.Marshal(cloneTodoItem(item))
		if err != nil {
			continue
		}
		encoded = append(encoded, payload)
	}
	if len(encoded) == 0 {
		return nil
	}
	return encoded
}

func flattenCreatePlanTodos(args *agentv1.CreatePlanArgs) []*agentv1.TodoItem {
	if args == nil {
		return nil
	}
	if len(args.GetTodos()) > 0 {
		return cloneTodoItems(args.GetTodos())
	}
	items := make([]*agentv1.TodoItem, 0, len(args.GetPhases()))
	for _, phase := range args.GetPhases() {
		if phase == nil {
			continue
		}
		items = append(items, cloneTodoItems(phase.GetTodos())...)
	}
	return items
}

type decodedUpdateTodosArgs struct {
	Args     *agentv1.UpdateTodosArgs
	MergeSet bool
}

func decodeUpdateTodosArgsJSON(raw []byte) (*agentv1.UpdateTodosArgs, error) {
	decoded, err := decodeUpdateTodosArgsJSONWithPresence(raw)
	if err != nil {
		return nil, err
	}
	return decoded.Args, nil
}

func decodeUpdateTodosArgsJSONWithPresence(raw []byte) (decodedUpdateTodosArgs, error) {
	payload, err := decodeJSONObject(raw)
	if err != nil {
		return decodedUpdateTodosArgs{}, err
	}
	todos, err := decodeTodoItemsValue(valueByAlias(payload, "todos"), false)
	if err != nil {
		return decodedUpdateTodosArgs{}, err
	}
	mergeValue, mergeSet := valueByAliasWithPresence(payload, "merge")
	return decodedUpdateTodosArgs{
		Args: &agentv1.UpdateTodosArgs{
			Todos: todos,
			Merge: boolValue(mergeValue),
		},
		MergeSet: mergeSet,
	}, nil
}

func decodeReadTodosArgsJSON(raw []byte) (*agentv1.ReadTodosArgs, error) {
	payload, err := decodeJSONObject(raw)
	if err != nil {
		return nil, err
	}
	statuses, err := decodeTodoStatusesValue(valueByAlias(payload, "status_filter", "statusFilter"))
	if err != nil {
		return nil, err
	}
	return &agentv1.ReadTodosArgs{
		StatusFilter: statuses,
		IdFilter:     stringSliceValue(valueByAlias(payload, "id_filter", "idFilter")),
	}, nil
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, nil
}

func decodeTodoItemsValue(value any, validate bool) ([]*agentv1.TodoItem, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, nil
	}
	todos := make([]*agentv1.TodoItem, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("todo item must be an object")
		}
		status, err := todoStatusFromValue(valueByAlias(item, "status"))
		if err != nil {
			return nil, err
		}
		todo := &agentv1.TodoItem{
			Id:           strings.TrimSpace(stringValue(valueByAlias(item, "id"))),
			Content:      strings.TrimSpace(stringValue(valueByAlias(item, "content"))),
			Status:       status,
			CreatedAt:    int64Value(valueByAlias(item, "created_at", "createdAt")),
			UpdatedAt:    int64Value(valueByAlias(item, "updated_at", "updatedAt")),
			Dependencies: stringSliceValue(valueByAlias(item, "dependencies")),
		}
		if validate {
			if todo.GetId() == "" {
				return nil, fmt.Errorf("todo id is required")
			}
			if todo.GetContent() == "" {
				return nil, fmt.Errorf("todo content is required")
			}
		}
		todos = append(todos, todo)
	}
	return todos, nil
}

func decodeTodoStatusesValue(value any) ([]agentv1.TodoStatus, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, nil
	}
	statuses := make([]agentv1.TodoStatus, 0, len(items))
	for _, item := range items {
		status, err := todoStatusFromValue(item)
		if err != nil {
			return nil, err
		}
		if status == agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED {
			continue
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func valueByAlias(values map[string]any, aliases ...string) any {
	value, _ := valueByAliasWithPresence(values, aliases...)
	return value
}

func valueByAliasWithPresence(values map[string]any, aliases ...string) (any, bool) {
	for _, alias := range aliases {
		if alias == "" {
			continue
		}
		if value, ok := values[alias]; ok {
			return value, true
		}
	}
	return nil, false
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func boolValue(value any) bool {
	switch item := value.(type) {
	case bool:
		return item
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(item))
		return parsed
	default:
		return false
	}
}

func int64Value(value any) int64 {
	switch item := value.(type) {
	case float64:
		return int64(item)
	case float32:
		return int64(item)
	case int64:
		return item
	case int32:
		return int64(item)
	case int:
		return int64(item)
	case json.Number:
		parsed, _ := item.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
		return parsed
	default:
		return 0
	}
}

func stringSliceValue(value any) []string {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text := stringValue(item)
		if text == "" {
			continue
		}
		result = append(result, text)
	}
	return result
}

func todoStatusFromValue(value any) (agentv1.TodoStatus, error) {
	switch item := value.(type) {
	case nil:
		return agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED, nil
	case float64:
		return agentv1.TodoStatus(int32(item)), nil
	case float32:
		return agentv1.TodoStatus(int32(item)), nil
	case int:
		return agentv1.TodoStatus(item), nil
	case int32:
		return agentv1.TodoStatus(item), nil
	case int64:
		return agentv1.TodoStatus(item), nil
	case string:
		return todoStatusFromString(item)
	default:
		return agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED, fmt.Errorf("unsupported todo status type %T", value)
	}
}

func todoStatusFromString(raw string) (agentv1.TodoStatus, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "unspecified", "todo_status_unspecified":
		return agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED, nil
	case "pending", "todo_status_pending":
		return agentv1.TodoStatus_TODO_STATUS_PENDING, nil
	case "in_progress", "in-progress", "inprogress", "todo_status_in_progress":
		return agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS, nil
	case "completed", "complete", "todo_status_completed":
		return agentv1.TodoStatus_TODO_STATUS_COMPLETED, nil
	case "cancelled", "canceled", "todo_status_cancelled":
		return agentv1.TodoStatus_TODO_STATUS_CANCELLED, nil
	default:
		return agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED, fmt.Errorf("unsupported todo status %q", raw)
	}
}

func todoStatusLabel(status agentv1.TodoStatus) string {
	switch status {
	case agentv1.TodoStatus_TODO_STATUS_PENDING:
		return "pending"
	case agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS:
		return "in_progress"
	case agentv1.TodoStatus_TODO_STATUS_COMPLETED:
		return "completed"
	case agentv1.TodoStatus_TODO_STATUS_CANCELLED:
		return "cancelled"
	default:
		return "unspecified"
	}
}

func normalizeTodoItems(items []*agentv1.TodoItem, baseTime time.Time, validate bool) ([]*agentv1.TodoItem, error) {
	if len(items) == 0 {
		return nil, nil
	}
	baseMillis := timestampMillis(baseTime)
	normalized := make([]*agentv1.TodoItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		next := cloneTodoItem(item)
		next.Id = strings.TrimSpace(next.GetId())
		next.Content = strings.TrimSpace(next.GetContent())
		if validate {
			if next.GetId() == "" {
				return nil, fmt.Errorf("todo id is required")
			}
			if next.GetContent() == "" {
				return nil, fmt.Errorf("todo content is required")
			}
		}
		if next.GetStatus() == agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED {
			next.Status = agentv1.TodoStatus_TODO_STATUS_PENDING
		}
		if next.GetCreatedAt() <= 0 {
			next.CreatedAt = baseMillis
		}
		if next.GetUpdatedAt() <= 0 {
			next.UpdatedAt = next.GetCreatedAt()
		}
		normalized = append(normalized, next)
	}
	return normalized, nil
}

func mergeTodoItems(existing []*agentv1.TodoItem, updates []*agentv1.TodoItem, baseTime time.Time) ([]*agentv1.TodoItem, error) {
	result, err := normalizeTodoItems(existing, baseTime, false)
	if err != nil {
		return nil, err
	}
	indexByID := make(map[string]int, len(result))
	for index, item := range result {
		if item == nil || strings.TrimSpace(item.GetId()) == "" {
			continue
		}
		indexByID[strings.TrimSpace(item.GetId())] = index
	}
	nowMillis := timestampMillis(baseTime)
	for _, update := range updates {
		if update == nil {
			continue
		}
		id := strings.TrimSpace(update.GetId())
		if id == "" {
			return nil, fmt.Errorf("todo id is required")
		}
		content := strings.TrimSpace(update.GetContent())
		if index, ok := indexByID[id]; ok {
			current := result[index]
			next := cloneTodoItem(current)
			next.Id = id
			if content != "" {
				next.Content = content
			}
			if update.GetStatus() != agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED {
				next.Status = update.GetStatus()
			}
			if len(update.GetDependencies()) > 0 {
				next.Dependencies = append([]string(nil), update.GetDependencies()...)
			}
			if next.GetContent() == "" {
				return nil, fmt.Errorf("todo content is required")
			}
			if next.GetStatus() == agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED {
				next.Status = agentv1.TodoStatus_TODO_STATUS_PENDING
			}
			next.CreatedAt = current.GetCreatedAt()
			if next.GetCreatedAt() <= 0 {
				next.CreatedAt = nowMillis
			}
			next.UpdatedAt = nowMillis
			result[index] = next
			continue
		}
		next := cloneTodoItem(update)
		next.Id = id
		next.Content = content
		if next.GetContent() == "" {
			return nil, fmt.Errorf("todo content is required for new todo")
		}
		if next.GetStatus() == agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED {
			next.Status = agentv1.TodoStatus_TODO_STATUS_PENDING
		}
		if next.GetCreatedAt() <= 0 {
			next.CreatedAt = nowMillis
		}
		if next.GetUpdatedAt() <= 0 {
			next.UpdatedAt = next.GetCreatedAt()
		}
		indexByID[id] = len(result)
		result = append(result, next)
	}
	return result, nil
}

func missingActiveTodoReplacementIDs(existing []*agentv1.TodoItem, replacement []*agentv1.TodoItem) []string {
	existingIDs := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		if item == nil || isTerminalTodoStatus(item.GetStatus()) {
			continue
		}
		id := strings.TrimSpace(item.GetId())
		if id == "" {
			continue
		}
		existingIDs[id] = struct{}{}
	}
	if len(existingIDs) == 0 {
		return nil
	}
	replacementIDs := make(map[string]struct{}, len(replacement))
	for _, item := range replacement {
		id := strings.TrimSpace(item.GetId())
		if id == "" {
			continue
		}
		replacementIDs[id] = struct{}{}
	}
	missing := make([]string, 0)
	for id := range existingIDs {
		if _, ok := replacementIDs[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

func isTerminalTodoStatus(status agentv1.TodoStatus) bool {
	switch status {
	case agentv1.TodoStatus_TODO_STATUS_COMPLETED,
		agentv1.TodoStatus_TODO_STATUS_CANCELLED:
		return true
	default:
		return false
	}
}

func unsafeTodoReplaceError(missingIDs []string) string {
	if len(missingIDs) == 0 {
		return todoUnsafeReplaceBaseError
	}
	return fmt.Sprintf("%s; missing active todo ids: %s", todoUnsafeReplaceBaseError, strings.Join(missingIDs, ", "))
}

func filterTodoItems(items []*agentv1.TodoItem, statuses []agentv1.TodoStatus, ids []string) []*agentv1.TodoItem {
	if len(items) == 0 {
		return nil
	}
	statusFilter := make(map[agentv1.TodoStatus]struct{}, len(statuses))
	for _, status := range statuses {
		statusFilter[status] = struct{}{}
	}
	idFilter := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		idFilter[trimmed] = struct{}{}
	}
	filtered := make([]*agentv1.TodoItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		if len(statusFilter) > 0 {
			if _, ok := statusFilter[item.GetStatus()]; !ok {
				continue
			}
		}
		if len(idFilter) > 0 {
			if _, ok := idFilter[strings.TrimSpace(item.GetId())]; !ok {
				continue
			}
		}
		filtered = append(filtered, cloneTodoItem(item))
	}
	return filtered
}

func renderTodoList(items []*agentv1.TodoItem) string {
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s", todoStatusLabel(item.GetStatus()), strings.TrimSpace(item.GetId()), strings.TrimSpace(item.GetContent())))
	}
	return strings.Join(lines, "\n")
}

func buildUpdateTodosToolCall(args *agentv1.UpdateTodosArgs, result *agentv1.UpdateTodosResult) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_UpdateTodosToolCall{
			UpdateTodosToolCall: &agentv1.UpdateTodosToolCall{
				Args:   args,
				Result: result,
			},
		},
	}
}

func buildUpdateTodosErrorResult(args *agentv1.UpdateTodosArgs, message string) *agentv1.UpdateTodosResult {
	return &agentv1.UpdateTodosResult{
		Result: &agentv1.UpdateTodosResult_Error{
			Error: &agentv1.UpdateTodosError{Error: strings.TrimSpace(message)},
		},
	}
}

func buildReadTodosToolCall(args *agentv1.ReadTodosArgs, result *agentv1.ReadTodosResult) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadTodosToolCall{
			ReadTodosToolCall: &agentv1.ReadTodosToolCall{
				Args:   args,
				Result: result,
			},
		},
	}
}

func summarizeUpdateTodosResult(result *agentv1.UpdateTodosResult) string {
	if result == nil {
		return "todo update missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.UpdateTodosResult_Success:
		return fmt.Sprintf("todo update success count=%d merge=%t", item.Success.GetTotalCount(), item.Success.GetWasMerge())
	case *agentv1.UpdateTodosResult_Error:
		return strings.TrimSpace(item.Error.GetError())
	default:
		return "todo update finished"
	}
}

func summarizeReadTodosResult(result *agentv1.ReadTodosResult) string {
	if result == nil {
		return "todo read missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.ReadTodosResult_Success:
		return fmt.Sprintf("todo read success count=%d", item.Success.GetTotalCount())
	case *agentv1.ReadTodosResult_Error:
		return strings.TrimSpace(item.Error.GetError())
	default:
		return "todo read finished"
	}
}

func buildTaskArgsFromJSON(raw []byte) *agentv1.TaskArgs {
	payload, err := decodeJSONObject(raw)
	if err != nil {
		return &agentv1.TaskArgs{}
	}
	return buildTaskArgsFromMap(payload)
}

func buildTaskArgsFromMap(payload map[string]any) *agentv1.TaskArgs {
	args := &agentv1.TaskArgs{
		Description: strings.TrimSpace(stringValue(valueByAlias(payload, "description"))),
		Prompt:      strings.TrimSpace(stringValue(valueByAlias(payload, "prompt"))),
		SubagentType: subagentTypeFromString(
			strings.TrimSpace(stringValue(valueByAlias(payload, "subagent_type", "subagentType"))),
		),
		Attachments: stringSliceValue(valueByAlias(payload, "attachments")),
		Mode:        taskModeFromReadonly(boolValue(valueByAlias(payload, "readonly", "readOnly"))),
	}
	if model := strings.TrimSpace(stringValue(valueByAlias(payload, "model"))); model != "" {
		args.Model = &model
	}
	if resume := strings.TrimSpace(stringValue(valueByAlias(payload, "resume"))); resume != "" {
		args.Resume = &resume
	}
	if agentID := strings.TrimSpace(stringValue(valueByAlias(payload, "agent_id", "agentId"))); agentID != "" {
		args.AgentId = &agentID
	}
	return args
}

func subagentTypeFromString(raw string) *agentv1.SubagentType {
	switch strings.TrimSpace(raw) {
	case "explore":
		return &agentv1.SubagentType{Type: &agentv1.SubagentType_Explore{Explore: &agentv1.SubagentTypeExplore{}}}
	case "browser-use", "browserUse":
		return &agentv1.SubagentType{Type: &agentv1.SubagentType_BrowserUse{BrowserUse: &agentv1.SubagentTypeBrowserUse{}}}
	case "shell":
		return &agentv1.SubagentType{Type: &agentv1.SubagentType_Shell{Shell: &agentv1.SubagentTypeShell{}}}
	case "":
		return &agentv1.SubagentType{Type: &agentv1.SubagentType_Unspecified{Unspecified: &agentv1.SubagentTypeUnspecified{}}}
	default:
		return &agentv1.SubagentType{
			Type: &agentv1.SubagentType_Custom{
				Custom: &agentv1.SubagentTypeCustom{Name: strings.TrimSpace(raw)},
			},
		}
	}
}

func taskModeFromReadonly(readonly bool) agentv1.TaskMode {
	if readonly {
		return agentv1.TaskMode_TASK_MODE_PLAN
	}
	return agentv1.TaskMode_TASK_MODE_AGENT
}

func cloneTodoItems(items []*agentv1.TodoItem) []*agentv1.TodoItem {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]*agentv1.TodoItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		cloned = append(cloned, cloneTodoItem(item))
	}
	return cloned
}

func cloneTodoItem(item *agentv1.TodoItem) *agentv1.TodoItem {
	if item == nil {
		return nil
	}
	return &agentv1.TodoItem{
		Id:           item.GetId(),
		Content:      item.GetContent(),
		Status:       item.GetStatus(),
		CreatedAt:    item.GetCreatedAt(),
		UpdatedAt:    item.GetUpdatedAt(),
		Dependencies: append([]string(nil), item.GetDependencies()...),
	}
}

func timestampMillis(baseTime time.Time) int64 {
	now := baseTime
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC().UnixMilli()
}
