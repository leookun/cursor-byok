package pet

import (
	"image"
	"image/color"
	"testing"
)

func makeTestSheet(cols, rows, fw, fh int) (*image.RGBA, *PetData) {
	sheet := image.NewRGBA(image.Rect(0, 0, cols*fw, rows*fh))
	// 给每帧填不同颜色，便于校验切片正确性。
	for i := 0; i < cols*rows; i++ {
		col := i % cols
		row := i / cols
		c := uint8((i*37)%200 + 30)
		for y := 0; y < fh; y++ {
			for x := 0; x < fw; x++ {
				sheet.Set(col*fw+x, row*fh+y, color.RGBA{c, c, c, 255})
			}
		}
	}
	pet := &PetData{
		Columns:     cols,
		Rows:        rows,
		FrameWidth:  fw,
		FrameHeight: fh,
		TotalFrames: cols * rows,
	}
	return sheet, pet
}

func TestAtlas_LazySlice(t *testing.T) {
	sheet, pet := makeTestSheet(4, 2, 8, 8)
	a, err := NewFrameAtlas(sheet, pet)
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 8 {
		t.Fatalf("expected 8 frames, got %d", a.Len())
	}
	f0 := a.GetFrame(0)
	f5 := a.GetFrame(5)
	if f0 == nil || f5 == nil {
		t.Fatal("frames should not be nil")
	}
	// 校验第 5 帧（col=1,row=1）首像素颜色与构造一致。
	// 注意：SubImage 的坐标仍基于原 sheet，因此比较 f5 的 Bounds().Min 处像素。
	r0, _, _, _ := sheet.At(1*8, 1*8).RGBA()
	r5, _, _, _ := f5.At(f5.Bounds().Min.X, f5.Bounds().Min.Y).RGBA()
	if r0 != r5 {
		t.Fatalf("frame 5 pixel mismatch: sheet=%d atlas=%d", r0>>8, r5>>8)
	}
}

func TestAtlas_ScaledCached(t *testing.T) {
	sheet, pet := makeTestSheet(4, 2, 16, 16)
	a, err := NewFrameAtlas(sheet, pet)
	if err != nil {
		t.Fatal(err)
	}
	s1 := a.GetFrameScaled(0, 2.0)
	s2 := a.GetFrameScaled(0, 2.0)
	if s1 == nil || s2 == nil {
		t.Fatal("scaled frame nil")
	}
	if s1.Bounds().Dx() != 32 || s1.Bounds().Dy() != 32 {
		t.Fatalf("expected 32x32 scaled, got %dx%d", s1.Bounds().Dx(), s1.Bounds().Dy())
	}
	// 同一 key 应返回缓存的同一对象（指针相等）。
	if s1 != s2 {
		t.Fatal("scaled frames should be cached (same pointer)")
	}
	// scale=1 等价于原帧。
	orig := a.GetFrame(0)
	if a.GetFrameScaled(0, 1.0) != orig {
		t.Fatal("scale=1 should return original frame")
	}
}

func TestAtlas_OutOfRange(t *testing.T) {
	sheet, pet := makeTestSheet(2, 1, 4, 4)
	a, _ := NewFrameAtlas(sheet, pet)
	if a.GetFrame(-1) != nil {
		t.Fatal("negative index should be nil")
	}
	if a.GetFrame(99) != nil {
		t.Fatal("out-of-range index should be nil")
	}
}

func TestAtlas_InvalidLayout(t *testing.T) {
	sheet := image.NewRGBA(image.Rect(0, 0, 10, 10))
	pet := &PetData{Columns: 0, FrameWidth: 4, FrameHeight: 4}
	if _, err := NewFrameAtlas(sheet, pet); err == nil {
		t.Fatal("expected error for zero columns")
	}
}
