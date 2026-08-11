package main

import "testing"

func TestVerifyReleaseTag(t *testing.T) {
	testCases := []struct {
		name    string
		tag     string
		version string
		wantErr bool
	}{
		{name: "stable", tag: "v0.0.48", version: "0.0.48"},
		{name: "prerelease", tag: "v1.2.3-rc.1", version: "1.2.3-rc.1"},
		{name: "missing v prefix", tag: "0.0.48", version: "0.0.48", wantErr: true},
		{name: "not semver", tag: "vnext", version: "next", wantErr: true},
		{name: "config mismatch", tag: "v0.0.48", version: "0.0.47", wantErr: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := verifyReleaseTag(testCase.tag, testCase.version)
			if testCase.wantErr && err == nil {
				t.Fatal("verifyReleaseTag() error = nil, want error")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("verifyReleaseTag() error = %v", err)
			}
		})
	}
}
