// Package compress provides a unified compression pipeline shared by
// Memory Runtime CompressionEngine and Forwarder compaction.
//
// ADR-035: Unified Compression Pipeline
// CompressionEngine (context/runtime) and forwarder/compaction.go previously
// ran two independent compression implementations. This package extracts shared
// token estimation, threshold configuration, and compression levels so both
// memory-level and persistence-level compression share a single pipeline.
package compress

import (
	"strings"
	"unicode/utf8"
)

// Level represents a compression aggressiveness tier.
type Level int

const (
	// LevelNone means no compression applied.
	LevelNone Level = iota
	// LevelLossless performs whitespace normalisation only.
	LevelLossless
	// LevelLossy summarises older messages into compact system annotations.
	LevelLossy
	// LevelSemantic uses embedding-based selective retention (future).
	LevelSemantic
)

func (l Level) String() string {
	switch l {
	case LevelNone:
		return "none"
	case LevelLossless:
		return "lossless"
	case LevelLossy:
		return "lossy"
	case LevelSemantic:
		return "semantic"
	default:
		return "unknown"
	}
}

// Config captures shared compression thresholds.
type Config struct {
	// MaxTokens is the target context window budget. 0 means use DefaultContextWindowTokens.
	MaxTokens int
	// SafetyMarginTokens reserved for provider overhead. Defaults to DefaultSafetyMargin.
	SafetyMarginTokens int
	// ReserveTokens reserved for the response. Defaults to DefaultReserveTokens.
	ReserveTokens int
	// PreferredTailTurns kept uncompacted for recency. Defaults to DefaultPreferredTailTurns.
	PreferredTailTurns int
	// MinimumTailTurns never compacted. Defaults to DefaultMinimumTailTurns.
	MinimumTailTurns int
}

const (
	// DefaultContextWindowTokens is the fallback context window when none is configured.
	DefaultContextWindowTokens = 130000

	// DefaultSafetyMargin keeps headroom for provider tokenisation variance.
	DefaultSafetyMargin = 4096

	// DefaultReserveTokens kept free for the model response.
	DefaultReserveTokens = 10000

	// DefaultPreferredTailTurns kept uncompacted for recency.
	DefaultPreferredTailTurns = 4

	// DefaultMinimumTailTurns never compacted.
	DefaultMinimumTailTurns = 1

	// DefaultSummaryMaxChars caps the raw text length of a generated summary.
	DefaultSummaryMaxChars = 12000
)

// DefaultConfig returns the shared baseline compression configuration.
func DefaultConfig() Config {
	return Config{
		SafetyMarginTokens:  DefaultSafetyMargin,
		ReserveTokens:       DefaultReserveTokens,
		PreferredTailTurns:  DefaultPreferredTailTurns,
		MinimumTailTurns:    DefaultMinimumTailTurns,
	}
}

// EffectiveMaxTokens returns the usable context budget after safety margin.
func (c Config) EffectiveMaxTokens() int {
	maxTokens := c.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultContextWindowTokens
	}
	safety := c.SafetyMarginTokens
	if safety <= 0 {
		safety = DefaultSafetyMargin
	}
	effective := maxTokens - safety
	if effective <= 0 {
		return maxTokens
	}
	return effective
}

// EffectiveBudget returns the budget after reserve tokens are accounted for.
func (c Config) EffectiveBudget() int {
	effective := c.EffectiveMaxTokens()
	reserve := c.ReserveTokens
	if reserve <= 0 {
		reserve = DefaultReserveTokens
	}
	budget := effective - reserve
	if budget <= 0 {
		return effective
	}
	return budget
}

// EstimateTokens returns a character-based token count for text.
// Uses the 4-chars-per-token heuristic shared by forwarder and context runtime.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return utf8.RuneCountInString(text) / 4
}

// EstimateTokensPrecise returns a higher-fidelity token estimate that accounts
// for newlines and rounds up (used by forwarder's token estimator for
// compaction budget calculations). Returns int64 for compat with existing
// forwarder callers.
//
// ponytail: simpler EstimateTokens is fine for context/runtime; this variant
// exists to replace forwarder/token_estimator.go's estimateTextTokens without
// breaking precision expectations there.
func EstimateTokensPrecise(text string) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount <= 0 {
		return 0
	}
	estimated := int64((runeCount + 3) / 4)
	estimated += int64(strings.Count(trimmed, "\n"))
	if estimated < 1 {
		return 1
	}
	return estimated
}

// EstimateMessagesTokens returns total estimated tokens for a slice of messages.
// Each message incurs 8 tokens of structural overhead (role + formatting).
func EstimateMessagesTokens(rolesAndContents []string) int {
	const overheadPerMessage = 8
	total := 0
	for _, s := range rolesAndContents {
		total += overheadPerMessage + EstimateTokens(s)
	}
	return total
}

// NeedsCompression returns the recommended compression level for the given token count.
func (c Config) NeedsCompression(tokenCount int) Level {
	budget := c.EffectiveBudget()
	if tokenCount <= budget {
		return LevelNone
	}
	eMax := c.EffectiveMaxTokens()
	if tokenCount <= eMax {
		return LevelLossless
	}
	return LevelLossy
}

// ReserveTokensForResponse returns the minimum tokens to reserve for the model response.
func (c Config) ReserveTokensForResponse() int {
	reserve := c.ReserveTokens
	if reserve <= 0 {
		return DefaultReserveTokens
	}
	return reserve
}