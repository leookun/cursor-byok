package pet

import (
	"fmt"
	"regexp"
	"strings"
)

// petIDPattern restricts pet IDs to safe relative identifiers:
// ascii letters, digits, underscore, hyphen; length 1..64.
var petIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidatePetID validates a user-supplied pet ID (e.g. from Wails frontend).
// Returns the trimmed ID if safe, or an error if it could escape the pets
// directory via path traversal or contains unsafe characters.
//
// Safe IDs match ^[A-Za-z0-9_-]{1,64}$ after trimming surrounding whitespace.
// This denies: ".", "..", paths with "/" or "\", absolute paths, hidden names
// starting with ".", names with spaces, and overly long identifiers.
func ValidatePetID(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if !petIDPattern.MatchString(trimmed) {
		return "", fmt.Errorf("invalid pet id: must be 1-64 chars of [A-Za-z0-9_-]")
	}
	return trimmed, nil
}
