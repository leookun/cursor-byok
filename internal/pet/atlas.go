package pet

import (
	"fmt"
	"image"
	"sync"

	"golang.org/x/image/draw"
)

// FrameAtlas 从 spritesheet 中切割的帧集合。
//
// v2 Phase 9 增强：
//   - 惰性切片：帧不在构造时一次性切好，而是首次 GetFrame 时按需切片，
//     降低大 spritesheet 的内存与启动开销；已切片帧缓存复用（零拷贝给渲染）。
//   - 缩放缓存：GetFrameScaled 提供多尺寸帧（如 DPR/高分屏），按 "idx@scale"
//     缓存，避免每帧重复缩放分配。
//   - 线程安全：所有访问经 mu 保护，引擎线程独占调用时无竞争。
type FrameAtlas struct {
	sheet  image.Image
	pet    *PetData
	cols   int
	fw, fh int

	mu     sync.Mutex
	frames []image.Image          // 懒填充，nil 表示尚未切片
	scaled map[string]image.Image // key: fmt.Sprintf("%d@%v", idx, scale)
}

// NewFrameAtlas 根据 pet.json 的定义从 spritesheet 创建帧图集（惰性切片）。
func NewFrameAtlas(sheet image.Image, pet *PetData) (*FrameAtlas, error) {
	if sheet == nil {
		return nil, fmt.Errorf("spritesheet image is nil")
	}
	if pet == nil {
		return nil, fmt.Errorf("pet data is nil")
	}

	cols := pet.Columns
	fw := pet.FrameWidth
	fh := pet.FrameHeight

	if cols <= 0 || fw <= 0 || fh <= 0 {
		return nil, fmt.Errorf("invalid spritesheet layout: columns=%d frame=%dx%d", cols, fw, fh)
	}

	rows := pet.Rows
	if rows <= 0 {
		rows = (pet.TotalFrames + cols - 1) / cols
	}

	// 校验 spritesheet 图像尺寸是否足够容纳所有帧
	bounds := sheet.Bounds()
	sheetW := bounds.Dx()
	sheetH := bounds.Dy()
	if cols*fw > sheetW {
		return nil, fmt.Errorf("spritesheet width %d too small: need at least %d (columns=%d * frameWidth=%d)",
			sheetW, cols*fw, cols, fw)
	}
	if rows*fh > sheetH {
		return nil, fmt.Errorf("spritesheet height %d too small: need at least %d (rows=%d * frameHeight=%d)",
			sheetH, rows*fh, rows, fh)
	}

	total := pet.TotalFrames
	if total <= 0 {
		total = cols * rows
	}

	return &FrameAtlas{
		sheet:  sheet,
		pet:    pet,
		cols:   cols,
		fw:     fw,
		fh:     fh,
		frames: make([]image.Image, total),
		scaled: make(map[string]image.Image),
	}, nil
}

// frameRect 返回第 i 帧在 spritesheet 中的矩形（图像本地坐标）。
func (a *FrameAtlas) frameRect(i int) image.Rectangle {
	col := i % a.cols
	row := i / a.cols
	x0 := col * a.fw
	y0 := row * a.fh
	return image.Rect(x0, y0, x0+a.fw, y0+a.fh)
}

// sliceFrame 切片第 i 帧（调用者须持有锁）。
func (a *FrameAtlas) sliceFrame(i int) image.Image {
	if a.frames[i] != nil {
		return a.frames[i]
	}
	r := a.frameRect(i)
	// 优先用 SubImage 零拷贝（*image.RGBA/*image.NRGBA 等支持），
	// 不支持时退回逐像素复制，保证对任何 spritesheet 来源都可用。
	type subImager interface {
		SubImage(rect image.Rectangle) image.Image
	}
	if si, ok := a.sheet.(subImager); ok {
		a.frames[i] = si.SubImage(r)
		return a.frames[i]
	}
	// fallback：逐像素复制。
	sub := image.NewRGBA(image.Rect(0, 0, a.fw, a.fh))
	for y := 0; y < a.fh; y++ {
		for x := 0; x < a.fw; x++ {
			sub.Set(x, y, a.sheet.At(r.Min.X+x, r.Min.Y+y))
		}
	}
	a.frames[i] = sub
	return a.frames[i]
}

// GetFrame 获取指定索引的帧（惰性切片，首次访问时切片并缓存）。
func (a *FrameAtlas) GetFrame(index int) image.Image {
	if index < 0 || index >= len(a.frames) {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sliceFrame(index)
}

// GetFrameScaled 获取按 scale 缩放后的帧（带缓存）。
// scale=1 等价于 GetFrame。缩放结果缓存于 scaled map，重复请求零分配。
func (a *FrameAtlas) GetFrameScaled(index int, scale float64) image.Image {
	if scale <= 0 {
		scale = 1
	}
	if scale == 1 {
		return a.GetFrame(index)
	}
	key := fmt.Sprintf("%d@%v", index, scale)
	a.mu.Lock()
	if f, ok := a.scaled[key]; ok {
		a.mu.Unlock()
		return f
	}
	a.mu.Unlock()

	src := a.GetFrame(index)
	if src == nil {
		return nil
	}
	dst := resizeNearest(src, scale)

	a.mu.Lock()
	a.scaled[key] = dst
	a.mu.Unlock()
	return dst
}

// resizeNearest 用最近邻算法把图像缩放到 scale 倍，返回 *image.RGBA。
// 最近邻实现简单、无外部依赖，且对像素风桌宠观感无损。
func resizeNearest(src image.Image, scale float64) *image.RGBA {
	b := src.Bounds()
	w := int(float64(b.Dx()) * scale)
	h := int(float64(b.Dy()) * scale)
	if w <= 0 || h <= 0 {
		w, h = b.Dx(), b.Dy()
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.NearestNeighbor.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// Len 返回帧总数。
func (a *FrameAtlas) Len() int {
	return len(a.frames)
}

// FrameSize 返回单帧尺寸。
func (a *FrameAtlas) FrameSize() (int, int) {
	return a.fw, a.fh
}
