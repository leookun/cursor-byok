package aos

// DefaultTeam returns a preset team profile for general software development.
func DefaultTeam(leaderAdapter string) *TeamProfile {
	return &TeamProfile{
		Leader: LeaderConfig{AdapterID: leaderAdapter},
		Members: []MemberConfig{
			{
				ID: "frontend", Name: "Frontend Engineer",
				AdapterID: leaderAdapter,
				SystemPrompt: "You are a senior frontend developer. Expert in React, Vue, CSS, TailwindCSS. Focus on user experience and performance.",
				Tags: []string{"frontend", "vue", "react"},
				Capabilities: []string{"tool"},
				MaxContext: 200000,
			},
			{
				ID: "backend", Name: "Backend Engineer",
				AdapterID: leaderAdapter,
				SystemPrompt: "You are a Go architect. Expert in gRPC, SQL, microservices. Focus on code quality and maintainability.",
				Tags: []string{"backend", "go", "api", "database"},
				Capabilities: []string{"tool"},
				MaxContext: 64000,
			},
			{
				ID: "testing", Name: "QA Engineer",
				AdapterID: leaderAdapter,
				SystemPrompt: "You are a QA engineer. Expert in unit testing, integration testing, E2E. Focus on edge cases and error handling.",
				Tags: []string{"testing", "qa"},
				Capabilities: []string{"tool"},
				MaxContext: 1000000,
			},
		},
		Workflow: WorkflowConfig{
			Mode: "auto", MaxParallel: 4, Timeout: "120s", Retry: 1,
		},
		Sprints: SprintConfig{MaxIterations: 3},
	}
}
