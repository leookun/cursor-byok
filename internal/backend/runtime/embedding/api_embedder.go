// api_embedder.go 实现 APIEmbedder：通过 OpenAI-compatible /v1/embeddings 端点生成真实语义向量。
// ADR-025: 从 SimpleEmbedder (TF-IDF) 升级为真实 embedding API。
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// APIEmbedder 通过 OpenAI-compatible API 生成 embedding（ADR-025）。
// 复用用户已配置的 ModelAdapter BaseURL + APIKey。
type APIEmbedder struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewAPIEmbedder 创建 API 嵌入器。
// baseURL 示例: "https://api.openai.com/v1"
// model 示例: "text-embedding-3-small"
func NewAPIEmbedder(baseURL, apiKey, model string) *APIEmbedder {
	return &APIEmbedder{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// embeddingRequest 是 OpenAI-compatible embedding API 请求体。
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingResponse 是 OpenAI-compatible embedding API 响应体。
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func (e *APIEmbedder) Embed(text string) []float32 {
	if e == nil || e.apiKey == "" || e.baseURL == "" {
		return nil
	}
	vecs := e.EmbedMulti([]string{text})
	if len(vecs) > 0 {
		return vecs[0]
	}
	return nil
}

func (e *APIEmbedder) EmbedMulti(texts []string) [][]float32 {
	if e == nil || e.apiKey == "" || e.baseURL == "" || len(texts) == 0 {
		return nil
	}

	reqBody := embeddingRequest{
		Model: e.model,
		Input: texts,
	}
	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil
	}

	url := e.baseURL + "/embeddings"
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil
	}
	if embResp.Error != nil {
		return nil
	}

	result := make([][]float32, 0, len(embResp.Data))
	for _, item := range embResp.Data {
		result = append(result, item.Embedding)
	}
	return result
}

// Ensure APIEmbedder satisfies Embedder interface.
var _ Embedder = (*APIEmbedder)(nil)

// EmbeddingModelSupported reports whether the given model name looks like
// an embedding model (heuristic: contains "embed").
func EmbeddingModelSupported(model string) bool {
	return len(model) > 0 && contains(model, "embed")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// ResolveEmbeddingModel picks an embedding model from an adapter's configured model name.
// If the model itself supports embeddings (e.g., "text-embedding-3-small"), it returns it.
// Otherwise it returns a sensible default ("text-embedding-3-small").
func ResolveEmbeddingModel(adapterModel string) string {
	if EmbeddingModelSupported(adapterModel) {
		return adapterModel
	}
	return "text-embedding-3-small"
}

// Suppress unused import warning for fmt (used in error paths if expanded).
var _ = fmt.Sprintf
