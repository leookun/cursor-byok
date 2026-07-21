// sqlite_store.go SQLite-backed Long Memory 存储实现。
// 替代原 JSON 文件持久化方案，保持 InMemoryStore 向量索引不变。
// ADR-011/012/023: Long Memory 生产级持久化升级。
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cursor/internal/backend/runtime/embedding"

	_ "modernc.org/sqlite"
)

// sqliteLongMemoryStore SQLite 持久化的 Long Memory 存储。
// - Entry 元数据: SQLite 表 long_memory_entries
// - 向量索引: embedding.InMemoryStore（进程内）
// - 启动时从 SQLite 全量重建索引
type sqliteLongMemoryStore struct {
	db          *sql.DB
	dir         string
	mu          sync.RWMutex
	embedder    embedding.Embedder
	vectorStore embedding.Store
}

func newSQLiteLongMemoryStore(dir string) (*sqliteLongMemoryStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create long memory dir: %w", err)
	}

	dbPath := filepath.Join(dir, "long_memory.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single connection keeps Windows file locks predictable and Close reliable.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// 最小 schema：一条记录一行，tags 存 JSON 数组
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS long_memory_entries (
			id        TEXT PRIMARY KEY,
			content   TEXT    NOT NULL,
			summary   TEXT    NOT NULL DEFAULT '',
			tags      TEXT    NOT NULL DEFAULT '[]',
			timestamp INTEGER NOT NULL,
			source    TEXT    NOT NULL DEFAULT '',
			ttl_ns    INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	// WAL 模式提升并发读性能（嵌入式场景）
	_, _ = db.Exec("PRAGMA journal_mode=WAL")

	store := &sqliteLongMemoryStore{
		db:          db,
		dir:         dir,
		embedder:    embedding.NewSimpleEmbedder(),
		vectorStore: embedding.NewInMemoryStore(),
	}

	// 一次性 JSON → SQLite 迁移（ponytail: 读 JSON → 写 SQLite → 重命名为 *.migrated）
	if err := store.migrateJSONToSQLite(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate json to sqlite: %w", err)
	}

	store.loadExistingIntoIndex()
	return store, nil
}

// Close 释放 SQLite 连接。
func (store *sqliteLongMemoryStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	// Checkpoint WAL so Windows can delete the temp db after tests.
	_, _ = store.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	err := store.db.Close()
	store.db = nil
	return err
}

// SetEmbedder 替换 embedder（例如生产环境用 APIEmbedder）。
func (store *sqliteLongMemoryStore) SetEmbedder(e embedding.Embedder) {
	if store == nil || e == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.embedder = e
}

// loadExistingIntoIndex 启动时从 SQLite 全量加载并重建向量索引。
func (store *sqliteLongMemoryStore) loadExistingIntoIndex() {
	rows, err := store.db.Query(`SELECT id, content, summary, tags, timestamp, source, ttl_ns FROM long_memory_entries`)
	if err != nil {
		return
	}
	defer rows.Close()

	now := time.Now()
	for rows.Next() {
		var (
			id, content, summary, tagsStr, source string
			tsUnixNano                            int64
			ttlNS                                 int64
		)
		if err := rows.Scan(&id, &content, &summary, &tagsStr, &tsUnixNano, &source, &ttlNS); err != nil {
			continue
		}
		ts := time.Unix(0, tsUnixNano)
		if ttlNS > 0 && now.After(ts.Add(time.Duration(ttlNS))) {
			_, _ = store.db.Exec(`DELETE FROM long_memory_entries WHERE id = ?`, id)
			continue
		}
		text := content
		if summary != "" {
			text += " " + summary
		}
		if text != "" {
			emb := store.embedder.Embed(text)
			if len(emb) > 0 {
				_ = store.vectorStore.Add(context.Background(), id, text, emb)
			}
		}
	}
}

// migrateJSONToSQLite 一次性迁移旧 JSON 文件到 SQLite（幂等）。
// ponytail: 读所有 *.json → 写入 SQLite → 重命名为 *.migrated；任一步失败则保留原文件不丢数据。
func (store *sqliteLongMemoryStore) migrateJSONToSQLite() error {
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		return nil // 目录不存在则跳过
	}

	var jsonFiles []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// 跳过 SQLite 自身的 WAL/SHM 等文件（只有 .json 后缀才考虑）
		jsonFiles = append(jsonFiles, e.Name())
	}
	if len(jsonFiles) == 0 {
		return nil // 无旧 JSON，无需迁移
	}

	// 检查 DB 是否已有数据（已迁移过则跳过）
	var count int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM long_memory_entries`).Scan(&count)
	if count > 0 {
		// 仍有 JSON 但 DB 已非空 → 可能是部分迁移失败；重试不做，只重命名残余 JSON
		for _, name := range jsonFiles {
			_ = os.Rename(filepath.Join(store.dir, name), filepath.Join(store.dir, name+".migrated"))
		}
		return nil
	}

	// 逐条读取并写入 SQLite（事务保证要么全写要么全不写）
	tx, err := store.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO long_memory_entries (id, content, summary, tags, timestamp, source, ttl_ns) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()

	for _, name := range jsonFiles {
		path := filepath.Join(store.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		var entry Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			return fmt.Errorf("unmarshal %s: %w", name, err)
		}
		if entry.ID == "" {
			continue // 跳过无 ID 条目
		}
		tagsJSON, _ := json.Marshal(entry.Tags)
		if tagsJSON == nil {
			tagsJSON = []byte("[]")
		}
		if _, err := stmt.Exec(entry.ID, entry.Content, entry.Summary, string(tagsJSON), entry.Timestamp.UnixNano(), entry.Source, int64(entry.TTL)); err != nil {
			return fmt.Errorf("insert %s: %w", entry.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	// 全部写入成功 → 重命名 JSON 文件标记已迁移
	for _, name := range jsonFiles {
		_ = os.Rename(filepath.Join(store.dir, name), filepath.Join(store.dir, name+".migrated"))
	}
	return nil
}

// scanEntry 从 SQLite 行扫描一条 Entry。
func scanEntry(scanner interface {
	Scan(dest ...interface{}) error
}) (Entry, bool) {
	var (
		id, content, summary, tagsStr, source string
		tsUnixNano                            int64
		ttlNS                                 int64
	)
	if err := scanner.Scan(&id, &content, &summary, &tagsStr, &tsUnixNano, &source, &ttlNS); err != nil {
		return Entry{}, false
	}
	var tags []string
	_ = json.Unmarshal([]byte(tagsStr), &tags)

	entry := Entry{
		ID:        id,
		Layer:     LayerLong,
		Content:   content,
		Summary:   summary,
		Tags:      tags,
		Timestamp: time.Unix(0, tsUnixNano),
		Source:    source,
		TTL:       time.Duration(ttlNS),
	}
	return entry, true
}

// Save 保存 Long Memory 条目。
func (store *sqliteLongMemoryStore) Save(ctx context.Context, entry *Entry) error {
	if store == nil || entry == nil {
		return nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	tagsJSON, _ := json.Marshal(entry.Tags)
	if tagsJSON == nil {
		tagsJSON = []byte("[]")
	}

	_, err := store.db.Exec(
		`INSERT OR REPLACE INTO long_memory_entries (id, content, summary, tags, timestamp, source, ttl_ns) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.Content, entry.Summary, string(tagsJSON), entry.Timestamp.UnixNano(), entry.Source, int64(entry.TTL),
	)
	if err != nil {
		return fmt.Errorf("sqlite save: %w", err)
	}

	// 更新向量索引
	text := entry.Content
	if entry.Summary != "" {
		text += " " + entry.Summary
	}
	if text != "" {
		emb := store.embedder.Embed(text)
		if len(emb) > 0 {
			_ = store.vectorStore.Add(ctx, entry.ID, text, emb)
		}
	}
	return nil
}

// Search 搜索 Long Memory：embedding 语义搜索 → 关键词 fallback。
func (store *sqliteLongMemoryStore) Search(ctx context.Context, query string, limit int) ([]Entry, error) {
	if store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	query = strings.ToLower(strings.TrimSpace(query))
	var results []Entry

	// Step 1: Embedding 语义搜索
	if query != "" && store.embedder != nil && store.vectorStore != nil && store.vectorStore.Size() > 0 {
		queryEmb := store.embedder.Embed(query)
		if len(queryEmb) > 0 {
			embResults, err := store.vectorStore.Search(ctx, queryEmb, limit)
			if err == nil && len(embResults) > 0 {
				seen := make(map[string]bool)
				for _, sr := range embResults {
					if sr.Similarity < 0.15 {
						continue
					}
					entry, ok := store.loadEntryByID(sr.ID)
					if !ok {
						continue
					}
					entry.Relevance = sr.Similarity
					results = append(results, entry)
					seen[sr.ID] = true
				}
				// Step 2: 关键词 fallback — 从 SQLite 查未被 embedding 覆盖的条目
				kws := strings.Fields(query)
				if len(kws) > 0 {
					fallback, _ := store.keywordSearch(ctx, query, kws, seen, limit)
					results = append(results, fallback...)
				}
			}
		}
	}

	// 空 query：返回最近 limit 条
	if query == "" {
		rows, err := store.db.Query(
			`SELECT id, content, summary, tags, timestamp, source, ttl_ns FROM long_memory_entries ORDER BY timestamp DESC LIMIT ?`,
			limit,
		)
		if err != nil {
			return nil, nil
		}
		defer rows.Close()
		for rows.Next() {
			entry, ok := scanEntry(rows)
			if !ok {
				continue
			}
			results = append(results, entry)
		}
		return results, nil
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Relevance > results[j].Relevance
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// keywordSearch 关键词 fallback 搜索（SQLite LIKE）。
func (store *sqliteLongMemoryStore) keywordSearch(ctx context.Context, query string, keywords []string, seen map[string]bool, limit int) ([]Entry, error) {
	_ = ctx
	// 用 LIKE 做简单关键词匹配
	likeClauses := make([]string, 0, len(keywords))
	args := make([]interface{}, 0, len(keywords))
	for _, kw := range keywords {
		likeClauses = append(likeClauses, "(content LIKE ? OR summary LIKE ?)")
		pat := "%" + kw + "%"
		args = append(args, pat, pat)
	}
	if len(likeClauses) == 0 {
		return nil, nil
	}

	now := time.Now()
	q := `SELECT id, content, summary, tags, timestamp, source, ttl_ns FROM long_memory_entries WHERE ` +
		strings.Join(likeClauses, " OR ") +
		` ORDER BY timestamp DESC LIMIT ?`
	args = append(args, limit)

	rows, err := store.db.Query(q, args...)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	var results []Entry
	for rows.Next() {
		entry, ok := scanEntry(rows)
		if !ok {
			continue
		}
		if seen != nil && seen[entry.ID] {
			continue
		}
		// TTL 检查
		if entry.TTL > 0 && now.After(entry.Timestamp.Add(entry.TTL)) {
			_, _ = store.db.Exec(`DELETE FROM long_memory_entries WHERE id = ?`, entry.ID)
			continue
		}
		// 关键词命中评分
		score := 0
		content := strings.ToLower(entry.Content)
		summary := strings.ToLower(entry.Summary)
		for _, kw := range keywords {
			score += strings.Count(content, kw) * 10
			score += strings.Count(summary, kw) * 20
		}
		for _, tag := range entry.Tags {
			if strings.Contains(query, strings.ToLower(tag)) {
				score += 50
			}
		}
		entry.Relevance = float64(score) / 100.0
		results = append(results, entry)
	}
	return results, nil
}

// loadEntryByID 从 SQLite 加载单条条目。
func (store *sqliteLongMemoryStore) loadEntryByID(id string) (Entry, bool) {
	row := store.db.QueryRow(
		`SELECT id, content, summary, tags, timestamp, source, ttl_ns FROM long_memory_entries WHERE id = ?`, id,
	)
	entry, ok := scanEntry(row)
	if !ok {
		return Entry{}, false
	}
	// TTL 检查
	if entry.TTL > 0 && time.Now().After(entry.Timestamp.Add(entry.TTL)) {
		_, _ = store.db.Exec(`DELETE FROM long_memory_entries WHERE id = ?`, id)
		return Entry{}, false
	}
	return entry, true
}

// Delete 删除条目。
func (store *sqliteLongMemoryStore) Delete(ctx context.Context, id string) error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	_, err := store.db.Exec(`DELETE FROM long_memory_entries WHERE id = ?`, id)
	if store.vectorStore != nil {
		_ = store.vectorStore.Delete(ctx, id)
	}
	return err
}

// CleanExpired 清理过期条目。
func (store *sqliteLongMemoryStore) CleanExpired(ctx context.Context) (int, error) {
	if store == nil {
		return 0, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	now := time.Now().UnixNano()
	rows, err := store.db.Query(
		`SELECT id FROM long_memory_entries WHERE ttl_ns > 0 AND (timestamp + ttl_ns) <= ?`, now,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var expiredIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		expiredIDs = append(expiredIDs, id)
	}

	for _, id := range expiredIDs {
		_, _ = store.db.Exec(`DELETE FROM long_memory_entries WHERE id = ?`, id)
		if store.vectorStore != nil {
			_ = store.vectorStore.Delete(ctx, id)
		}
	}
	return len(expiredIDs), nil
}

// Stats 返回存储统计。
func (store *sqliteLongMemoryStore) Stats() (map[string]int, error) {
	if store == nil {
		return nil, fmt.Errorf("store is nil")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()

	stats := map[string]int{"total": 0, "with_embedding": 0, "index_size": 0}
	if store.vectorStore != nil {
		stats["index_size"] = store.vectorStore.Size()
	}

	row := store.db.QueryRow(`SELECT COUNT(*) FROM long_memory_entries`)
	var total int
	if err := row.Scan(&total); err == nil {
		stats["total"] = total
	}

	// with_embedding: 近似为有向量索引的条目数
	if store.vectorStore != nil {
		stats["with_embedding"] = store.vectorStore.Size()
	}
	return stats, nil
}