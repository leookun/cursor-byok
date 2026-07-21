package virtualmodel

import "strings"

// LastUserMessage returns the trimmed content of the most recent non-empty
// user message in messages, scanning from the end of the slice backwards.
//
// It returns an empty string when the slice contains no eligible user message.
//
// This is the shared replacement for the five previously duplicated
// extractUserText helpers in tot / reflection / debate / bestofn / aos. The
// signature intentionally takes []Message (the canonical virtualmodel.Message
// type) so all five subpackages can call it via virtualmodel.LastUserMessage
// without any local adapter.
func LastUserMessage(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && strings.TrimSpace(messages[i].Content) != "" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}
