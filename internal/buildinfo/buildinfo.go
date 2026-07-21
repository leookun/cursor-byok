package buildinfo

import "strings"

const (
	ReleaseRepo    = "leookun/cursor-byok"
	UpdateBaseURL  = "https://github.com/leookun/cursor-byok/releases/latest/download/"
	ReleasePageURL = "https://github.com/leookun/cursor-byok/releases"
)

// Version is the application version. Set at build time via ldflags:
//   go build -ldflags="-X cursor/internal/buildinfo.Version=0.0.41"
// Also kept in sync with build/config.yml info.version.
var Version = "0.0.41"

func CurrentVersion() string {
	version := strings.TrimSpace(strings.TrimPrefix(Version, "v"))
	if version == "" {
		return "0.0.0"
	}
	return version
}
