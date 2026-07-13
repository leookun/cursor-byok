# ADR-006: Cache Runtime 语义缓存 + Forwarder 集成

**状态**：Accepted

**日期**：2026-07-13

**决策者**：AI Chief Architect

---

## 背景

Cache Runtime 已有精确缓存（SHA-256 hash），需要增加语义缓存（embedding + cosine similarity）并集成到 Forwarder 主链路。

## 决策

实现两级缓存架构并完整集成到 Forwarder：

### 两级缓存

```
Layer 1: 精确缓存（SHA-256 hash）
  → 完全相同的 prompt 直接命中
  → TTL: 30 分钟（默认）
  → 命中类型: "exact"

Layer 2: 语义缓存（SimpleEmbedder + cosine similarity）
  → 相似度 > 0.85 命中
  → TopK=5, 取最高相似度
  → 命中类型: "semantic"
```

### 集成点

1. **`DefaultProviderGateway`** — 增加 `cache *cacheruntime.Runtime` 字段
2. **`StartStream`** — 调用前先查缓存（精确 → 语义），命中直接返回
3. **`runProviderStream`** — 调用成功后写入缓存
4. **`host.rebuildLocked`** — 创建 Cache Runtime 实例，传入 Forwarder

### 为什么 SimpleEmbedder（TF-IDF 关键词向量）

- **零依赖**：不依赖外部 embedding API
- **零成本**：不需要额外的 API 调用
- **低延迟**：纯本地计算，< 1ms
- **渐进升级**：Phase 6 可替换为 adapter-based embedding

### 为什么阈值 0.85

- 0.90 太严格：关键词略有不同就会 miss
- 0.80 太宽松：可能返回不相关的结果
- 0.85 平衡了命中率和准确率

## 测试覆盖

### Cache Runtime 测试（12 个，全部通过）

| 测试 | 覆盖场景 |
|---|---|
| TestNewRuntime | 创建和初始统计 |
| TestExactCache_Hit | 精确缓存命中 |
| TestExactCache_Miss | 精确缓存未命中 |
| TestExactCache_Expired | 缓存过期 |
| TestSemanticCache_Hit | 语义缓存命中 |
| TestSemanticCache_VerySimilar | 高重叠关键词 |
| TestCacheStats_Accuracy | 统计准确性 |
| TestCacheCleanExpired | 过期清理 |
| TestCachePersistence | 重启后恢复 |
| TestEmptyCache | 空缓存/Nil Runtime 安全 |
| TestExtractUserText | 用户文本提取 |
| TestCacheStatsPersistence | 统计持久化 |

### Embedding 测试（5 个，全部通过）

| 测试 | 覆盖场景 |
|---|---|
| TestCosineSimilarity | 相同/正交/相反/不同长度/空 |
| TestSimpleEmbedder_Embed | 基本/空/短词/词汇增长/归一化 |
| TestSimpleEmbedder_EmbedMulti | 批量嵌入 |
| TestSimpleEmbedder_Similarity | 相似度比较 |
| TestInMemoryStore | 添加/搜索/删除/空搜索 |

## 影响

- `internal/backend/runtime/cache/runtime.go` — 重构为两级缓存
- `internal/backend/forwarder/provider.go` — 集成 Cache Runtime
- `internal/backend/forwarder/service.go` — `runProviderStream` 写入缓存
- `internal/backend/forwarder/module.go` — 接受 Cache Runtime 参数
- `internal/backend/host.go` — 创建 Cache Runtime 实例

## 参考

- ADR-003: Runtime 装饰器模式
- MemGPT (arxiv.org/abs/2310.08560) — embedding-based retrieval
- `internal/backend/runtime/embedding/embedding.go`
