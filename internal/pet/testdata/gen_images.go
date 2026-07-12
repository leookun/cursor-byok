package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	// Pet_A: 4 columns x 96px = 384w, 1 row x 96px = 96h
	makeImg("Pet_A/spritesheet.webp", 384, 96)
	// Pet_B: 8 columns x 64px = 512w, 2 rows x 64px = 128h
	makeImg("Pet_B/spritesheet.png", 512, 128)
	// Pet_E: 2 columns x 48px = 96w, 2 rows x 48px = 96h
	makeImg("Pet_E/hero.webp", 96, 96)
	// Pet_F: 1 column x 32px = 32w, 1 row x 32px = 32h
	makeImg("Pet_F/atlas.png", 32, 32)
}

func makeImg(path string, w, h int) {
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0o755)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, image.Black)
		}
	}
	out, _ := os.Create(path)
	defer out.Close()
	png.Encode(out, img)
}
