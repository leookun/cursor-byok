package pet

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"log"
	"os"
	"path/filepath"

	_ "golang.org/x/image/webp"
)

//go:embed nezukocoder/*
var embeddedPets embed.FS

const (
	// EmbeddedPetDir 嵌入资源的逻辑目录名（用于显示和 ID）。
	EmbeddedPetDir = "nezukocoder"
	// EmbeddedDir sentinel：使用嵌入资源。
	EmbeddedDir = ""
)

// PetData 对应 pet.json 的完整结构（内部使用）。
type PetData struct {
	ID              string             `json:"id"`
	DisplayName     string             `json:"displayName"`
	SpritesheetPath string             `json:"spritesheetPath"`
	FrameWidth      int                `json:"frameWidth"`
	FrameHeight     int                `json:"frameHeight"`
	Columns         int                `json:"columns"`
	Rows            int                `json:"rows"`
	TotalFrames     int                `json:"totalFrames"`
	FPS             int                `json:"fps"`
	Animations      map[string]AnimDef `json:"animations"`
}

type AnimDef struct {
	FPS      int   `json:"fps"`
	Loop     bool  `json:"loop"`
	Priority int   `json:"priority"`
	Frames   []int `json:"frames"`
}

// LoadPetJSON 加载 pet.json（优先文件系统，回退嵌入资源）。
// petDir == EmbeddedDir 时跳过文件系统直接读嵌入资源。
func LoadPetJSON(dir string) (*PetData, error) {
	if dir != EmbeddedDir {
		fsPath := filepath.Join(dir, "pet.json")
		data, err := os.ReadFile(fsPath)
		if err == nil {
			return parsePetData(data)
		}
	}
	// 嵌入资源的根目录是 nezukocoder/
	data, err := embeddedPets.ReadFile(EmbeddedPetDir + "/pet.json")
	if err == nil {
		return parsePetData(data)
	}
	return nil, fmt.Errorf("load pet.json: no file system or embedded resource found")
}

// parsePetData 解析 pet.json 字节数据。
func parsePetData(data []byte) (*PetData, error) {
	var pet PetData
	if err := json.Unmarshal(data, &pet); err != nil {
		return nil, fmt.Errorf("parse pet.json: %w", err)
	}
	if pet.SpritesheetPath == "" {
		return nil, fmt.Errorf("pet.json: spritesheetPath is required")
	}
	if pet.FrameWidth <= 0 || pet.FrameHeight <= 0 {
		return nil, fmt.Errorf("pet.json: frameWidth and frameHeight must be > 0")
	}
	if pet.Columns <= 0 || pet.Rows <= 0 {
		return nil, fmt.Errorf("pet.json: columns and rows must be > 0")
	}
	return &pet, nil
}

// LoadSheet 加载 spritesheet（优先文件系统，回退嵌入）。
func LoadSheet(sheetPath string) (image.Image, error) {
	f, err := os.Open(sheetPath)
	if err == nil {
		defer f.Close()
		img, _, err := image.Decode(f)
		if err == nil {
			return img, nil
		}
	}
	// 嵌入资源的根目录是 nezukocoder/
	data, err := embeddedPets.ReadFile(EmbeddedPetDir + "/spritesheet.webp")
	if err == nil {
		img, _, err := image.Decode(bytes.NewReader(data))
		return img, err
	}
	return nil, fmt.Errorf("load spritesheet %s: file not found and no embedded fallback", sheetPath)
}

// LoadEngine 从 Manifest 加载并构建桌宠引擎。
// 这是 Loader 的统一入口：未来可根据 m.Format 选择不同的加载策略
// （codex / deepseek / cursor 各有不同的资源布局与解析规则）。
func LoadEngine(m *PetManifest) (*Engine, error) {
	switch m.Format {
	case "deepseek", "cursor":
		// 预留：未来为不同来源实现专门 Loader
		log.Printf("[Pet] LoadEngine: format=%s (using codex-compatible loader for now)", m.Format)
		return NewEngineFromManifest(m)
	default:
		// codex（含未声明 format 的情况）走默认加载路径
		return NewEngineFromManifest(m)
	}
}
