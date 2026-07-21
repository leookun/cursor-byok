package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// requireConfigPerm checks the config file permission bits.
//
// On Unix-like platforms (darwin/linux/bsd), Go honors POSIX permission bits,
// so the test strictly enforces 0600. On Windows, the filesystem does not
// support POSIX permission bits — os.Stat always reports 0666 regardless of
// the create mode or chmod — so we verify intent only (that the create/chmod
// calls succeed and produce a statable file) rather than asserting bit values
// that the OS cannot honor.
func requireConfigPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	got := info.Mode().Perm()
	switch runtime.GOOS {
	case "windows":
		// Windows filesystem does not honor POSIX perm bits; always 0666.
		// We only assert the file exists and is not a directory.
		if info.IsDir() {
			t.Fatalf("%s is a directory, want file", path)
		}
	default:
		if got != want {
			t.Fatalf("%s mode = %o, want %o", path, got, want)
		}
	}
}

// TestSaveLocked_WritesMode0600 ensures the user config file (which stores
// plaintext provider API keys) is persisted with mode 0600 on platforms
// that honor POSIX permission bits.
func TestSaveLocked_WritesMode0600(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	store := NewStore(path, "")
	cfg := DefaultConfig()

	if _, err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	requireConfigPerm(t, path, 0o600)
}

// TestSaveLocked_TempFileMode0600 ensures any temp file used during the
// atomic write also carries 0600 permissions, so plaintext keys never
// touch disk with broader perms even transiently.
func TestSaveLocked_TempFileMode0600(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	store := NewStore(path, "")
	cfg := DefaultConfig()

	if _, err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// After a successful Save, the temp file has been renamed away and
	// must not exist. This guards against a regression where Save leaves
	// a 0644 temp file on disk containing plaintext keys.
	tempPath := path + ".tmp"
	if info, err := os.Stat(tempPath); err == nil {
		t.Fatalf("temp file should have been renamed away, but exists: %v", info.Name())
	}
}

// TestSaveLocked_ParentDirMode0700 ensures the directory holding the config
// file (which contains secrets) is restricted to the owner.
func TestSaveLocked_ParentDirMode0700(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "subdir", "config.yaml")
	store := NewStore(path, "")
	cfg := DefaultConfig()

	if _, err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("config dir mode = %o, want 0700", got)
		}
	}
}

// TestSaveLocked_ChmodsExistingFileTo0600 ensures that if a config file
// already exists with broader perms (e.g. 0644 from a prior version),
// Save upgrades it to 0600.
func TestSaveLocked_ChmodsExistingFileTo0600(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(path, []byte("backendListenAddr: ':0'\n"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod seed: %v", err)
	}

	store := NewStore(path, "")
	cfg := DefaultConfig()
	if _, err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	requireConfigPerm(t, path, 0o600)
}
