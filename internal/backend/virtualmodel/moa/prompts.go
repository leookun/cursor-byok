// moa/prompts.go 定义 MOA 各角色的 system prompt。

package moa

import (
	"fmt"
	"strings"

	vmconfig "cursor/internal/backend/virtualmodel/config"
	virtualmodel "cursor/internal/backend/virtualmodel"
)

// buildPlannerPrompt 构建规划器 prompt。
func buildPlannerPrompt(workflow *vmconfig.WorkflowConfig) string {
	availableRoles := collectAvailableRoles(workflow)
	return fmt.Sprintf(`You are a task planner. Analyze the user's request and determine which expert roles are needed.

Available expert roles:
%s

Output ONLY a JSON object with this exact structure:
{
  "tasks": [
    {"role": "coding", "reason": "brief reason why this role is needed"},
    {"role": "research", "reason": "brief reason why this role is needed"}
  ]
}

Rules:
- Only include roles from the available list.
- If no specialized expert is needed, return {"tasks": []}.
- The "reason" field should briefly explain why each role is needed.`, availableRoles)
}

// buildExpertPrompt 构建专家节点 prompt。
func buildExpertPrompt(role vmconfig.NodeRole, planText string, node vmconfig.WorkflowNodeConfig) string {
	rolePrompt := getRoleDescription(role)
	base := fmt.Sprintf(`You are a %s expert. %s

Context from planner: %s

Provide your expert analysis and answer to the user's request.
Focus on your area of expertise. Be thorough and precise.`, role, rolePrompt, planText)

	if node.Prompt != "" {
		base += "\n\n" + node.Prompt
	}
	if node.SystemPrompt != "" {
		base += "\n\n" + node.SystemPrompt
	}
	return base
}

// buildExpertUserPrompt 构建专家节点的 user prompt。
func buildExpertUserPrompt(role vmconfig.NodeRole, req *virtualmodel.ExecuteRequest) string {
	return fmt.Sprintf("As the %s expert, please respond to:\n\n%s", role, req.LatestUserText)
}

// buildCriticPrompt 构建批评者 prompt。
func buildCriticPrompt() string {
	return `You are a quality critic. Review the expert outputs below and identify:

1. Logical flaws or errors
2. Missing information or gaps
3. Security risks or potential issues
4. Inconsistencies between different expert opinions

Be constructive and specific. Focus on actionable feedback.`
}

// buildJudgePrompt 构建评判者 prompt。
func buildJudgePrompt() string {
	return `You are a quality judge. Evaluate the expert outputs based on:

1. Accuracy - Are the answers factually correct?
2. Completeness - Does the answer fully address the request?
3. Consistency - Are the expert opinions aligned or contradictory?
4. Overall quality - Which output is best and why?

Provide a clear judgment and recommendation for the final aggregator.`
}

// buildAggregatorPrompt 构建聚合器 prompt。
func buildAggregatorPrompt() string {
	return `You are a final aggregator. Your task is to combine multiple expert outputs into a single, cohesive response.

Instructions:
1. Identify and resolve any conflicts between expert opinions.
2. Keep the best solutions from each expert.
3. Fill in any gaps or omissions.
4. Produce a polished, unified final answer.
5. Do NOT reference individual expert names in the output.
6. Do NOT use phrases like "Expert A says..." or "According to the coding expert...".
7. Write as if YOU are the one providing the answer directly to the user.

Output only the final polished response.`
}

// getRoleDescription 返回角色的能力描述。
func getRoleDescription(role vmconfig.NodeRole) string {
	switch role {
	case vmconfig.RoleCoding:
		return "You specialize in software development, code generation, debugging, and code review. Write clean, efficient, and well-documented code."
	case vmconfig.RoleResearch:
		return "You specialize in research, information gathering, and analysis. Provide well-sourced, comprehensive answers."
	case vmconfig.RoleReasoning:
		return "You specialize in logical reasoning, mathematical analysis, and step-by-step problem solving."
	case vmconfig.RoleVision:
		return "You specialize in image understanding, visual analysis, and diagram interpretation."
	case vmconfig.RoleMemory:
		return "You specialize in recalling and connecting relevant past information and context."
	case vmconfig.RoleSecurity:
		return "You specialize in security analysis, vulnerability detection, and secure coding practices."
	case vmconfig.RoleTool:
		return "You specialize in using tools, APIs, and external systems to accomplish tasks."
	default:
		return "You are a domain expert providing specialized knowledge."
	}
}

// collectAvailableRoles 收集工作流中所有可用角色。
func collectAvailableRoles(workflow *vmconfig.WorkflowConfig) string {
	var roles []string
	for _, node := range workflow.Nodes {
		if node.Enabled && node.Role != vmconfig.RolePlanner &&
			node.Role != vmconfig.RoleCritic && node.Role != vmconfig.RoleJudge &&
			node.Role != vmconfig.RoleAggregator {
			roles = append(roles, fmt.Sprintf("- %s: %s", node.Role, getRoleDescription(node.Role)))
		}
	}
	if len(roles) == 0 {
		return "  (no expert roles configured)"
	}
	return strings.Join(roles, "\n")
}
