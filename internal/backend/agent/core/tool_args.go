package runtimecore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// DecodeArgsMap decodes model-produced built-in tool arguments while preserving
// JSON number spellings for lossless numeric coercion by the typed readers below.
func DecodeArgsMap(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		return map[string]any{}, nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("invalid JSON arguments: multiple top-level values")
		}
		return nil, err
	}
	return result, nil
}

// ReadStringArg reads the first string value matching one of the provided keys.
func ReadStringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}

// ReadBoolArg reads the first bool value matching one of the provided keys.
func ReadBoolArg(args map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		if item, ok := value.(bool); ok {
			return item
		}
	}
	return false
}

// HasArgKey reports whether any candidate key is present with a non-null value.
func HasArgKey(args map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := args[key]
		if ok && value != nil {
			return true
		}
	}
	return false
}

// BoolPtrIfPresent returns a bool pointer only when a matching key is present.
func BoolPtrIfPresent(args map[string]any, keys ...string) *bool {
	if !HasArgKey(args, keys...) {
		return nil
	}
	value := ReadBoolArg(args, keys...)
	return &value
}

// ReadStringSliceArg reads a string array value matching one of the provided keys.
func ReadStringSliceArg(args map[string]any, keys ...string) []string {
	for _, key := range keys {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		if direct, ok := value.([]string); ok {
			return append([]string(nil), direct...)
		}
		items, ok := value.([]any)
		if !ok {
			continue
		}
		result := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				continue
			}
			trimmed := strings.TrimSpace(text)
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result
	}
	return nil
}

func readArgValue(args map[string]any, keys ...string) (any, string, bool) {
	for _, key := range keys {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		return value, key, true
	}
	return nil, "", false
}

// ReadIntArg reads an int field from a JSON number or lossless numeric string.
func ReadIntArg(args map[string]any, keys ...string) (int, bool, error) {
	value, key, found := readArgValue(args, keys...)
	if !found {
		return 0, false, nil
	}
	parsed, err := parseIntegerValue(value, key, int64MinForBits(strconv.IntSize), int64MaxForBits(strconv.IntSize))
	if err != nil {
		return 0, true, err
	}
	return int(parsed), true, nil
}

// ReadInt32Arg reads an int32 field from a JSON number or lossless numeric string.
func ReadInt32Arg(args map[string]any, keys ...string) (int32, bool, error) {
	value, key, found := readArgValue(args, keys...)
	if !found {
		return 0, false, nil
	}
	parsed, err := parseIntegerValue(value, key, math.MinInt32, math.MaxInt32)
	if err != nil {
		return 0, true, err
	}
	return int32(parsed), true, nil
}

// ReadInt64Arg reads an int64 field from a JSON number or lossless numeric string.
func ReadInt64Arg(args map[string]any, keys ...string) (int64, bool, error) {
	value, key, found := readArgValue(args, keys...)
	if !found {
		return 0, false, nil
	}
	parsed, err := parseIntegerValue(value, key, math.MinInt64, math.MaxInt64)
	if err != nil {
		return 0, true, err
	}
	return parsed, true, nil
}

// ReadUint32Arg reads a uint32 field from a JSON number or lossless numeric string.
func ReadUint32Arg(args map[string]any, keys ...string) (uint32, bool, error) {
	value, key, found := readArgValue(args, keys...)
	if !found {
		return 0, false, nil
	}
	parsed, err := parseUnsignedIntegerValue(value, key, math.MaxUint32)
	if err != nil {
		return 0, true, err
	}
	return uint32(parsed), true, nil
}

// ReadFloat64Arg reads a float64 field from a JSON number or numeric string.
func ReadFloat64Arg(args map[string]any, keys ...string) (float64, bool, error) {
	value, key, found := readArgValue(args, keys...)
	if !found {
		return 0, false, nil
	}
	parsed, err := parseFloatValue(value, key)
	if err != nil {
		return 0, true, err
	}
	return parsed, true, nil
}

func parseIntegerValue(value any, key string, minValue int64, maxValue int64) (int64, error) {
	switch item := value.(type) {
	case json.Number:
		return parseIntegerLiteral(item.String(), key, minValue, maxValue)
	case string:
		return parseIntegerLiteral(strings.TrimSpace(item), key, minValue, maxValue)
	case float64:
		return parseIntegerFloat(item, key, minValue, maxValue)
	case float32:
		return parseIntegerFloat(float64(item), key, minValue, maxValue)
	default:
		reflected := reflect.ValueOf(value)
		switch reflected.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			parsed := reflected.Int()
			if parsed < minValue || parsed > maxValue {
				return 0, fmt.Errorf("%s is outside supported integer range", key)
			}
			return parsed, nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			parsed := reflected.Uint()
			if parsed > uint64(maxValue) {
				return 0, fmt.Errorf("%s is outside supported integer range", key)
			}
			return int64(parsed), nil
		default:
			return 0, fmt.Errorf("%s must be an integer", key)
		}
	}
}

func parseUnsignedIntegerValue(value any, key string, maxValue uint64) (uint64, error) {
	switch item := value.(type) {
	case json.Number:
		return parseUnsignedIntegerLiteral(item.String(), key, maxValue)
	case string:
		return parseUnsignedIntegerLiteral(strings.TrimSpace(item), key, maxValue)
	case float64:
		return parseUnsignedIntegerFloat(item, key, maxValue)
	case float32:
		return parseUnsignedIntegerFloat(float64(item), key, maxValue)
	default:
		reflected := reflect.ValueOf(value)
		switch reflected.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			parsed := reflected.Int()
			if parsed < 0 {
				return 0, fmt.Errorf("%s must be a non-negative integer", key)
			}
			if uint64(parsed) > maxValue {
				return 0, fmt.Errorf("%s is outside supported unsigned integer range", key)
			}
			return uint64(parsed), nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			parsed := reflected.Uint()
			if parsed > maxValue {
				return 0, fmt.Errorf("%s is outside supported unsigned integer range", key)
			}
			return parsed, nil
		default:
			return 0, fmt.Errorf("%s must be a non-negative integer", key)
		}
	}
}

func parseIntegerLiteral(raw string, key string, minValue int64, maxValue int64) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if parsed < minValue || parsed > maxValue {
		return 0, fmt.Errorf("%s is outside supported integer range", key)
	}
	return parsed, nil
}

func parseUnsignedIntegerLiteral(raw string, key string, maxValue uint64) (uint64, error) {
	if raw == "" {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		if strings.HasPrefix(raw, "-") {
			return 0, fmt.Errorf("%s must be a non-negative integer", key)
		}
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	if parsed > maxValue {
		return 0, fmt.Errorf("%s is outside supported unsigned integer range", key)
	}
	return parsed, nil
}

func parseIntegerFloat(value float64, key string, minValue int64, maxValue int64) (int64, error) {
	if !isFiniteFloat(value) || math.Trunc(value) != value {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if value < float64(minValue) || value > float64(maxValue) {
		return 0, fmt.Errorf("%s is outside supported integer range", key)
	}
	return int64(value), nil
}

func parseUnsignedIntegerFloat(value float64, key string, maxValue uint64) (uint64, error) {
	if !isFiniteFloat(value) || math.Trunc(value) != value {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	if value > float64(maxValue) {
		return 0, fmt.Errorf("%s is outside supported unsigned integer range", key)
	}
	return uint64(value), nil
}

func parseFloatValue(value any, key string) (float64, error) {
	switch item := value.(type) {
	case json.Number:
		parsed, err := item.Float64()
		if err != nil || !isFiniteFloat(parsed) {
			return 0, fmt.Errorf("%s must be a finite number", key)
		}
		return parsed, nil
	case string:
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			return 0, fmt.Errorf("%s must be a finite number", key)
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || !isFiniteFloat(parsed) {
			return 0, fmt.Errorf("%s must be a finite number", key)
		}
		return parsed, nil
	case float64:
		if !isFiniteFloat(item) {
			return 0, fmt.Errorf("%s must be a finite number", key)
		}
		return item, nil
	case float32:
		parsed := float64(item)
		if !isFiniteFloat(parsed) {
			return 0, fmt.Errorf("%s must be a finite number", key)
		}
		return parsed, nil
	default:
		reflected := reflect.ValueOf(value)
		switch reflected.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return float64(reflected.Int()), nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return float64(reflected.Uint()), nil
		default:
			return 0, fmt.Errorf("%s must be a finite number", key)
		}
	}
}

func int64MinForBits(bits int) int64 {
	if bits >= 64 {
		return math.MinInt64
	}
	return -(int64(1) << (bits - 1))
}

func int64MaxForBits(bits int) int64 {
	if bits >= 64 {
		return math.MaxInt64
	}
	return (int64(1) << (bits - 1)) - 1
}

func isFiniteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
