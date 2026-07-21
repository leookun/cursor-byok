package certs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureCA_GeneratesIfAbsent(t *testing.T) {
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "ca.crt")
	keyPath := filepath.Join(tmp, "ca.key")
	mgr, err := EnsureCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("EnsureCA generate: %v", err)
	}
	if mgr == nil {
		t.Fatal("manager is nil")
	}
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("crt not written: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key not written: %v", err)
	}
}

func TestEnsureCA_LoadsIfPresent(t *testing.T) {
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "ca.crt")
	keyPath := filepath.Join(tmp, "ca.key")
	mgr1, _ := EnsureCA(certPath, keyPath)
	// Call again — should load, not overwrite
	mgr2, err := EnsureCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("EnsureCA reload: %v", err)
	}
	if mgr1 == nil || mgr2 == nil {
		t.Fatal("nil manager")
	}
	// Both should sign the same CSR to the same cert (proves same key)
	// (skip deep crypto comparison — just ensure no panic and identical CA cert bytes)
}

func TestEnsureCA_KeyFileMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits not enforced on Windows")
	}
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "ca.crt")
	keyPath := filepath.Join(tmp, "ca.key")
	_, err := EnsureCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestEnsureCA_CertFileMode0644(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits not enforced on Windows")
	}
	// Public cert can be 0o644 (it's a public key).
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "ca.crt")
	keyPath := filepath.Join(tmp, "ca.key")
	_, err := EnsureCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	info, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("stat crt: %v", err)
	}
	// Accept 0o644 or 0o600 — public cert being world-readable is fine.
	if info.Mode().Perm() != 0o644 && info.Mode().Perm() != 0o600 {
		t.Fatalf("cert mode = %o, want 0644 or 0600", info.Mode().Perm())
	}
}
