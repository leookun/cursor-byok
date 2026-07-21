package aos

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// WorkflowScheduler executes dependency-aware task batches with bounded parallelism.
type WorkflowScheduler struct {
	maxParallel int
}

// NewWorkflowScheduler creates a scheduler with the given parallelism limit.
func NewWorkflowScheduler(maxParallel int) *WorkflowScheduler {
	if maxParallel <= 0 {
		maxParallel = 4
	}
	return &WorkflowScheduler{maxParallel: maxParallel}
}

type taskExecutor func(ctx context.Context, task Task) (string, error)
type spawnFunc func(ctx context.Context, task Task) (execID string, err error)
type resolveFunc func(ctx context.Context, task Task, execID string) (result string, err error)

// Execute runs pending tasks level by level, executing each ready level in parallel.
func (s *WorkflowScheduler) Execute(ctx context.Context, tasks []Task, execute taskExecutor, ws *Workspace) error {
	if len(tasks) == 0 {
		return nil
	}
	if err := validateTaskGraph(tasks); err != nil {
		return err
	}
	completed := completedTasks(tasks)
	remaining := pendingTasks(tasks)
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, notReady := readyTasks(remaining, completed)
		if len(ready) == 0 {
			return fmt.Errorf("task dependency graph has no ready tasks")
		}
		if err := s.executeParallel(ctx, ready, execute, ws); err != nil {
			return err
		}
		for _, task := range ready {
			completed[task.ID] = true
		}
		remaining = notReady
	}
	return nil
}

func (s *WorkflowScheduler) executeParallel(ctx context.Context, tasks []Task, execute taskExecutor, ws *Workspace) error {
	if len(tasks) == 0 {
		return nil
	}
	if len(tasks) == 1 || s.maxParallel <= 1 {
		for _, task := range tasks {
			if err := ctx.Err(); err != nil {
				return err
			}
			result, err := execute(ctx, task)
			if err != nil {
				ws.SetTaskResult(task.ID, task.AssigneeID, fmt.Sprintf("ERROR: %v", err))
				return err
			}
			ws.SetTaskResult(task.ID, task.AssigneeID, result)
		}
		return nil
	}
	sem := make(chan struct{}, s.maxParallel)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for _, task := range tasks {
		wg.Add(1)
		go func(task Task) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errMu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				errMu.Unlock()
				return
			}
			defer func() { <-sem }()
			result, err := execute(ctx, task)
			if err != nil {
				ws.SetTaskResult(task.ID, task.AssigneeID, fmt.Sprintf("ERROR: %v", err))
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			ws.SetTaskResult(task.ID, task.AssigneeID, result)
		}(task)
	}
	wg.Wait()
	return firstErr
}

// ExecuteBatch preserves Cursor Task's two-phase design: spawn a ready level,
// resolve every successful spawn, then advance to the next dependency level.
func (s *WorkflowScheduler) ExecuteBatch(ctx context.Context, tasks []Task, spawn spawnFunc, resolve resolveFunc, ws *Workspace) error {
	if len(tasks) == 0 {
		return nil
	}
	if err := validateTaskGraph(tasks); err != nil {
		return err
	}
	completed := completedTasks(tasks)
	remaining := pendingTasks(tasks)
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, notReady := readyTasks(remaining, completed)
		if len(ready) == 0 {
			return fmt.Errorf("task dependency graph has no ready tasks")
		}

		type spawnedTask struct {
			task   Task
			execID string
		}
		spawnedByIndex := make([]spawnedTask, len(ready))
		spawnErrs := make([]error, len(ready))
		sem := make(chan struct{}, s.maxParallel)
		var wg sync.WaitGroup
		for i, task := range ready {
			wg.Add(1)
			go func(i int, task Task) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					spawnErrs[i] = ctx.Err()
					return
				}
				defer func() { <-sem }()
				execID, err := spawn(ctx, task)
				if err != nil {
					ws.SetTaskResult(task.ID, task.AssigneeID, fmt.Sprintf("ERROR: %v", err))
					spawnErrs[i] = err
					return
				}
				spawnedByIndex[i] = spawnedTask{task: task, execID: execID}
			}(i, task)
		}
		wg.Wait()

		var spawnErr error
		spawned := make([]spawnedTask, 0, len(ready))
		for i := range ready {
			if spawnErr == nil && spawnErrs[i] != nil {
				spawnErr = spawnErrs[i]
			}
			if spawnErrs[i] == nil {
				spawned = append(spawned, spawnedByIndex[i])
			}
		}
		if len(spawned) == 0 {
			if spawnErr != nil {
				return spawnErr
			}
			return fmt.Errorf("task batch produced no spawned tasks")
		}

		var resolveErr error
		resolveErrMu := sync.Mutex{}
		sem = make(chan struct{}, s.maxParallel)
		wg = sync.WaitGroup{}
		for _, st := range spawned {
			wg.Add(1)
			go func(st spawnedTask) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					// A successful spawn must always reach its resolver so
					// cancellation-aware resolvers can remove their registry
					// expectations. This cleanup runs outside the normal slot
					// after cancellation, preserving bounded normal-path work.
					resolveErrMu.Lock()
					if resolveErr == nil {
						resolveErr = ctx.Err()
					}
					resolveErrMu.Unlock()
					_, _ = resolve(ctx, st.task, st.execID)
					return
				}
				defer func() { <-sem }()
				result, err := resolve(ctx, st.task, st.execID)
				if err != nil {
					ws.SetTaskResult(st.task.ID, st.task.AssigneeID, fmt.Sprintf("ERROR: %v", err))
					resolveErrMu.Lock()
					if resolveErr == nil {
						resolveErr = err
					}
					resolveErrMu.Unlock()
					return
				}
				ws.SetTaskResult(st.task.ID, st.task.AssigneeID, result)
			}(st)
		}
		wg.Wait()

		if spawnErr != nil {
			return spawnErr
		}
		if resolveErr != nil {
			return resolveErr
		}
		for _, st := range spawned {
			completed[st.task.ID] = true
		}
		remaining = notReady
	}
	return nil
}

func completedTasks(tasks []Task) map[string]bool {
	completed := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		if task.Status == "done" {
			completed[task.ID] = true
		}
	}
	return completed
}

func pendingTasks(tasks []Task) []Task {
	remaining := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Status == "pending" {
			remaining = append(remaining, task)
		}
	}
	return remaining
}

func readyTasks(remaining []Task, completed map[string]bool) (ready, notReady []Task) {
	for _, task := range remaining {
		isReady := true
		for _, dep := range task.Dependencies {
			if !completed[dep] {
				isReady = false
				break
			}
		}
		if isReady {
			ready = append(ready, task)
		} else {
			notReady = append(notReady, task)
		}
	}
	return ready, notReady
}

func validateTaskGraph(tasks []Task) error {
	ids := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) == "" {
			return fmt.Errorf("task ID must not be empty")
		}
		if _, exists := ids[task.ID]; exists {
			return fmt.Errorf("duplicate task ID %q", task.ID)
		}
		ids[task.ID] = struct{}{}
	}
	indegree := make(map[string]int, len(tasks))
	dependents := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		indegree[task.ID] = len(task.Dependencies)
		for _, dep := range task.Dependencies {
			if _, exists := ids[dep]; !exists {
				return fmt.Errorf("task %q depends on unknown task %q", task.ID, dep)
			}
			dependents[dep] = append(dependents[dep], task.ID)
		}
	}
	queue := make([]string, 0, len(tasks))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if visited != len(tasks) {
		return fmt.Errorf("task dependency graph contains a cycle")
	}
	return nil
}
