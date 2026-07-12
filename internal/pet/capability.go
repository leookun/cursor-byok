package pet

// PetCapability 是宠物声明支持的能力标识（Phase 10）。
// 用于 Manifest v2 中的能力声明、引擎与前端的能力协商。
type PetCapability string

const (
	// CapDisplay 宠物可渲染显示（基本能力）。
	CapDisplay PetCapability = "display"
	// CapDrag 宠物支持拖拽交互。
	CapDrag PetCapability = "drag"
	// CapBehavior 宠物支持自主行为（walk/jump/wave/sit/sleep）。
	CapBehavior PetCapability = "behavior"
	// CapAnimation 宠物有完整动画定义（非单帧图片）。
	CapAnimation PetCapability = "animation"
	// CapPlugin 宠物支持插件扩展。
	CapPlugin PetCapability = "plugin"
	// CapAgent AI Agent 集成（如代码审查）。
	CapAgent PetCapability = "agent"
	// CapMotion 支持平滑移动。
	CapMotion PetCapability = "motion"
	// CapSound 支持音效（预留）。
	CapSound PetCapability = "sound"
	// CapMultiState 支持多状态机（非仅 idle/walk 两种）。
	CapMultiState PetCapability = "multi_state"
)

// CapabilitySet 是能力的集合，支持快速查找与交集运算。
type CapabilitySet map[PetCapability]bool

// NewCapabilitySet 从字符串列表构建能力集合。
func NewCapabilitySet(caps []string) CapabilitySet {
	s := make(CapabilitySet, len(caps))
	for _, c := range caps {
		s[PetCapability(c)] = true
	}
	return s
}

// DefaultCapabilities 返回引擎默认支持的能力集合（Display + Drag + Behavior）。
func DefaultCapabilities() CapabilitySet {
	return NewCapabilitySet([]string{"display", "drag", "behavior"})
}

// Has 检查是否拥有某项能力。
func (s CapabilitySet) Has(c PetCapability) bool {
	return s[PetCapability(c)]
}

// Intersect 返回两个能力集合的交集。
// 用于引擎支持的能力 ∩ 宠物声明的能力，确定实际可用能力。
func (s CapabilitySet) Intersect(o CapabilitySet) CapabilitySet {
	result := make(CapabilitySet)
	for c := range s {
		if o[c] {
			result[c] = true
		}
	}
	return result
}

// ToStrings 转换为字符串切片（用于 JSON 序列化）。
func (s CapabilitySet) ToStrings() []string {
	result := make([]string, 0, len(s))
	for c := range s {
		result = append(result, string(c))
	}
	return result
}

// AllCapabilities 返回引擎支持的全部能力列表（用于向插件声明）。
func AllCapabilities() []string {
	return []string{
		string(CapDisplay),
		string(CapDrag),
		string(CapBehavior),
		string(CapAnimation),
		string(CapPlugin),
		string(CapAgent),
		string(CapMotion),
		string(CapSound),
		string(CapMultiState),
	}
}

// NegotiateCapabilities 能力协商：返回引擎支持 ∩ 宠物声明的可用能力。
// engine 是引擎支持的能力（通常 AllCapabilities() 或子集）。
// pet 是宠物声明的能力（来自 Manifest.Capabilities）。
func NegotiateCapabilities(engine CapabilitySet, pet CapabilitySet) CapabilitySet {
	if engine == nil {
		engine = DefaultCapabilities()
	}
	if pet == nil {
		pet = DefaultCapabilities()
	}
	return engine.Intersect(pet)
}
