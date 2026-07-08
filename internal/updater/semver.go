package updater

import (
	"strconv"
	"strings"
)

type semanticVersion struct {
	major      int
	minor      int
	patch      int
	prerelease string
}

func compareVersions(a, b string) int {
	left := parseVersion(a)
	right := parseVersion(b)

	switch {
	case left.major != right.major:
		return compareInts(left.major, right.major)
	case left.minor != right.minor:
		return compareInts(left.minor, right.minor)
	case left.patch != right.patch:
		return compareInts(left.patch, right.patch)
	}

	if left.prerelease == right.prerelease {
		return 0
	}
	if left.prerelease == "" {
		return 1
	}
	if right.prerelease == "" {
		return -1
	}
	if left.prerelease > right.prerelease {
		return 1
	}
	if left.prerelease < right.prerelease {
		return -1
	}
	return 0
}

func parseVersion(raw string) semanticVersion {
	clean := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	if clean == "" {
		return semanticVersion{}
	}

	var prerelease string
	if idx := strings.Index(clean, "-"); idx >= 0 {
		prerelease = clean[idx+1:]
		clean = clean[:idx]
	}

	parts := strings.Split(clean, ".")
	result := semanticVersion{prerelease: prerelease}
	if len(parts) > 0 {
		result.major = atoi(parts[0])
	}
	if len(parts) > 1 {
		result.minor = atoi(parts[1])
	}
	if len(parts) > 2 {
		result.patch = atoi(parts[2])
	}
	return result
}

func atoi(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}

func compareInts(a, b int) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	default:
		return 0
	}
}
