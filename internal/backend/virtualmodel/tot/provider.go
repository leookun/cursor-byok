// Package tot implements the Tree-of-Thought Virtual Model.
//
// ToT generates K thoughts at each level, evaluates them, and greedily
// selects the best to continue. After D levels, generates a final answer.
//
// Based on Tree of Thoughts (arxiv.org/abs/2305.10601).
// Design: ADR-019. Research: docs/research/tree-of-thought-virtual-model.md.
package tot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
	optimize "cursor/internal/backend/runtime/optimize"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

const ModelID = "treeOfThought"
const DisplayName = "Tree-of-Thought"

// TreeOfThoughtModel implements the VirtualModel interface for Tree-of-Thought.
type TreeOfThoughtModel struct {
	adapterID   string
	branches    int // K: thoughts per level
	depth       int // D: search depth
	channelSvc  vm_moa.ChannelService
	optimize    *optimize.Runtime
}

// NewTreeOfThoughtModel creates a Tree-of-Thought virtual model instance.
func NewTreeOfThoughtModel(adapterID string, branches, depth int, channelSvc vm_moa.ChannelService, optRuntime *optimize.Runtime) *TreeOfThoughtModel {
	if branches <= 0 {
		branches = 3
	}
	if depth <= 0 {
		depth = 2
	}
	return &TreeOfThoughtModel{
		adapterID:  adapterID,
		branches:   branches,
		depth:      depth,
		channelSvc: channelSvc,
		optimize:   optRuntime,
	}
}

func (m *TreeOfThoughtModel) ID() string          { return ModelID }
func (m *TreeOfThoughtModel) DisplayName() string  { return DisplayName }
func (m *TreeOfThoughtModel) Enabled() bool {
	return m != nil && m.adapterID != ""
}

// AdapterMetadata 返回 Tree-of-Thought 虚拟模型的适配器元数据。
func (m *TreeOfThoughtModel) AdapterMetadata(_ context.Context) virtualmodel.AdapterMetadata {
	return virtualmodel.AdapterMetadata{TooltipData: "Tree-of-Thought — 树状推理路径搜索，适合多步骤规划"}
}

// HasChannelService reports whether a production/test ChannelService is injected.
func (m *TreeOfThoughtModel) HasChannelService() bool {
	return m != nil && m.channelSvc != nil
}

// Execute runs the Tree-of-Thought workflow.
func (m *TreeOfThoughtModel) Execute(ctx context.Context, req *virtualmodel.ExecuteRequest) (*virtualmodel.ExecuteResult, error) {
	if req == nil {
		return nil, fmt.Errorf("execute request is nil")
	}
	if m.channelSvc == nil {
		return nil, fmt.Errorf("channel service is nil (production must inject non-nil)")
	}

	startTime := time.Now()
	userText := extractUserText(req.Messages)

	// Start with the user question as the root
	currentThought := userText

	// For each depth level:
	// 1. Generate K thoughts from current thought
	// 2. Evaluate each thought
	// 3. Select the best
	for level := 0; level < m.depth; level++ {
		thoughts := m.generateThoughts(ctx, userText, currentThought, level)
		if len(thoughts) == 0 {
			break
		}

		// Evaluate and select best
		bestIdx := m.evaluateAndSelect(ctx, userText, thoughts)
		if bestIdx >= 0 && bestIdx < len(thoughts) {
			currentThought = thoughts[bestIdx]
		}
	}

	// Generate final answer from the best thought path
	finalAnswer, err := m.generateAnswer(ctx, userText, currentThought)
	if err != nil {
		finalAnswer = currentThought
	}

	return &virtualmodel.ExecuteResult{
		Text: finalAnswer,
		NodeResults: []virtualmodel.NodeExecuteResult{{
			NodeID:     "tot",
			AdapterID:  m.adapterID,
			Success:    true,
			OutputText: finalAnswer,
			DurationMS: time.Since(startTime).Milliseconds(),
		}},
	}, nil
}

// generateThoughts generates K thoughts from the current thought.
func (m *TreeOfThoughtModel) generateThoughts(ctx context.Context, userText, currentThought string, level int) []string {
	thoughts := make([]string, 0, m.branches)
	prompt := fmt.Sprintf(
		"User question: %s\n\n"+
			"Current reasoning so far (level %d):\n%s\n\n"+
			"Generate a NEW reasoning step that approaches the question from a different angle. Be concise.",
		userText, level, currentThought)

	for i := 0; i < m.branches; i++ {
		messages := []vm_moa.Message{{Role: "user", Content: prompt}}
		result, err := m.callAdapter(ctx, messages, "You are an expert reasoner. Think step by step.")
		if err != nil {
			continue
		}
		if strings.TrimSpace(result.Text) != "" {
			thoughts = append(thoughts, result.Text)
		}
	}
	return thoughts
}

// evaluateAndSelect evaluates each thought and returns the index of the best one.
func (m *TreeOfThoughtModel) evaluateAndSelect(ctx context.Context, userText string, thoughts []string) int {
	if len(thoughts) == 0 {
		return -1
	}
	if len(thoughts) == 1 {
		return 0
	}

	var sb strings.Builder
	sb.WriteString("User question: ")
	sb.WriteString(userText)
	sb.WriteString("\n\nEvaluate each reasoning step on a scale of 1-10 for accuracy and relevance.\n\n")
	for i, t := range thoughts {
		sb.WriteString(fmt.Sprintf("--- Thought %d ---\n%s\n\n", i+1, t))
	}
	sb.WriteString("Output ONLY the number of the best thought (e.g., 1, 2, 3).")

	messages := []vm_moa.Message{{Role: "user", Content: sb.String()}}
	result, err := m.callAdapter(ctx, messages, "You are an evaluator. Select the best reasoning step.")
	if err != nil {
		return 0
	}

	// Parse number
	return parseEvaluation(result.Text, len(thoughts))
}

// generateAnswer produces the final answer from the best thought path.
func (m *TreeOfThoughtModel) generateAnswer(ctx context.Context, userText, bestThought string) (string, error) {
	prompt := fmt.Sprintf(
		"User question: %s\n\n"+
			"Best reasoning path:\n%s\n\n"+
			"Based on this reasoning, provide the final answer.",
		userText, bestThought)
	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	result, err := m.callAdapter(ctx, messages, "")
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// callAdapter resolves the channel and calls the physical model.
func (m *TreeOfThoughtModel) callAdapter(ctx context.Context, messages []vm_moa.Message, systemPrompt string) (*vm_moa.AdapterResult, error) {
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
	if m.optimize != nil && result != nil {
		m.optimize.RecordCost(m.adapterID, result.PromptTokens, result.CompletionTokens)
	}
	return result, nil
}

// parseEvaluation extracts the selected thought number from the evaluator's text.
func parseEvaluation(text string, max int) int {
	text = strings.TrimSpace(text)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		n, err := strconv.Atoi(line)
		if err == nil && n >= 1 && n <= max {
			return n - 1
		}
	}
	// Fallback: find any number
	for i := 0; i < len(text); i++ {
		if text[i] >= '0' && text[i] <= '9' {
			n, err := strconv.Atoi(string(text[i]))
			if err == nil && n >= 1 && n <= max {
				return n - 1
			}
		}
	}
	return 0
}

// extractUserText gets the latest user message from the request.
// Delegates to the shared virtualmodel.LastUserMessage to avoid drift across
// the AOS / VM providers.
func extractUserText(messages []virtualmodel.Message) string {
	return virtualmodel.LastUserMessage(messages)
}
