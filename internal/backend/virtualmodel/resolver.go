// resolver.go VMR 解析器：连接虚拟模型管理与现有的 Runtime 系统。

package virtualmodel

import (
	"context"
	"fmt"
	"strings"

	legacyruntime "cursor/internal/runtime"
	vmconfig "cursor/internal/backend/virtualmodel/config"
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
// 这些条目将被合并到 AvailableModels 响应中，使 Cursor 能看到 MOA 等虚拟模型。
func (r *VMResolver) BuildVirtualModelAdapterConfigs(ctx context.Context) []legacyruntime.ModelAdapterConfig {
	if r == nil || r.manager == nil {
		return nil
	}

	// 尝试解析 fallback adapter（用于判断是否有 adapter 可用）
	if _, err := r.ResolveFallbackAdapterID(ctx); err != nil {
		// 没有物理 adapter 时仍可注册虚拟模型（只是无法实际执行）
		_ = err
	}

	var configs []legacyruntime.ModelAdapterConfig
	for _, model := range r.manager.List() {
		if !model.Enabled() {
			continue
		}
		configs = append(configs, legacyruntime.ModelAdapterConfig{
			ID:          model.ID(),
			DisplayName: model.DisplayName(),
			Type:        "virtual",
			BaseURL:     "http://127.0.0.1:18090", // 指向本地 backend（实际不会调用，仅占位）
			APIKey:      "virtual",
			TooltipData: vmconfig.MOATooltipData,
			ModelID:     model.ID(),
		})
	}
	return configs
}

// MergeVirtualModelAdapters 将虚拟模型的 adapter 条目合并到物理模型 adapter 列表中。
func (r *VMResolver) MergeVirtualModelAdapters(ctx context.Context, physicalAdapters []legacyruntime.ModelAdapterConfig) []legacyruntime.ModelAdapterConfig {
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
