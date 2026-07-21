package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxArchiveSizeBytes caps downloaded update archives at 200MB to prevent
// resource exhaustion from a malicious or corrupted feed.
const maxArchiveSizeBytes = 200 * 1024 * 1024

// ensureArchiveWithinDir verifies archivePath is inside allowedDir.
// It blocks path traversal via "..", absolute paths outside the allowed dir,
// and sibling escapes. The archive does not need to exist; only the path
// relationship is validated.
func ensureArchiveWithinDir(archivePath, allowedDir string) error {
	if strings.TrimSpace(archivePath) == "" {
		return fmt.Errorf("archive path is empty")
	}
	if strings.TrimSpace(allowedDir) == "" {
		return fmt.Errorf("allowed dir is empty")
	}

	cleanArchive, err := filepath.Abs(filepath.Clean(archivePath))
	if err != nil {
		return fmt.Errorf("archive path abs: %w", err)
	}
	cleanAllowed, err := filepath.Abs(filepath.Clean(allowedDir))
	if err != nil {
		return fmt.Errorf("allowed dir abs: %w", err)
	}

	rel, err := filepath.Rel(cleanAllowed, cleanArchive)
	if err != nil {
		return fmt.Errorf("archive rel: %w", err)
	}
	rel = filepath.Clean(rel)
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return fmt.Errorf("archive path escapes allowed dir: %q (rel=%q)", archivePath, rel)
	}
	return nil
}

// verifyArchiveChecksum compares the SHA256 of archivePath to expectedSHA256.
// The size cap (maxArchiveSizeBytes) is enforced regardless of whether a
// checksum was supplied. If expectedSHA256 is empty, checksum verification
// is skipped (returns nil) — the caller accepts the risk, e.g. when the
// upstream feed does not publish a checksum. A warning should be logged by
// the caller in that case.
func verifyArchiveChecksum(archivePath, expectedSHA256 string) error {
	info, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("stat archive: %w", err)
	}
	if info.Size() > maxArchiveSizeBytes {
		return fmt.Errorf("archive size %d exceeds max %d", info.Size(), maxArchiveSizeBytes)
	}

	expected := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(expectedSHA256), "sha256:"))
	if expected == "" {
		return nil
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("archive checksum mismatch: want %s got %s", expected, got)
	}
	return nil
}
