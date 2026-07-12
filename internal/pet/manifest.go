package pet

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// PetStatus 表示宠物的扫描状态。
type PetStatus int

const (
	StatusUnknown PetStatus = iota
	StatusScanned           // 已扫描，但未验证
	StatusWarning           // 有小问题但可加载
	StatusBroken            // 严重错误，无法加载
	StatusReady             // 完全就绪
	StatusRunning           // 正在运行
)

func (s PetStatus) String() string {
	switch s {
	case StatusUnknown:
		return "unknown"
	case StatusScanned:
		return "scanned"
	case StatusWarning:
		return "warning"
	case StatusBroken:
		return "broken"
	case StatusReady:
		return "ready"
	case StatusRunning:
		return "running"
	default:
		return "unknown"
	}
}

// PetManifest 是桌宠的完整描述，Scanner 产出，Loader 消费（Phase 10 v2 增强）。
//
// v2 新增字段：
//   - SchemaVersion：Manifest 格式自身的版本号（"v2"），用于前端/后端判断兼容性。
//   - MinEngineVersion：加载此宠物所需的最小引擎版本。
//   - SheetVersion：spritesheet 的 SHA256 + Size 资源指纹，用于缓存键与变更检测。
//   - AnimationSummary：每个动画的帧数/FPS/Loop 摘要，无需解析完整 PetData 即可预览。
//   - AutoDetected：标记布局是否由 AutoDetect 推断（便于诊断）。
//
// 对应 Codex 官方桌宠的 manifest 格式，同时兼容多种来源。
type PetManifest struct {
	// --- v1 字段（向后兼容）---
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	RootPath    string `json:"rootPath"`
	PetJSONPath string `json:"petJsonPath"`
	SheetPath   string `json:"sheetPath"`
	PreviewPath string `json:"previewPath,omitempty"`
	IconPath    string `json:"iconPath,omitempty"`

	// 从 pet.json 解析的元数据
	FrameWidth  int `json:"frameWidth"`
	FrameHeight int `json:"frameHeight"`
	Columns     int `json:"columns"`
	Rows        int `json:"rows"`
	TotalFrames int `json:"totalFrames"`
	FPS         int `json:"fps"`

	// 动画列表（v2 升级为带摘要的结构）
	AnimationNames   []string           `json:"animationNames"`
	AnimationSummary []AnimationSummary `json:"animationSummary,omitempty"`

	// 格式与能力
	Format        string   `json:"format,omitempty"`
	FormatVersion string   `json:"formatVersion,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`

	// 状态和诊断
	Status     PetStatus `json:"status"`
	StatusText string    `json:"statusText"`
	Errors     []string  `json:"errors,omitempty"`
	Warnings   []string  `json:"warnings,omitempty"`

	// --- v2 新增字段 ---

	// SchemaVersion Manifest 格式自身的版本号（"v2"）。
	// 用于前端/后端判断 Manifest 结构的兼容性，与宠物资源版本（Version）区分。
	SchemaVersion string `json:"schemaVersion"`

	// MinEngineVersion 加载此宠物所需的最小引擎版本（如 "1.0.0"）。
	// 若引擎版本低于此值，应拒绝加载或降级运行。
	MinEngineVersion string `json:"minEngineVersion,omitempty"`

	// SheetVersion spritesheet 的 SHA256 哈希 + 文件大小。
	// 用于缓存失效判断：资源不变时可复用解码缓存，避免重复解码大图。
	SheetVersion *ResourceVersion `json:"sheetVersion,omitempty"`

	// AutoDetected 标记布局（FrameWidth/Height/Columns/Rows）是否由 AutoDetect 自动推断。
	// true = pet.json 未提供布局参数，由图片尺寸反推。
	AutoDetected bool `json:"autoDetected,omitempty"`
}

// AnimationSummary 动画摘要（不包含完整帧数据，仅关键信息）。
type AnimationSummary struct {
	Name     string `json:"name"`
	Frames   int    `json:"frames"`   // 帧数
	FPS      int    `json:"fps"`      // 帧率
	Loop     bool   `json:"loop"`     // 是否循环
	Priority int    `json:"priority"` // 优先级
}

// ToPetData 将 Manifest 转换为内部 PetData（用于 Loader）。
func (m *PetManifest) ToPetData() (*PetData, error) {
	if m.Status == StatusBroken {
		return nil, fmt.Errorf("manifest is broken: %v", m.Errors)
	}

	data, err := os.ReadFile(m.PetJSONPath)
	if err != nil {
		return nil, fmt.Errorf("read pet.json: %w", err)
	}

	var pet PetData
	if err := json.Unmarshal(data, &pet); err != nil {
		return nil, fmt.Errorf("parse pet.json: %w", err)
	}

	// 用 repair 结果覆盖可能缺失的字段
	if pet.FrameWidth <= 0 {
		pet.FrameWidth = m.FrameWidth
	}
	if pet.FrameHeight <= 0 {
		pet.FrameHeight = m.FrameHeight
	}
	if pet.Columns <= 0 {
		pet.Columns = m.Columns
	}
	if pet.Rows <= 0 {
		pet.Rows = m.Rows
	}
	if pet.FPS <= 0 {
		pet.FPS = m.FPS
	}
	if pet.SpritesheetPath == "" {
		pet.SpritesheetPath = filepath.Base(m.SheetPath)
	}
	if pet.TotalFrames <= 0 {
		pet.TotalFrames = m.TotalFrames
	}

	return &pet, nil
}

// --- Scanner ---

// ScanPetDir 扫描单个目录，生成 Manifest（v2 增强）。
// 此函数只负责发现和收集信息，不做加载。
func ScanPetDir(petDir string) *PetManifest {
	m := &PetManifest{
		RootPath:      petDir,
		Status:        StatusScanned,
		SchemaVersion: "v2",
	}

	// 读取 pet.json
	petJSONPath := filepath.Join(petDir, "pet.json")
	data, err := os.ReadFile(petJSONPath)
	if err != nil {
		m.Status = StatusBroken
		m.Errors = append(m.Errors, "缺少 pet.json")
		return m
	}
	m.PetJSONPath = petJSONPath

	// 解析 JSON（只提取元数据，不校验动画）
	var raw struct {
		ID              string                     `json:"id"`
		DisplayName     string                     `json:"displayName"`
		Name            string                     `json:"name"`
		Version         string                     `json:"version"`
		Author          string                     `json:"author"`
		MinEngineVersion string                    `json:"minEngineVersion"`
		SpritesheetPath string                     `json:"spritesheetPath"`
		FrameWidth      int                        `json:"frameWidth"`
		FrameHeight     int                        `json:"frameHeight"`
		Columns         int                        `json:"columns"`
		Rows            int                        `json:"rows"`
		TotalFrames     int                        `json:"totalFrames"`
		FPS             int                        `json:"fps"`
		Format          string                     `json:"format"`
		Capabilities    []string                   `json:"capabilities"`
		Animations      map[string]json.RawMessage `json:"animations"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		m.Status = StatusBroken
		m.Errors = append(m.Errors, fmt.Sprintf("pet.json 解析失败: %v", err))
		return m
	}

	// 基本信息
	m.ID = raw.ID
	if m.ID == "" {
		m.ID = filepath.Base(petDir)
	}
	m.Name = raw.Name
	if m.Name == "" {
		m.Name = raw.DisplayName
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	m.Version = raw.Version
	m.Author = raw.Author
	m.MinEngineVersion = raw.MinEngineVersion

	// 格式：优先用 pet.json 声明的 format（如 "codex"），未声明则默认 codex。
	m.Format = raw.Format
	if m.Format == "" {
		m.Format = "codex"
	}
	m.FormatVersion = raw.Version
	m.Capabilities = raw.Capabilities
	if len(m.Capabilities) == 0 {
		// 默认能力：可显示、可拖拽、可自主行为
		m.Capabilities = []string{"display", "drag", "behavior"}
	}

	// 尺寸信息
	m.FrameWidth = raw.FrameWidth
	m.FrameHeight = raw.FrameHeight
	m.Columns = raw.Columns
	m.Rows = raw.Rows
	m.TotalFrames = raw.TotalFrames
	m.FPS = raw.FPS

	// 动画摘要（Phase 10 v2）：解析每个动画的帧数/FPS/Loop/Priority。
	for name, defRaw := range raw.Animations {
		m.AnimationNames = append(m.AnimationNames, name)
		// 尝试解析动画摘要
		var adef struct {
			FPS      int   `json:"fps"`
			Loop     bool  `json:"loop"`
			Priority int   `json:"priority"`
			Frames   []int `json:"frames"`
		}
		if err := json.Unmarshal(defRaw, &adef); err == nil {
			m.AnimationSummary = append(m.AnimationSummary, AnimationSummary{
				Name:     name,
				Frames:   len(adef.Frames),
				FPS:      adef.FPS,
				Loop:     adef.Loop,
				Priority: adef.Priority,
			})
		}
	}

	// 查找 spritesheet
	m.SheetPath = findSheet(petDir, raw.SpritesheetPath)
	if m.SheetPath != "" {
		log.Printf("[Pet Scanner]   Found spritesheet: %s", filepath.Base(m.SheetPath))
	} else {
		log.Printf("[Pet Scanner]   MISSING spritesheet")
	}
	m.PreviewPath = findPreview(petDir)
	validateManifest(m)
	// 只读修复：补全可推断的缺失字段（尺寸/默认 idle），让扫描结果反映真实可加载状态，
	// 但不写回 pet.json（避免扫描阶段产生副作用）。真正加载时再由 Loader 落盘。
	if m.Status == StatusWarning {
		RepairDefaults(m, false)
	}

	// Phase 10 v2：计算 spritesheet 资源指纹（SHA256 + Size）。
	if m.SheetPath != "" {
		m.SheetVersion = ComputeResourceVersion(m.SheetPath)
	}

	log.Printf("[Pet Scanner]   Status: %s (errors=%d, warnings=%d)", m.Status, len(m.Errors), len(m.Warnings))
	return m
}

// validateManifest 验证 Manifest 完整性，设置 Status + Errors/Warnings。
func validateManifest(m *PetManifest) {
	// Spritesheet
	if m.SheetPath == "" {
		m.Status = StatusBroken
		m.Errors = append(m.Errors, "缺少 spritesheet 文件")
	} else {
		// 验证图片尺寸
		if f, err := os.Open(m.SheetPath); err == nil {
			cfg, _, err := image.DecodeConfig(f)
			_ = f.Close()
			if err == nil {
				expectedW := m.Columns * m.FrameWidth
				expectedH := m.Rows * m.FrameHeight
				if m.Columns > 0 && m.FrameWidth > 0 && expectedW > cfg.Width {
					m.Warnings = append(m.Warnings,
						fmt.Sprintf("spritesheet 宽度不足：需要 %dpx，实际 %dpx", expectedW, cfg.Width))
				}
				if m.Rows > 0 && m.FrameHeight > 0 && expectedH > cfg.Height {
					m.Warnings = append(m.Warnings,
						fmt.Sprintf("spritesheet 高度不足：需要 %dpx，实际 %dpx", expectedH, cfg.Height))
				}
			}
		}
	}

	// 尺寸参数
	if m.FrameWidth <= 0 {
		m.Warnings = append(m.Warnings, "缺少 frameWidth")
	}
	if m.FrameHeight <= 0 {
		m.Warnings = append(m.Warnings, "缺少 frameHeight")
	}
	if m.Columns <= 0 {
		m.Warnings = append(m.Warnings, "缺少 columns")
	}
	if m.Rows <= 0 {
		m.Warnings = append(m.Warnings, "缺少 rows")
	}

	// 动画
	if len(m.AnimationNames) == 0 {
		m.Warnings = append(m.Warnings, "animations 为空")
	}

	// 最终状态
	if len(m.Errors) > 0 {
		m.Status = StatusBroken
	} else if len(m.Warnings) > 0 {
		m.Status = StatusWarning
	} else {
		m.Status = StatusReady
	}
	m.StatusText = m.Status.String()
}

// --- Spritesheet 查找 ---

var imageExts = []string{".webp", ".png", ".apng", ".gif", ".bmp", ".jpg", ".jpeg"}

func findSheet(petDir, specified string) string {
	// 1. pet.json 中明确指定
	if specified != "" {
		p := filepath.Join(petDir, specified)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// 2. 常见文件名
	basenames := []string{"spritesheet", "sprite", "sheet", "atlas"}
	for _, base := range basenames {
		for _, ext := range imageExts {
			p := filepath.Join(petDir, base+ext)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	// 3. 兜底：目录中唯一图片文件
	entries, _ := os.ReadDir(petDir)
	var found string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		for _, supported := range imageExts {
			if ext == supported {
				if found != "" {
					return "" // 多张图片，不确定选哪个
				}
				found = filepath.Join(petDir, e.Name())
			}
		}
	}
	return found
}

func findPreview(petDir string) string {
	names := []string{"preview.png", "preview.webp", "preview.jpg", "icon.png", "icon.webp"}
	for _, n := range names {
		p := filepath.Join(petDir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// --- Repair ---

// RepairDefaults 填充 Manifest 中缺失的可推断字段（Phase 10 v2 增强）。
// 在 Loader 调用前执行，尽量让有缺陷的 pet.json 也能加载。
// writeBack=true 时会把补全的动画/尺寸写回 pet.json；false 时仅修正内存状态用于展示。
func RepairDefaults(m *PetManifest, writeBack bool) {
	// 记录自动检测标记：任意布局参数缺失即由 AutoDetect 推断。
	needAutoDetect := m.Columns <= 0 || m.FrameWidth <= 0 || m.FrameHeight <= 0

	// 尝试从 spritesheet 真实图片尺寸自动推断帧布局（不再写死 96×96 / 1×1）。
	if m.SheetPath != "" && needAutoDetect {
		if f, err := os.Open(m.SheetPath); err == nil {
			cfg, _, err := image.DecodeConfig(f)
			_ = f.Close()
			if err == nil {
				fw, fh, cols, rows, total := AutoDetectLayoutFromSize(cfg.Width, cfg.Height)
				if m.FrameWidth <= 0 {
					m.FrameWidth = fw
				}
				if m.FrameHeight <= 0 {
					m.FrameHeight = fh
				}
				if m.Columns <= 0 {
					m.Columns = cols
				}
				if m.Rows <= 0 {
					m.Rows = rows
				}
				if m.TotalFrames <= 0 {
					m.TotalFrames = total
				}
				log.Printf("[Pet] RepairDefaults: inferred layout %dx%d, %dx%d grid, %d frames",
					fw, fh, cols, rows, total)
			}
		}
	}

	// 兜底默认值（仅在完全无法读取图片时）
	if m.FrameWidth <= 0 {
		m.FrameWidth = codexStandardFrame[0]
	}
	if m.FrameHeight <= 0 {
		m.FrameHeight = codexStandardFrame[1]
	}
	if m.Columns <= 0 {
		m.Columns = 1
	}
	if m.Rows <= 0 {
		m.Rows = 1
	}
	if m.FPS <= 0 {
		m.FPS = 8
	}
	if m.TotalFrames <= 0 {
		m.TotalFrames = m.Columns * m.Rows
	}

	// Phase 10 v2：标记 AutoDetected（布局参数由引擎自动推断）。
	m.AutoDetected = needAutoDetect

	// 没有动画时，从 spritesheet 生成默认 idle
	if len(m.AnimationNames) == 0 && m.SheetPath != "" {
		m.AnimationNames = []string{"idle"}
		// 写入 pet.json 以便后续 Loader 使用（仅当允许写回时）
		if writeBack {
			ensureDefaultAnimation(m)
		}
	}

	// 重新验证
	m.Errors = nil
	m.Warnings = nil
	validateManifest(m)
}

// ensureDefaultAnimation 为没有动画的 pet.json 写入默认 idle 动画。
func ensureDefaultAnimation(m *PetManifest) {
	if m.PetJSONPath == "" {
		return
	}
	data, err := os.ReadFile(m.PetJSONPath)
	if err != nil {
		return
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	if _, ok := raw["animations"]; ok {
		return // 已有动画定义
	}
	// 添加默认 idle 动画
	total := m.Columns * m.Rows
	if m.TotalFrames > 0 {
		total = m.TotalFrames
	}
	frames := make([]int, total)
	for i := range frames {
		frames[i] = i
	}
	raw["animations"] = map[string]interface{}{
		"idle": map[string]interface{}{
			"fps":      m.FPS,
			"loop":     true,
			"priority": 1,
			"frames":   frames,
		},
	}
	raw["spritesheetPath"] = filepath.Base(m.SheetPath)
	raw["frameWidth"] = m.FrameWidth
	raw["frameHeight"] = m.FrameHeight
	raw["columns"] = m.Columns
	raw["rows"] = m.Rows
	raw["totalFrames"] = total
	raw["fps"] = m.FPS

	newData, _ := json.MarshalIndent(raw, "", "  ")
	os.WriteFile(m.PetJSONPath, newData, 0o644)
}
