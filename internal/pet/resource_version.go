package pet

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// ResourceVersion 是资源文件的版本指纹（Phase 10）。
// 用于检测资源变更、缓存失效判断、增量更新支持。
type ResourceVersion struct {
	// Path 资源文件路径。
	Path string `json:"path"`
	// SHA256 文件内容 SHA-256 哈希（hex 字符串）。
	SHA256 string `json:"sha256"`
	// Size 文件大小（字节）。
	Size int64 `json:"size"`
}

// ComputeResourceVersion 计算指定文件的版本指纹。
// 文件不存在时返回 nil（调用方应处理缺失）。
func ComputeResourceVersion(path string) *ResourceVersion {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return nil
	}

	return &ResourceVersion{
		Path:   path,
		SHA256: fmt.Sprintf("%x", h.Sum(nil)),
		Size:   size,
	}
}

// Equal 比较两个版本指纹是否完全一致（同一文件）。
func (v *ResourceVersion) Equal(o *ResourceVersion) bool {
	if v == nil || o == nil {
		return v == o
	}
	return v.SHA256 == o.SHA256 && v.Size == o.Size
}

// CacheKey 返回可用于缓存查找的键（基于 SHA256 前 12 位，冲突概率极低）。
func (v *ResourceVersion) CacheKey() string {
	if v == nil || v.SHA256 == "" {
		return ""
	}
	if len(v.SHA256) >= 12 {
		return v.SHA256[:12]
	}
	return v.SHA256
}
