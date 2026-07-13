// moa/default_workflow.go MOA 默认工作流定义。

package moa

import vmconfig "cursor/internal/backend/virtualmodel/config"

// DefaultWorkflow 返回 MOA 的默认工作流配置。
// 这是一个完全可自定义的工作流，用户可以在前端或配置文件中覆盖。
func DefaultWorkflow() *vmconfig.WorkflowConfig {
	return &vmconfig.WorkflowConfig{
		ID:          vmconfig.DefaultMOAWorkflowID,
		Name:        "MOA Default",
		Description: "标准 MOA 工作流：Planner → 多专家并行 → Critic → Judge → Aggregator",
		Nodes: []vmconfig.WorkflowNodeConfig{
			{
				ID:            "planner",
				Role:          vmconfig.RolePlanner,
				ExecutionMode: vmconfig.ModeAlways,
				Enabled:       true,
				TimeoutSeconds: 60,
				RetryCount:    1,
			},
			{
				ID:            "coding",
				Role:          vmconfig.RoleCoding,
				ExecutionMode: vmconfig.ModeConditional,
				Dependencies:  []string{"planner"},
				Enabled:       true,
				TimeoutSeconds: 120,
				RetryCount:    1,
			},
			{
				ID:            "research",
				Role:          vmconfig.RoleResearch,
				ExecutionMode: vmconfig.ModeConditional,
				Dependencies:  []string{"planner"},
				Enabled:       true,
				TimeoutSeconds: 120,
				RetryCount:    1,
			},
			{
				ID:            "reasoning",
				Role:          vmconfig.RoleReasoning,
				ExecutionMode: vmconfig.ModeConditional,
				Dependencies:  []string{"planner"},
				Enabled:       true,
				TimeoutSeconds: 120,
				RetryCount:    1,
			},
			{
				ID:            "critic",
				Role:          vmconfig.RoleCritic,
				ExecutionMode: vmconfig.ModeSequential,
				Dependencies:  []string{"coding", "research", "reasoning"},
				Enabled:       true,
				TimeoutSeconds: 60,
				RetryCount:    0,
			},
			{
				ID:            "judge",
				Role:          vmconfig.RoleJudge,
				ExecutionMode: vmconfig.ModeSequential,
				Dependencies:  []string{"critic"},
				Enabled:       true,
				TimeoutSeconds: 60,
				RetryCount:    0,
			},
			{
				ID:            "aggregator",
				Role:          vmconfig.RoleAggregator,
				ExecutionMode: vmconfig.ModeSequential,
				Dependencies:  []string{"judge"},
				Enabled:       true,
				TimeoutSeconds: 120,
				RetryCount:    1,
			},
		},
	}
}

// LightweightWorkflow 返回一个轻量 MOA 工作流（仅 Planner + 2 个专家 + Aggregator）。
// 适合 token 敏感场景。
func LightweightWorkflow() *vmconfig.WorkflowConfig {
	return &vmconfig.WorkflowConfig{
		ID:          "moa-lightweight",
		Name:        "MOA Lightweight",
		Description: "轻量 MOA：Planner → 并行 Coding/Research → Aggregator（无 Critic/Judge）",
		Nodes: []vmconfig.WorkflowNodeConfig{
			{
				ID:            "planner",
				Role:          vmconfig.RolePlanner,
				ExecutionMode: vmconfig.ModeAlways,
				Enabled:       true,
				TimeoutSeconds: 60,
			},
			{
				ID:            "coding",
				Role:          vmconfig.RoleCoding,
				ExecutionMode: vmconfig.ModeConditional,
				Dependencies:  []string{"planner"},
				Enabled:       true,
				TimeoutSeconds: 120,
			},
			{
				ID:            "research",
				Role:          vmconfig.RoleResearch,
				ExecutionMode: vmconfig.ModeConditional,
				Dependencies:  []string{"planner"},
				Enabled:       true,
				TimeoutSeconds: 120,
			},
			{
				ID:            "aggregator",
				Role:          vmconfig.RoleAggregator,
				ExecutionMode: vmconfig.ModeSequential,
				Dependencies:  []string{"coding", "research"},
				Enabled:       true,
				TimeoutSeconds: 120,
			},
		},
	}
}
