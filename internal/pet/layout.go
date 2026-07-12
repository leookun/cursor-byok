package pet

import (
	"image"
	"log"
)

// codexStandardFrame 是 Codex 官方桌宠最常见的单帧尺寸（宽×高）。
// 当无法从 pet.json 得到精确布局时，优先匹配它，兼容性最好。
var codexStandardFrame = [2]int{96, 144}

// commonFrameCandidates 是候选单帧尺寸（宽×高），按优先级排列。
// 用于在只有一张 spritesheet、无任何布局参数时自动推断网格。
var commonFrameCandidates = [][2]int{
	{96, 144},  // Codex 标准
	{144, 144}, // 正方形放大
	{128, 128},
	{120, 120},
	{100, 100},
	{96, 96},
	{120, 160},
	{150, 150},
	{192, 192},
}

// AutoDetectSpriteLayout 从 spritesheet 真实尺寸推断帧布局。
// 返回 frameWidth、frameHeight、columns、rows、totalFrames。
//
// 策略：
//  1. 若图宽高都能被 Codex 标准帧尺寸整除 -> 直接用（最可靠，对应官方宠物）。
//  2. 否则遍历候选尺寸，找出所有"能整除图宽高"的解，按评分择优：
//     帧数适中（20~400）、单帧接近正方形者优先。
//  3. 都没命中 -> 回退为单帧整图（1×1）。
//
// 该函数不依赖任何写死的 1×1 默认，而是真正从图片像素尺寸反推。
func AutoDetectSpriteLayout(sheet image.Image) (frameW, frameH, cols, rows, total int) {
	if sheet == nil {
		return codexStandardFrame[0], codexStandardFrame[1], 1, 1, 1
	}
	b := sheet.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return codexStandardFrame[0], codexStandardFrame[1], 1, 1, 1
	}

	// 1) Codex 标准帧尺寸优先
	cw, ch := codexStandardFrame[0], codexStandardFrame[1]
	if sw%cw == 0 && sh%ch == 0 {
		cols, rows = sw/cw, sh/ch
		log.Printf("[Pet] AutoDetectSpriteLayout: matched Codex standard %dx%d -> %dx%d grid (%d frames)",
			cw, ch, cols, rows, cols*rows)
		return cw, ch, cols, rows, cols * rows
	}

	// 2) 候选尺寸择优
	var best *layoutCand
	for _, cc := range commonFrameCandidates {
		fw, fh := cc[0], cc[1]
		if sw%fw == 0 && sh%fh == 0 {
			c, r := sw/fw, sh/fh
			t := c * r
			cur := &layoutCand{fw, fh, c, r, t}
			if best == nil || scoreCand(cur) > scoreCand(best) {
				best = cur
			}
		}
	}
	if best != nil {
		log.Printf("[Pet] AutoDetectSpriteLayout: inferred %dx%d -> %dx%d grid (%d frames)",
			best.fw, best.fh, best.c, best.r, best.t)
		return best.fw, best.fh, best.c, best.r, best.t
	}

	// 3) 回退：整图作为单帧
	log.Printf("[Pet] AutoDetectSpriteLayout: no grid matched, fallback single frame %dx%d", sw, sh)
	return sw, sh, 1, 1, 1
}

// layoutCand 是 AutoDetectSpriteLayout 内部使用的候选布局。
type layoutCand struct {
	fw, fh, c, r, t int
}

// scoreCand 给一个候选布局打分（越高越优）。
// 偏好：帧数适中（20~400）、单帧接近正方形、面积较大（细节更丰富）。
func scoreCand(c *layoutCand) int {
	score := 0
	if c.t >= 20 && c.t <= 400 {
		score += 100
	} else if c.t > 400 {
		score += 40
	} else {
		score += 10
	}
	// 单帧越接近正方形越好
	ratio := float64(c.fw) / float64(c.fh)
	if ratio > 0.8 && ratio < 1.25 {
		score += 30
	}
	// 单帧面积越大细节越好（但太小不好）
	area := c.fw * c.fh
	if area >= 8000 && area <= 40000 {
		score += 20
	}
	return score
}

// AutoDetectLayoutFromSize 仅根据 imagesize（无需完整解码）推断帧布局。
// 供 Scanner/RepairDefaults 在只读阶段调用，避免为推断而全量解码大图。
func AutoDetectLayoutFromSize(sw, sh int) (frameW, frameH, cols, rows, total int) {
	if sw <= 0 || sh <= 0 {
		return codexStandardFrame[0], codexStandardFrame[1], 1, 1, 1
	}
	cw, ch := codexStandardFrame[0], codexStandardFrame[1]
	if sw%cw == 0 && sh%ch == 0 {
		cols, rows = sw/cw, sh/ch
		return cw, ch, cols, rows, cols * rows
	}
	var best *layoutCand
	for _, cc := range commonFrameCandidates {
		fw, fh := cc[0], cc[1]
		if sw%fw == 0 && sh%fh == 0 {
			c, r := sw/fw, sh/fh
			cur := &layoutCand{fw, fh, c, r, c * r}
			if best == nil || scoreCand(cur) > scoreCand(best) {
				best = cur
			}
		}
	}
	if best != nil {
		return best.fw, best.fh, best.c, best.r, best.t
	}
	return sw, sh, 1, 1, 1
}
