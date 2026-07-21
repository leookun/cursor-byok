package aos

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

func TestExecuteBatchCompletedDependenciesAndSpawnFailure(t *testing.T) {
	ws := NewWorkspace("test")
	ws.Tasks = []Task{
		{ID: "prior", Status: "done", Result: "prior output"},
		{ID: "a", Status: "pending", Dependencies: []string{"prior"}},
		{ID: "sibling", Status: "pending", Dependencies: []string{"prior"}},
		{ID: "b", Status: "pending", Dependencies: []string{"a"}},
	}
	s := NewWorkflowScheduler(2)
	var mu sync.Mutex
	var spawned, resolved []string
	spawnErr := errors.New("spawn a failed")
	err := s.ExecuteBatch(context.Background(), ws.Tasks,
		func(_ context.Context, task Task) (string, error) {
			mu.Lock()
			spawned = append(spawned, task.ID)
			mu.Unlock()
			if task.ID == "a" {
				return "", spawnErr
			}
			return "exec-" + task.ID, nil
		},
		func(_ context.Context, task Task, _ string) (string, error) {
			mu.Lock()
			resolved = append(resolved, task.ID)
			mu.Unlock()
			return "result-" + task.ID, nil
		}, ws)
	if !errors.Is(err, spawnErr) {
		t.Fatalf("ExecuteBatch error = %v, want spawn error", err)
	}
	if !contains(spawned, "a") || !contains(spawned, "sibling") || contains(spawned, "b") {
		t.Fatalf("spawned = %v, want a and sibling only from the first ready level", spawned)
	}
	if len(resolved) != 1 || resolved[0] != "sibling" {
		t.Fatalf("resolved = %v, want sibling resolved before spawn error", resolved)
	}

	// A completed task from the full workspace must satisfy a later dependency.
	ws.Tasks = []Task{
		{ID: "prior", Status: "done", Result: "prior output"},
		{ID: "next", Status: "pending", Dependencies: []string{"prior"}},
	}
	spawned = nil
	resolved = nil
	if err := s.ExecuteBatch(context.Background(), ws.Tasks,
		func(_ context.Context, task Task) (string, error) {
			spawned = append(spawned, task.ID)
			return "exec-" + task.ID, nil
		},
		func(_ context.Context, task Task, _ string) (string, error) {
			resolved = append(resolved, task.ID)
			return "result-" + task.ID, nil
		}, ws); err != nil {
		t.Fatalf("completed dependency batch: %v", err)
	}
	if strings.Join(spawned, ",") != "next" || strings.Join(resolved, ",") != "next" {
		t.Fatalf("spawned=%v resolved=%v, want next", spawned, resolved)
	}
}

func TestValidateTaskGraphRejectsMalformedGraphs(t *testing.T) {
	tests := []struct {
		name  string
		tasks []Task
		want  string
	}{
		{name: "empty", tasks: []Task{{ID: ""}}, want: "empty"},
		{name: "duplicate", tasks: []Task{{ID: "a"}, {ID: "a"}}, want: "duplicate"},
		{name: "unknown dependency", tasks: []Task{{ID: "a", Dependencies: []string{"missing"}}}, want: "unknown"},
		{name: "cycle", tasks: []Task{{ID: "a", Dependencies: []string{"b"}}, {ID: "b", Dependencies: []string{"a"}}}, want: "cycle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := NewWorkflowScheduler(1).ExecuteBatch(context.Background(), test.tasks,
				func(context.Context, Task) (string, error) { t.Fatal("spawn should not run"); return "", nil },
				func(context.Context, Task, string) (string, error) { return "", nil }, NewWorkspace("test"))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

type testChannelService struct{}

func (testChannelService) ResolveChannel(context.Context, string) (*vm_moa.ChannelInfo, error) {
	return &vm_moa.ChannelInfo{ID: "test"}, nil
}

func (testChannelService) CallAdapter(context.Context, *vm_moa.ChannelInfo, []vm_moa.Message, string) (*vm_moa.AdapterResult, error) {
	return &vm_moa.AdapterResult{Text: "leader output"}, nil
}

func TestExecuteSprintCursorTaskRetainsLeaderResult(t *testing.T) {
	team := &TeamProfile{Leader: LeaderConfig{AdapterID: "leader-adapter"}, ExecutionMode: ExecutionModeCursorTask, Workflow: WorkflowConfig{MaxParallel: 1}}
	m := &AOSModel{team: team, channelSvc: testChannelService{}, executionMode: ExecutionModeCursorTask}
	ws := NewWorkspace("test")
	ws.Tasks = []Task{{ID: "leader-task", AssigneeID: "leader", Status: "pending", Description: "do leader work"}}
	if err := m.executeSprint(context.Background(), nil, ws, &TaskPlan{Tasks: ws.Tasks}); err != nil {
		t.Fatalf("executeSprint: %v", err)
	}
	if ws.Tasks[0].Status != "done" || ws.Tasks[0].Result != "leader output" {
		t.Fatalf("leader task = %#v, want done result retained", ws.Tasks[0])
	}
}

func TestExecuteSprintInternalUsesCompletedWorkspaceDependencies(t *testing.T) {
	team := &TeamProfile{
		Leader:        LeaderConfig{AdapterID: "leader-adapter"},
		ExecutionMode: ExecutionModeInternal,
		Workflow:      WorkflowConfig{MaxParallel: 1},
	}
	m := &AOSModel{team: team, channelSvc: testChannelService{}, executionMode: ExecutionModeInternal}
	ws := NewWorkspace("test")
	ws.Tasks = []Task{
		{ID: "prior", AssigneeID: "leader", Status: "done", Result: "prior output"},
		{ID: "dependent", AssigneeID: "leader", Status: "pending", Dependencies: []string{"prior"}, Description: "dependent work"},
	}
	if err := m.executeSprint(context.Background(), nil, ws, &TaskPlan{Tasks: ws.Tasks}); err != nil {
		t.Fatalf("executeSprint: %v", err)
	}
	if ws.Tasks[1].Status != "done" || ws.Tasks[1].Result != "leader output" {
		t.Fatalf("dependent task = %#v, want done result retained", ws.Tasks[1])
	}
}

func TestExecuteBatchCursorTaskCancellationCleansEverySpawnedRegistryEntry(t *testing.T) {
	reg := virtualmodel.NewAOSResultRegistry()
	ctx, cancel := context.WithCancel(virtualmodel.WithAOSResultRegistry(context.Background(), reg))
	defer cancel()
	team := &TeamProfile{Members: []MemberConfig{
		{ID: "member-a", AdapterID: "adapter-a"},
		{ID: "member-b", AdapterID: "adapter-b"},
	}}
	m := &AOSModel{team: team, executionMode: ExecutionModeCursorTask, memberTimeout: time.Minute}
	ctx = virtualmodel.WithAOSMemberSpawner(ctx, func(taskID, _, _, _, _ string) (string, error) {
		return "cursor-" + taskID, nil
	})
	ws := NewWorkspace("test")
	ws.Tasks = []Task{
		{ID: "member-a", AssigneeID: "member-a", Status: "pending"},
		{ID: "member-b", AssigneeID: "member-b", Status: "pending"},
	}
	firstResolveStarted := make(chan struct{})
	allowFirstResolve := make(chan struct{})
	secondCleanup := make(chan struct{})
	var resolvedMu sync.Mutex
	resolved := make([]string, 0, 2)

	s := NewWorkflowScheduler(1)
	batchDone := make(chan error, 1)
	go func() {
		batchDone <- s.ExecuteBatch(ctx, ws.Tasks,
			func(spawnCtx context.Context, task Task) (string, error) {
				return m.spawnMemberTask(spawnCtx, nil, ws, task)
			},
			func(resolveCtx context.Context, task Task, execID string) (string, error) {
				resolvedMu.Lock()
				isFirst := len(resolved) == 0
				resolved = append(resolved, task.ID)
				resolvedMu.Unlock()
				if isFirst {
					close(firstResolveStarted)
					<-resolveCtx.Done()
					<-allowFirstResolve
				}
				result, err := m.resolveMemberTask(resolveCtx, task, execID)
				if !isFirst {
					close(secondCleanup)
				}
				return result, err
			}, ws)
	}()

	<-firstResolveStarted
	cancel()
	// The first resolver still owns the only slot. The second resolver must be
	// invoked with the canceled context outside that slot for cleanup.
	select {
	case <-secondCleanup:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second resolver cleanup")
	}
	close(allowFirstResolve)

	select {
	case err := <-batchDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExecuteBatch error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ExecuteBatch")
	}
	resolvedMu.Lock()
	defer resolvedMu.Unlock()
	if len(resolved) != 2 || !contains(resolved, "member-a") || !contains(resolved, "member-b") {
		t.Fatalf("resolved = %v, want both spawned members", resolved)
	}
	if got := reg.Count(); got != 0 {
		t.Fatalf("registry pending count = %d, want 0 after cancellation cleanup", got)
	}
}

func TestResolveMemberTaskCancellationCleansRegistry(t *testing.T) {
	reg := virtualmodel.NewAOSResultRegistry()
	ctx, cancel := context.WithCancel(virtualmodel.WithAOSResultRegistry(context.Background(), reg))
	reg.Expect("exec-1")
	cancel()
	m := &AOSModel{memberTimeout: time.Minute}
	_, err := m.resolveMemberTask(ctx, Task{ID: "task", AssigneeID: "member"}, "exec-1")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("resolve error = %v, want context cancellation", err)
	}
	if got := reg.Count(); got != 0 {
		t.Fatalf("registry pending count = %d, want 0 after cancellation", got)
	}
}

func TestResolveMemberTaskTimeoutCleansRegistry(t *testing.T) {
	reg := virtualmodel.NewAOSResultRegistry()
	ctx := virtualmodel.WithAOSResultRegistry(context.Background(), reg)
	reg.Expect("exec-timeout")
	m := &AOSModel{memberTimeout: time.Millisecond}
	_, err := m.resolveMemberTask(ctx, Task{ID: "task", AssigneeID: "member"}, "exec-timeout")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("resolve error = %v, want timeout", err)
	}
	if got := reg.Count(); got != 0 {
		t.Fatalf("registry pending count = %d, want 0 after timeout", got)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
