package appdata

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setHomeTempDir sets the platform-appropriate home-directory env var
// to tmpDir so os.UserHomeDir() returns the isolated test directory.
func setHomeTempDir(t *testing.T, tmpDir string) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("USERPROFILE", tmpDir)
	default:
		setHomeTempDir(t, tmpDir)
	}
}

func TestRootDir(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	dir := RootDir()
	expected := filepath.Join(tmpDir, appDirName)
	if dir != expected {
		t.Errorf("RootDir() = %s, want %s", dir, expected)
	}
}

func TestRootDir_AfterMigration(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	// Set up only the old dir — RootDir should trigger migration
	oldDir := filepath.Join(tmpDir, legacyAppDirName)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDir); os.IsNotExist(err) {
		t.Fatal("old dir should exist before migration")
	}

	_ = RootDir()

	// Old dir should be renamed to new dir after MigrateIfNeeded runs
	newDir := filepath.Join(tmpDir, appDirName)
	if _, err := os.Stat(newDir); os.IsNotExist(err) {
		t.Error("new dir should exist after RootDir triggers migration")
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old dir should be gone after migration")
	}
}

func TestConfigFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := ConfigFilePath()
	expected := filepath.Join(tmpDir, appDirName, "config.yaml")
	if path != expected {
		t.Errorf("ConfigFilePath() = %s, want %s", path, expected)
	}
}

func TestDataRootPath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := DataRootPath()
	expected := filepath.Join(tmpDir, appDirName, "data")
	if path != expected {
		t.Errorf("DataRootPath() = %s, want %s", path, expected)
	}
}

func TestHistoryRootPath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := HistoryRootPath()
	expected := filepath.Join(tmpDir, appDirName, "history")
	if path != expected {
		t.Errorf("HistoryRootPath() = %s, want %s", path, expected)
	}
}

func TestLogsRootPath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := LogsRootPath()
	expected := filepath.Join(tmpDir, appDirName, "logs")
	if path != expected {
		t.Errorf("LogsRootPath() = %s, want %s", path, expected)
	}
}

func TestCACertFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := CACertFilePath()
	expected := filepath.Join(tmpDir, appDirName, "data", "ca.crt")
	if path != expected {
		t.Errorf("CACertFilePath() = %s, want %s", path, expected)
	}
}

func TestCAKeyFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := CAKeyFilePath()
	expected := filepath.Join(tmpDir, appDirName, "data", "ca.key")
	if path != expected {
		t.Errorf("CAKeyFilePath() = %s, want %s", path, expected)
	}
}

func TestUpdatesRootPath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := UpdatesRootPath()
	expected := filepath.Join(tmpDir, appDirName, "data", "updates")
	if path != expected {
		t.Errorf("UpdatesRootPath() = %s, want %s", path, expected)
	}
}

func TestUsageFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := UsageFilePath()
	expected := filepath.Join(tmpDir, appDirName, "history", "usage.json")
	if path != expected {
		t.Errorf("UsageFilePath() = %s, want %s", path, expected)
	}
}

func TestRulesRootPath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := RulesRootPath()
	expected := filepath.Join(tmpDir, appDirName, "rules")
	if path != expected {
		t.Errorf("RulesRootPath() = %s, want %s", path, expected)
	}
}

func TestCodebaseIndexRootPath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := CodebaseIndexRootPath()
	expected := filepath.Join(tmpDir, appDirName, "data", "codebase-index")
	if path != expected {
		t.Errorf("CodebaseIndexRootPath() = %s, want %s", path, expected)
	}
}

func TestDocsIndexRootPath(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	path := DocsIndexRootPath()
	expected := filepath.Join(tmpDir, appDirName, "data", "docs-index")
	if path != expected {
		t.Errorf("DocsIndexRootPath() = %s, want %s", path, expected)
	}
}

func TestMigrateIfNeeded_NewDirExists_Noop(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	newDir := filepath.Join(tmpDir, appDirName)
	oldDir := filepath.Join(tmpDir, legacyAppDirName)
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}

	MigrateIfNeeded()

	// Both should survive: new-dir-exists check fires first
	if _, err := os.Stat(newDir); os.IsNotExist(err) {
		t.Error("new dir should still exist after no-op")
	}
	if _, err := os.Stat(oldDir); os.IsNotExist(err) {
		t.Error("old dir should still exist when migration was skipped")
	}
}

func TestMigrateIfNeeded_OnlyOldDirExists_Renames(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	oldDir := filepath.Join(tmpDir, legacyAppDirName)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Place a file inside to verify the rename carries content
	trackFile := filepath.Join(oldDir, ".migrated")
	if err := os.WriteFile(trackFile, []byte("tracked"), 0o644); err != nil {
		t.Fatal(err)
	}

	newDir := filepath.Join(tmpDir, appDirName)
	MigrateIfNeeded()

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old dir should be renamed after migration")
	}
	if _, err := os.Stat(newDir); os.IsNotExist(err) {
		t.Fatal("new dir should exist after migration")
	}
	// Content should be preserved
	migratedFile := filepath.Join(newDir, ".migrated")
	if _, err := os.Stat(migratedFile); os.IsNotExist(err) {
		t.Error("file inside old dir should be preserved after migration")
	}
}

func TestMigrateIfNeeded_NeitherDirExists_Noop(t *testing.T) {
	tmpDir := t.TempDir()
	setHomeTempDir(t, tmpDir)

	MigrateIfNeeded()

	newDir := filepath.Join(tmpDir, appDirName)
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Error("MigrateIfNeeded must not create the new dir when neither exists")
	}
}
