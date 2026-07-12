package pet

import (
	"sync"
	"testing"
)

// fakePlugin 记录生命周期与收到的事件，用于测试 PluginManager。
type fakePlugin struct {
	name     string
	initErr  error
	inited   bool
	disposed bool
	events   []EventType
	mu       sync.Mutex
	api      PluginAPI
}

func (f *fakePlugin) Name() string { return f.name }
func (f *fakePlugin) Init(api PluginAPI) error {
	f.mu.Lock()
	f.inited = true
	f.api = api
	f.mu.Unlock()
	if f.initErr != nil {
		return f.initErr
	}
	// 订阅状态变化事件。
	api.Bus().Subscribe(EventStateChanged, func(evt Event) {
		f.mu.Lock()
		f.events = append(f.events, evt.Type)
		f.mu.Unlock()
	})
	return nil
}
func (f *fakePlugin) Dispose() {
	f.mu.Lock()
	f.disposed = true
	f.mu.Unlock()
}

func TestPluginManager_Lifecycle(t *testing.T) {
	bus := NewEventBus()
	fsm := NewStateMachine()
	api := newPluginAPI(nil, bus, fsm, nil)
	m := NewPluginManager(api)
	p := &fakePlugin{name: "greeter"}
	if err := m.Register(p); err != nil {
		t.Fatal(err)
	}
	// 未启动前不应 inited。
	if p.inited {
		t.Fatal("plugin should not init before StartAll")
	}
	m.StartAll()
	if !p.inited || p.disposed {
		t.Fatal("plugin should be inited and not disposed after StartAll")
	}
	// 发布一个事件，插件应收到。
	bus.Publish(Event{Type: EventStateChanged})
	p.mu.Lock()
	got := len(p.events)
	p.mu.Unlock()
	if got != 1 {
		t.Fatalf("plugin should receive 1 event, got %d", got)
	}
	m.StopAll()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.disposed {
		t.Fatal("plugin should be disposed after StopAll")
	}
}

func TestPluginManager_DuplicateIgnored(t *testing.T) {
	bus := NewEventBus()
	fsm := NewStateMachine()
	api := newPluginAPI(nil, bus, fsm, nil)
	m := NewPluginManager(api)
	p1 := &fakePlugin{name: "dup"}
	p2 := &fakePlugin{name: "dup"}
	if err := m.Register(p1); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(p2); err != nil {
		t.Fatal(err)
	}
	m.StartAll()
	p1.mu.Lock()
	inited1 := p1.inited
	p1.mu.Unlock()
	p2.mu.Lock()
	inited2 := p2.inited
	p2.mu.Unlock()
	if !inited1 || inited2 {
		t.Fatal("duplicate plugin name should be ignored (only first inited)")
	}
	m.StopAll()
}
