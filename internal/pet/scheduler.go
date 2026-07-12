package pet

import (
	"log"
	"sort"
	"sync"
	"time"
)

// Owner 描述一个任务所有者的身份与默认优先级。
// 越大 Priority 越高，同一到期时刻高优先级任务先执行。
type Owner struct {
	Name     string
	Priority int
}

// OwnerHandle 是所有者在 Scheduler 内的唯一句柄。
// 模块通过句柄而非字符串名称来创建/取消任务，避免拼写错误与命名冲突。
type OwnerHandle int

// Task 是调度器内部执行单元（Phase 7.5 解耦模型）。
type Task struct {
	Owner    OwnerHandle
	Priority int
	Fn       func()
}

// TaskSpec 是创建任务的规格说明。
// Delay 为首次执行的延迟；Interval > 0 时表示周期任务。
// Priority == 0 时继承 Owner 的默认优先级，否则覆盖。
type TaskSpec struct {
	Owner    OwnerHandle
	Delay    time.Duration
	Interval time.Duration
	Priority int
	Fn       func()
}

// taskEntry 是 Scheduler 内部的任务记录。
type taskEntry struct {
	id         int
	owner      OwnerHandle
	priority   int
	firstDelay time.Duration // 首次延迟
	interval   time.Duration // 0 表示一次性
	fn         func()
	deadline   time.Time
	canceled   bool
}

// Scheduler 是统一任务调度器（v2 Phase 7.5）。
//
// 设计原则：
//   - 所有跨线程任务统一进 Scheduler，由它按时间+优先级排序后派发到引擎线程。
//   - 模块不再直接调用 Engine.Post，Scheduler 通过构造时传入的 execute 投递。
//   - Owner 必须先注册获得句柄，才能创建任务，便于按 owner 批量取消与审计。
//   - 支持 Once/Interval、按 owner 取消、优先级抢占、零引擎耦合。
type Scheduler struct {
	mu          sync.Mutex
	owners      map[OwnerHandle]*Owner
	ownerByName map[string]OwnerHandle
	nextHandle  OwnerHandle
	tasks       map[int]*taskEntry
	nextID      int
	stopCh      chan struct{}
	tickCh      chan struct{}
	now         func() time.Time
	execute     func(func())
}

// NewScheduler 创建调度器。
func NewScheduler() *Scheduler {
	return &Scheduler{
		owners:      make(map[OwnerHandle]*Owner),
		ownerByName: make(map[string]OwnerHandle),
		tasks:       make(map[int]*taskEntry),
		stopCh:      make(chan struct{}),
		tickCh:      make(chan struct{}, 1),
		now:         time.Now,
	}
}

// SetExecute 注入任务执行器（典型实现是 Engine.Post）。
// 必须在 Start 前调用。
func (s *Scheduler) SetExecute(execute func(func())) {
	s.mu.Lock()
	s.execute = execute
	s.mu.Unlock()
}

// Start 实现 Lifecycle，启动调度循环。
func (s *Scheduler) Start() {
	s.mu.Lock()
	execute := s.execute
	s.mu.Unlock()
	if execute == nil {
		log.Println("[Pet][Scheduler] Start: no execute injected, skip")
		return
	}
	go s.Run(execute)
}

// Stop 实现 Lifecycle，停止调度循环。
func (s *Scheduler) Stop() {
	s.stopLoop()
}

// Dispose 实现 Lifecycle，取消所有任务并停止调度循环。
func (s *Scheduler) Dispose() {
	s.stopLoop()
	s.CancelAll()
}

// stopLoop 关闭调度循环的停止通道。
func (s *Scheduler) stopLoop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

// Ensure Scheduler implements Lifecycle.
var _ Lifecycle = (*Scheduler)(nil)

// RegisterOwner 注册一个任务所有者，返回其句柄。
// 同名 owner 重复注册会返回已存在的句柄（幂等）。
func (s *Scheduler) RegisterOwner(o Owner) OwnerHandle {
	if o.Name == "" {
		return OwnerHandle(0)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.ownerByName[o.Name]; ok {
		return h
	}
	s.nextHandle++
	h := s.nextHandle
	s.owners[h] = &Owner{Name: o.Name, Priority: o.Priority}
	s.ownerByName[o.Name] = h
	return h
}

// OwnerHandle 按名称查询已注册句柄；未注册返回 0。
func (s *Scheduler) OwnerHandle(name string) OwnerHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownerByName[name]
}

// UnregisterOwner 注销所有者并取消该 owner 的所有任务。
func (s *Scheduler) UnregisterOwner(h OwnerHandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.owners[h]
	if !ok {
		return
	}
	delete(s.owners, h)
	delete(s.ownerByName, o.Name)
	for id, t := range s.tasks {
		if t.owner == h {
			t.canceled = true
			delete(s.tasks, id)
		}
	}
}

// Schedule 按 TaskSpec 调度一个任务，返回取消函数。
// 非法 owner 或 nil fn 返回空操作。
func (s *Scheduler) Schedule(spec TaskSpec) func() {
	if spec.Fn == nil {
		return func() {}
	}
	s.mu.Lock()
	owner, ok := s.owners[spec.Owner]
	if !ok {
		s.mu.Unlock()
		log.Printf("[Pet][Scheduler] Schedule: unknown owner handle %d, ignored", spec.Owner)
		return func() {}
	}
	s.nextID++
	id := s.nextID
	priority := spec.Priority
	if priority == 0 {
		priority = owner.Priority
	}
	s.tasks[id] = &taskEntry{
		id:         id,
		owner:      spec.Owner,
		priority:   priority,
		firstDelay: spec.Delay,
		interval:   spec.Interval,
		fn:         spec.Fn,
	}
	s.mu.Unlock()
	select {
	case s.tickCh <- struct{}{}:
	default:
	}
	return func() {
		s.mu.Lock()
		if t, ok := s.tasks[id]; ok {
			t.canceled = true
			delete(s.tasks, id)
		}
		s.mu.Unlock()
	}
}

// Once 是 Schedule 的一次性快捷方式（兼容旧调用，新代码优先用 TaskSpec）。
func (s *Scheduler) Once(owner OwnerHandle, delay time.Duration, fn func()) func() {
	return s.Schedule(TaskSpec{Owner: owner, Delay: delay, Fn: fn})
}

// Interval 是 Schedule 的周期性快捷方式。
func (s *Scheduler) Interval(owner OwnerHandle, interval time.Duration, fn func()) func() {
	return s.Schedule(TaskSpec{Owner: owner, Delay: interval, Interval: interval, Fn: fn})
}

// CancelByOwner 取消某 owner 下的所有任务。
func (s *Scheduler) CancelByOwner(owner OwnerHandle) {
	s.mu.Lock()
	for id, t := range s.tasks {
		if t.owner == owner {
			t.canceled = true
			delete(s.tasks, id)
		}
	}
	s.mu.Unlock()
}

// CancelAll 取消所有任务。
func (s *Scheduler) CancelAll() {
	s.mu.Lock()
	for id, t := range s.tasks {
		t.canceled = true
		delete(s.tasks, id)
	}
	s.mu.Unlock()
}

// Run 启动调度循环。execute 负责把 fn 投递到正确的执行线程（如 Engine.Post）。
// Run 阻塞直到 stopLoop 被调用。
func (s *Scheduler) Run(execute func(fn func())) {
	s.mu.Lock()
	s.execute = execute
	s.mu.Unlock()
	log.Println("[Pet][Scheduler] Run: started")
	defer log.Println("[Pet][Scheduler] Run: exited")

	for {
		next := s.nextTick()
		var timer *time.Timer
		var timerC <-chan time.Time
		if next > 0 {
			timer = time.NewTimer(next)
			timerC = timer.C
		}

		select {
		case <-s.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.tickCh:
			if timer != nil {
				timer.Stop()
			}
		case <-timerC:
			s.fireDue()
		}
	}
}

// nextTick 返回到最近一次任务到期的等待时长（无任务返回 0）。
func (s *Scheduler) nextTick() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var min time.Duration
	has := false
	for _, t := range s.tasks {
		if t.canceled {
			continue
		}
		if t.deadline.IsZero() {
			t.deadline = now.Add(t.firstDelay)
		}
		d := t.deadline.Sub(now)
		if !has || d < min {
			min = d
			has = true
		}
	}
	if !has {
		return 0
	}
	if min < 0 {
		return 0
	}
	return min
}

// fireDue 执行所有已到期任务，并按优先级高到低排序。
func (s *Scheduler) fireDue() {
	s.mu.Lock()
	now := s.now()
	due := make([]*taskEntry, 0)
	for _, t := range s.tasks {
		if t.canceled {
			delete(s.tasks, t.id)
			continue
		}
		if t.deadline.IsZero() {
			t.deadline = now.Add(t.firstDelay)
		}
		if !now.Before(t.deadline) {
			due = append(due, t)
		}
	}
	// 同一时刻按优先级高到低执行。
	sort.Slice(due, func(i, j int) bool {
		return due[i].priority > due[j].priority
	})
	execute := s.execute
	s.mu.Unlock()

	if execute == nil {
		return
	}

	for _, t := range due {
		execute(t.fn)
		s.mu.Lock()
		if t.interval > 0 && !t.canceled {
			t.deadline = s.now().Add(t.interval)
		} else {
			delete(s.tasks, t.id)
		}
		s.mu.Unlock()
	}
}
