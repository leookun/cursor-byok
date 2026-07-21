// resolver.go VMR 解析器：连接虚拟模型管理与现有的 Runtime 系统。

package virtualmodel

import (
	"context"
	"fmt"
	"strings"

	legacyruntime "cursor/internal/runtime"
)

// [修复] 硬编码: 提取为命名常量, 避免魔法字符串
const (
	// VirtualAdapterType 虚拟模型适配器类型标识
	VirtualAdapterType = "virtual"
	// VirtualPlaceholderBaseURL 虚拟模型占位 BaseURL（Cursor 不会实际调用）
	VirtualPlaceholderBaseURL = ""
	// VirtualPlaceholderAPIKey 虚拟模型占位 APIKey
	VirtualPlaceholderAPIKey = "virtual"
)

// AdapterResolver 将 adapterID 解析为实际的模型渠道。
type AdapterResolver interface {
	// ResolveModelAdapters 获取所有已配置的模型适配器。
	ResolveModelAdapters(ctx context.Context) ([]legacyruntime.ModelAdapterConfig, error)
}

// VMResolver 解析虚拟模型：判断模型是否为虚拟模型、解析其配置。
type VMResolver struct {
	manager    *Manager
	adapterSvc AdapterResolver
}

// NewVMResolver 创建虚拟模型解析器。
// [修复] 空值防护: 允许 manager 和 adapterSvc 为 nil（将在各方法中防御性检查）
func NewVMResolver(manager *Manager, adapterSvc AdapterResolver) *VMResolver {
	return &VMResolver{
		manager:    manager,
		adapterSvc: adapterSvc,
	}
}

// IsVirtualModel 判断指定模型 ID 是否为虚拟模型。
func (r *VMResolver) IsVirtualModel(modelID string) bool {
	if r == nil || r.manager == nil {
		return false
	}
	return r.manager.IsVirtualModel(modelID)
}

// GetVirtualModel 获取虚拟模型实例。
func (r *VMResolver) GetVirtualModel(modelID string) (VirtualModel, bool) {
	if r == nil || r.manager == nil {
		return nil, false
	}
	return r.manager.Get(modelID)
}

// ResolveFallbackAdapterID 为虚拟模型节点解析 fallback adapter ID。
// 当节点未指定具体 adapter 时，使用第一个可用的物理 adapter。
func (r *VMResolver) ResolveFallbackAdapterID(ctx context.Context) (string, error) {
	if r == nil || r.adapterSvc == nil {
		return "", fmt.Errorf("adapter resolver is unavailable")
	}
	adapters, err := r.adapterSvc.ResolveModelAdapters(ctx)
	if err != nil {
		return "", err
	}
	if len(adapters) == 0 {
		return "", fmt.Errorf("no model adapters configured")
	}
	return strings.TrimSpace(adapters[0].ID), nil
}

// BuildVirtualModelAdapterConfigs 为已启用的虚拟模型构建 ModelAdapterConfig 条目。
// 这些条目将被合并到 AvailableModels 响应中，使 Cursor 能看到 MOA/AOS 等虚拟模型。
//
// 禁止在此处调用 adapterSvc.ResolveModelAdapters：host.serverSystemSettings
// 把 VMResolver 的 adapterSvc 设为自己，而 ResolveModelAdapters 又会 Merge
// 虚拟模型，形成 AvailableModels 路径上的无限递归（Cursor 拉模型列表时整机卡死）。
func (r *VMResolver) BuildVirtualModelAdapterConfigs(ctx context.Context) []legacyruntime.ModelAdapterConfig {
	if r == nil || r.manager == nil {
		return nil
	}

	var configs []legacyruntime.ModelAdapterConfig
	for _, model := range r.manager.List() {
		if !model.Enabled() {
			continue
		}
		// AdapterMetadata 必须快速、无网络、不可再进入 ResolveModelAdapters。
		meta := model.AdapterMetadata(ctx)
		tooltip := strings.TrimSpace(meta.TooltipData)
		if tooltip == "" {
			tooltip = model.DisplayName()
		}
		// Context window 默认值：Cursor 侧部分逻辑对 0 窗口不友好。
		// 未继承到物理 adapter 元数据时给 200k，与 local_runtime 默认一致。
		ctxWin := meta.ContextWindowTokens
		if ctxWin <= 0 {
			ctxWin = 200_000
		}
		maxOut := meta.MaxCompletionTokens
		if maxOut <= 0 {
			maxOut = 65_536
		}
		configs = append(configs, legacyruntime.ModelAdapterConfig{
			ID:                      model.ID(),
			DisplayName:             model.DisplayName(),
			Type:                    VirtualAdapterType,
			BaseURL:                 VirtualPlaceholderBaseURL,
			APIKey:                  VirtualPlaceholderAPIKey,
			TooltipData:             tooltip,
			ModelID:                 model.ID(),
			ContextWindowTokens:     ctxWin,
			MaxCompletionTokens:     maxOut,
			ReasoningEffort:         meta.ReasoningEffort,
			AnthropicThinkingEffort: meta.AnthropicThinkingEffort,
			ThinkingBudgetTokens:    meta.ThinkingBudgetTokens,
			AnthropicMaxTokens:      meta.AnthropicMaxTokens,
		})
	}
	// 无论如何都正常返回——不暴露日志库依赖
	return configs
}

// MergeVirtualModelAdapters 将虚拟模型的 adapter 条目合并到物理模型 adapter 列表中。
// [修复] 空值防护: 增加 nil receiver 检查
func (r *VMResolver) MergeVirtualModelAdapters(ctx context.Context, physicalAdapters []legacyruntime.ModelAdapterConfig) []legacyruntime.ModelAdapterConfig {
	if r == nil || r.manager == nil {
		return physicalAdapters
	}
	virtualAdapters := r.BuildVirtualModelAdapterConfigs(ctx)
	if len(virtualAdapters) == 0 {
		return physicalAdapters
	}
	// 虚拟模型放在列表前面，作为默认选择
	merged := make([]legacyruntime.ModelAdapterConfig, 0, len(virtualAdapters)+len(physicalAdapters))
	merged = append(merged, virtualAdapters...)
	merged = append(merged, physicalAdapters...)
	return merged
}
