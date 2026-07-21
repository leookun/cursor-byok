// Package debate implements the Debate Virtual Model.
//
// Two Agents generate initial answers, then critique each other for N rounds,
// and a Judge selects the winner.
//
// Based on AI Safety via Debate (arxiv.org/abs/1805.00899) and
// Multi-Agent Debate (arxiv.org/abs/2305.14325).
// Design: ADR-018. Research: docs/research/debate-virtual-model.md.
package debate

import (
	"context"
	"fmt"
	"strings"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
	optimize "cursor/internal/backend/runtime/optimize"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

const ModelID = "debate"
const DisplayName = "Debate"

// DebateModel implements the VirtualModel interface for Debate.
type DebateModel struct {
	adapterA      string
	adapterB      string
	judgeAdapter  string
	rounds        int
	channelSvc    vm_moa.ChannelService
	optimize      *optimize.Runtime
}

// NewDebateModel creates a Debate virtual model instance.
func NewDebateModel(adapterA, adapterB, judgeAdapter string, rounds int, channelSvc vm_moa.ChannelService, optRuntime *optimize.Runtime) *DebateModel {
	if rounds <= 0 {
		rounds = 2
	}
	if adapterB == "" {
		adapterB = adapterA
	}
	if judgeAdapter == "" {
		judgeAdapter = adapterA
	}
	return &DebateModel{
		adapterA:     adapterA,
		adapterB:     adapterB,
		judgeAdapter: judgeAdapter,
		rounds:       rounds,
		channelSvc:   channelSvc,
		optimize:     optRuntime,
	}
}

func (m *DebateModel) ID() string          { return ModelID }
func (m *DebateModel) DisplayName() string  { return DisplayName }
func (m *DebateModel) Enabled() bool {
	return m != nil && m.adapterA != ""
}

// AdapterMetadata 返回 Debate 虚拟模型的适配器元数据。
func (m *DebateModel) AdapterMetadata(_ context.Context) virtualmodel.AdapterMetadata {
	return virtualmodel.AdapterMetadata{TooltipData: "Debate — 两代理辩论取最优，适合观点对抗与批判性分析"}
}

// HasChannelService reports whether a production/test ChannelService is injected.
func (m *DebateModel) HasChannelService() bool {
	return m != nil && m.channelSvc != nil
}

// Execute runs the Debate workflow.
func (m *DebateModel) Execute(ctx context.Context, req *virtualmodel.ExecuteRequest) (*virtualmodel.ExecuteResult, error) {
	if req == nil {
		return nil, fmt.Errorf("execute request is nil")
	}
	if m.channelSvc == nil {
		return nil, fmt.Errorf("channel service is nil (production must inject non-nil)")
	}

	startTime := time.Now()
	userText := extractUserText(req.Messages)

	// Step 1: Both agents generate initial answers
	answerA, err := m.generate(ctx, m.adapterA, userText, "You are Agent A. Answer the question directly.")
	if err != nil {
		answerA = ""
	}
	answerB, err := m.generate(ctx, m.adapterB, userText, "You are Agent B. Answer the question directly.")
	if err != nil {
		answerB = ""
	}

	if answerA == "" && answerB == "" {
		return &virtualmodel.ExecuteResult{
			Text: "No answers generated.",
			NodeResults: []virtualmodel.NodeExecuteResult{{
				NodeID:     "debate",
				Success:    false,
				DurationMS: time.Since(startTime).Milliseconds(),
			}},
		}, nil
	}

	// Step 2: N rounds of critique + revision
	for round := 0; round < m.rounds; round++ {
		// Agent A critiques B and revises
		if answerB != "" {
			answerA = m.critiqueAndRevise(ctx, m.adapterA, userText, answerA, answerB, "A")
		}
		// Agent B critiques A and revises
		if answerA != "" {
			answerB = m.critiqueAndRevise(ctx, m.adapterB, userText, answerB, answerA, "B")
		}
	}

	// Step 3: Judge selects the winner
	winner := m.judge(ctx, userText, answerA, answerB)

	var finalText string
	if winner == "B" && answerB != "" {
		finalText = answerB
	} else {
		finalText = answerA
	}
	if finalText == "" {
		finalText = answerB
	}

	return &virtualmodel.ExecuteResult{
		Text: finalText,
		NodeResults: []virtualmodel.NodeExecuteResult{{
			NodeID:     "debate",
			AdapterID:  m.adapterA,
			Success:    true,
			OutputText: finalText,
			DurationMS: time.Since(startTime).Milliseconds(),
		}},
	}, nil
}

// generate produces an initial answer from the given adapter.
func (m *DebateModel) generate(ctx context.Context, adapterID, userText, systemPrompt string) (string, error) {
	messages := []vm_moa.Message{{Role: "user", Content: userText}}
	result, err := m.callAdapter(ctx, adapterID, messages, systemPrompt)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// critiqueAndRevise asks an agent to critique the opponent and revise its own answer.
func (m *DebateModel) critiqueAndRevise(ctx context.Context, adapterID, userText, myAnswer, opponentAnswer, agentLabel string) string {
	prompt := fmt.Sprintf(
		"User question: %s\n\n"+
			"Your current answer:\n%s\n\n"+
			"Opponent's answer:\n%s\n\n"+
			"Critique the opponent's answer, identify flaws or missing points, then provide your REVISED answer. Output only your revised answer.",
		userText, myAnswer, opponentAnswer)

	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	systemPrompt := fmt.Sprintf("You are Agent %s in a debate. Be rigorous and fair.", agentLabel)
	result, err := m.callAdapter(ctx, adapterID, messages, systemPrompt)
	if err != nil {
		return myAnswer // keep old answer if revision fails
	}
	return result.Text
}

// judge asks the Judge model to select the better answer.
func (m *DebateModel) judge(ctx context.Context, userText, answerA, answerB string) string {
	prompt := fmt.Sprintf(
		"User question: %s\n\n"+
			"Agent A's final answer:\n%s\n\n"+
			"Agent B's final answer:\n%s\n\n"+
			"Which answer is better? Output ONLY 'A' or 'B'.",
		userText, answerA, answerB)

	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	result, err := m.callAdapter(ctx, m.judgeAdapter, messages, "You are a Judge. Select the better answer.")
	if err != nil {
		return "A" // default
	}

	text := strings.ToUpper(strings.TrimSpace(result.Text))
	if strings.Contains(text, "B") {
		return "B"
	}
	return "A"
}

// callAdapter resolves the channel and calls the physical model.
func (m *DebateModel) callAdapter(ctx context.Context, adapterID string, messages []vm_moa.Message, systemPrompt string) (*vm_moa.AdapterResult, error) {
	if m.channelSvc == nil {
		return nil, fmt.Errorf("channel service is nil")
	}
	info, err := m.channelSvc.ResolveChannel(ctx, adapterID)
	if err != nil {
		return nil, fmt.Errorf("resolve channel %s: %w", adapterID, err)
	}
	result, err := m.channelSvc.CallAdapter(ctx, info, messages, systemPrompt)
	if err != nil {
		return nil, err
	}
	if m.optimize != nil && result != nil {
		m.optimize.RecordCost(adapterID, result.PromptTokens, result.CompletionTokens)
	}
	return result, nil
}

// extractUserText gets the latest user message from the request.
// Delegates to the shared virtualmodel.LastUserMessage to avoid drift across
// the AOS / VM providers.
func extractUserText(messages []virtualmodel.Message) string {
	return virtualmodel.LastUserMessage(messages)
}
