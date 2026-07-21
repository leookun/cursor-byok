package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Artifact represents a single documentation artifact (ADR, research note, or report).
type Artifact struct {
	Type     string `json:"type"`     // "adr", "research", "report"
	Filename string `json:"filename"`
	Path     string `json:"path"`     // relative to repo root
	// ADRID is the numeric ID for ADRs (e.g., "028"); empty for other types.
	ADRID string `json:"adrId,omitempty"`
}

// KnowledgeGraph is the output of Sediment(). It inventories all project
// artifacts and cross-references them to detect orphans and gaps.
type KnowledgeGraph struct {
	ADRs         []Artifact `json:"adrs"`
	ResearchNotes []Artifact `json:"researchNotes"`
	Reports      []Artifact `json:"reports"`
	// OrphanResearch lists research notes that have no corresponding ADR.
	OrphanResearch []string `json:"orphanResearch"`
	// OrphanADR lists ADRs that cite no research note.
	OrphanADR []string `json:"orphanADR"`
	// TotalArtifacts is the count of all discovered artifacts.
	TotalArtifacts int `json:"totalArtifacts"`
}

// Sediment inventories all project artifacts (ADRs, research notes, reports)
// and cross-references them to detect orphans and gaps.
// This is the "knowledge sedimentation" step: it builds a map of what the
// project knows and what evidence backs each decision.
func (e *Evolver) Sediment(repoRoot string) *KnowledgeGraph {
	kg := &KnowledgeGraph{}

	kg.ADRs = listArtifacts(repoRoot, "docs/adr", "adr")
	kg.ResearchNotes = listArtifacts(repoRoot, "docs/research", "research")
	kg.Reports = listArtifacts(repoRoot, "docs/reports", "report")
	kg.TotalArtifacts = len(kg.ADRs) + len(kg.ResearchNotes) + len(kg.Reports)

	kg.detectOrphans(repoRoot)

	return kg
}

// listArtifacts reads a directory and returns Artifact entries for each .md file.
func listArtifacts(repoRoot, relDir, artifactType string) []Artifact {
	dir := filepath.Join(repoRoot, filepath.FromSlash(relDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Artifact
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		art := Artifact{
			Type:     artifactType,
			Filename: e.Name(),
			Path:     filepath.ToSlash(filepath.Join(relDir, e.Name())),
		}
		if artifactType == "adr" {
			// Extract numeric ID from filename like "028-self-evolution-runtime.md".
			if len(e.Name()) >= 3 {
				art.ADRID = e.Name()[:3]
			}
		}
		out = append(out, art)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Filename < out[j].Filename
	})
	return out
}

// detectOrphans cross-references ADRs and research notes.
// An ADR is "orphaned" if it does not cite any docs/research/ path.
// A research note is "orphaned" if no ADR cites it.
func (kg *KnowledgeGraph) detectOrphans(repoRoot string) {
	// Build a set of research basenames cited by ADRs.
	citedResearch := map[string]bool{}
	adrDir := filepath.Join(repoRoot, "docs", "adr")
	for _, adr := range kg.ADRs {
		content, err := os.ReadFile(filepath.Join(adrDir, adr.Filename))
		if err != nil {
			continue
		}
		text := string(content)
		for _, rn := range kg.ResearchNotes {
			if strings.Contains(text, rn.Filename) || strings.Contains(text, "docs/research/"+rn.Filename) {
				citedResearch[rn.Filename] = true
			}
		}
	}

	// Research notes not cited by any ADR.
	for _, rn := range kg.ResearchNotes {
		if !citedResearch[rn.Filename] {
			kg.OrphanResearch = append(kg.OrphanResearch, rn.Filename)
		}
	}

	// ADRs that cite no research note at all.
	for _, adr := range kg.ADRs {
		content, err := os.ReadFile(filepath.Join(adrDir, adr.Filename))
		if err != nil {
			continue
		}
		text := string(content)
		citesAny := false
		for _, rn := range kg.ResearchNotes {
			if strings.Contains(text, rn.Filename) || strings.Contains(text, "docs/research/"+rn.Filename) {
				citesAny = true
				break
			}
		}
		if !citesAny {
			kg.OrphanADR = append(kg.OrphanADR, adr.ADRID)
		}
	}

	sort.Strings(kg.OrphanResearch)
	sort.Strings(kg.OrphanADR)
}

// FormatSediment returns a human-readable knowledge graph summary.
func (kg *KnowledgeGraph) FormatSediment() string {
	var sb strings.Builder
	sb.WriteString("=== Knowledge Graph (Sediment) ===\n")
	sb.WriteString(fmt.Sprintf("Total artifacts: %d\n", kg.TotalArtifacts))
	sb.WriteString(fmt.Sprintf("  ADRs:          %d\n", len(kg.ADRs)))
	sb.WriteString(fmt.Sprintf("  Research notes: %d\n", len(kg.ResearchNotes)))
	sb.WriteString(fmt.Sprintf("  Reports:       %d\n", len(kg.Reports)))
	sb.WriteString("\n")

	if len(kg.OrphanResearch) > 0 {
		sb.WriteString(fmt.Sprintf("Research notes without ADR (%d):\n", len(kg.OrphanResearch)))
		for _, name := range kg.OrphanResearch {
			sb.WriteString(fmt.Sprintf("  - %s\n", name))
		}
		sb.WriteString("\n")
	}

	if len(kg.OrphanADR) > 0 {
		sb.WriteString(fmt.Sprintf("ADRs without research citation (%d):\n", len(kg.OrphanADR)))
		for _, id := range kg.OrphanADR {
			sb.WriteString(fmt.Sprintf("  - ADR-%s\n", id))
		}
		sb.WriteString("\n")
	}

	if len(kg.OrphanResearch) == 0 && len(kg.OrphanADR) == 0 {
		sb.WriteString("No orphans detected: all ADRs cite research and all research is cited.\n")
	}

	return sb.String()
}
