package pet

import (
	"path/filepath"
	"testing"
)

func testdata(path string) string {
	return filepath.Join("testdata", path)
}

func TestScanPetDir_Ready_Webp(t *testing.T) {
	m := ScanPetDir(testdata("Pet_A"))
	if m == nil {
		t.Fatal("ScanPetDir returned nil")
	}
	if m.Status != StatusReady {
		t.Errorf("expected StatusReady, got %s: errors=%v warnings=%v", m.Status, m.Errors, m.Warnings)
	}
	if m.SheetPath == "" {
		t.Error("spritesheet not found")
	}
	if filepath.Base(m.SheetPath) != "spritesheet.webp" {
		t.Errorf("expected spritesheet.webp, got %s", filepath.Base(m.SheetPath))
	}
}

func TestScanPetDir_Ready_Png(t *testing.T) {
	m := ScanPetDir(testdata("Pet_B"))
	if m == nil {
		t.Fatal("ScanPetDir returned nil")
	}
	if m.Status != StatusReady {
		t.Errorf("expected StatusReady, got %s", m.Status)
	}
	if filepath.Base(m.SheetPath) != "spritesheet.png" {
		t.Errorf("expected spritesheet.png, got %s", filepath.Base(m.SheetPath))
	}
}

func TestScanPetDir_Warning_NoSprite(t *testing.T) {
	m := ScanPetDir(testdata("Pet_C"))
	if m == nil {
		t.Fatal("ScanPetDir returned nil")
	}
	if m.Status != StatusBroken {
		t.Errorf("expected StatusBroken for missing spritesheet, got %s", m.Status)
	}
}

func TestScanPetDir_Broken_NoJson(t *testing.T) {
	m := ScanPetDir(testdata("Pet_D"))
	if m == nil {
		t.Fatal("ScanPetDir returned nil for Pet_D")
	}
	if m.Status != StatusBroken {
		t.Errorf("expected StatusBroken, got %s", m.Status)
	}
	if len(m.Errors) == 0 || m.Errors[0] != "缺少 pet.json" {
		t.Errorf("expected '缺少 pet.json' error, got %v", m.Errors)
	}
}

func TestScanPetDir_AutoDetect_Hero(t *testing.T) {
	m := ScanPetDir(testdata("Pet_E"))
	if m == nil {
		t.Fatal("ScanPetDir returned nil")
	}
	if m.SheetPath == "" {
		t.Error("spritesheet not found for hero.webp")
	}
	if filepath.Base(m.SheetPath) != "hero.webp" {
		t.Errorf("expected hero.webp, got %s", filepath.Base(m.SheetPath))
	}
}

func TestScanPetDir_AutoDetect_Atlas(t *testing.T) {
	m := ScanPetDir(testdata("Pet_F"))
	if m == nil {
		t.Fatal("ScanPetDir returned nil")
	}
	if m.SheetPath == "" {
		t.Error("spritesheet not found for atlas.png")
	}
	if filepath.Base(m.SheetPath) != "atlas.png" {
		t.Errorf("expected atlas.png, got %s", filepath.Base(m.SheetPath))
	}
}

func TestScanPetDir_All(t *testing.T) {
	dirs := []struct {
		name   string
		status PetStatus
	}{
		{"Pet_A", StatusReady},
		{"Pet_B", StatusReady},
		{"Pet_C", StatusBroken},
		{"Pet_D", StatusBroken},
		{"Pet_E", StatusReady}, // 有空 animations 和缺失 fps，但扫描阶段已做只读修复
		{"Pet_F", StatusReady},
	}
	for _, d := range dirs {
		t.Run(d.name, func(t *testing.T) {
			m := ScanPetDir(testdata(d.name))
			if m == nil {
				t.Fatal("nil")
			}
			if m.Status != d.status {
				t.Errorf("%s: expected %s, got %s (errors=%v, warnings=%v)",
					d.name, d.status, m.Status, m.Errors, m.Warnings)
			}
		})
	}
}

func TestRepairDefaults(t *testing.T) {
	// Pet_E 有空 animations 和缺失 fps/totalFrames
	m := ScanPetDir(testdata("Pet_E"))
	if m == nil {
		t.Fatal("nil")
	}
	RepairDefaults(m, true)
	if m.FPS <= 0 {
		t.Errorf("Repair should set default FPS, got %d", m.FPS)
	}
	if m.TotalFrames <= 0 {
		t.Errorf("Repair should set default TotalFrames, got %d", m.TotalFrames)
	}
	if len(m.AnimationNames) == 0 {
		t.Errorf("Repair should add default idle animation")
	}
	if m.Status != StatusReady {
		t.Errorf("after repair, expected Ready, got %s", m.Status)
	}
}

func TestStatusString(t *testing.T) {
	tests := map[PetStatus]string{
		StatusUnknown: "unknown",
		StatusScanned: "scanned",
		StatusWarning: "warning",
		StatusBroken:  "broken",
		StatusReady:   "ready",
		StatusRunning: "running",
	}
	for s, expected := range tests {
		if s.String() != expected {
			t.Errorf("Status %d: expected %q, got %q", s, expected, s.String())
		}
	}
}
