// content_blob_store.go 负责持久化 history 引用的内容寻址二进制数据。
package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const contentBlobDirectoryName = ".blobs"

// ContentBlobStore 使用 SHA-256 内容哈希保存不可变二进制数据。
type ContentBlobStore struct {
	root string
}

// NewContentBlobStore 创建独立于 context.json 和 checkpoint 的内容寻址存储。
func NewContentBlobStore(historyRoot string) *ContentBlobStore {
	historyRoot = strings.TrimSpace(historyRoot)
	if historyRoot == "" {
		return &ContentBlobStore{}
	}
	return &ContentBlobStore{root: filepath.Join(historyRoot, contentBlobDirectoryName, "sha256")}
}

// Put 校验内容哈希并幂等保存数据。
func (store *ContentBlobStore) Put(id []byte, data []byte) error {
	if store == nil || strings.TrimSpace(store.root) == "" {
		return fmt.Errorf("content blob store is not initialized")
	}
	normalizedID, err := normalizeContentBlobID(id)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(normalizedID, digest[:]) {
		return fmt.Errorf("content blob id does not match payload sha256")
	}
	path := store.blobPath(normalizedID)
	if existing, err := store.Get(normalizedID); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("content blob payload conflicts with existing id")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		return fmt.Errorf("create content blob directory: %w", err)
	}
	temporary, err := os.CreateTemp(store.root, ".blob-*")
	if err != nil {
		return fmt.Errorf("create content blob temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set content blob permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write content blob: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync content blob: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close content blob: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit content blob: %w", err)
	}
	return nil
}

// Get 读取内容并再次校验哈希，避免损坏数据进入模型请求。
func (store *ContentBlobStore) Get(id []byte) ([]byte, error) {
	if store == nil || strings.TrimSpace(store.root) == "" {
		return nil, fmt.Errorf("content blob store is not initialized")
	}
	normalizedID, err := normalizeContentBlobID(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(store.blobPath(normalizedID))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(normalizedID, digest[:]) {
		return nil, fmt.Errorf("content blob sha256 verification failed")
	}
	return append([]byte(nil), data...), nil
}

func (store *ContentBlobStore) blobPath(id []byte) string {
	return filepath.Join(store.root, hex.EncodeToString(id))
}

func normalizeContentBlobID(id []byte) ([]byte, error) {
	if len(id) != sha256.Size {
		return nil, fmt.Errorf("content blob id must be %d bytes", sha256.Size)
	}
	return append([]byte(nil), id...), nil
}
