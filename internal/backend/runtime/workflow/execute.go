package workflow

import (
	"fmt"
	"sort"
)

// ExecLogEntry 单步执行日志。
type ExecLogEntry struct {
	NodeID   string `json:"nodeId"`
	NodeType string `json:"nodeType"`
	NodeName string `json:"nodeName"`
	Action   string `json:"action"`
	Detail   string `json:"detail"`
}

// ExecResult 执行结果。
type ExecResult struct {
	WorkflowID string         `json:"workflowId"`
	Success    bool           `json:"success"`
	Log        []ExecLogEntry `json:"log"`
}

// Engine 执行引擎（无状态，基于 Store 读取工作流）。
type Engine struct {
	store *Store
}

// NewEngine 创建执行引擎。
func NewEngine(store *Store) *Engine {
	return &Engine{store: store}
}

// Execute 按节点依赖顺序执行工作流：start → task → condition → end。
// 条件边支持 "true"/"false" 分支：condition 节点输出一个布尔结果（来自
// Config["result"]，缺省按节点名是否包含 "false"/"fail" 判定），沿对应分支继续。
func (e *Engine) Execute(workflowID string, input map[string]any) (*ExecResult, error) {
	if e == nil || e.store == nil {
		return nil, fmt.Errorf("workflow engine not initialized")
	}
	wf, ok := e.store.Get(workflowID)
	if !ok {
		return nil, fmt.Errorf("workflow %q not found", workflowID)
	}
	result := &ExecResult{WorkflowID: workflowID, Log: []ExecLogEntry{}}
	if input != nil {
		result.Log = append(result.Log, ExecLogEntry{
			NodeID:   "-",
			NodeType: "input",
			Action:   "received",
			Detail:   fmt.Sprintf("%d input field(s)", len(input)),
		})
	}

	byID := make(map[string]Node, len(wf.Nodes))
	for _, n := range wf.Nodes {
		byID[n.ID] = n
	}
	outEdges := make(map[string][]Edge)
	for _, ed := range wf.Edges {
		outEdges[ed.From] = append(outEdges[ed.From], ed)
	}

	// 找到 start 节点。
	var start *Node
	for i := range wf.Nodes {
		if wf.Nodes[i].Type == NodeStart {
			start = &wf.Nodes[i]
			break
		}
	}
	if start == nil {
		return nil, fmt.Errorf("workflow has no start node")
	}

	visited := make(map[string]bool)
	current := start
	for current != nil {
		if visited[current.ID] {
			result.Log = append(result.Log, ExecLogEntry{
				NodeID:   current.ID,
				NodeType: string(current.Type),
				NodeName: current.Name,
				Action:   "skip",
				Detail:   "already visited (cycle guard)",
			})
			break
		}
		visited[current.ID] = true

		switch current.Type {
		case NodeEnd:
			result.Log = append(result.Log, ExecLogEntry{
				NodeID:   current.ID,
				NodeType: string(current.Type),
				NodeName: current.Name,
				Action:   "end",
				Detail:   "workflow finished",
			})
			result.Success = true
			return result, nil
		case NodeTask:
			result.Log = append(result.Log, ExecLogEntry{
				NodeID:   current.ID,
				NodeType: string(current.Type),
				NodeName: current.Name,
				Action:   "exec",
				Detail:   summarizeConfig(current.Config),
			})
			current = nextNode(outEdges, byID, current.ID, "")
		case NodeCondition:
			branch := evalCondition(current, input)
			result.Log = append(result.Log, ExecLogEntry{
				NodeID:   current.ID,
				NodeType: string(current.Type),
				NodeName: current.Name,
				Action:   "branch",
				Detail:   fmt.Sprintf("evaluated %v -> %s", conditionValue(current, input), branch),
			})
			current = nextNode(outEdges, byID, current.ID, branch)
		default:
			result.Log = append(result.Log, ExecLogEntry{
				NodeID:   current.ID,
				NodeType: string(current.Type),
				NodeName: current.Name,
				Action:   "exec",
				Detail:   summarizeConfig(current.Config),
			})
			current = nextNode(outEdges, byID, current.ID, "")
		}
	}

	result.Log = append(result.Log, ExecLogEntry{
		NodeID:   "-",
		NodeType: "flow",
		Action:   "terminate",
		Detail:   "no further node to follow (no end reached)",
	})
	return result, nil
}

// nextNode 根据条件选择下一个节点。branch 为空表示无条件边；
// "true"/"false" 则优先匹配对应 condition 的边，否则退回无条件边。
func nextNode(outEdges map[string][]Edge, byID map[string]Node, fromID, branch string) *Node {
	edges := outEdges[fromID]
	if branch != "" {
		for _, ed := range edges {
			if ed.Condition == branch {
				if n, ok := byID[ed.To]; ok {
					return &n
				}
			}
		}
	}
	for _, ed := range edges {
		if ed.Condition == "" {
			if n, ok := byID[ed.To]; ok {
				return &n
			}
		}
	}
	return nil
}

// conditionValue 计算 condition 节点的布尔结果。
// 优先取 Config["result"]：bool/string("true"/"false")；缺省按节点名推断。
func conditionValue(n *Node, input map[string]any) bool {
	if n == nil || n.Config == nil {
		return false
	}
	if v, ok := n.Config["result"]; ok {
		switch val := v.(type) {
		case bool:
			return val
		case string:
			return val == "true" || val == "True"
		case float64:
			return val != 0
		}
	}
	// 回退：从 input 读取与节点名同名的布尔字段。
	if input != nil {
		if v, ok := input[n.Name]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	return false
}

func evalCondition(n *Node, input map[string]any) string {
	if conditionValue(n, input) {
		return "true"
	}
	return "false"
}

func summarizeConfig(cfg map[string]any) string {
	if len(cfg) == 0 {
		return "no config"
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s=%v", k, cfg[k])
	}
	return out
}