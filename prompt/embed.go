package prompt

import (
	"embed"
	"fmt"
	"strings"
)

// Mode 表示 prompt 资产对应的运行模式。
type Mode string

const (
	// ModeAsk 表示 Ask 模式的静态资产。
	ModeAsk Mode = "ask"
	// ModePlan 表示 Plan 模式的静态资产。
	ModePlan Mode = "plan"
	// ModeAgent 表示 Agent 模式的静态资产。
	ModeAgent Mode = "agent"
	// ModeDebug 表示 Debug 模式的静态资产。
	ModeDebug Mode = "debug"
	// ModeMultitask 表示 Multitask 模式的静态资产。
	ModeMultitask Mode = "multitask"
	// ModeSubagent 表示子代理只读会话的静态资产。
	ModeSubagent Mode = "subagent"
)

// assetFS 保存按模式组织的静态 prompt 与 tools 资产。
//
//go:embed common_prefix.md ask/prompt.md ask/tools.json plan/prompt.md plan/system_reminder.txt plan/tools.json agent/prompt.md agent/tools.json debug/prompt.md debug/tools.json debug/system_reminder_initial.txt debug/system_reminder_continuing.txt multitask/prompt.md multitask/tools.json subagent/prompt.md subagent/tools.json compaction/prompt.md commit/prompt.md
var assetFS embed.FS

// normalizeMode 校验并归一化传入的模式值。
func normalizeMode(mode Mode) (Mode, error) {
	switch mode {
	case ModeAsk, ModePlan, ModeAgent, ModeDebug, ModeMultitask, ModeSubagent:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported prompt mode: %q", mode)
	}
}

// PromptPath 返回指定模式的静态提示词资产路径。
func PromptPath(mode Mode) (string, error) {
	normalized, err := normalizeMode(mode)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/prompt.md", normalized), nil
}

// ToolsPath 返回指定模式的静态工具资产路径。
func ToolsPath(mode Mode) (string, error) {
	normalized, err := normalizeMode(mode)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/tools.json", normalized), nil
}

// ReadPrompt 读取指定模式的静态提示词文本。
func ReadPrompt(mode Mode) (string, error) {
	normalized, err := normalizeMode(mode)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/prompt.md", normalized)
	if normalized == ModeSubagent || normalized == ModeDebug {
		data, err := assetFS.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read prompt asset %q: %w", path, err)
		}
		return string(data), nil
	}
	prefix, err := assetFS.ReadFile("common_prefix.md")
	if err != nil {
		return "", fmt.Errorf("read prompt common prefix asset %q: %w", "common_prefix.md", err)
	}
	data, err := assetFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt asset %q: %w", path, err)
	}
	return string(prefix) + "\n\n" + string(data), nil
}

// MustReadPrompt 读取指定模式的静态提示词文本，失败时直接 panic。
func MustReadPrompt(mode Mode) string {
	text, err := ReadPrompt(mode)
	if err != nil {
		panic(err)
	}
	return text
}

// ReadTools 读取指定模式的原始工具 JSON。
func ReadTools(mode Mode) ([]byte, error) {
	path, err := ToolsPath(mode)
	if err != nil {
		return nil, err
	}
	data, err := assetFS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tools asset %q: %w", path, err)
	}
	return data, nil
}

// MustReadTools 读取指定模式的原始工具 JSON，失败时直接 panic。
func MustReadTools(mode Mode) []byte {
	data, err := ReadTools(mode)
	if err != nil {
		panic(err)
	}
	return data
}

// ReadDebugSystemReminder 读取 Debug 模式每轮追加的提醒资产。
func ReadDebugSystemReminder(initial bool) (string, error) {
	path := "debug/system_reminder_continuing.txt"
	if initial {
		path = "debug/system_reminder_initial.txt"
	}
	data, err := assetFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read debug system reminder asset %q: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// MustReadDebugSystemReminder 读取 Debug 模式提醒资产，失败时直接 panic。
func MustReadDebugSystemReminder(initial bool) string {
	text, err := ReadDebugSystemReminder(initial)
	if err != nil {
		panic(err)
	}
	return text
}

// ReadPlanSystemReminder 读取 Plan 模式每轮追加的动态提醒资产。
func ReadPlanSystemReminder() (string, error) {
	const path = "plan/system_reminder.txt"
	data, err := assetFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read plan system reminder asset %q: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// MustReadPlanSystemReminder 读取 Plan 模式动态提醒资产，失败时直接 panic。
func MustReadPlanSystemReminder() string {
	text, err := ReadPlanSystemReminder()
	if err != nil {
		panic(err)
	}
	return text
}

// ReadCompactionPrompt 读取共享的压缩提示词资产。
func ReadCompactionPrompt() (string, error) {
	const path = "compaction/prompt.md"
	data, err := assetFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read compaction prompt asset %q: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// MustReadCompactionPrompt 读取共享的压缩提示词资产，失败时直接 panic。
func MustReadCompactionPrompt() string {
	text, err := ReadCompactionPrompt()
	if err != nil {
		panic(err)
	}
	return text
}

// ReadCommitPrompt 读取提交信息生成专用提示词资产。
func ReadCommitPrompt() (string, error) {
	const path = "commit/prompt.md"
	data, err := assetFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read commit prompt asset %q: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// MustReadCommitPrompt 读取提交信息生成专用提示词资产，失败时直接 panic。
func MustReadCommitPrompt() string {
	text, err := ReadCommitPrompt()
	if err != nil {
		panic(err)
	}
	return text
}
