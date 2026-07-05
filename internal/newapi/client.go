package newapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout 限制单次 newapi HTTP 调用最长耗时。
const DefaultTimeout = 20 * time.Second

// NewAPIClient 是一个无状态的 HTTP 封装。
// 所有方法都需要显式传入 baseURL 与凭证，便于上层测试与替换。
type NewAPIClient struct {
	httpClient *http.Client
}

// NewClient 构造一个使用默认超时的新客户端。
func NewClient() *NewAPIClient {
	return &NewAPIClient{
		httpClient: &http.Client{Timeout: DefaultTimeout},
	}
}

// NewClientWithHTTP 允许注入自定义 http.Client（例如复用 netproxy 的 transport）。
func NewClientWithHTTP(client *http.Client) *NewAPIClient {
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return &NewAPIClient{httpClient: client}
}

// Login 用用户名/密码换取会话 token。
// 成功时返回的 token 字符串可直接作为后续接口的 Authorization 头。
func (c *NewAPIClient) Login(ctx context.Context, baseURL, username, password string) (string, error) {
	reqBody, err := json.Marshal(LoginRequest{Username: username, Password: password})
	if err != nil {
		return "", fmt.Errorf("构造登录请求失败: %w", err)
	}
	url := strings.TrimRight(baseURL, "/") + "/api/user/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("登录请求失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取登录响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("登录失败: HTTP %d %s", resp.StatusCode, truncateBody(raw))
	}

	var parsed LoginResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("解析登录响应失败: %w", err)
	}
	token := NormalizeToken(parsed)
	if token == "" {
		msg := strings.TrimSpace(parsed.Message)
		if msg == "" {
			msg = "登录响应未包含 token"
		}
		return "", errors.New(msg)
	}
	return token, nil
}

// GetUserInfo 用会话 token 拉取用户资料（含 quota 余额）。
func (c *NewAPIClient) GetUserInfo(ctx context.Context, baseURL, token string) (UserInfo, error) {
	var parsed UserSelfResponse
	if err := c.doWithToken(ctx, baseURL, "/api/user/self", token, nil, &parsed); err != nil {
		return UserInfo{}, err
	}
	if !parsed.Success {
		return UserInfo{}, errors.New(strings.TrimSpace(parsed.Message))
	}
	return parsed.Data, nil
}

// UserTokenListItem 是 /api/token/ 返回的单条令牌记录。
// Key 在列表接口中被 mask，不可直接使用，需通过 /api/token/:id/key 获取完整 key。
type UserTokenListItem struct {
	ID                 int    `json:"id"`
	Status             int    `json:"status"`
	Name               string `json:"name"`
	Key                string `json:"key"`
	RemainQuota        int64  `json:"remain_quota"`
	UnlimitedQuota     bool   `json:"unlimited_quota"`
	ExpiredTime        int64  `json:"expired_time"`
	ModelLimitsEnabled bool   `json:"model_limits_enabled"`
	ModelLimits        string `json:"model_limits"`
}

// UserTokensResponse 是 /api/token/ 的响应包装。
// data 字段可能是对象（含 items/total）或直接数组，用 RawMessage 兼容两种格式。
type UserTokensResponse struct {
	Success bool              `json:"success"`
	Message string            `json:"message"`
	Data    json.RawMessage   `json:"data"`
	Total   int               `json:"total"`
}

// UserTokensPageData 是 data 为对象时的分页包装。
type UserTokensPageData struct {
	Items   []UserTokenListItem `json:"items"`
	Total   int                 `json:"total"`
}

// TokenKeyResponse 是 /api/token/:id/key 的响应包装。
type TokenKeyResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    TokenKeyData    `json:"data"`
}

type TokenKeyData struct {
	Key string `json:"key"`
}

// isTokenUsable 判断令牌是否可用于调用 /v1/* 接口。
// status==1 表示 enabled；expired_time==-1 表示永不过期，其他 >0 则需小于当前时间戳。
func isTokenUsable(t UserTokenListItem) bool {
	if t.Status != 1 {
		return false
	}
	if t.ExpiredTime == -1 {
		return true
	}
	return t.ExpiredTime == 0 || t.ExpiredTime > time.Now().Unix()
}

// ListUserTokens 拉取当前账号下的 API 令牌列表。
func (c *NewAPIClient) ListUserTokens(ctx context.Context, baseURL, token string) ([]UserTokenListItem, error) {
	var tokensResp UserTokensResponse
	if err := c.doWithToken(ctx, baseURL, "/api/token/?p=0&size=500", token, nil, &tokensResp); err != nil {
		return nil, err
	}
	if !tokensResp.Success {
		return nil, errors.New(strings.TrimSpace(tokensResp.Message))
	}
	// data 可能是数组或对象，兼容两种格式
	raw := tokensResp.Data
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// 先尝试直接数组
	var asArray []UserTokenListItem
	if err := json.Unmarshal(raw, &asArray); err == nil {
		return asArray, nil
	}
	// 再尝试对象 {items: [...], total: n}
	var asPage UserTokensPageData
	if err := json.Unmarshal(raw, &asPage); err == nil {
		return asPage.Items, nil
	}
	return nil, fmt.Errorf("无法解析令牌列表 data 字段: %s", truncateBody(raw))
}

// GetUserAPIKeyByID 按指定令牌 ID 拉取完整的 sk-key。
// 遇到 HTTP 429 速率限制时自动重试，最多 3 次。
func (c *NewAPIClient) GetUserAPIKeyByID(ctx context.Context, baseURL, token string, tokenID int) (string, error) {
	keyURL := strings.TrimRight(baseURL, "/") + fmt.Sprintf("/api/token/%d/key", tokenID)

	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, keyURL, nil)
		if err != nil {
			return "", err
		}
		applyCompositeTokenHeaders(req, token)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("拉取令牌完整 key 失败: %w", err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("读取令牌 key 响应失败: %w", err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			lastErr = fmt.Errorf("拉取令牌完整 key 失败: HTTP %d（等待重试）", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("拉取令牌完整 key 失败: HTTP %d %s", resp.StatusCode, truncateBody(raw))
		}

		var keyResp TokenKeyResponse
		if err := json.Unmarshal(raw, &keyResp); err != nil {
			return "", fmt.Errorf("解析令牌 key 响应失败: %w", err)
		}
		if !keyResp.Success {
			return "", errors.New(strings.TrimSpace(keyResp.Message))
		}
		key := strings.TrimSpace(keyResp.Data.Key)
		if key == "" {
			return "", errors.New("NewAPI 返回的令牌 key 为空")
		}
		return key, nil
	}
	return "", lastErr
}

// GetUserAPIKey 兼容旧调用：取第一个可用令牌的完整 sk-key。
func (c *NewAPIClient) GetUserAPIKey(ctx context.Context, baseURL, token string) (string, error) {
	tokens, err := c.ListUserTokens(ctx, baseURL, token)
	if err != nil {
		return "", err
	}
	var usable *UserTokenListItem
	for i := range tokens {
		if isTokenUsable(tokens[i]) {
			usable = &tokens[i]
			break
		}
	}
	if usable == nil {
		return "", errors.New("NewAPI 账号下没有可用的 API 令牌，请在后台创建至少一个启用的令牌")
	}
	return c.GetUserAPIKeyByID(ctx, baseURL, token, usable.ID)
}

// ListModels 用用户的 api_key 拉取可用模型列表（OpenAI 标准格式）。
// 注意这里使用 Bearer 鉴权，与会话 token 的 Authorization: <token> 不同。
func (c *NewAPIClient) ListModels(ctx context.Context, baseURL, apiKey string) ([]ModelItem, error) {
	url := strings.TrimRight(baseURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取模型列表失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取模型列表响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取模型列表失败: HTTP %d %s", resp.StatusCode, truncateBody(raw))
	}

	var parsed ModelsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析模型列表响应失败: %w", err)
	}
	return parsed.Data, nil
}

// ListLogs 用会话 token 拉取分页使用记录。
// page 从 1 开始；size 为每页条数。
func (c *NewAPIClient) ListLogs(ctx context.Context, baseURL, token string, page, size int) ([]LogRecord, int, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	path := fmt.Sprintf("/api/log/self?p=%d&size=%d", page, size)
	var parsed LogsResponse
	if err := c.doWithToken(ctx, baseURL, path, token, nil, &parsed); err != nil {
		return nil, 0, err
	}
	if !parsed.Success {
		return nil, 0, errors.New(strings.TrimSpace(parsed.Message))
	}
	raw := parsed.Data
	if len(raw) == 0 || string(raw) == "null" {
		return nil, parsed.Total, nil
	}
	// 先尝试直接数组
	var asArray []LogRecord
	if err := json.Unmarshal(raw, &asArray); err == nil {
		return asArray, parsed.Total, nil
	}
	// 再尝试对象 {items: [...], total: n}
	var asPage LogsPageData
	if err := json.Unmarshal(raw, &asPage); err == nil {
		total := asPage.Total
		if total == 0 {
			total = parsed.Total
		}
		return asPage.Items, total, nil
	}
	return nil, 0, fmt.Errorf("无法解析使用记录 data 字段: %s", truncateBody(raw))
}

func applyCompositeTokenHeaders(req *http.Request, token string) {
	accessToken := token
	userID := ""
	if idx := strings.Index(token, "|"); idx >= 0 {
		accessToken = token[:idx]
		userID = strings.TrimSpace(token[idx+1:])
	}
	if accessToken != "" {
		req.Header.Set("Authorization", accessToken)
	}
	if userID != "" {
		req.Header.Set("New-Api-User", userID)
	}
}

// doWithToken 是 GET 风格的内部辅助函数。
// body 为 nil 时发 GET；否则发 POST+JSON。
func (c *NewAPIClient) doWithToken(ctx context.Context, baseURL, path, token string, body []byte, out interface{}) error {
	url := strings.TrimRight(baseURL, "/") + path
	var bodyReader io.Reader
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	applyCompositeTokenHeaders(req, token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("调用 newapi 失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取 newapi 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("newapi 调用失败: HTTP %d %s", resp.StatusCode, truncateBody(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析 newapi 响应失败: %w", err)
	}
	return nil
}

// NormalizeToken 从登录响应中提取 token。
// 兼容两种 newapi 分支：message 字段直接是 token；或 data/token 字段。
func NormalizeToken(resp LoginResponse) string {
	if t := strings.TrimSpace(resp.Token); t != "" {
		return t
	}
	if t := NormalizeAPIKey(resp.Data); t != "" {
		return t
	}
	msg := strings.TrimSpace(resp.Message)
	status := strings.ToLower(strings.TrimSpace(resp.Status))
	ok := resp.Success || status == "success" || status == "ok"
	// 标准 newapi 的部分分支在 success=true 时把 token 放在 message。
	if ok && msg != "" && !strings.Contains(strings.ToLower(msg), "fail") {
		return msg
	}
	return ""
}

// NormalizeAPIKey 从 /api/user/token 的 data 字段提取字符串。
// data 可能是 string、或 {key: "..."}、或直接是 nil。
func NormalizeAPIKey(data interface{}) string {
	switch v := data.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	case map[string]interface{}:
		for _, key := range []string{"key", "token", "api_key"} {
			if raw, ok := v[key].(string); ok {
				return strings.TrimSpace(raw)
			}
		}
	}
	return ""
}

// truncateBody 限制错误消息中的响应体长度，避免日志爆炸。
func truncateBody(raw []byte) string {
	const max = 200
	if len(raw) <= max {
		return string(raw)
	}
	return string(raw[:max]) + "...(truncated)"
}
