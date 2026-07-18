package forwarder

import (
	"sort"
	"strings"
	"unicode"
)

const fallbackResponseLanguage = "en"

type LanguagePolicySource string

const (
	languagePolicySourceIDERule         LanguagePolicySource = "ide_rule"
	languagePolicySourceExplicitRequest LanguagePolicySource = "explicit_request"
	languagePolicySourceDetectedInput   LanguagePolicySource = "detected_input"
	languagePolicySourceFallback        LanguagePolicySource = "fallback"
)

type LanguagePolicy struct {
	Language string
	Source   LanguagePolicySource
	Locked   bool
}

type userRuleFrontmatter struct {
	ResponseLanguage     string
	LockResponseLanguage bool
	Body                 string
}

func resolveLanguagePolicy(records []UserRuleRecord, latestUserText string) LanguagePolicy {
	if language, ok := explicitResponseLanguage(latestUserText); ok {
		return LanguagePolicy{
			Language: language,
			Source:   languagePolicySourceExplicitRequest,
		}
	}
	if language, ok := lockedResponseLanguage(records); ok {
		return LanguagePolicy{
			Language: language,
			Source:   languagePolicySourceIDERule,
			Locked:   true,
		}
	}
	if language, ok := detectedResponseLanguage(latestUserText); ok {
		return LanguagePolicy{
			Language: language,
			Source:   languagePolicySourceDetectedInput,
		}
	}
	return LanguagePolicy{
		Language: fallbackResponseLanguage,
		Source:   languagePolicySourceFallback,
	}
}

func lockedResponseLanguage(records []UserRuleRecord) (string, bool) {
	ordered := append([]UserRuleRecord(nil), records...)
	sort.SliceStable(ordered, func(i int, j int) bool {
		return ordered[i].Filename < ordered[j].Filename
	})
	for _, record := range ordered {
		metadata := parseUserRuleFrontmatter(record.Knowledge)
		language, ok := normalizeResponseLanguage(metadata.ResponseLanguage)
		if metadata.LockResponseLanguage && ok {
			return language, true
		}
	}
	return "", false
}

func parseUserRuleFrontmatter(knowledge string) userRuleFrontmatter {
	trimmed := strings.TrimSpace(knowledge)
	if !strings.HasPrefix(trimmed, "---") {
		return userRuleFrontmatter{Body: trimmed}
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return userRuleFrontmatter{Body: trimmed}
	}
	closingIndex := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			closingIndex = index
			break
		}
	}
	if closingIndex < 0 {
		return userRuleFrontmatter{Body: trimmed}
	}

	metadata := userRuleFrontmatter{
		Body: strings.TrimSpace(strings.Join(lines[closingIndex+1:], "\n")),
	}
	for _, line := range lines[1:closingIndex] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "response_language":
			metadata.ResponseLanguage = strings.Trim(strings.TrimSpace(value), "\"'")
		case "lock_response_language":
			metadata.LockResponseLanguage = strings.EqualFold(strings.Trim(strings.TrimSpace(value), "\"'"), "true")
		}
	}
	return metadata
}

func normalizeResponseLanguage(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "vi", "vi-vn":
		return "vi", true
	case "en", "en-us", "en-gb":
		return "en", true
	case "zh", "zh-cn", "zh-hans":
		return "zh-CN", true
	default:
		return "", false
	}
}

func explicitResponseLanguage(text string) (string, bool) {
	normalized := strings.ToLower(text)
	for _, candidate := range []struct {
		Language string
		Phrases  []string
	}{
		{Language: "vi", Phrases: []string{"trả lời bằng tiếng việt", "trả lời tiếng việt", "phản hồi bằng tiếng việt", "phản hồi tiếng việt", "reply in vietnamese", "respond in vietnamese", "answer in vietnamese", "write your response in vietnamese"}},
		{Language: "en", Phrases: []string{"trả lời bằng tiếng anh", "trả lời tiếng anh", "phản hồi bằng tiếng anh", "phản hồi tiếng anh", "reply in english", "respond in english", "answer in english", "write your response in english"}},
		{Language: "zh-CN", Phrases: []string{"trả lời bằng tiếng trung", "trả lời tiếng trung", "phản hồi bằng tiếng trung", "phản hồi tiếng trung", "用中文回复", "请用中文", "reply in chinese", "respond in chinese", "answer in chinese", "write your response in chinese"}},
	} {
		for _, phrase := range candidate.Phrases {
			if strings.Contains(normalized, phrase) {
				return candidate.Language, true
			}
		}
	}
	return "", false
}

func detectedResponseLanguage(text string) (string, bool) {
	var latin, vietnamese, han int
	for _, value := range text {
		switch {
		case unicode.Is(unicode.Han, value):
			han++
		case isVietnameseRune(value):
			vietnamese++
		case unicode.IsLetter(value) && value <= unicode.MaxASCII:
			latin++
		}
	}
	switch {
	case han > 0 && han >= vietnamese && han >= latin/2:
		return "zh-CN", true
	case vietnamese > 0:
		return "vi", true
	case latin >= 3:
		return "en", true
	default:
		return "", false
	}
}

func isVietnameseRune(value rune) bool {
	return strings.ContainsRune("ăâđêôơưĂÂĐÊÔƠƯàáạảãằắặẳẵầấậẩẫèéẹẻẽềếệểễìíịỉĩòóọỏõồốộổỗờớợởỡùúụủũừứựửữỳýỵỷỹ", value)
}

func (service *Service) resolveLanguagePolicy(latestUserText string) LanguagePolicy {
	if service == nil || service.rules == nil {
		return resolveLanguagePolicy(nil, latestUserText)
	}
	records, err := service.rules.List()
	if err != nil {
		return resolveLanguagePolicy(nil, latestUserText)
	}
	return resolveLanguagePolicy(records, latestUserText)
}

func languagePolicySystemText(policy LanguagePolicy) string {
	if policy.Locked {
		return "Response language has an IDE default. An explicit response-language request in the current user message overrides that default. Otherwise, respond in " + languageDisplayName(policy.Language) + " for all user-facing natural-language content."
	}
	return "An explicit response-language request in the current user message overrides conflicting response-language guidance in shared IDE rules."
}

func languagePolicyLatestReminderText(policy LanguagePolicy) string {
	if policy.Locked {
		return ""
	}
	return "For this turn, respond in " + languageDisplayName(policy.Language) + " for user-facing natural-language content. This language was selected from the user's explicit request or message language."
}

func languageDisplayName(language string) string {
	switch language {
	case "vi":
		return "Vietnamese"
	case "zh-CN":
		return "Simplified Chinese"
	default:
		return "English"
	}
}
