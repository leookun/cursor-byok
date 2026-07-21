package aos

import (
	"encoding/json"
	"strings"
)

// IntentGate — Leader planning 的前置意图分类层。
//
// 借鉴 oh-my-openagent 的 IntentGate：在完整 task planning 之前，先判断
// 用户请求的复杂度。简单请求（问候、单文件改动、查询）Leader 直接回复，
// 不拆分不 spawn 子代理；复杂请求才走完整 sprint 流程。
//
// 实现方式：一次 LLM 调用同时完成意图判断和（如果需要的话）任务规划。
// Leader 输出 JSON 中带 "intent" 字段：
//   - "simple"  → Leader 直接回复，reply 字段包含回复文本
//   - "complex" → 走完整 planning，tasks 数组包含拆分的任务
//
// 这样零额外 LLM 调用延迟——简单请求一轮就返回，复杂请求和原来一样。

// leaderPlanOutput 是 Leader planning 调用的完整 JSON 输出结构。
// 它同时承载意图分类和任务计划。
type leaderPlanOutput struct {
	Intent       string `json:"intent"`       // "simple" | "complex"
	Reply        string `json:"reply"`        // intent=="simple" 时的直接回复
	Tasks        []struct {
		ID           string   `json:"id"`
		Role         string   `json:"role"`
		Description  string   `json:"description"`
		Assignee     string   `json:"assignee"`
		Dependencies []string `json:"dependencies"`
		Priority     string   `json:"priority"`
	} `json:"tasks"`
	Architecture string `json:"architecture"`
}

// parseLeaderPlanOutput 解析 Leader 的 planning JSON 输出。
// 返回 (output, ok) — ok=false 表示无法解析为 JSON。
func parseLeaderPlanOutput(text string) (*leaderPlanOutput, bool) {
	jsonStr := extractJSON(text)
	if jsonStr == "" {
		return nil, false
	}
	var out leaderPlanOutput
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		return nil, false
	}
	return &out, true
}

// isSimpleIntent 判断 Leader 输出是否标记为简单意图。
func isSimpleIntent(out *leaderPlanOutput) bool {
	if out == nil {
		return false
	}
	intent := strings.ToLower(strings.TrimSpace(out.Intent))
	return intent == "simple"
}

// toTaskPlan 将 leaderPlanOutput 转换为 TaskPlan。
// 复用 parseTaskPlan 的 assignee 校验逻辑。
func (out *leaderPlanOutput) toTaskPlan(team *TeamProfile) *TaskPlan {
	if out == nil {
		return nil
	}
	plan := &TaskPlan{Architecture: out.Architecture}
	for _, t := range out.Tasks {
		assignee := strings.TrimSpace(t.Assignee)
		if assignee == "" {
			assignee = "leader"
		} else if assignee != "leader" && team != nil {
			if _, isMember := team.FindMember(assignee); !isMember {
				assignee = "leader"
			}
		}
		plan.Tasks = append(plan.Tasks, Task{
			ID:           t.ID,
			Role:         t.Role,
			Description:  t.Description,
			AssigneeID:   assignee,
			Dependencies: t.Dependencies,
			Priority:     t.Priority,
			Status:       "pending",
		})
	}
	return plan
}
