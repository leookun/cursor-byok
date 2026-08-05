package app

import (
	"reflect"
	"testing"
)

func TestWindowsAdditionalBrowserArgs(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want []string
	}{
		{name: "unset"},
		{name: "enabled with one", env: "1", want: []string{"--no-sandbox"}},
		{name: "enabled with true", env: " true ", want: []string{"--no-sandbox"}},
		{name: "disabled", env: "false"},
		{name: "invalid", env: "yes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(disableWebViewSandboxEnv, tt.env)
			if got := windowsAdditionalBrowserArgs(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("windowsAdditionalBrowserArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}
