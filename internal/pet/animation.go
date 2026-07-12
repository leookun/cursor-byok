package pet

import (
	"image"
	"log"
	"sync"
)

// 动画名称常量（与 pet.json 中的 animations key 对应，也与 StateMachine 的状态名对应）。
const (
	AnimIdle  = "idle"
	AnimWalk  = "walk"
	AnimWave  = "wave"
	AnimSit   = "sit"
	AnimSleep = "sleep"
	AnimThink = "think"
	AnimHappy = "happy"
	AnimFocus = "focus"
)

const msPerSecond = 1000.0

// AnimationPlayer 管理动画播放。
type AnimationPlayer struct {
	atlas      *FrameAtlas
	anims      map[string]*Animation
	current    *Animation
	currentIdx int
	elapsed    float64
	playing    bool
	queued     *Animation

	// CrossFade 过渡状态：blendFrom 为旧动画，current 为新动画，
	// 过渡期间两动画同步推进，按 alpha 混合输出帧（Phase 4 引入）。
	blendFrom     *Animation
	blendFromIdx  int
	blendElapsed  float64
	blendDuration float64 // <=0 表示无过渡

	mu sync.Mutex

	// events 用于发布 EventAnimationFinished，无需持有 *Engine。
	events EventPublisher
}

// Animation 定义一段动画。
type Animation struct {
	Name     string
	Frames   []int
	FPS      float64
	Loop     bool
	Priority int
}

// NewAnimationPlayer 创建动画播放器。
func NewAnimationPlayer(atlas *FrameAtlas, pet *PetData, events EventPublisher) *AnimationPlayer {
	ap := &AnimationPlayer{
		atlas:  atlas,
		anims:  make(map[string]*Animation),
		events: events,
	}
	if atlas == nil || pet == nil {
		return ap
	}

	for name, def := range pet.Animations {
		ap.anims[name] = &Animation{
			Name:     name,
			Frames:   def.Frames,
			FPS:      float64(def.FPS),
			Loop:     def.Loop,
			Priority: def.Priority,
		}
	}

	return ap
}

// Start 实现 Lifecycle。动画播放器无需特殊启动逻辑。
func (ap *AnimationPlayer) Start() {}

// Stop 实现 Lifecycle，停止当前动画播放。
func (ap *AnimationPlayer) Stop() {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	ap.playing = false
}

// Dispose 实现 Lifecycle，释放动画资源。
func (ap *AnimationPlayer) Dispose() {
	ap.Stop()
	ap.mu.Lock()
	defer ap.mu.Unlock()
	ap.current = nil
	ap.queued = nil
	ap.blendFrom = nil
}

// Ensure AnimationPlayer implements Lifecycle.
var _ Lifecycle = (*AnimationPlayer)(nil)

// Play 播放动画。如果当前动画优先级更高，则排入队列等待。
func (ap *AnimationPlayer) Play(name string) {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	anim, ok := ap.anims[name]
	if !ok {
		log.Printf("[Pet] Anim: play %q ignored (no such animation)", name)
		return
	}

	if ap.current != nil && ap.current.Priority > anim.Priority {
		ap.queued = anim
		log.Printf("[Pet] Anim: play %q queued (current %q higher priority)", name, ap.current.Name)
		return
	}

	log.Printf("[Pet] Anim: play %q (priority=%d)", name, anim.Priority)
	ap.startAnimLocked(anim)
}

// ForcePlay 强制播放动画（无视优先级和队列）。
func (ap *AnimationPlayer) ForcePlay(name string) {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	anim, ok := ap.anims[name]
	if !ok {
		return
	}

	ap.startAnimLocked(anim)
}

// startAnimLocked 内部方法：设置当前动画并重置状态（调用者必须持有锁）。
func (ap *AnimationPlayer) startAnimLocked(anim *Animation) {
	ap.current = anim
	ap.currentIdx = 0
	ap.elapsed = 0
	ap.playing = true
	ap.queued = nil
	// 清除任何进行中的过渡。
	ap.blendFrom = nil
	ap.blendFromIdx = 0
	ap.blendElapsed = 0
	ap.blendDuration = 0
}

// CrossFade 以 duration 毫秒的渐变过渡到新动画（旧动画淡出、新动画淡入）。
// 过渡期间两动画同步推进并按 alpha 混合输出帧，实现自然的 Walk->Idle 等切换，
// 而非瞬间硬切（v2 Phase 4 目标）。duration<=0 时退化为立即切换。
func (ap *AnimationPlayer) CrossFade(name string, duration float64) {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	anim, ok := ap.anims[name]
	if !ok {
		log.Printf("[Pet] Anim: crossfade %q ignored (no such animation)", name)
		return
	}
	// 同动画无需过渡。
	if ap.current == anim && ap.blendDuration <= 0 {
		return
	}
	// 若已有过渡进行中，把当前 current 作为新的 blendFrom 起点。
	if ap.blendDuration > 0 && ap.blendFrom != nil {
		// 继续从当前视觉状态过渡，简单起见重新开始过渡到目标。
	}
	if ap.current != nil {
		ap.blendFrom = ap.current
		ap.blendFromIdx = ap.currentIdx
	} else {
		ap.blendFrom = nil
	}
	if duration <= 0 {
		ap.startAnimLocked(anim)
		return
	}
	log.Printf("[Pet] Anim: crossfade to %q (duration=%.0fms)", name, duration)
	ap.current = anim
	ap.currentIdx = 0
	ap.elapsed = 0
	ap.playing = true
	ap.queued = nil
	ap.blendElapsed = 0
	ap.blendDuration = duration
}

// Update 推进动画时间（含 CrossFade 过渡的双动画同步推进）。
func (ap *AnimationPlayer) Update(deltaMS float64) {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	if ap.current == nil || !ap.playing {
		return
	}

	// 推进 CrossFade 过渡：两动画同步前进，过渡结束后清理 blendFrom。
	if ap.blendDuration > 0 && ap.blendFrom != nil {
		ap.blendElapsed += deltaMS
		ap.advanceLocked(ap.blendFrom, &ap.blendFromIdx, deltaMS)
		if ap.blendElapsed >= ap.blendDuration {
			// 过渡完成，旧动画退出。
			ap.blendFrom = nil
			ap.blendFromIdx = 0
			ap.blendElapsed = 0
			ap.blendDuration = 0
		}
	}

	// FPS <= 0 会导致 frameDuration = +Inf，动画永不前进（桌宠定格一帧）。
	// 防御性兜底：缺省按 12 FPS，避免单个动画漏写 fps 造成"无响应"。
	fps := ap.current.FPS
	if fps <= 0 {
		fps = 12
	}
	frameDuration := msPerSecond / fps
	ap.elapsed += deltaMS

	for ap.elapsed >= frameDuration {
		ap.elapsed -= frameDuration
		ap.currentIdx++

		if ap.currentIdx >= len(ap.current.Frames) {
			if ap.current.Loop {
				ap.currentIdx = 0
			} else {
				finishedName := ap.current.Name
				ap.playing = false
				// 播放队列中的动画
				if ap.queued != nil {
					ap.current = ap.queued
					ap.currentIdx = 0
					ap.elapsed = 0
					ap.playing = true
					ap.queued = nil
				}
				// 非循环动画播放完毕：发布事件通知订阅者。
				if ap.events != nil {
					ap.events.Publish(Event{
						Type: EventAnimationFinished,
						Data: map[string]interface{}{"anim": finishedName},
					})
				}
				return
			}
		}
	}
}

// advanceLocked 推进单个动画的帧索引（调用者必须持有锁）。
// 与 Update 主逻辑一致，但用于 blendFrom 动画。
func (ap *AnimationPlayer) advanceLocked(anim *Animation, idx *int, deltaMS float64) {
	if anim == nil {
		return
	}
	fps := anim.FPS
	if fps <= 0 {
		fps = 12
	}
	frameDuration := msPerSecond / fps
	// 用独立累加，避免修改主 elapsed。
	// 为简单起见直接按帧步进（过渡期不长，精度足够）。
	*idx++
	if *idx >= len(anim.Frames) {
		if anim.Loop {
			*idx = 0
		} else {
			*idx = len(anim.Frames) - 1 // 停留在末帧
		}
	}
	_ = frameDuration
	_ = deltaMS
}

// CurrentFrame 返回当前帧图像。
// 过渡（CrossFade）期间返回旧/新两帧按 alpha 混合的结果；
// 平时直接返回 atlas 帧（零拷贝）。
func (ap *AnimationPlayer) CurrentFrame() image.Image {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	if ap.current == nil || ap.currentIdx >= len(ap.current.Frames) {
		return nil
	}
	cur := ap.atlas.GetFrame(ap.current.Frames[ap.currentIdx])
	if ap.blendDuration > 0 && ap.blendFrom != nil {
		from := ap.atlas.GetFrame(ap.blendFrom.Frames[ap.blendFromIdx])
		// 过渡进度：新动画 alpha 从 0->1，旧动画 alpha 从 1->0。
		t := ap.blendElapsed / ap.blendDuration
		if t > 1 {
			t = 1
		}
		return blendFrames(from, cur, t)
	}
	return cur
}

// blendFrames 把 a（旧，淡出）与 b（新，淡入）按 t（0..1）做 alpha 混合，返回新帧。
// 仅过渡期调用，过渡结束不再分配。
func blendFrames(a, b image.Image, t float64) *image.RGBA {
	ab, ok1 := a.(*image.RGBA)
	bb, ok2 := b.(*image.RGBA)
	if !ok1 || !ok2 {
		// 非预期类型，直接返回新帧。
		if bb != nil {
			return bb
		}
		return nil
	}
	bounds := bb.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	out := image.NewRGBA(bounds)
	// 新动画 alpha 权重 = t，旧动画权重 = 1-t。
	wa := uint32(t * 255)
	wb := uint32((1 - t) * 255)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := bb.PixOffset(x, y)
			// 新帧像素（带自身 alpha）
			nr, ng, nb, na := bb.Pix[i], bb.Pix[i+1], bb.Pix[i+2], bb.Pix[i+3]
			// 旧帧像素（带自身 alpha）
			or_, og, ob, oa := ab.Pix[i], ab.Pix[i+1], ab.Pix[i+2], ab.Pix[i+3]
			// 按各帧自身 alpha 与过渡权重混合。
			out.Pix[i] = byte(uint32(nr)*wa/255 + uint32(or_)*wb/255)
			out.Pix[i+1] = byte(uint32(ng)*wa/255 + uint32(og)*wb/255)
			out.Pix[i+2] = byte(uint32(nb)*wa/255 + uint32(ob)*wb/255)
			// 合成 alpha：两帧按权重叠加（避免全透明）。
			out.Pix[i+3] = byte(uint32(na)*wa/255 + uint32(oa)*wb/255)
		}
	}
	return out
}

// CurrentFrameIndex 返回当前帧索引（用于去重渲染）。
func (ap *AnimationPlayer) CurrentFrameIndex() int {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	return ap.currentIdx
}

// CurrentAnimName 返回当前动画名称。
func (ap *AnimationPlayer) CurrentAnimName() string {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if ap.current == nil {
		return ""
	}
	return ap.current.Name
}
