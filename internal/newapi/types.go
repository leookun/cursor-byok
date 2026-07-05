// Package newapi 提供与 newapi 中转站实例交互的纯 HTTP 客户端。
// 不持有任何状态，所有方法均为纯函数，由上层注入 baseURL 与凭证。
package newapi

import (
	"encoding/json"
)

// LoginRequest 是 /api/user/login 的请求体。
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse 是 /api/user/login 的响应。
// newapi 标准约定：status="success" 时 message 字段携带 token，
// 为兼容直接返回 token 字符串或多变体，提供了 NormalizeToken。
type LoginResponse struct {
	Success bool        `json:"success"`
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Token   string      `json:"token"`
	Data    interface{} `json:"data"`
}

// UserInfo 是 /api/user/self 的精选字段。
// Quota 单位为 newapi 内部 token 单位（1 USD = 500000 quota），由上层格式化。
type UserInfo struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Quota       int64  `json:"quota"`
	UsedQuota   int64  `json:"used_quota"`
	Email       string `json:"email"`
}

// UserSelfResponse 是 /api/user/self 的响应包装。
type UserSelfResponse struct {
	Success bool     `json:"success"`
	Message string   `json:"message"`
	Data    UserInfo `json:"data"`
}

// APIKeyResponse 是 /api/user/token 的响应。
// newapi 在 data 字段直接返回字符串形式的 api_key（sk-...）。
type APIKeyResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

// ModelItem 是 /v1/models 返回的单个模型条目（OpenAI 标准）。
type ModelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// ModelsResponse 是 /v1/models 的响应包装。
type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelItem `json:"data"`
}

// LogRecord 是 /api/log/self 的单条使用记录。
// 字段名沿用 newapi 原始命名以便直接反序列化。
// CreatedAt 是 Unix 时间戳整数，由上层格式化为字符串。
type LogRecord struct {
	ID               int    `json:"id"`
	CreatedAt        int64  `json:"created_at"`
	ModelName        string `json:"model_name"`
	TokenName        string `json:"token_name"`
	Quota            int64  `json:"quota"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Content          string `json:"content"`
}

// LogsResponse 是 /api/log/self 的响应包装。
// data 字段可能是对象（含 items/total）或直接数组，用 RawMessage 兼容两种格式。
type LogsResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Total   int             `json:"total"` // 顶层 total（部分 newapi 分支放这里）
}

// LogsPageData 是 data 为对象时的分页包装。
type LogsPageData struct {
	Items []LogRecord `json:"items"`
	Total int         `json:"total"` // data 内层 total（优先于顶层）
}
