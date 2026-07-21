package config

import "testing"

func TestNormalizeAOSExecutionMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: AOSExecutionModeCursorTask},
		{input: "unknown", want: AOSExecutionModeCursorTask},
		{input: "hybrid", want: AOSExecutionModeCursorTask},
		{input: " cursor_task ", want: AOSExecutionModeCursorTask},
		{input: "INTERNAL", want: AOSExecutionModeInternal},
	}
	for _, test := range tests {
		if got := NormalizeAOSExecutionMode(test.input); got != test.want {
			t.Errorf("NormalizeAOSExecutionMode(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestNormalizeConfigPreservesAOSExecutionMode(t *testing.T) {
	input := DefaultConfig()
	input.VirtualModels.AOS = &AOSConfig{
		Enabled:       true,
		ExecutionMode: "internal",
		Leader:        AOSLeaderConfig{AdapterID: " leader "},
	}
	output, err := NormalizeConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if output.VirtualModels.AOS == nil {
		t.Fatal("normalized AOS config was dropped")
	}
	if output.VirtualModels.AOS.ExecutionMode != AOSExecutionModeInternal {
		t.Fatalf("execution mode = %q, want %q", output.VirtualModels.AOS.ExecutionMode, AOSExecutionModeInternal)
	}
	if output.VirtualModels.AOS.Leader.AdapterID != "leader" {
		t.Fatalf("leader adapter = %q, want trimmed value", output.VirtualModels.AOS.Leader.AdapterID)
	}
}

func TestNormalizeConfigPreservesDisabledAOSConfig(t *testing.T) {
	input := DefaultConfig()
	input.VirtualModels.AOS = &AOSConfig{
		Enabled:       false,
		ExecutionMode: " internal ",
		Leader:        AOSLeaderConfig{AdapterID: " leader "},
		Members: []AOSMemberConfig{{
			ID:        " member ",
			AdapterID: " adapter ",
		}},
	}

	output, err := NormalizeConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if output.VirtualModels.AOS == nil {
		t.Fatal("disabled AOS config was dropped")
	}
	if output.VirtualModels.AOS.Enabled {
		t.Fatal("disabled AOS config became enabled")
	}
	if output.VirtualModels.AOS.ExecutionMode != AOSExecutionModeInternal {
		t.Fatalf("execution mode = %q, want %q", output.VirtualModels.AOS.ExecutionMode, AOSExecutionModeInternal)
	}
	if output.VirtualModels.AOS.Leader.AdapterID != "leader" {
		t.Fatalf("leader adapter = %q, want trimmed value", output.VirtualModels.AOS.Leader.AdapterID)
	}
	member := output.VirtualModels.AOS.Members[0]
	if member.ID != "member" || member.AdapterID != "adapter" {
		t.Fatalf("member = %#v, want trimmed ID and adapter", member)
	}
}
