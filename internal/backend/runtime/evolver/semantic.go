// semantic.go implements rule-based semantic handbook?code diagnosis (ADR-037).
//
// Unlike path existence checks, these rules validate constitutional *meaning*:
// production invariants claimed by AGENTS.md / handbook constitution must hold
// in source. Rules are deterministic string/AST-light scanners ? no LLM judge.
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SemanticRule is one closed, deterministic invariant.
type SemanticRule struct {
	ID          string
	Title       string
	Severity    Severity
	Description string
}

// CanonicalSemanticRules is the v1 closed set of semantic constraints.
var CanonicalSemanticRules = []SemanticRule{
	{
		ID:          "mitm-whitelist",
		Title:       "MITM only intercepts *.cursor.sh",
		Severity:    SeverityError,
		Description: "MITM proxy must whitelist cursor.sh hosts; non-matching traffic stays direct.",
	},
	{
		ID:          "channelservice-nil-guard",
		Title:       "VirtualModel Execute rejects nil ChannelService",
		Severity:    SeverityError,
		Description: "Every VirtualModel provider Execute path must nil-guard channelSvc.",
	},
	{
		ID:          "no-parallel-model-registry",
		Title:       "No parallel Model Registry in VirtualModel packages",
		Severity:    SeverityError,
		Description: "Virtual models must bind existing ModelAdapter/channel IDs, not own key/registry tables.",
	},
	{
		ID:          "host-channel-resolver-injection",
		Title:       "Host injects non-nil channel resolver into VMR assembly",
		Severity:    SeverityError,
		Description: "buildVirtualModelManager production call must pass host.configs as ChannelResolver.",
	},
	{
		ID:          "aos-reuses-moa-channel-stack",
		Title:       "AOS reuses MOA ChannelService stack",
		Severity:    SeverityWarning,
		Description: "AOS must not invent a second HTTP client registry; it reuses vm_moa.ChannelService.",
	},
	{
		ID:          "dual-mode-routing",
		Title:       "Dual-mode routing local/upstream exists",
		Severity:    SeverityError,
		Description: "Server routing must support local and upstream execution modes from routing.mode.",
	},
	{
		ID:          "config-persistence-root",
		Title:       "Config/data root persistence path is defined",
		Severity:    SeverityWarning,
		Description: "User config/data should persist under the project data root convention.",
	},
	{
		ID:          "no-second-http-stack-in-vm",
		Title:       "VirtualModel packages do not own raw provider HTTP stacks",
		Severity:    SeverityWarning,
		Description: "VirtualModel packages should call through ChannelService/ModelAdapter, not ad-hoc provider HTTP clients with API keys.",
	},
	{
		ID:          "channel-service-nil-guard-ast",
		Title:       "AST verifies Execute channelSvc nil-guards",
		Severity:    SeverityError,
		Description: "Go AST scan of VirtualModel providers ensures Execute paths nil-check channelSvc.",
	},
	{
		ID:          "aos-benchmark-quality-surface",
		Title:       "AOS benchmark exposes phase quality surface",
		Severity:    SeverityWarning,
		Description: "AOS benchmark helpers must expose phase completeness and quality summary hooks.",
	},
	{
		ID:          "dual-mode-routing-ast",
		Title:       "AST verifies dual-mode routing constants",
		Severity:    SeverityError,
		Description: "Go AST scan ensures ModeLocal/ModeUpstream constants exist in server policy.",
	},
	{
		ID:          "runtime-efficiency-surfaces",
		Title:       "Runtime efficiency surfaces exist for propose prioritization",
		Severity:    SeverityWarning,
		Description: "Cache/tool/memory/optimize expose Summary/Stats helpers used by evolution proposals.",
	},
}

// diagnoseSemanticConstraints evaluates constitutional semantic rules against code.
func (e *Evolver) diagnoseSemanticConstraints(repoRoot string, report *DiagnosisReport) {
	checked := 0
	failed := 0

	if ok, detail := checkMITMWhitelist(repoRoot); !ok {
		failed++
		report.add(SeverityError, "semantic-constraint",
			fmt.Sprintf("mitm-whitelist: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkChannelServiceNilGuards(repoRoot); !ok {
		failed++
		report.add(SeverityError, "semantic-constraint",
			fmt.Sprintf("channelservice-nil-guard: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkNoParallelModelRegistry(repoRoot); !ok {
		failed++
		report.add(SeverityError, "semantic-constraint",
			fmt.Sprintf("no-parallel-model-registry: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkHostChannelResolverInjection(repoRoot); !ok {
		failed++
		report.add(SeverityError, "semantic-constraint",
			fmt.Sprintf("host-channel-resolver-injection: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkAOSReusesMOAChannelStack(repoRoot); !ok {
		failed++
		report.add(SeverityWarning, "semantic-constraint",
			fmt.Sprintf("aos-reuses-moa-channel-stack: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkDualModeRouting(repoRoot); !ok {
		failed++
		report.add(SeverityError, "semantic-constraint",
			fmt.Sprintf("dual-mode-routing: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkConfigPersistenceRoot(repoRoot); !ok {
		failed++
		report.add(SeverityWarning, "semantic-constraint",
			fmt.Sprintf("config-persistence-root: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkNoSecondHTTPStackInVM(repoRoot); !ok {
		failed++
		report.add(SeverityWarning, "semantic-constraint",
			fmt.Sprintf("no-second-http-stack-in-vm: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkChannelServiceNilGuardsAST(repoRoot); !ok {
		failed++
		report.add(SeverityError, "semantic-constraint",
			fmt.Sprintf("channel-service-nil-guard-ast: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkAOSBenchmarkQualitySurface(repoRoot); !ok {
		failed++
		report.add(SeverityWarning, "semantic-constraint",
			fmt.Sprintf("aos-benchmark-quality-surface: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkDualModeRoutingAST(repoRoot); !ok {
		failed++
		report.add(SeverityError, "semantic-constraint",
			fmt.Sprintf("dual-mode-routing-ast: %s", detail))
	} else {
		checked++
	}

	if ok, detail := checkRuntimeEfficiencySurfaces(repoRoot); !ok {
		failed++
		report.add(SeverityWarning, "semantic-constraint",
			fmt.Sprintf("runtime-efficiency-surfaces: %s", detail))
	} else {
		checked++
	}

	if failed == 0 {
		report.add(SeverityInfo, "semantic-constraint",
			fmt.Sprintf("semantic constraints satisfied: %d/%d rules", checked, len(CanonicalSemanticRules)))
	}
}

func readRepoFile(repoRoot, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func checkMITMWhitelist(repoRoot string) (bool, string) {
	text, err := readRepoFile(repoRoot, "internal/mitm/service.go")
	if err != nil {
		// Soft when package layout changes.
		return true, ""
	}
	hasSuffix := strings.Contains(text, `strings.HasSuffix(host, ".cursor.sh")`) ||
		strings.Contains(text, `HasSuffix(host, ".cursor.sh")`)
	hasAPI2 := strings.Contains(text, "api2.cursor.sh")
	if !hasSuffix && !hasAPI2 {
		return false, "internal/mitm/service.go lacks *.cursor.sh host whitelist checks"
	}
	// Guard against obvious "hijack everything" regressions in the same file.
	if strings.Contains(text, "return true // intercept all") || strings.Contains(text, "hijackAll := true") {
		return false, "MITM appears configured to intercept all hosts"
	}
	return true, ""
}

func checkChannelServiceNilGuards(repoRoot string) (bool, string) {
	root := filepath.Join(repoRoot, "internal", "backend", "virtualmodel")
	entries, err := os.ReadDir(root)
	if err != nil {
		return true, ""
	}
	var missing []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Only packages that implement providers with channel services.
		provider := filepath.Join(root, entry.Name(), "provider.go")
		raw, err := os.ReadFile(provider)
		if err != nil {
			continue
		}
		text := string(raw)
		if !strings.Contains(text, "channelSvc") {
			continue
		}
		// Require an Execute-path nil guard.
		if !(strings.Contains(text, "channelSvc == nil") || strings.Contains(text, "channelSvc==nil")) {
			missing = append(missing, entry.Name()+"/provider.go")
			continue
		}
		// Prefer explicit production error text for clarity.
		if !strings.Contains(text, "channel service is nil") && !strings.Contains(text, "ChannelService") {
			missing = append(missing, entry.Name()+"/provider.go(weak-guard)")
		}
	}
	if len(missing) > 0 {
		return false, "missing ChannelService nil-guard in: " + strings.Join(missing, ", ")
	}
	return true, ""
}

func checkNoParallelModelRegistry(repoRoot string) (bool, string) {
	root := filepath.Join(repoRoot, "internal", "backend", "virtualmodel")
	var offenders []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(raw)
		// Forbidden patterns for a parallel registry/key store.
		for _, bad := range []string{
			"type ModelRegistry struct",
			"type modelRegistry struct",
			"map[string]apiKey",
			"map[string]*APIKey",
			"RegisterProvider(",
			"providerAPIKeys",
		} {
			if strings.Contains(text, bad) {
				rel, _ := filepath.Rel(repoRoot, path)
				offenders = append(offenders, fmt.Sprintf("%s (%s)", filepath.ToSlash(rel), bad))
			}
		}
		return nil
	})
	if len(offenders) > 0 {
		return false, "possible parallel registry/key store: " + strings.Join(offenders, "; ")
	}
	return true, ""
}

func checkHostChannelResolverInjection(repoRoot string) (bool, string) {
	text, err := readRepoFile(repoRoot, "internal/backend/host.go")
	if err != nil {
		return false, "cannot read internal/backend/host.go"
	}
	if !strings.Contains(text, "buildVirtualModelManager(&cfg, optRuntime, host.configs)") {
		return false, "rebuild path does not pass host.configs as channelResolver"
	}
	if strings.Count(text, "if channelResolver != nil") < 2 {
		return false, "expected MOA+AOS channelResolver nil-guards in host assembly"
	}
	if !strings.Contains(text, "NewAdapterChannelService") {
		return false, "host does not construct AdapterChannelService"
	}
	return true, ""
}

func checkAOSReusesMOAChannelStack(repoRoot string) (bool, string) {
	text, err := readRepoFile(repoRoot, "internal/backend/virtualmodel/aos/provider.go")
	if err != nil {
		return true, ""
	}
	if !strings.Contains(text, "vm_moa.ChannelService") {
		return false, "AOS provider does not type against vm_moa.ChannelService"
	}
	// Disallow obvious second HTTP stack in AOS package.
	aosDir := filepath.Join(repoRoot, "internal", "backend", "virtualmodel", "aos")
	entries, err := os.ReadDir(aosDir)
	if err != nil {
		return true, ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(aosDir, entry.Name()))
		if err != nil {
			continue
		}
		body := string(raw)
		if strings.Contains(body, "http.Client") && strings.Contains(body, "apiKeys") {
			return false, "AOS appears to own HTTP client + api key store"
		}
		if strings.Contains(body, "type providerRegistry") {
			return false, "AOS defines providerRegistry"
		}
	}
	return true, ""
}

func checkDualModeRouting(repoRoot string) (bool, string) {
	text, err := readRepoFile(repoRoot, "internal/backend/server/policy.go")
	if err != nil {
		return false, "cannot read internal/backend/server/policy.go"
	}
	hasLocal := strings.Contains(text, `ModeLocal`) && strings.Contains(text, `"local"`)
	hasUpstream := strings.Contains(text, `ModeUpstream`) && strings.Contains(text, `"upstream"`)
	if !hasLocal || !hasUpstream {
		return false, "ExecutionMode must define both local and upstream"
	}
	mw, err := readRepoFile(repoRoot, "internal/backend/server/middleware.go")
	if err != nil {
		return false, "cannot read middleware.go for route mode wiring"
	}
	if !strings.Contains(mw, "parseExecutionMode") && !strings.Contains(mw, "RouteMode") {
		return false, "middleware does not wire routing mode into request context"
	}
	return true, ""
}

func checkConfigPersistenceRoot(repoRoot string) (bool, string) {
	// Accept either appdata helper or explicit config root convention.
	candidates := []string{
		"internal/appdata/paths.go",
		"internal/appdata/appdata.go",
		"internal/backend/server/config/store.go",
		"internal/backend/server/config/manager.go",
	}
	foundMarker := false
	for _, rel := range candidates {
		text, err := readRepoFile(repoRoot, rel)
		if err != nil {
			continue
		}
		if strings.Contains(text, "cursor-local-assistant") ||
			strings.Contains(text, "cursor-byok") ||
			strings.Contains(text, "DataRoot") ||
			strings.Contains(text, "ConfigPath") ||
			strings.Contains(text, "config.yaml") {
			foundMarker = true
			break
		}
	}
	// Also accept appdata package existence with any go file mentioning root.
	if !foundMarker {
		dir := filepath.Join(repoRoot, "internal", "appdata")
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					continue
				}
				body := string(raw)
				if strings.Contains(body, "cursor-local-assistant") || strings.Contains(body, "cursor-byok") || strings.Contains(body, "DataRoot") || strings.Contains(body, "config.yaml") {
					foundMarker = true
					break
				}
			}
		}
	}
	if !foundMarker {
		return false, "no config/data persistence root convention found under appdata/server config"
	}
	return true, ""
}

func checkNoSecondHTTPStackInVM(repoRoot string) (bool, string) {
	root := filepath.Join(repoRoot, "internal", "backend", "virtualmodel")
	var offenders []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// channel_bridge and adapter call paths are allowed to talk to existing runtime.
		base := filepath.Base(path)
		if base == "channel_bridge.go" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(raw)
		ownsHTTP := strings.Contains(text, "http.NewRequest") || strings.Contains(text, "http.Client{")
		ownsKeys := strings.Contains(text, "apiKey") || strings.Contains(text, "APIKey") || strings.Contains(text, "Authorization")
		if ownsHTTP && ownsKeys {
			rel, _ := filepath.Rel(repoRoot, path)
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(offenders) > 0 {
		return false, "virtualmodel package owns HTTP+auth stack: " + strings.Join(offenders, ", ")
	}
	return true, ""
}

func checkAOSBenchmarkQualitySurface(repoRoot string) (bool, string) {
	text, err := readRepoFile(repoRoot, "internal/benchmark/aos.go")
	if err != nil {
		return false, "internal/benchmark/aos.go missing"
	}
	needles := []string{
		"PhasesComplete",
		"AOSPhases",
		"QualityScore",
	}
	var missing []string
	for _, n := range needles {
		if !strings.Contains(text, n) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return false, "AOS benchmark quality surface missing: " + strings.Join(missing, ", ")
	}
	return true, ""
}

func checkRuntimeEfficiencySurfaces(repoRoot string) (bool, string) {
	checks := []struct {
		path   string
		needle string
	}{
		{"internal/backend/runtime/cache/runtime.go", "func (s *CacheStats) Summary("},
		{"internal/backend/runtime/tool/runtime.go", "func (rt *Runtime) CacheStats("},
		{"internal/backend/runtime/memory/runtime.go", "func (rt *Runtime) Stats("},
		{"internal/backend/runtime/optimize/runtime.go", "func (t *CostTracker) Summary("},
		{"internal/backend/forwarder/efficiency.go", "func DefaultEfficiencyNote("},
	}
	var missing []string
	for _, c := range checks {
		raw, err := readRepoFile(repoRoot, c.path)
		if err != nil || !strings.Contains(raw, c.needle) {
			missing = append(missing, c.path)
		}
	}
	if len(missing) > 0 {
		return false, "missing efficiency surfaces: " + strings.Join(missing, ", ")
	}
	return true, ""
}
