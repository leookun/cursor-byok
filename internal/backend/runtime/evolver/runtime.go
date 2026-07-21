package evolver

// Evolver is the Self-Evolution Runtime. It orchestrates the closed loop
// mandated by the project constitution (Chapter 00):
//
//	Research -> ADR -> Implementation -> Test -> Benchmark -> Documentation
//
// The Evolver diagnoses contradictions between handbook and code, inventories
// knowledge artifacts (sediment), optionally runs curated package tests
// (ADR-031), runs benchmarks, proposes next-phase priorities, and persists
// evidence. Source code is never auto-modified. Handbook mutation is limited
// to deterministic index/catalog repairs via AutoWriteback (ADR-030/031).
//
// Design: ADR-028/029/030/031. Research: docs/research/self-evolution-runtime.md.
type Evolver struct {
	// repoRoot is the cached repository root (go.mod location).
	// May be empty; most methods accept repoRoot as a parameter.
	repoRoot string
}

// NewEvolver creates a new Evolver runtime.
func NewEvolver() *Evolver {
	return &Evolver{}
}

// NewEvolverWithRoot creates an Evolver with a cached repo root.
// This is convenient when the caller already knows the repo root
// (e.g., from docguard.RepoRoot).
func NewEvolverWithRoot(repoRoot string) *Evolver {
	return &Evolver{repoRoot: repoRoot}
}

// RepoRoot returns the cached repository root, or empty string if unset.
func (e *Evolver) RepoRoot() string {
	if e == nil {
		return ""
	}
	return e.repoRoot
}
