package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeletePet_RejectsTraversalID(t *testing.T) {
	tmp := t.TempDir()
	svc := newPetServiceWithDir(tmp)

	parent := filepath.Join(tmp, "..")
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		t.Fatalf("parent dir baseline stat failed: %v", err)
	}

	if err := svc.DeletePet(".."); err == nil {
		t.Fatal("expected error for '..' petID")
	}

	info2, err := os.Stat(parent)
	if err != nil || !info2.IsDir() {
		t.Fatalf("parent dir stat changed or failed after DeletePet('..'): %v", err)
	}

	if err := svc.DeletePet("/etc/passwd"); err == nil {
		t.Fatal("expected error for absolute petID")
	}

	if err := svc.DeletePet("has/slash"); err == nil {
		t.Fatal("expected error for slash-containing petID")
	}
}
