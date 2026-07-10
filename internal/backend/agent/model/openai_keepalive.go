package modeladapter

import (
	"context"
	"sync"
	"time"
)

// streamKeepaliveInterval 是静默心跳的间隔。取值需明显小于下游（Cursor）判定
// “流断线”的空闲阈值——实测该阈值约在数十秒级，这里取 10s 留足余量。
const streamKeepaliveInterval = 10 * time.Second

// newStreamKeepalive 包裹 sink，并在“真实事件静默 >= interval”时补发一个空的
// ThinkingDelta 心跳，避免下游（Cursor）在长推理（首 token 前几十秒无数据）时
// 误判断线、反复 reconnecting。
//
// 安全设计（确保上游真断/真挂时不会“无限思考”）：
//   - 心跳与真实事件共用一把锁串行化；任何真实事件都会刷新“最后活动时间”，
//     心跳只在真正的静默间隙补发，不会与真实数据抢序。
//   - 心跳只写给下游，绝不触碰 provider 空闲看门狗（不调用 MarkEffectiveContent）。
//     因此上游若真的挂起（不发数据也不关闭），看门狗仍会按 ProviderStreamIdleTimeout
//     到点中止 → 错误照常向上抛出 → Cursor 收到结束/错误，不会一直转。
//   - 上游若真的断开，读循环立即退出、流函数返回，defer 的 stop() 停掉心跳协程。
//   - 心跳协程还监听 ctx.Done()；下游写入返回错误时也立即退出。
//     三重保证：心跳绝不会比真实流活得更久。
func newStreamKeepalive(
	ctx context.Context,
	sink func(ModelEvent) error,
	provider string,
	model string,
) (wrapped func(ModelEvent) error, stop func()) {
	var mu sync.Mutex
	lastActivity := time.Now()

	wrapped = func(ev ModelEvent) error {
		mu.Lock()
		defer mu.Unlock()
		lastActivity = time.Now()
		return sink(ev)
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(streamKeepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				idle := time.Since(lastActivity)
				mu.Unlock()
				if idle < streamKeepaliveInterval {
					continue
				}
				if err := wrapped(ModelEvent{
					Kind:       ModelEventKindThinkingDelta,
					OccurredAt: time.Now().UTC(),
					Provider:   provider,
					Model:      model,
					Text:       "",
				}); err != nil {
					// 下游已关闭/出错：停止心跳，让真实错误自然向上抛。
					return
				}
			}
		}
	}()

	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	return wrapped, stop
}
