// cost_tracking.go extracts cost tracking functions from service.go (TD-002).
// Contains: recordProviderCost, GetCostSummary.
package forwarder

import (
	"strings"

	optimize "cursor/internal/backend/runtime/optimize"
)

// recordProviderCost 记录一次 provider 调用的成本到 Optimization Runtime。
// providerName 优先使用流事件中的 provider；否则回退到 ModelID / knobs。
func (service *Service) recordProviderCost(request ProviderRequest, providerName string, promptTokens, outputTokens int) {
	if service == nil || service.optimize == nil {
		return
	}
	name := strings.TrimSpace(providerName)
	if name == "" && request.RequestKnobs != nil {
		if v, ok := request.RequestKnobs["provider"].(string); ok {
			name = strings.TrimSpace(v)
		}
	}
	if name == "" {
		name = strings.TrimSpace(request.ModelID)
	}
	if promptTokens < 0 {
		promptTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	service.optimize.RecordCost(name, promptTokens, outputTokens)
}

// GetCostSummary 获取 Optimization Runtime 的成本摘要（供 Wails 前端调用）。
func (service *Service) GetCostSummary() *optimize.CostTracker {
	if service == nil || service.optimize == nil {
		return &optimize.CostTracker{}
	}
	return service.optimize.GetCostSummary()
}