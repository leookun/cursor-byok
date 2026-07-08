package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"cursor/internal/appdata"
	"cursor/internal/historymetrics"
)

func main() {
	flags := flag.NewFlagSet("historymetrics", flag.ExitOnError)
	usagePath := flags.String("usage", appdata.UsageFilePath(), "usage JSON file to read")
	if err := flags.Parse(os.Args[1:]); err != nil {
		exitf(err.Error())
	}

	if flags.NArg() > 0 {
		exitf("positional history scan targets are no longer supported; usage totals come from history/usage.json")
	}

	summary, err := historymetrics.LoadUsageSummary(*usagePath)
	if err != nil {
		exitf(err.Error())
	}

	fmt.Printf("Usage JSON file: %s\n\n", strings.TrimSpace(*usagePath))

	fmt.Println("Overall")
	fmt.Printf("- Provider calls total: %s\n", formatNumber(int64(summary.ProviderCallsTotal)))
	fmt.Printf("- Turns total: %s\n", formatNumber(int64(summary.TurnsTotal)))
	fmt.Printf("- Cache hit rate: %s\n", formatPercent(summary.CacheHitRate))
	fmt.Printf("- Cache read tokens: %s\n", formatNumber(summary.CacheReadTokens))
	fmt.Printf("- Cache write tokens: %s\n", formatNumber(summary.CacheWriteTokens))
	fmt.Printf("- Prompt tokens total: %s\n", formatNumber(summary.PromptTokensTotal))
	fmt.Printf("- Request tokens total: %s\n", formatNumber(summary.RequestTokensTotal))
}

func exitf(message string) {
	fmt.Fprintf(os.Stderr, "historymetrics failed: %s\n", strings.TrimSpace(message))
	os.Exit(1)
}

func formatNumber(value int64) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	digits := fmt.Sprintf("%d", value)
	if len(digits) <= 3 {
		return sign + digits
	}
	parts := make([]string, 0, (len(digits)+2)/3)
	for len(digits) > 3 {
		parts = append([]string{digits[len(digits)-3:]}, parts...)
		digits = digits[:len(digits)-3]
	}
	parts = append([]string{digits}, parts...)
	return sign + strings.Join(parts, ",")
}

func formatPercent(value *float64) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", *value*100)
}
