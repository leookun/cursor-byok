// channel_bridge.go 将已有 ModelAdapter / ChannelResolver 适配为 MOA ChannelService。
// 禁止在此维护任何独立 Model Registry——所有解析走用户配置的 modelAdapters。
package moa

import (
	"context"
	"fmt"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	legacyruntime "cursor/internal/runtime"
)

// ChannelResolver 与 modeladapter.ChannelResolver 对齐，由 serverconfig.Manager 等实现。
type ChannelResolver interface {
	SelectChannelForModel(context.Context, string) (*legacyruntime.ResolvedChannel, error)
	ProviderStreamIdleTimeout(context.Context) time.Duration
}

// AdapterChannelService 基于已有 ChannelResolver + ModelAdapter Router 实现 ChannelService。
// 专家节点只通过 adapterID / modelID 解析到用户配置的渠道，不新建 registry。
type AdapterChannelService struct {
	resolver ChannelResolver
	router   *modeladapter.Router
}

// NewAdapterChannelService 创建生产用 ChannelService。
// resolver 必须非 nil（通常为 *serverconfig.Manager）。
func NewAdapterChannelService(resolver ChannelResolver) *AdapterChannelService {
	if resolver == nil {
		return nil
	}
	return &AdapterChannelService{
		resolver: resolver,
		router:   modeladapter.NewRouter(resolver),
	}
}

// ResolveChannel 按 adapter channel ID 或 modelID 解析已有 ModelAdapter 渠道。
func (s *AdapterChannelService) ResolveChannel(ctx context.Context, adapterID string) (*ChannelInfo, error) {
	if s == nil || s.resolver == nil {
		return nil, fmt.Errorf("channel service is not initialized — virtual model runtime requires a ChannelService adapter")
	}
	id := strings.TrimSpace(adapterID)
	if id == "" {
		// 空 ID：尝试用占位解析失败，由调用方回退
		return nil, fmt.Errorf("adapter id is empty")
	}
	ch, err := s.resolver.SelectChannelForModel(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("resolve model adapter %q: %w", id, err)
	}
	if ch == nil {
		return nil, fmt.Errorf("no model adapter channel for %q", id)
	}
	return &ChannelInfo{
		ID:          strings.TrimSpace(ch.ID),
		DisplayName: strings.TrimSpace(ch.Name),
		ModelID:     strings.TrimSpace(ch.Model),
		Provider:    strings.TrimSpace(ch.Provider),
		BaseURL:     strings.TrimSpace(ch.BaseURL),
	}, nil
}

// CallAdapter 通过已有 ModelAdapter Router 调用物理模型（非流式聚合）。
func (s *AdapterChannelService) CallAdapter(ctx context.Context, info *ChannelInfo, messages []Message, systemPrompt string) (*AdapterResult, error) {
	if s == nil || s.router == nil {
		return nil, fmt.Errorf("channel service router is not initialized")
	}
	if info == nil {
		return nil, fmt.Errorf("channel info is nil")
	}
	modelKey := strings.TrimSpace(info.ID)
	if modelKey == "" {
		modelKey = strings.TrimSpace(info.ModelID)
	}
	if modelKey == "" {
		return nil, fmt.Errorf("channel info missing id/modelID")
	}

	adapterMsgs := make([]modeladapter.Message, 0, len(messages)+1)
	if strings.TrimSpace(systemPrompt) != "" {
		adapterMsgs = append(adapterMsgs, modeladapter.Message{Role: "system", Content: systemPrompt})
	}
	for _, m := range messages {
		adapterMsgs = append(adapterMsgs, modeladapter.Message{
			Role:    m.Role,
			Content: m.Content,
			Name:    m.Name,
		})
	}

	start := time.Now()
	var text strings.Builder
	var promptTokens, completionTokens int
	err := s.router.Stream(ctx, modeladapter.StreamRequest{
		ModelID:  modelKey,
		Messages: adapterMsgs,
		Stream:   true,
	}, func(ev modeladapter.ModelEvent) error {
		if ev.Text != "" {
			text.WriteString(ev.Text)
		}
		if ev.UsagePresent {
			if ev.InputTokens > 0 {
				promptTokens = int(ev.InputTokens)
			}
			if ev.OutputTokens > 0 {
				completionTokens = int(ev.OutputTokens)
			}
		}
		return nil
	})
	duration := time.Since(start)
	if err != nil {
		return nil, err
	}
	if promptTokens == 0 {
		for _, m := range adapterMsgs {
			promptTokens += len(m.Content) / 4
		}
	}
	if completionTokens == 0 {
		completionTokens = len(text.String()) / 4
	}
	return &AdapterResult{
		Text:             text.String(),
		FinishReason:     "stop",
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		DurationMS:       duration.Milliseconds(),
	}, nil
}

// Ensure AdapterChannelService implements ChannelService.
var _ ChannelService = (*AdapterChannelService)(nil)
