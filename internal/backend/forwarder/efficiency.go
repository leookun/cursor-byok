package forwarder

// EfficiencyNote is a lightweight evidence surface for evolution reports (ADR-044).
// It does not alter request routing; it only exposes stable counters for diagnostics.
type EfficiencyNote struct {
	Name    string
	Detail  string
	Healthy bool
}

// DefaultEfficiencyNote returns a deterministic baseline note for the forwarder package.
func DefaultEfficiencyNote() EfficiencyNote {
	return EfficiencyNote{
		Name:    "forwarder",
		Detail:  "bounded-recipe-surface",
		Healthy: true,
	}
}

// Summary returns a compact forwarder efficiency line.
func (n EfficiencyNote) Summary() string {
	status := "degraded"
	if n.Healthy {
		status = "healthy"
	}
	if n.Name == "" {
		n.Name = "forwarder"
	}
	if n.Detail == "" {
		n.Detail = "n/a"
	}
	return n.Name + " status=" + status + " detail=" + n.Detail
}
