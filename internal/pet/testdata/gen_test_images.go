//go:build ignore

package main

import (
	"image"
	"image/png"
	"os"
)

func main() {
	// 创建 1x1 PNG 占位图片
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, image.Black)

	files := []string{
		"Pet_A/spritesheet.webp", // 占位，实际用 png 代替
		"Pet_B/spritesheet.png",
		"Pet_E/hero.webp", // 占位，用 png 代替
		"Pet_F/atlas.png",
	}
	for _, f := range files {
		os.MkdirAll("testdata/"+dirOf(f), 0o755)
		out, _ := os.Create("testdata/" + f)
		png.Encode(out, img)
		out.Close()
	}
}

func dirOf(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == '\\' {
			return s[:i]
		}
	}
	return ""
}
