// activity.go — 桌宠活动状态桥接层。
//
// 设计目标（高内聚低耦合）：
//   - forwarder/runner 等外部子系统需要让桌宠根据"模型流量"做出反应
//     （思考/工作/待机/错误），但不应直接依赖 Engine 内部 FSM/动画细节。
//   - 本文件定义 ActivityState 枚举 + Engine.SetActivity + PetManager.SetActivity，
//     作为外部调用方与 Engine 内部状态机的唯一接口。
//   - Engine.SetActivity 通过 Post 投递到引擎线程执行，保证线程安全；
//     FSM 转移失败（如已在目标状态）会静默忽略，避免外部调用方处理噪音。
package pet

import "log"

// ActivityState 表示外部系统观察到的桌宠活动状态。
type ActivityState string

const (
	// ActivityThinking 模型开始处理请求（推理中，尚未产出正文）。
	ActivityThinking ActivityState = "thinking"
	// ActivityWorking 模型已开始输出正文内容。
	ActivityWorking ActivityState = "working"
	// ActivityIdle 无活动，回到待机。
	ActivityIdle ActivityState = "idle"
	// ActivityError 模型调用出错。
	ActivityError ActivityState = "error"
)

// activityToState 把外部 ActivityState 映射到 FSM 状态。
// StateWaiting  → AnimThink   （思考动画）
// StateReviewing → AnimFocus  （专注/工作动画）
// StateIdle     → AnimIdle    （待机）
// StateFailed   → 错误（当前映射 happy，未来可扩展专用错误动画）
func activityToState(s ActivityState) *State {
	switch s {
	case ActivityThinking:
		return StateWaiting
	case ActivityWorking:
		return StateReviewing
	case ActivityIdle:
		return StateIdle
	case ActivityError:
		return StateFailed
	default:
		return nil
	}
}

// SetActivity 让 Engine 根据外部活动状态转移 FSM。
// 线程安全：通过 Post 投递到引擎线程执行，调用方可在任意线程调用。
// Engine 未运行或已停止时静默丢弃，避免外部调用方处理错误。
func (e *Engine) SetActivity(s ActivityState) {
	if e == nil {
		return
	}
	target := activityToState(s)
	if target == nil {
		return
	}
	e.Post(func() {
		if e.fsm == nil {
			return
		}
		// 使用 ForceTransition 保证确定性转移，绕过优先级/转移表限制。
		// 这样 ActivityThinking/Working 可以从任意状态进入（如打断 Sleep）。
		e.fsm.ForceTransition(target)
	})
}

// SetActivity 让所有已注册的桌宠实例转移到指定活动状态。
// 供 runner 订阅 forwarder 事件后调用，是外部子系统与 pet 包的唯一接口。
// 返回实际下发的实例数（便于日志诊断，调用方无需处理错误）。
func (m *PetManager) SetActivity(s ActivityState) int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	pets := make([]PetInstance, 0, len(m.pets))
	for _, p := range m.pets {
		pets = append(pets, p)
	}
	m.mu.RUnlock()

	dispatched := 0
	for _, p := range pets {
		// 类型断言：只有 *Engine 才支持 SetActivity；
		// 测试 fake 可能不实现该方法，跳过即可。
		if eng, ok := p.(*Engine); ok && eng != nil {
			eng.SetActivity(s)
			dispatched++
		}
	}
	log.Printf("[Pet][Manager] SetActivity(%s): dispatched to %d/%d pets", s, dispatched, len(pets))
	return dispatched
}
