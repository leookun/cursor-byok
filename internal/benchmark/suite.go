// Package benchmark provides a framework for benchmarking Virtual Models.
//
// Measures Latency, Token Usage, Cost, Cache Hit, and Quality
// for any VirtualModel implementation.
//
// Design: ADR-020. Handbook: docs/handbook/27_Benchmark.md.
package benchmark

import (
	"context"
	"fmt"
	"sync"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
)

// Task represents a single benchmark task.
type Task struct {
	Name        string
	Messages    []virtualmodel.Message
	Description string
}

// Result records the benchmark result for a single task.
type Result struct {
	TaskName         string
	Success          bool
	Error            string
	LatencyMS        int64
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	OutputText       string
	Timestamp        time.Time
	Metadata         map[string]string
}

// Report aggregates results across all tasks.
type Report struct {
	SuiteName       string
	StartTime       time.Time
	EndTime         time.Time
	TotalDurationMS int64
	Results         []Result
	Summary         Summary
}

// Summary contains aggregate statistics.
type Summary struct {
	TasksTotal       int
	TasksSucceeded   int
	TasksFailed      int
	AvgLatencyMS     int64
	TotalTokens      int
	AvgTokensPerTask int
}

// Suite is a collection of benchmark tasks.
type Suite struct {
	Name  string
	tasks []Task
	mu    sync.Mutex
}

// NewSuite creates a new benchmark suite.
func NewSuite(name string) *Suite {
	return &Suite{Name: name}
}

// AddTask adds a benchmark task to the suite.
func (s *Suite) AddTask(task Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, task)
}

// Run executes all tasks against the given VirtualModel and returns a Report.
func (s *Suite) Run(ctx context.Context, model virtualmodel.VirtualModel) *Report {
	s.mu.Lock()
	tasks := make([]Task, len(s.tasks))
	copy(tasks, s.tasks)
	s.mu.Unlock()

	report := &Report{
		SuiteName: s.Name,
		StartTime: time.Now(),
		Results:   make([]Result, 0, len(tasks)),
	}

	for _, task := range tasks {
		result := s.runTask(ctx, model, task)
		report.Results = append(report.Results, result)
	}

	report.EndTime = time.Now()
	report.TotalDurationMS = time.Since(report.StartTime).Milliseconds()
	report.Summary = s.computeSummary(report.Results)

	return report
}

// runTask executes a single benchmark task.
func (s *Suite) runTask(ctx context.Context, model virtualmodel.VirtualModel, task Task) Result {
	result := Result{
		TaskName:  task.Name,
		Timestamp: time.Now(),
	}

	start := time.Now()
	executeResult, err := model.Execute(ctx, &virtualmodel.ExecuteRequest{
		RequestID: fmt.Sprintf("benchmark-%s-%d", task.Name, start.UnixNano()),
		Messages:  task.Messages,
	})
	result.LatencyMS = time.Since(start).Milliseconds()

	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	if executeResult == nil {
		result.Success = false
		result.Error = "virtual model returned nil result"
		return result
	}

	result.Success = true
	result.OutputText = executeResult.Text
	if executeResult.Usage != nil {
		result.PromptTokens = executeResult.Usage.PromptTokens
		result.CompletionTokens = executeResult.Usage.CompletionTokens
	}
	result.TotalTokens = result.PromptTokens + result.CompletionTokens
	if len(executeResult.Metadata) > 0 {
		result.Metadata = make(map[string]string, len(executeResult.Metadata))
		for key, value := range executeResult.Metadata {
			result.Metadata[key] = value
		}
	}

	return result
}

// computeSummary calculates aggregate statistics from results.
func (s *Suite) computeSummary(results []Result) Summary {
	summary := Summary{TasksTotal: len(results)}
	var totalLatency int64
	var totalTokens int

	for _, r := range results {
		if r.Success {
			summary.TasksSucceeded++
		} else {
			summary.TasksFailed++
		}
		totalLatency += r.LatencyMS
		totalTokens += r.TotalTokens
	}

	if summary.TasksTotal > 0 {
		summary.AvgLatencyMS = totalLatency / int64(summary.TasksTotal)
	}
	summary.TotalTokens = totalTokens
	if summary.TasksTotal > 0 {
		summary.AvgTokensPerTask = totalTokens / summary.TasksTotal
	}

	return summary
}

// FormatReport returns a human-readable summary of the report.
func (r *Report) FormatReport() string {
	var sb fmt.Stringer
	_ = sb
	output := fmt.Sprintf("Benchmark Report: %s\n", r.SuiteName)
	output += fmt.Sprintf("Duration: %dms\n", r.TotalDurationMS)
	output += fmt.Sprintf("Tasks: %d total, %d succeeded, %d failed\n",
		r.Summary.TasksTotal, r.Summary.TasksSucceeded, r.Summary.TasksFailed)
	output += fmt.Sprintf("Avg Latency: %dms\n", r.Summary.AvgLatencyMS)
	output += fmt.Sprintf("Total Tokens: %d (avg %d/task)\n",
		r.Summary.TotalTokens, r.Summary.AvgTokensPerTask)
	output += "\n"

	for _, r := range r.Results {
		status := "OK"
		if !r.Success {
			status = "FAIL"
		}
		output += fmt.Sprintf("  %s: %s %dms %dt\n", r.TaskName, status, r.LatencyMS, r.TotalTokens)
	}

	return output
}
