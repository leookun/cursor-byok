// Package promptasset provides shared helpers for sanitizing static prompt
// asset files before they are rendered into compiled prompts.
package promptasset

import (
	"strings"

	promptassets "cursor/prompt"
)

// Sanitize removes documentation-only headers and separators from a prompt
// asset body (e.g. "# 通用系统提示词", "# 模式静态补充", "---") and renders the
// remaining text through the standard prompt template renderer using the
// given model name.
//
// It is the shared replacement for the byte-identical sanitizePromptAsset
// helpers that previously lived in both the forwarder and prompt engine
// packages.
func Sanitize(text string, modelName string) string {
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "# 通用系统提示词", "# 模式静态补充", "---":
			continue
		default:
			filtered = append(filtered, line)
		}
	}
	return promptassets.RenderPromptTemplate(strings.TrimSpace(strings.Join(filtered, "\n")), modelName)
}
