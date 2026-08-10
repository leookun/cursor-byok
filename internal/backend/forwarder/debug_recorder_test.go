package forwarder

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

type testObservabilityConfig struct {
	enabled bool
}

func (config testObservabilityConfig) IsObservabilityLogEnabled(context.Context) bool {
	return config.enabled
}

func TestDebugRecorderQueueMetricsAndFlush(t *testing.T) {
	recorder := newDebugRecorderWithQueue(t.TempDir(), nil, testObservabilityConfig{enabled: true}, 8)
	defer recorder.Close()

	for index := 0; index < 32; index++ {
		recorder.LogRuntime(context.Background(), "request", "conversation", "chunk", map[string]any{
			"index": index,
		})
	}
	if err := recorder.Flush(context.Background()); err != nil {
		t.Fatalf("flush debug recorder: %v", err)
	}

	metrics := recorder.Metrics()
	if metrics.Enqueued == 0 {
		t.Fatal("expected debug events to be enqueued")
	}
	if metrics.Written == 0 {
		t.Fatal("expected debug events to be written")
	}
	if metrics.QueueDepth != 0 {
		t.Fatalf("queue depth after flush = %d, want 0", metrics.QueueDepth)
	}
	if metrics.Enqueued+metrics.Dropped != 32 {
		t.Fatalf("enqueued + dropped = %d, want 32", metrics.Enqueued+metrics.Dropped)
	}

	if _, err := os.Stat(debugFilePath(recorder.historyRoot, "conversation", "debug", "runtime.jsonl")); err != nil {
		t.Fatalf("runtime debug file: %v", err)
	}
}

func BenchmarkDebugRecorderSSEChunks(b *testing.B) {
	for _, chunks := range []int{1000, 10000} {
		for _, logging := range []bool{false, true} {
			name := benchmarkName(chunks, logging)
			b.Run(name, func(b *testing.B) {
				recorder := newDebugRecorderWithQueue(b.TempDir(), nil, testObservabilityConfig{enabled: logging}, chunks+16)
				defer recorder.Close()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					for chunk := 0; chunk < chunks; chunk++ {
						recorder.LogProvider(context.Background(), "request", "conversation", "chunk", map[string]any{
							"chunk": chunk,
						})
					}
					if err := recorder.Flush(context.Background()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkInterTokenLatencyWithObservability(b *testing.B) {
	for _, logging := range []bool{false, true} {
		b.Run(benchmarkLoggingName(logging), func(b *testing.B) {
			recorder := newDebugRecorderWithQueue(b.TempDir(), nil, testObservabilityConfig{enabled: logging}, 10000)
			defer recorder.Close()
			const chunks = 10000
			const sampleSize = 64
			latencies := make([]time.Duration, 0, chunks/sampleSize)
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				latencies = latencies[:0]
				for batchStart := 0; batchStart < chunks; batchStart += sampleSize {
					started := time.Now()
					for chunk := batchStart; chunk < batchStart+sampleSize; chunk++ {
						recorder.LogProvider(context.Background(), "request", "conversation", "chunk", map[string]any{
							"iteration": iteration,
							"chunk":     chunk,
						})
					}
					latencies = append(latencies, time.Since(started)/sampleSize)
				}
			}
			b.StopTimer()
			if err := recorder.Flush(context.Background()); err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(percentileDuration(latencies, 0.50).Nanoseconds()), "p50-ns")
			b.ReportMetric(float64(percentileDuration(latencies, 0.95).Nanoseconds()), "p95-ns")
		})
	}
}

func benchmarkName(chunks int, logging bool) string {
	return fmt.Sprintf("chunks-%d/%s", chunks, benchmarkLoggingName(logging))
}

func benchmarkLoggingName(logging bool) string {
	if logging {
		return "observability-on"
	}
	return "observability-off"
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	items := append([]time.Duration(nil), values...)
	sort.Slice(items, func(left int, right int) bool { return items[left] < items[right] })
	index := int(float64(len(items)-1) * percentile)
	return items[index]
}
