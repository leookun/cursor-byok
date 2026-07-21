package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureArchiveWithinDir_RejectsEscape(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "updates")
	if err := os.MkdirAll(allowed, 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		filepath.Join(tmp, "evil.bin"),
		filepath.Join(tmp, "..", "evil.bin"),
		"/etc/passwd",
		filepath.Join(allowed, "..", "evil.bin"),
	}
	for _, p := range cases {
		err := ensureArchiveWithinDir(p, allowed)
		if err == nil {
			t.Fatalf("expected error for %q, got nil", p)
		}
	}
}

func TestEnsureArchiveWithinDir_AcceptsInside(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "updates")
	if err := os.MkdirAll(allowed, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(allowed, "update.zip")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureArchiveWithinDir(inside, allowed); err != nil {
		t.Fatalf("expected OK for %q, got %v", inside, err)
	}
}

func TestVerifyArchiveChecksum_RejectsMismatch(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "a.zip")
	if err := os.WriteFile(archive, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := "deadbeef"
	if err := verifyArchiveChecksum(archive, bad); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestVerifyArchiveChecksum_AcceptsMatch(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "a.zip")
	if err := os.WriteFile(archive, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("payload"))
	good := hex.EncodeToString(sum[:])
	if err := verifyArchiveChecksum(archive, good); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestVerifyArchiveChecksum_SkipsWhenNoExpected(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "a.zip")
	if err := os.WriteFile(archive, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchiveChecksum(archive, ""); err != nil {
		t.Fatalf("expected nil for empty expected checksum, got %v", err)
	}
}

func TestVerifyArchiveChecksum_RejectsOversize(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "big.zip")
	big := make([]byte, maxArchiveSizeBytes+1)
	if err := os.WriteFile(archive, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchiveChecksum(archive, ""); err == nil {
		t.Fatal("expected oversize error")
	}
}
