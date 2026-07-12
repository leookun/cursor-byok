package pet

import (
	"testing"
)

func TestEmbeddedLoad(t *testing.T) {
	// 测试嵌入资源是否能正常加载
	data, err := LoadPetJSON(EmbeddedDir)
	if err != nil {
		t.Fatalf("LoadPetJSON(EmbeddedDir) failed: %v", err)
	}
	if data == nil {
		t.Fatal("data is nil")
	}
	t.Logf("Embedded pet.json: id=%s, frame=%dx%d", data.ID, data.FrameWidth, data.FrameHeight)

	// 测试嵌入 spritesheet
	img, err := LoadSheet("nonexistent.png")
	if err != nil {
		t.Fatalf("LoadSheet fallback to embedded failed: %v", err)
	}
	if img == nil {
		t.Fatal("image is nil")
	}
	t.Logf("Embedded spritesheet: %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
}
