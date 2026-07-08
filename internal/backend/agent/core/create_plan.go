package runtimecore

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cursor/gen/agentv1"
)

// DecodeCreatePlanArgsJSON 解析 CreatePlan 参数，并兼容字符串形式的 todo status。
func DecodeCreatePlanArgsJSON(raw []byte) (*agentv1.CreatePlanArgs, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return &agentv1.CreatePlanArgs{}, nil
	}

	var direct agentv1.CreatePlanArgs
	if err := json.Unmarshal(raw, &direct); err == nil {
		return &direct, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return &agentv1.CreatePlanArgs{}, nil
	}

	todos, err := decodeCreatePlanTodoItems(createPlanValueByAlias(payload, "todos"))
	if err != nil {
		return nil, fmt.Errorf("decode todos: %w", err)
	}
	phases, err := decodeCreatePlanPhases(createPlanValueByAlias(payload, "phases"))
	if err != nil {
		return nil, fmt.Errorf("decode phases: %w", err)
	}

	return &agentv1.CreatePlanArgs{
		Plan:      createPlanStringValue(createPlanValueByAlias(payload, "plan")),
		Overview:  createPlanStringValue(createPlanValueByAlias(payload, "overview")),
		Name:      strings.TrimSpace(createPlanStringValue(createPlanValueByAlias(payload, "name"))),
		IsProject: createPlanBoolValue(createPlanValueByAlias(payload, "is_project", "isProject")),
		Todos:     todos,
		Phases:    phases,
	}, nil
}

func decodeCreatePlanPhases(value any) ([]*agentv1.Phase, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("phases must be an array")
	}
	if len(items) == 0 {
		return nil, nil
	}
	phases := make([]*agentv1.Phase, 0, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("phase %d must be an object", index)
		}
		todos, err := decodeCreatePlanTodoItems(createPlanValueByAlias(object, "todos"))
		if err != nil {
			return nil, fmt.Errorf("phase %d todos: %w", index, err)
		}
		phases = append(phases, &agentv1.Phase{
			Name:  strings.TrimSpace(createPlanStringValue(createPlanValueByAlias(object, "name"))),
			Todos: todos,
		})
	}
	return phases, nil
}

func decodeCreatePlanTodoItems(value any) ([]*agentv1.TodoItem, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("todos must be an array")
	}
	if len(items) == 0 {
		return nil, nil
	}
	todos := make([]*agentv1.TodoItem, 0, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("todo %d must be an object", index)
		}
		status, err := decodeCreatePlanTodoStatus(createPlanValueByAlias(object, "status"))
		if err != nil {
			return nil, fmt.Errorf("todo %d status: %w", index, err)
		}
		todos = append(todos, &agentv1.TodoItem{
			Id:           strings.TrimSpace(createPlanStringValue(createPlanValueByAlias(object, "id"))),
			Content:      strings.TrimSpace(createPlanStringValue(createPlanValueByAlias(object, "content"))),
			Status:       status,
			CreatedAt:    createPlanInt64Value(createPlanValueByAlias(object, "created_at", "createdAt")),
			UpdatedAt:    createPlanInt64Value(createPlanValueByAlias(object, "updated_at", "updatedAt")),
			Dependencies: createPlanStringSliceValue(createPlanValueByAlias(object, "dependencies")),
		})
	}
	return todos, nil
}

func decodeCreatePlanTodoStatus(value any) (agentv1.TodoStatus, error) {
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
		return decodeCreatePlanTodoStatusString(item)
	default:
		return agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED, fmt.Errorf("unsupported todo status type %T", value)
	}
}

func decodeCreatePlanTodoStatusString(raw string) (agentv1.TodoStatus, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" || normalized == "unspecified" || normalized == "todo_status_unspecified" {
		return agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED, nil
	}
	if numeric, err := strconv.ParseInt(normalized, 10, 32); err == nil {
		return agentv1.TodoStatus(numeric), nil
	}
	switch normalized {
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

func createPlanValueByAlias(payload map[string]any, aliases ...string) any {
	for _, alias := range aliases {
		if value, ok := payload[alias]; ok {
			return value
		}
	}
	return nil
}

func createPlanStringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func createPlanBoolValue(value any) bool {
	switch item := value.(type) {
	case bool:
		return item
	case string:
		return strings.EqualFold(strings.TrimSpace(item), "true")
	default:
		return false
	}
}

func createPlanInt64Value(value any) int64 {
	switch item := value.(type) {
	case float64:
		return int64(item)
	case float32:
		return int64(item)
	case int:
		return int64(item)
	case int32:
		return int64(item)
	case int64:
		return item
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func createPlanStringSliceValue(value any) []string {
	switch item := value.(type) {
	case []string:
		if len(item) == 0 {
			return nil
		}
		result := make([]string, 0, len(item))
		for _, text := range item {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				continue
			}
			result = append(result, trimmed)
		}
		return result
	case []any:
		if len(item) == 0 {
			return nil
		}
		result := make([]string, 0, len(item))
		for _, entry := range item {
			text := strings.TrimSpace(createPlanStringValue(entry))
			if text == "" {
				continue
			}
			result = append(result, text)
		}
		return result
	default:
		return nil
	}
}
