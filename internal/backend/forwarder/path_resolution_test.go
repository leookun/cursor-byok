package forwarder

import "testing"

func TestIsAbsoluteToolPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "empty", path: "", want: false},
		{name: "spaces", path: "   ", want: false},
		{name: "relative", path: "internal/backend/forwarder/path_resolution.go", want: false},
		{name: "dot relative", path: "./path_resolution.go", want: false},
		{name: "parent relative", path: "../path_resolution.go", want: false},
		{name: "windows drive relative", path: `C:path_resolution.go`, want: false},
		{name: "windows drive only", path: `C:`, want: false},
		{name: "invalid drive prefix", path: `1:/path_resolution.go`, want: false},
		{name: "posix absolute", path: "/Users/example/path_resolution.go", want: true},
		{name: "posix absolute with spaces", path: "  /tmp/path_resolution.go  ", want: true},
		{name: "windows backslash absolute", path: `C:\Users\example\path_resolution.go`, want: true},
		{name: "windows slash absolute", path: `C:/Users/example/path_resolution.go`, want: true},
		{name: "windows unc backslash", path: `\\server\share\path_resolution.go`, want: true},
		{name: "windows unc slash", path: `//server/share/path_resolution.go`, want: true},
		{name: "windows extended drive", path: `\\?\C:\Users\example\path_resolution.go`, want: true},
		{name: "windows extended unc", path: `\\?\UNC\server\share\path_resolution.go`, want: true},
		{name: "windows device path", path: `\\.\C:\Users\example\path_resolution.go`, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isAbsoluteToolPath(test.path); got != test.want {
				t.Fatalf("isAbsoluteToolPath(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}
