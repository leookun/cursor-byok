package pet

import (
	"math/rand"
	"testing"
)

func TestIntentDecider_AgentBusyStaysIdle(t *testing.T) {
	d := NewIntentDecider()
	rng := rand.New(rand.NewSource(1))
	ctx := BehaviorContext{AgentBusy: true, RNG: rng}
	for i := 0; i < 50; i++ {
		if d.Decide(ctx) != IntentIdle {
			t.Fatal("agent busy should never produce active intent")
		}
	}
}

func TestIntentDecider_ReviewingStaysIdle(t *testing.T) {
	d := NewIntentDecider()
	rng := rand.New(rand.NewSource(2))
	ctx := BehaviorContext{Reviewing: true, RNG: rng}
	if d.Decide(ctx) != IntentIdle {
		t.Fatal("reviewing should stay idle")
	}
}

func TestIntentDecider_InteractionCooldown(t *testing.T) {
	d := NewIntentDecider()
	// 刚交互完（0s），冷却期内大多应为 idle。
	rng := rand.New(rand.NewSource(3))
	ctx := BehaviorContext{LastInteractionSec: 0, RNG: rng}
	idleCount := 0
	const n = 1000
	for i := 0; i < n; i++ {
		if d.Decide(ctx) == IntentIdle {
			idleCount++
		}
	}
	if idleCount < n*70/100 {
		t.Fatalf("expected >=70%% idle during cooldown, got %d/%d", idleCount, n)
	}
}

func TestIntentDecider_IdleLongerFavorsRest(t *testing.T) {
	d := NewIntentDecider()
	rng := rand.New(rand.NewSource(4))
	// 空闲 120s，sit/sleep 权重显著升高，walk 应较少。
	ctx := BehaviorContext{IdleSeconds: 120, RNG: rng}
	walkCount := 0
	const n = 2000
	for i := 0; i < n; i++ {
		if d.Decide(ctx) == IntentWalk {
			walkCount++
		}
	}
	// walk 基础权重 6，idleFactor=1 时 wWalk=3；sit=6, sleep=4, jump=1, wave=3 -> 总 17
	// walk 概率约 3/17 ≈ 17.6%。应明显低于无空闲时的 6/13≈46%。
	if walkCount > n*30/100 {
		t.Fatalf("expected walk <30%% when idle long, got %d/%d (%.1f%%)", walkCount, n, float64(walkCount)*100/n)
	}
}

func TestIntentDecider_AllIntentsReachable(t *testing.T) {
	d := NewIntentDecider()
	rng := rand.New(rand.NewSource(5))
	seen := map[Intent]bool{}
	ctx := BehaviorContext{IdleSeconds: 30, RNG: rng}
	for i := 0; i < 5000; i++ {
		seen[d.Decide(ctx)] = true
	}
	for _, it := range []Intent{IntentWalk, IntentJump, IntentWave, IntentSit, IntentSleep} {
		if !seen[it] {
			t.Fatalf("intent %s never produced", it)
		}
	}
}
