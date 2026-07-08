package modelchannel

import "strings"

func IsMetaModelAlias(modelRef string) bool {
	switch strings.ToLower(strings.TrimSpace(modelRef)) {
	case "fast", "default", "auto":
		return true
	default:
		return false
	}
}

func ResolveAdapterIndex[T any](adapters []T, requestedModelRef string, id func(T) string, providerModelID func(T) string, legacyIDs ...func(T) string) (int, bool) {
	if len(adapters) == 0 {
		return -1, false
	}

	targetModelRef := strings.TrimSpace(requestedModelRef)
	if targetModelRef == "" || IsMetaModelAlias(targetModelRef) {
		targetModelRef = strings.TrimSpace(id(adapters[0]))
	}
	if targetModelRef == "" {
		return -1, false
	}

	for index, adapter := range adapters {
		if strings.TrimSpace(id(adapter)) == targetModelRef {
			return index, true
		}
	}

	legacyIndex := -1
	for _, legacyID := range legacyIDs {
		if legacyID == nil {
			continue
		}
		for index, adapter := range adapters {
			if strings.TrimSpace(legacyID(adapter)) != targetModelRef {
				continue
			}
			if legacyIndex >= 0 && legacyIndex != index {
				return -1, false
			}
			legacyIndex = index
		}
	}
	if legacyIndex >= 0 {
		return legacyIndex, true
	}

	fallbackIndex := -1
	for index, adapter := range adapters {
		if strings.TrimSpace(providerModelID(adapter)) != targetModelRef {
			continue
		}
		if fallbackIndex >= 0 {
			return -1, false
		}
		fallbackIndex = index
	}
	if fallbackIndex < 0 {
		return -1, false
	}
	return fallbackIndex, true
}
