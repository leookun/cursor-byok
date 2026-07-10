package modeladapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func cloneRequestBodyOverride(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return nil
	}
	return cloned
}

func requestBodyToMap(input any) (map[string]any, error) {
	if body, ok := input.(map[string]any); ok {
		return body, nil
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, nil
}

func ApplyOpenAIExtraParams(body map[string]any, enabled bool, paramsJSON string) error {
	return applyExtraParams(body, enabled, paramsJSON, "openai extra params json")
}

func ApplyAnthropicExtraParams(body map[string]any, enabled bool, paramsJSON string) error {
	return applyExtraParams(body, enabled, paramsJSON, "anthropic extra params json")
}

// applyOpenAIFastServiceTier 让「Fast 开关」成为 service_tier=priority 的唯一来源，
// 在「额外参数 JSON」合并之后调用，从而覆盖用户手填的 service_tier：
//   - fast=true  ：强制写入 service_tier=priority（Codex fast，约 1.5x）。
//   - fast=false ：若请求体里存在 priority（无论来自额外参数还是其他），一律剔除，
//     即“以开关为准，忽略配置的 priority”。其它 tier（如 flex）保持不动。
func applyOpenAIFastServiceTier(body map[string]any, fast bool) {
	if body == nil {
		return
	}
	if fast {
		body["service_tier"] = "priority"
		return
	}
	if v, ok := body["service_tier"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "priority") {
		delete(body, "service_tier")
	}
}

func applyExtraParams(body map[string]any, enabled bool, paramsJSON string, label string) error {
	if !enabled {
		return nil
	}
	if body == nil {
		return fmt.Errorf("%s target body is nil", label)
	}
	extraParams, err := parseJSONMap(paramsJSON, label)
	if err != nil {
		return err
	}
	for key, value := range extraParams {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		body[name] = value
	}
	return nil
}

func ApplyCustomHeaders(httpReq *http.Request, enabled bool, headersJSON string) error {
	if !enabled {
		return nil
	}
	if httpReq == nil {
		return fmt.Errorf("custom headers target request is nil")
	}
	headers, err := parseStringJSONMap(headersJSON, "custom headers json")
	if err != nil {
		return err
	}
	for key, value := range headers {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		httpReq.Header.Set(name, value)
	}
	return nil
}

func parseJSONMap(value string, label string) (map[string]any, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil, fmt.Errorf("%s is empty", label)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("%s must be an object: %w", label, err)
	}
	if parsed == nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	return parsed, nil
}

func parseStringJSONMap(value string, label string) (map[string]string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil, fmt.Errorf("%s is empty", label)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("%s must be an object with string values: %w", label, err)
	}
	if parsed == nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	return parsed, nil
}
