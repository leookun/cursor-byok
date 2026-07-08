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
