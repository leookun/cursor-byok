// Package bestofn implements the Best-of-N Virtual Model.
//
// Best-of-N generates N candidates in parallel, then uses a Judge model
// to select the best answer.
//
// Based on Best-of-N Sampling (arxiv.org/abs/2310.16748).
// Design: ADR-017. Research: docs/research/best-of-n-virtual-model.md.
package bestofn

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
	optimize "cursor/internal/backend/runtime/optimize"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

const ModelID = "bestOfN"
const DisplayName = "Best-of-N"

// DefaultMaxParallelCandidates is the default upper bound on concurrent
// candidate adapter calls when MaxParallelCandidates is unset or
// non-positive. It prevents N candidates from spawning N simultaneous
// HTTP requests to the upstream provider. Users who need stricter
// (e.g. low-RPM keys) or looser limits should set
// BestOfNModel.MaxParallelCandidates explicitly.
const DefaultMaxParallelCandidates = 8

// BestOfNModel implements the VirtualModel interface for Best-of-N.
type BestOfNModel struct {
	adapterID      string
	judgeAdapterID string
	n              int
	// MaxParallelCandidates caps the number of concurrent candidate
	// adapter calls in generateCandidates. <=0 falls back to
	// DefaultMaxParallelCandidates. The effective cap is additionally
	// clamped to min(n, MaxParallelCandidates) so we never spawn more
	// goroutines than there are candidates.
	MaxParallelCandidates int
	channelSvc            vm_moa.ChannelService
	optimize              *optimize.Runtime
}

// NewBestOfNModel creates a Best-of-N virtual model instance.
func NewBestOfNModel(adapterID, judgeAdapterID string, n int, channelSvc vm_moa.ChannelService, optRuntime *optimize.Runtime) *BestOfNModel {
	if n <= 0 {
		n = 3
	}
	if judgeAdapterID == "" {
		judgeAdapterID = adapterID
	}
	return &BestOfNModel{
		adapterID:      adapterID,
		judgeAdapterID: judgeAdapterID,
		n:              n,
		channelSvc:     channelSvc,
		optimize:       optRuntime,
	}
}

func (m *BestOfNModel) ID() string          { return ModelID }
func (m *BestOfNModel) DisplayName() string  { return DisplayName }
func (m *BestOfNModel) Enabled() bool {
	return m != nil && m.adapterID != ""
}

// AdapterMetadata 返回 Best-of-N 虚拟模型的适配器元数据。
func (m *BestOfNModel) AdapterMetadata(_ context.Context) virtualmodel.AdapterMetadata {
	return virtualmodel.AdapterMetadata{TooltipData: "Best-of-N — 多次采样取最优，适合高方差任务"}
}

// HasChannelService reports whether a production/test ChannelService is injected.
func (m *BestOfNModel) HasChannelService() bool {
	return m != nil && m.channelSvc != nil
}

// Execute runs the Best-of-N workflow.
func (m *BestOfNModel) Execute(ctx context.Context, req *virtualmodel.ExecuteRequest) (*virtualmodel.ExecuteResult, error) {
	if req == nil {
		return nil, fmt.Errorf("execute request is nil")
	}
	if m.channelSvc == nil {
		return nil, fmt.Errorf("channel service is nil (production must inject non-nil)")
	}

	startTime := time.Now()
	userText := extractUserText(req.Messages)

	// Step 1: Generate N candidates in parallel
	candidates := m.generateCandidates(ctx, userText)

	// If only one candidate or all failed, return first
	validCount := 0
	for _, c := range candidates {
		if c != "" {
			validCount++
		}
	}
	if validCount == 0 {
		return &virtualmodel.ExecuteResult{
			Text: "No candidates generated.",
			NodeResults: []virtualmodel.NodeExecuteResult{{
				NodeID:     "bestOfN",
				AdapterID:  m.adapterID,
				Success:    false,
				DurationMS: time.Since(startTime).Milliseconds(),
			}},
		}, nil
	}
	if validCount == 1 {
		for _, c := range candidates {
			if c != "" {
				return &virtualmodel.ExecuteResult{
					Text: c,
					NodeResults: []virtualmodel.NodeExecuteResult{{
						NodeID:     "bestOfN",
						AdapterID:  m.adapterID,
						Success:    true,
						OutputText: c,
						DurationMS: time.Since(startTime).Milliseconds(),
					}},
				}, nil
			}
		}
	}

	// Step 2: Judge selects the best candidate
	bestIdx, err := m.judge(ctx, userText, candidates)
	if err != nil || bestIdx < 0 || bestIdx >= len(candidates) || candidates[bestIdx] == "" {
		// Fallback: return first valid candidate
		for _, c := range candidates {
			if c != "" {
				bestIdx = -1
				_ = c
				break
			}
		}
		if bestIdx >= 0 && bestIdx < len(candidates) && candidates[bestIdx] != "" {
			// good
		} else {
			// return first non-empty
			for i, c := range candidates {
				if c != "" {
					bestIdx = i
					break
				}
			}
		}
	}

	var finalText string
	if bestIdx >= 0 && bestIdx < len(candidates) {
		finalText = candidates[bestIdx]
	} else {
		finalText = candidates[0]
	}

	return &virtualmodel.ExecuteResult{
		Text: finalText,
		NodeResults: []virtualmodel.NodeExecuteResult{{
			NodeID:     "bestOfN",
			AdapterID:  m.adapterID,
			Success:    true,
			OutputText: finalText,
			DurationMS: time.Since(startTime).Milliseconds(),
		}},
	}, nil
}

// generateCandidates generates N candidates in parallel.
//
// Concurrency is bounded by MaxParallelCandidates (default
// DefaultMaxParallelCandidates) to avoid spawning m.n concurrent HTTP
// calls when the user sets a high N. The semaphore is acquired before
// spawning the worker goroutine so ctx cancellation is honored even
// while waiting for a slot.
func (m *BestOfNModel) generateCandidates(ctx context.Context, userText string) []string {
	candidates := make([]string, m.n)
	var wg sync.WaitGroup

	maxParallel := m.MaxParallelCandidates
	if maxParallel <= 0 {
		maxParallel = DefaultMaxParallelCandidates
	}
	if maxParallel > m.n {
		maxParallel = m.n
	}
	if maxParallel < 1 {
		maxParallel = 1
	}
	sem := make(chan struct{}, maxParallel)
	ctxCancelled := false

	for i := 0; i < m.n; i++ {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			ctxCancelled = true
		}
		if ctxCancelled {
			break
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			messages := []vm_moa.Message{{Role: "user", Content: userText}}
			result, err := m.callAdapter(ctx, messages, "")
			if err != nil {
				candidates[idx] = ""
				return
			}
			candidates[idx] = result.Text
		}(i)
	}

	wg.Wait()
	return candidates
}

// judge asks the Judge model to select the best candidate.
func (m *BestOfNModel) judge(ctx context.Context, userText string, candidates []string) (int, error) {
	var sb strings.Builder
	sb.WriteString("User question: ")
	sb.WriteString(userText)
	sb.WriteString("\n\nBelow are ")
	sb.WriteString(fmt.Sprintf("%d", len(candidates)))
	sb.WriteString(" candidate answers. Select the BEST one based on accuracy, completeness, and clarity.\n\n")

	for i, c := range candidates {
		if c == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("--- Candidate %d ---\n%s\n\n", i+1, c))
	}

	sb.WriteString("Output ONLY the number of the best candidate (e.g., 1, 2, 3).")

	messages := []vm_moa.Message{{Role: "user", Content: sb.String()}}
	result, err := m.callAdapter(ctx, messages, "You are a Judge. Select the best answer.")
	if err != nil {
		return -1, err
	}

	// Parse the number from the judge's response
	return parseJudgeResponse(result.Text, len(candidates)), nil
}

// callAdapter resolves the channel and calls the physical model.
func (m *BestOfNModel) callAdapter(ctx context.Context, messages []vm_moa.Message, systemPrompt string) (*vm_moa.AdapterResult, error) {
	if m.channelSvc == nil {
		return nil, fmt.Errorf("channel service is nil")
	}
	info, err := m.channelSvc.ResolveChannel(ctx, m.adapterID)
	if err != nil {
		return nil, fmt.Errorf("resolve channel %s: %w", m.adapterID, err)
	}
	result, err := m.channelSvc.CallAdapter(ctx, info, messages, systemPrompt)
	if err != nil {
		return nil, err
	}
	// Record cost to Optimization Runtime
	if m.optimize != nil && result != nil {
		m.optimize.RecordCost(m.adapterID, result.PromptTokens, result.CompletionTokens)
	}
	return result, nil
}

// parseJudgeResponse extracts a candidate number from the judge's text.
func parseJudgeResponse(text string, max int) int {
	text = strings.TrimSpace(text)
	// Try to find a number in the text
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		// Try to parse as a number
		var n int
		if _, err := fmt.Sscanf(line, "%d", &n); err == nil && n >= 1 && n <= max {
			return n - 1 // 0-indexed
		}
	}
	// Try to find any number in the text
	for i := 0; i < len(text); i++ {
		if text[i] >= '0' && text[i] <= '9' {
			var n int
			if _, err := fmt.Sscanf(text[i:], "%d", &n); err == nil && n >= 1 && n <= max {
				return n - 1
			}
		}
	}
	return -1
}

// extractUserText gets the latest user message from the request.
// Delegates to the shared virtualmodel.LastUserMessage to avoid drift across
// the AOS / VM providers.
func extractUserText(messages []virtualmodel.Message) string {
	return virtualmodel.LastUserMessage(messages)
}
