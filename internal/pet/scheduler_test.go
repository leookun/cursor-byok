package pet

import (
	"sync"
	"testing"
	"time"
)

// TestScheduler_OwnerRegistry 验证 Owner 注册、按名查询、注销。
func TestScheduler_OwnerRegistry(t *testing.T) {
	s := NewScheduler()
	h1 := s.RegisterOwner(Owner{Name: "behavior", Priority: 10})
	h2 := s.RegisterOwner(Owner{Name: "behavior", Priority: 99}) // 重复注册返回旧句柄
	if h1 == 0 || h1 != h2 {
		t.Fatalf("owner handle should be stable, got %d and %d", h1, h2)
	}
	if s.OwnerHandle("behavior") != h1 {
		t.Fatal("OwnerHandle lookup failed")
	}
	s.UnregisterOwner(h1)
	if s.OwnerHandle("behavior") != 0 {
		t.Fatal("owner should be unregistered")
	}
}

// TestScheduler_ScheduleAndCancel 验证任务调度、按 owner 取消。
func TestScheduler_ScheduleAndCancel(t *testing.T) {
	s := NewScheduler()
	h := s.RegisterOwner(Owner{Name: "test", Priority: 5})
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }

	var mu sync.Mutex
	var calls []int
	execute := func(fn func()) { fn() }
	s.execute = execute

	s.Schedule(TaskSpec{Owner: h, Delay: 10 * time.Millisecond, Fn: func() {
		mu.Lock()
		calls = append(calls, 1)
		mu.Unlock()
	}})


	// 未到期不应执行。
	s.fireDue()
	mu.Lock()
	if len(calls) != 0 {
		t.Fatalf("expected 0 calls before deadline, got %d", len(calls))
	}
	mu.Unlock()

	// 到期后执行。
	now = now.Add(20 * time.Millisecond)
	s.fireDue()
	mu.Lock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call after deadline, got %d", len(calls))
	}
	mu.Unlock()

	// 取消第二个任务。
	cancel2 := s.Schedule(TaskSpec{Owner: h, Delay: 5 * time.Millisecond, Fn: func() {
		mu.Lock()
		calls = append(calls, 2)
		mu.Unlock()
	}})
	cancel2()
	now = now.Add(10 * time.Millisecond)
	s.fireDue()
	mu.Lock()
	if len(calls) != 1 {
		t.Fatalf("canceled task should not run, got %d calls", len(calls))
	}
	mu.Unlock()
}

// TestScheduler_Priority 验证同一到期时刻高优先级任务先执行。
func TestScheduler_Priority(t *testing.T) {
	s := NewScheduler()
	h := s.RegisterOwner(Owner{Name: "prio", Priority: 1})
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	s.execute = func(fn func()) { fn() }

	var order []int
	var mu sync.Mutex
	s.Schedule(TaskSpec{Owner: h, Delay: 0, Priority: 1, Fn: func() {
		mu.Lock()
		order = append(order, 1)
		mu.Unlock()
	}})
	s.Schedule(TaskSpec{Owner: h, Delay: 0, Priority: 9, Fn: func() {
		mu.Lock()
		order = append(order, 9)
		mu.Unlock()
	}})
	s.Schedule(TaskSpec{Owner: h, Delay: 0, Priority: 5, Fn: func() {
		mu.Lock()
		order = append(order, 5)
		mu.Unlock()
	}})

	s.fireDue()
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != 9 || order[1] != 5 || order[2] != 1 {
		t.Fatalf("expected execution order [9 5 1], got %v", order)
	}
}

// TestScheduler_Interval 验证周期任务会重复排队。
func TestScheduler_Interval(t *testing.T) {
	s := NewScheduler()
	h := s.RegisterOwner(Owner{Name: "interval", Priority: 1})
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	s.execute = func(fn func()) { fn() }

	var count int
	var mu sync.Mutex
	s.Schedule(TaskSpec{Owner: h, Delay: 0, Interval: 10 * time.Millisecond, Fn: func() {
		mu.Lock()
		count++
		mu.Unlock()
	}})

	s.fireDue() // count=1, reschedule at now+10ms
	now = now.Add(15 * time.Millisecond)
	s.fireDue() // count=2
	mu.Lock()
	if count != 2 {
		t.Fatalf("expected interval task to run twice, got %d", count)
	}
	mu.Unlock()
}
