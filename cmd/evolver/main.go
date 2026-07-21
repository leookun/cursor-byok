package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"cursor/internal/backend/runtime/evolver"
	"cursor/internal/docguard"
)

func main() {
	writeback := flag.Bool("writeback", false, "apply safe deterministic handbook index writeback (ADR-030/031)")
	runTests := flag.Bool("test", false, "run curated package Test stage (ADR-031)")
	ciMode := flag.Bool("ci", false, "CI gate: run tests and exit non-zero on diagnosis errors or test failures (ADR-031)")
	executePlan := flag.Bool("execute", false, "execute allowlisted TaskPlan actions (auto-writeback/run-tests) with audit trail (ADR-039)")
	scaffoldADRFromResearch := flag.String("scaffold-adr-from-research", "", "scaffold Proposed ADR draft from research note path or basename (deterministic, no LLM)")
	flag.Parse()

	if *ciMode {
		*runTests = true
	}

	wd, _ := os.Getwd()
	root, err := docguard.RepoRoot(wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RepoRoot: %v\n", err)
		os.Exit(1)
	}

	e := evolver.NewEvolverWithRoot(root)

	if *scaffoldADRFromResearch != "" {
		result, err := e.ScaffoldADRFromResearch(root, *scaffoldADRFromResearch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "-scaffold-adr-from-research: %v\n", err)
			os.Exit(1)
		}
		if result == nil {
			fmt.Println("ADR already exists for this research topic; no new scaffold created.")
			return
		}
		fmt.Printf("Created ADR scaffold: %s\n", result.Path)
		fmt.Println("Status: Proposed (not accepted). Review and update before marking Accepted.")
		fmt.Println("Tip: after finalising, run:")
		fmt.Println("  go run ./cmd/evolver -writeback")
		fmt.Println("to sync the ADR index in 28_ADR_Guide.md.")
		return
	}

	opts := evolver.EvolveOptions{RunTests: *runTests}
	report := e.EvolveWithOptions(context.Background(), root, nil, opts)

	// ADR-029: close the operational loop by persisting Markdown + JSON evidence.
	result, err := e.Persist(root, report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Persist: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(report.FormatEvolutionReport())
	fmt.Println("=== Persistence ===")
	fmt.Printf("Markdown: %s\n", result.MarkdownPath)
	fmt.Printf("JSON:     %s\n", result.JSONPath)
	fmt.Printf("Baseline: %v\n", result.BaselineUpdated)
	fmt.Print(result.FormatWritebackGuidance())

	exitCode := 0
	if *executePlan {
		exec := e.ExecuteTaskPlan(context.Background(), root, report.TaskPlan)
		report.TaskPlanExecution = exec
		fmt.Print(exec.FormatTaskPlanExecution())
		// Re-persist so the execution audit is part of living evidence.
		if result2, err := e.Persist(root, report); err == nil {
			fmt.Printf("Execution audit persisted: %s\n", result2.MarkdownPath)
		}
		if exec != nil && exec.Failed > 0 {
			exitCode = 1
		}
		// Execute implies writeback-equivalent for docs tasks; keep indexes healed.
		if _, err := e.AutoWriteback(root, nil); err != nil {
			fmt.Fprintf(os.Stderr, "AutoWriteback(after execute): %v\n", err)
			os.Exit(1)
		}
	}
	if *writeback {
		// ADR-030/031: apply only deterministic index/catalog repairs, then re-diagnose.
		aw, err := e.AutoWriteback(root, result.WritebackGuidance)
		if err != nil {
			fmt.Fprintf(os.Stderr, "AutoWriteback: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("=== AutoWriteback ===")
		fmt.Print(aw.FormatAutoWriteback())

		report2 := e.EvolveWithOptions(context.Background(), root, nil, opts)
		result2, err := e.Persist(root, report2)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Persist(after writeback): %v\n", err)
			os.Exit(1)
		}
		fmt.Println("=== Post-Writeback Diagnosis ===")
		fmt.Println(report2.FormatEvolutionReport())
		fmt.Printf("Markdown: %s\n", result2.MarkdownPath)
		fmt.Print(result2.FormatWritebackGuidance())

		// Second Persist may create a new evolution report; always re-heal safe indexes.
		aw2, err := e.AutoWriteback(root, result2.WritebackGuidance)
		if err != nil {
			fmt.Fprintf(os.Stderr, "AutoWriteback(final): %v\n", err)
			os.Exit(1)
		}
		fmt.Println("=== Final AutoWriteback ===")
		fmt.Print(aw2.FormatAutoWriteback())
		report = report2
	}

	if *ciMode {
		if report.Diagnosis != nil && report.Diagnosis.Errors > 0 {
			fmt.Fprintf(os.Stderr, "CI gate failed: %d diagnosis error(s)\n", report.Diagnosis.Errors)
			exitCode = 1
		}
		if report.Tests != nil && report.Tests.Ran && report.Tests.Failed > 0 {
			fmt.Fprintf(os.Stderr, "CI gate failed: %d package test failure(s)\n", report.Tests.Failed)
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}
