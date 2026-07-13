// config/types.go 定义虚拟模型运行时的配置数据结构。

package config

// VirtualModelConfig 表示一个虚拟模型的完整配置。
type VirtualModelConfig struct {
	// Enabled 是否启用该虚拟模型。
	Enabled bool `json:"enabled" yaml:"enabled"`
	// WorkflowID 使用的 workflow 标识。
	WorkflowID string `json:"workflowID" yaml:"workflowID"`
	// Planner 规划器节点配置。
	Planner *NodeBindingConfig `json:"planner,omitempty" yaml:"planner,omitempty"`
	// Nodes 各专家节点的 adapter 绑定。
	Nodes map[string]*NodeBindingConfig `json:"nodes,omitempty" yaml:"nodes,omitempty"`
}

// NodeBindingConfig 将一个角色绑定到具体 adapter 的配置。
type NodeBindingConfig struct {
	// AdapterID 引用的 ModelAdapter ID。
	AdapterID string `json:"adapterID" yaml:"adapterID"`
	// Enabled 是否启用该节点。
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// VirtualModelsConfig 是所有虚拟模型的顶层配置。
type VirtualModelsConfig struct {
	// MOA 模型配置。
	MOA *VirtualModelConfig `json:"moa,omitempty" yaml:"moa,omitempty"`
}

// WorkflowConfig 定义工作流的结构。
type WorkflowConfig struct {
	// ID 工作流唯一标识。
	ID string `json:"id" yaml:"id"`
	// Name 工作流显示名称。
	Name string `json:"name" yaml:"name"`
	// Description 工作流描述。
	Description string `json:"description" yaml:"description"`
	// Nodes 工作流节点列表（按执行顺序）。
	Nodes []WorkflowNodeConfig `json:"nodes" yaml:"nodes"`
}

// WorkflowNodeConfig 定义工作流中的一个节点。
type WorkflowNodeConfig struct {
	// ID 节点唯一标识。
	ID string `json:"id" yaml:"id"`
	// Role 节点角色。
	Role NodeRole `json:"role" yaml:"role"`
	// ExecutionMode 执行模式。
	ExecutionMode NodeExecutionMode `json:"executionMode" yaml:"executionMode"`
	// Dependencies 依赖的其他节点 ID。
	Dependencies []string `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	// TimeoutSeconds 超时时间（秒），0 表示使用默认值。
	TimeoutSeconds int `json:"timeoutSeconds" yaml:"timeoutSeconds"`
	// RetryCount 失败重试次数。
	RetryCount int `json:"retryCount" yaml:"retryCount"`
	// Enabled 是否启用。
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Prompt 自定义 prompt（覆盖默认角色 prompt）。
	Prompt string `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	// SystemPrompt 自定义系统 prompt 追加内容。
	SystemPrompt string `json:"systemPrompt,omitempty" yaml:"systemPrompt,omitempty"`
}

// NodeRole 节点角色类型。
type NodeRole string

const (
	RolePlanner     NodeRole = "planner"
	RoleCoding      NodeRole = "coding"
	RoleResearch    NodeRole = "research"
	RoleReasoning   NodeRole = "reasoning"
	RoleVision      NodeRole = "vision"
	RoleMemory      NodeRole = "memory"
	RoleTool        NodeRole = "tool"
	RoleCritic      NodeRole = "critic"
	RoleJudge       NodeRole = "judge"
	RoleAggregator  NodeRole = "aggregator"
	RoleSecurity    NodeRole = "security"
	RoleCustom      NodeRole = "custom"
)

// NodeExecutionMode 节点执行模式。
type NodeExecutionMode string

const (
	// ModeSequential 顺序执行。
	ModeSequential NodeExecutionMode = "sequential"
	// ModeParallel 并行执行。
	ModeParallel NodeExecutionMode = "parallel"
	// ModeConditional 条件执行（由 planner 决定是否执行）。
	ModeConditional NodeExecutionMode = "conditional"
	// ModeAlways 总是执行。
	ModeAlways NodeExecutionMode = "always"
	// ModeOptional 可选执行。
	ModeOptional NodeExecutionMode = "optional"
)

// MOAModelID 是 MOA 虚拟模型在 AvailableModels 中的 channel ID。
const MOAModelID = "moa"

// MOADisplayName 是 MOA 的显示名称。
const MOADisplayName = "MOA"

// MOATooltipData 是 MOA 在模型选择器中的描述。
const MOATooltipData = "Multi-model Orchestration Architecture — 多模型协作，自动调度多个专家协同完成任务"

// DefaultMOAWorkflowID 是 MOA 默认工作流的标识。
const DefaultMOAWorkflowID = "moa-default"

// DefaultPlannerAdapterID 是 planner 默认使用的 adapter 标识（为空表示使用第一个已配置的 adapter）。
const DefaultPlannerAdapterID = ""

// IsVirtualModelID 判断给定 modelID 是否为虚拟模型。
func IsVirtualModelID(modelID string) bool {
	switch modelID {
	case MOAModelID:
		return true
	default:
		return false
	}
}

// DefaultMOAWorkflow 返回 MOA 的默认工作流配置。
func DefaultMOAWorkflow() *WorkflowConfig {
	return &WorkflowConfig{
		ID:          DefaultMOAWorkflowID,
		Name:        "MOA Default",
		Description: "标准 MOA 工作流：Planner → 多专家并行 → Critic → Judge → Aggregator",
		Nodes: []WorkflowNodeConfig{
			{
				ID:            "planner",
				Role:          RolePlanner,
				ExecutionMode: ModeAlways,
				Enabled:       true,
			},
			{
				ID:            "coding",
				Role:          RoleCoding,
				ExecutionMode: ModeConditional,
				Dependencies:  []string{"planner"},
				Enabled:       true,
			},
			{
				ID:            "research",
				Role:          RoleResearch,
				ExecutionMode: ModeConditional,
				Dependencies:  []string{"planner"},
				Enabled:       true,
			},
			{
				ID:            "reasoning",
				Role:          RoleReasoning,
				ExecutionMode: ModeConditional,
				Dependencies:  []string{"planner"},
				Enabled:       true,
			},
			{
				ID:            "critic",
				Role:          RoleCritic,
				ExecutionMode: ModeSequential,
				Dependencies:  []string{"coding", "research", "reasoning"},
				Enabled:       true,
			},
			{
				ID:            "judge",
				Role:          RoleJudge,
				ExecutionMode: ModeSequential,
				Dependencies:  []string{"critic"},
				Enabled:       true,
			},
			{
				ID:            "aggregator",
				Role:          RoleAggregator,
				ExecutionMode: ModeSequential,
				Dependencies:  []string{"judge"},
				Enabled:       true,
			},
		},
	}
}

// DefaultMOAConfig 返回 MOA 的默认配置。
func DefaultMOAConfig() *VirtualModelConfig {
	return &VirtualModelConfig{
		Enabled:    false,
		WorkflowID: DefaultMOAWorkflowID,
		Planner: &NodeBindingConfig{
			AdapterID: "",
			Enabled:   true,
		},
		Nodes: map[string]*NodeBindingConfig{
			"coding":    {AdapterID: "", Enabled: true},
			"research":  {AdapterID: "", Enabled: true},
			"reasoning": {AdapterID: "", Enabled: true},
			"critic":    {AdapterID: "", Enabled: true},
			"judge":     {AdapterID: "", Enabled: true},
			"aggregator": {AdapterID: "", Enabled: true},
		},
	}
}

// DefaultVirtualModelsConfig 返回默认的虚拟模型配置。
func DefaultVirtualModelsConfig() *VirtualModelsConfig {
	return &VirtualModelsConfig{
		MOA: DefaultMOAConfig(),
	}
}
