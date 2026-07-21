// test_step.go implements the constitutional Test stage of the Evolver closed
// loop (Research -> ADR -> Implementation -> Test -> Benchmark -> Documentation).
//
// Design: ADR-031. It runs a curated set of Go packages that gate handbook/code
// health. Host background evolution keeps this off (timeout budget); CLI/CI
// enable it via EvolveOptions.RunTests.
package evolver

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTestPackages are the packages always exercised when RunTests is enabled.
// Keep this list small and high-signal so CI stays fast and recursive test
// invocation does not explode (these packages must not call Evolve with RunTests).
var DefaultTestPackages = []string{
	"./internal/docguard/",
	"./internal/backend/runtime/evolver/",
}

// TestPackageResult is the outcome of one `go test` package run.
type TestPackageResult struct {
	Package    string `json:"package"`
	Passed     bool   `json:"passed"`
	DurationMS int64  `json:"durationMS"`
	// Output is truncated combined stdout/stderr for diagnosis evidence.
	Output string `json:"output,omitempty"`
	// Error is the process-level error (non-zero exit, timeout, missing go).
	Error string `json:"error,omitempty"`
}

// TestReport is the output of the Test stage.
type TestReport struct {
	// Ran is false when the Test stage was skipped (Host path / default Evolve).
	Ran        bool                `json:"ran"`
	Packages   []TestPackageResult `json:"packages,omitempty"`
	Passed     int                 `json:"passed"`
	Failed     int                 `json:"failed"`
	DurationMS int64               `json:"durationMS"`
}

// RunTests executes curated package tests under repoRoot.
// If packages is empty, DefaultTestPackages is used.
// Never panics; missing go or timeouts become failed package results.
func (e *Evolver) RunTests(ctx context.Context, repoRoot string, packages []string) *TestReport {
	report := &TestReport{Ran: true}
	if len(packages) == 0 {
		packages = append([]string(nil), DefaultTestPackages...)
	}
	start := time.Now()
	for _, pkg := range packages {
		report.Packages = append(report.Packages, e.runOnePackageTest(ctx, repoRoot, pkg))
	}
	for _, p := range report.Packages {
		if p.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	report.DurationMS = time.Since(start).Milliseconds()
	return report
}

func (e *Evolver) runOnePackageTest(ctx context.Context, repoRoot, pkg string) TestPackageResult {
	result := TestPackageResult{Package: pkg}
	start := time.Now()
	// Bound each package so a hung test cannot stall the evolution loop forever.
	pkgCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(pkgCtx, "go", "test", pkg, "-count=1", "-timeout", "60s")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	result.DurationMS = time.Since(start).Milliseconds()
	result.Output = truncate(string(out), 2000)
	if err != nil {
		result.Passed = false
		if pkgCtx.Err() != nil {
			result.Error = "timeout: " + pkgCtx.Err().Error()
		} else {
			result.Error = err.Error()
		}
		return result
	}
	result.Passed = true
	return result
}

// FormatTestReport returns a human-readable Test stage section.
func (r *TestReport) FormatTestReport() string {
	if r == nil || !r.Ran {
		return "=== Test ===\nSkipped (RunTests=false).\n"
	}
	var sb strings.Builder
	sb.WriteString("=== Test ===\n")
	sb.WriteString(fmt.Sprintf("Packages: %d  Passed: %d  Failed: %d  Duration: %dms\n",
		len(r.Packages), r.Passed, r.Failed, r.DurationMS))
	for _, p := range r.Packages {
		status := "PASS"
		if !p.Passed {
			status = "FAIL"
		}
		sb.WriteString(fmt.Sprintf("  [%s] %s (%dms)\n", status, p.Package, p.DurationMS))
		if !p.Passed && p.Error != "" {
			sb.WriteString(fmt.Sprintf("         error: %s\n", truncate(p.Error, 200)))
		}
	}
	return sb.String()
}
