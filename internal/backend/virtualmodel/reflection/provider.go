// Package reflection implements the Reflection Virtual Model.
//
// Reflection performs multi-round self-improvement:
// Generate -> Critique -> Refine -> Critique -> Refine -> ... -> Final
//
// Based on Self-Refine (arxiv.org/abs/2303.17651) and Reflexion (arxiv.org/abs/2303.11366).
// Design: ADR-015. Research: docs/research/reflection-virtual-model.md.
package reflection

import (
	"context"
	"fmt"
	"strings"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
	optimize "cursor/internal/backend/runtime/optimize"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

const ModelID = "reflection"
const DisplayName = "Reflection"

const defaultCritiquePrompt = "Review your previous answer for errors, omissions, and logical flaws. If the answer is correct and complete, respond with exactly NO_ISSUES. Otherwise, describe the problems found."

const defaultRefinePrompt = "Improve your answer based on the following critique. Output only the improved answer."

const noIssuesMarker = "NO_ISSUES"

// ReflectionModel implements the VirtualModel interface for Reflection.
type ReflectionModel struct {
	adapterID      string
	maxIterations  int
	critiquePrompt string
	channelSvc     vm_moa.ChannelService
	optimize       *optimize.Runtime
}

// NewReflectionModel creates a Reflection virtual model instance.
// channelSvc must be non-nil in production (ADR-002).
func NewReflectionModel(adapterID string, maxIterations int, channelSvc vm_moa.ChannelService, optRuntime *optimize.Runtime) *ReflectionModel {
	if maxIterations <= 0 {
		maxIterations = 3
	}
	return &ReflectionModel{
		adapterID:      adapterID,
		maxIterations:  maxIterations,
		critiquePrompt: defaultCritiquePrompt,
		channelSvc:     channelSvc,
		optimize:       optRuntime,
	}
}

func (m *ReflectionModel) ID() string          { return ModelID }
func (m *ReflectionModel) DisplayName() string  { return DisplayName }
func (m *ReflectionModel) Enabled() bool {
	return m != nil && m.adapterID != ""
}

// AdapterMetadata 返回 Reflection 虚拟模型的适配器元数据（从绑定 adapter 继承 tooltip）。
func (m *ReflectionModel) AdapterMetadata(_ context.Context) virtualmodel.AdapterMetadata {
	return virtualmodel.AdapterMetadata{TooltipData: "Reflection — 自我反思与批判循环，逐步精炼答案"}
}

// HasChannelService reports whether a production/test ChannelService is injected.
func (m *ReflectionModel) HasChannelService() bool {
	return m != nil && m.channelSvc != nil
}

// Execute runs the Reflection workflow.
func (m *ReflectionModel) Execute(ctx context.Context, req *virtualmodel.ExecuteRequest) (*virtualmodel.ExecuteResult, error) {
	if req == nil {
		return nil, fmt.Errorf("execute request is nil")
	}
	if m.channelSvc == nil {
		return nil, fmt.Errorf("channel service is nil (production must inject non-nil)")
	}

	startTime := time.Now()
	userText := extractUserText(req.Messages)

	// Step 1: Generate initial answer
	currentAnswer, err := m.generate(ctx, userText)
	if err != nil {
		return nil, fmt.Errorf("generate failed: %w", err)
	}

	// Step 2: Iterate: Critique -> Refine
	for i := 0; i < m.maxIterations; i++ {
		critique, err := m.critique(ctx, currentAnswer)
		if err != nil {
			// Fail open: return current answer if critique fails
			break
		}

		// Check if critique says no issues
		if strings.Contains(strings.ToUpper(strings.TrimSpace(critique)), noIssuesMarker) {
			break
		}

		// Refine based on critique
		refined, err := m.refine(ctx, currentAnswer, critique)
		if err != nil {
			// Fail open: return current answer if refine fails
			break
		}
		currentAnswer = refined
	}

	return &virtualmodel.ExecuteResult{
		Text: currentAnswer,
		NodeResults: []virtualmodel.NodeExecuteResult{{
			NodeID:     "reflection",
			AdapterID:  m.adapterID,
			Success:    true,
			OutputText: currentAnswer,
			DurationMS: time.Since(startTime).Milliseconds(),
		}},
	}, nil
}

// generate produces the initial answer.
func (m *ReflectionModel) generate(ctx context.Context, userText string) (string, error) {
	messages := []vm_moa.Message{{Role: "user", Content: userText}}
	result, err := m.callAdapter(ctx, messages, "")
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// critique asks the model to review its answer.
func (m *ReflectionModel) critique(ctx context.Context, answer string) (string, error) {
	prompt := fmt.Sprintf("%s\n\nAnswer to review:\n%s", m.critiquePrompt, answer)
	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	result, err := m.callAdapter(ctx, messages, "")
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// refine asks the model to improve its answer based on critique.
func (m *ReflectionModel) refine(ctx context.Context, answer, critique string) (string, error) {
	prompt := fmt.Sprintf("%s\n\nPrevious answer:\n%s\n\nCritique:\n%s", defaultRefinePrompt, answer, critique)
	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	result, err := m.callAdapter(ctx, messages, "")
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// callAdapter resolves the channel and calls the physical model.
// ponytail: duplicated across 5 VMs (reflection/tot/bestofn/debate/aos).
// Consider extracting to moa.CallAdapter when registering more VMs beyond MOA/AOS.
// Current duplication is acceptable (YAGNI) until virtualmodel variants are exposed to Cursor.
func (m *ReflectionModel) callAdapter(ctx context.Context, messages []vm_moa.Message, systemPrompt string) (*vm_moa.AdapterResult, error) {
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

// extractUserText gets the latest user message from the request.
// Delegates to the shared virtualmodel.LastUserMessage to avoid drift across
// the AOS / VM providers.
func extractUserText(messages []virtualmodel.Message) string {
	return virtualmodel.LastUserMessage(messages)
}
