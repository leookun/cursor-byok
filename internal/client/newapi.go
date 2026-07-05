package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cursor/internal/backend/server/config"
	"cursor/internal/modelchannel"
	"cursor/internal/newapi"

	"github.com/pkg/browser"
)

// NewAPIQuotaPerUSD 是 newapi 内部 quota 与美元的换算系数。
// newapi 约定 500000 quota = 1 USD，所有余额/费用均以此换算。
const NewAPIQuotaPerUSD = 500000.0

// NewAPILoginRequest 是前端发起登录的入参。
// 同时支持两种连接方式：用户名/密码登录和令牌登录。
// 连接方式之间互斥：用户名/密码登录和令牌登录只能同时使用一种。
type NewAPILoginRequest struct {
	BaseURL  string `json:"baseURL"`
	Username string `json:"username"`
	Password string `json:"password"`
	// 个人令牌登录相关字段
	Token       string `json:"token"`
	UserID      string `json:"userID"`
	DisplayName string `json:"displayName"`
}

// NewAPIStatus 是登录后展示给前端的账号快照。
// QuotaInUSD 已换算为美元，余额=剩余额度。
type NewAPIStatus struct {
	LoggedIn    bool    `json:"loggedIn"`
	BaseURL     string  `json:"baseURL"`
	Username    string  `json:"username"`
	DisplayName string  `json:"displayName"`
	Quota       int64   `json:"quota"`
	QuotaInUSD  float64 `json:"quotaInUSD"`
	UsedQuota   int64   `json:"usedQuota"`
}

// NewAPIModelItem 是前端可见的模型条目 DTO。
// Type/OpenAIEndpoint 由前端导入时按令牌组选择，覆盖后端默认值。
type NewAPIModelItem struct {
	ID             string `json:"id"`
	Object         string `json:"object"`
	OwnedBy        string `json:"owned_by"`
	TokenID        int    `json:"tokenId"`
	TokenName      string `json:"tokenName"`
	Type           string `json:"type,omitempty"`
	OpenAIEndpoint string `json:"openAIEndpoint,omitempty"`
}

// NewAPITokenGroup 是按 API 令牌分组后的模型列表。
type NewAPITokenGroup struct {
	TokenID            int               `json:"tokenId"`
	TokenName          string            `json:"tokenName"`
	ModelLimitsEnabled bool              `json:"modelLimitsEnabled"`
	Models             []NewAPIModelItem `json:"models"`
	Error              string            `json:"error"`
}

// NewAPILogRecord 是前端可见的使用记录 DTO。
type NewAPILogRecord struct {
	ID               int    `json:"id"`
	CreatedAt        string `json:"created_at"`
	ModelName        string `json:"model_name"`
	TokenName        string `json:"token_name"`
	Quota            int64  `json:"quota"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Content          string `json:"content"`
}

// NewAPILogsRequest 是分页查询使用记录的入参。
type NewAPILogsRequest struct {
	Page int `json:"page"`
	Size int `json:"size"`
}

// NewAPILogsResult 是使用记录的分页结果。
type NewAPILogsResult struct {
	Records []NewAPILogRecord `json:"records"`
	Total   int               `json:"total"`
	Page    int               `json:"page"`
	Size    int               `json:"size"`
}

// NewAPIImportRequest 是模型导入的入参。
// Models 为用户勾选的“令牌-模型”组合项列表。
type NewAPIImportRequest struct {
	Models []NewAPIModelItem `json:"models"`
}

// NewAPIImportResult 报告导入条数与跳过的重复条数。
type NewAPIImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

// newapiClient 返回一个复用 ProxyService.publicClient 的 newapi 客户端。
// publicClient 已通过 netproxy 注入系统代理，可正确穿越用户网络环境。
func (s *ProxyService) newapiClient() *newapi.NewAPIClient {
	return newapi.NewClientWithHTTP(s.publicClient)
}

// NewAPITokenLogin 使用个人令牌登录，不调用 /api/user/login。
// 用 token 调用 /api/user/self 验证有效性并获取用户资料/余额；
// 如果 /api/user/self 不可用，则降级为离线状态（仅 token + DisplayName）。
func (s *ProxyService) NewAPITokenLogin(ctx context.Context, req NewAPILoginRequest) (NewAPIStatus, error) {
	if s == nil {
		return NewAPIStatus{}, errors.New("服务未初始化")
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		return NewAPIStatus{}, errors.New("newapi 实例地址不能为空")
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		return NewAPIStatus{}, errors.New("个人令牌不能为空")
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = baseURL
	}

	// 用户 ID 是 NewAPI UserAuth 接口必需的 header（New-Api-User）。
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		return NewAPIStatus{}, errors.New("用户 ID 不能为空（NewAPI 后台可查看）")
	}

	// 复合 token 格式 "accessToken|userId"，doWithToken 会解析并设置两个 header。
	compositeToken := token + "|" + userID

	client := s.newapiClient()

	// 尝试用个人令牌拉取用户信息验证有效性。
	info, infoErr := client.GetUserInfo(ctx, baseURL, compositeToken)
	if infoErr != nil {
		// 个人令牌无效时直接返回错误，避免把错误 token 持久化为登录态。
		return NewAPIStatus{}, infoErr
	}

	picked := pickTokenDisplayName(info, displayName)

	// 写入并持久化 config。
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return NewAPIStatus{}, fmt.Errorf("读取本地配置失败: %w", err)
	}
	cfg.NewAPI = config.NewAPIBinding{
		BaseURL:     baseURL,
		Token:       compositeToken,
		DisplayName: picked,
	}
	if err := s.SaveUserConfig(cfg); err != nil {
		return NewAPIStatus{}, fmt.Errorf("保存登录态失败: %w", err)
	}

	status := NewAPIStatus{
		LoggedIn:    true,
		BaseURL:     baseURL,
		Username:    info.Username,
		DisplayName: picked,
		Quota:       info.Quota,
		QuotaInUSD:  quotaToUSD(info.Quota),
		UsedQuota:   info.UsedQuota,
	}

	if infoErr != nil {
		// /api/user/self 不可用时保持登录成功
		return status, nil
	}

	return status, nil
}

// NewAPILogin 完成用户名/密码登录，并将 token 持久化到配置。
// 成功后立即刷新 DisplayName 写回配置，供下次启动时免登录展示。
func (s *ProxyService) NewAPILogin(ctx context.Context, req NewAPILoginRequest) (NewAPIStatus, error) {
	if s == nil {
		return NewAPIStatus{}, errors.New("服务未初始化")
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		return NewAPIStatus{}, errors.New("newapi 实例地址不能为空")
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		return NewAPIStatus{}, errors.New("用户名和密码不能为空")
	}

	client := s.newapiClient()
	token, err := client.Login(ctx, baseURL, req.Username, req.Password)
	if err != nil {
		return NewAPIStatus{}, err
	}

	// 登录成功立即拉取用户信息，确认 token 有效并拿到 DisplayName。
	info, err := client.GetUserInfo(ctx, baseURL, token)
	if err != nil {
		// token 已拿到，但拉资料失败时仍保留登录态，返回最小信息。
		info = newapi.UserInfo{Username: req.Username, DisplayName: req.Username}
	}

	// 写入并持久化 config。
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return NewAPIStatus{}, fmt.Errorf("读取本地配置失败: %w", err)
	}
	cfg.NewAPI = config.NewAPIBinding{
		BaseURL:     baseURL,
		Token:       token,
		DisplayName: pickDisplayName(info),
	}
	if err := s.SaveUserConfig(cfg); err != nil {
		return NewAPIStatus{}, fmt.Errorf("保存登录态失败: %w", err)
	}

	return NewAPIStatus{
		LoggedIn:    true,
		BaseURL:     baseURL,
		Username:    info.Username,
		DisplayName: pickDisplayName(info),
		Quota:       info.Quota,
		QuotaInUSD:  quotaToUSD(info.Quota),
		UsedQuota:   info.UsedQuota,
	}, nil
}

// NewAPIGetStatus 返回当前账号绑定状态。
// Token 为空时返回 LoggedIn=false，不发起网络请求。
func (s *ProxyService) NewAPIGetStatus(ctx context.Context) (NewAPIStatus, error) {
	if s == nil {
		return NewAPIStatus{}, nil
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return NewAPIStatus{}, err
	}
	binding := cfg.NewAPI
	if binding.Token == "" || binding.BaseURL == "" {
		return NewAPIStatus{LoggedIn: false}, nil
	}
	// 拉取实时余额
	info, err := s.newapiClient().GetUserInfo(ctx, binding.BaseURL, binding.Token)
	if err != nil {
		if isInvalidAccessTokenError(err) {
			// 明确无效的 access token 不应继续持久化，否则后续导入模型会反复 RuntimeError。
			cfg.NewAPI.Token = ""
			_ = s.SaveUserConfig(cfg)
			return NewAPIStatus{LoggedIn: false}, nil
		}
		// 网络不可达等非凭证错误：保留离线缓存登录态（不返回 error，
		// 否则 Wails 会 reject 前端 Promise，导致前端拿不到 LoggedIn=true）。
		return NewAPIStatus{
			LoggedIn:    true,
			BaseURL:     binding.BaseURL,
			DisplayName: binding.DisplayName,
		}, nil
	}
	return NewAPIStatus{
		LoggedIn:    true,
		BaseURL:     binding.BaseURL,
		Username:    info.Username,
		DisplayName: pickDisplayName(info),
		Quota:       info.Quota,
		QuotaInUSD:  quotaToUSD(info.Quota),
		UsedQuota:   info.UsedQuota,
	}, nil
}

// NewAPIGetLogs 分页拉取当前账号的使用记录。
func (s *ProxyService) NewAPIGetLogs(ctx context.Context, req NewAPILogsRequest) (NewAPILogsResult, error) {
	if s == nil {
		return NewAPILogsResult{}, errors.New("服务未初始化")
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return NewAPILogsResult{}, err
	}
	if cfg.NewAPI.Token == "" || cfg.NewAPI.BaseURL == "" {
		return NewAPILogsResult{}, errors.New("未登录 newapi 账号")
	}
	records, total, err := s.newapiClient().ListLogs(ctx, cfg.NewAPI.BaseURL, cfg.NewAPI.Token, req.Page, req.Size)
	if err != nil {
		return NewAPILogsResult{}, err
	}
	return NewAPILogsResult{
		Records: toClientLogRecords(records),
		Total:   total,
		Page:    req.Page,
		Size:    req.Size,
	}, nil
}

// NewAPIGetModels 拉取当前账号下所有可用 API 令牌的模型列表，并按令牌分组返回。
func (s *ProxyService) NewAPIGetModels(ctx context.Context) ([]NewAPITokenGroup, error) {
	if s == nil {
		return nil, errors.New("服务未初始化")
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return nil, err
	}
	if cfg.NewAPI.Token == "" || cfg.NewAPI.BaseURL == "" {
		return nil, errors.New("未登录 newapi 账号")
	}
	client := s.newapiClient()
	tokens, err := client.ListUserTokens(ctx, cfg.NewAPI.BaseURL, cfg.NewAPI.Token)
	if err != nil {
		if isInvalidAccessTokenError(err) {
			cfg.NewAPI.Token = ""
			_ = s.SaveUserConfig(cfg)
			return nil, errors.New("个人令牌已失效，请重新登录 NewAPI")
		}
		return nil, err
	}
	groups := make([]NewAPITokenGroup, 0, len(tokens))
	for _, token := range tokens {
		if !isUserTokenUsable(token) {
			continue
		}
		group := NewAPITokenGroup{
			TokenID:            token.ID,
			TokenName:          strings.TrimSpace(token.Name),
			ModelLimitsEnabled: token.ModelLimitsEnabled,
			Models:             []NewAPIModelItem{},
		}
		apiKey, keyErr := client.GetUserAPIKeyByID(ctx, cfg.NewAPI.BaseURL, cfg.NewAPI.Token, token.ID)
		if keyErr != nil {
			group.Error = keyErr.Error()
			groups = append(groups, group)
			continue
		}
		models, modelErr := client.ListModels(ctx, cfg.NewAPI.BaseURL, apiKey)
		if modelErr != nil {
			group.Error = modelErr.Error()
			groups = append(groups, group)
			continue
		}
		group.Models = toClientModelItemsForToken(models, token.ID, token.Name)
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return nil, errors.New("NewAPI 账号下没有可用的 API 令牌，请先在后台创建并启用令牌")
	}
	return groups, nil
}

// NewAPIImportModels 将用户勾选的模型写入 ModelAdapters。
// 去重策略：以 (BaseURL, ModelID, APIKey, DisplayName, OpenAIEndpoint) 指纹判重，
// 与现有适配器冲突的条目计入 Skipped 而非报错。
// 用户的新模型统一使用从 newapi 拉取到的 sk-... key 作为凭证。
func (s *ProxyService) NewAPIImportModels(ctx context.Context, req NewAPIImportRequest) (NewAPIImportResult, error) {
	if s == nil {
		return NewAPIImportResult{}, errors.New("服务未初始化")
	}
	if len(req.Models) == 0 {
		return NewAPIImportResult{}, errors.New("未选择要导入的模型")
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return NewAPIImportResult{}, err
	}
	if cfg.NewAPI.Token == "" || cfg.NewAPI.BaseURL == "" {
		return NewAPIImportResult{}, errors.New("未登录 newapi 账号")
	}

	imported := 0
	skipped := 0
	// 用现有适配器建立指纹集合，避免线性扫描重复。
	existing := make(map[string]struct{}, len(cfg.ModelAdapters))
	for _, a := range cfg.ModelAdapters {
		existing[modelAdapterFingerprint(a)] = struct{}{}
	}

	for _, m := range req.Models {
		if strings.TrimSpace(m.ID) == "" || m.TokenID <= 0 {
			skipped++
			continue
		}
		apiKey, keyErr := s.newapiClient().GetUserAPIKeyByID(ctx, cfg.NewAPI.BaseURL, cfg.NewAPI.Token, m.TokenID)
		if keyErr != nil {
			if isInvalidAccessTokenError(keyErr) {
				cfg.NewAPI.Token = ""
				_ = s.SaveUserConfig(cfg)
				return NewAPIImportResult{}, errors.New("个人令牌已失效，请重新登录 NewAPI")
			}
			skipped++
			continue
		}
		displayName := buildImportDisplayName(m.ID)
		adapterType := strings.TrimSpace(m.Type)
		if adapterType == "" {
			adapterType = "openai"
		}
		endpoint := ""
		if adapterType == "openai" {
			endpoint = strings.TrimSpace(m.OpenAIEndpoint)
			if endpoint == "" {
				endpoint = modelchannel.OpenAIEndpointChatCompletions
			}
		}
		adapter := config.ModelAdapterConfig{
			DisplayName:     displayName,
			Type:            adapterType,
			BaseURL:         cfg.NewAPI.BaseURL,
			APIKey:          apiKey,
			TooltipData:     buildImportTooltip(m.TokenName),
			ModelID:         m.ID,
			ReasoningEffort: "medium",
			OpenAIEndpoint:  endpoint,
		}
		fp := modelAdapterFingerprint(adapter)
		if _, ok := existing[fp]; ok {
			skipped++
			continue
		}
		existing[fp] = struct{}{}
		cfg.ModelAdapters = append(cfg.ModelAdapters, adapter)
		imported++
	}

	if imported == 0 {
		return NewAPIImportResult{Imported: 0, Skipped: skipped}, nil
	}
	if err := s.SaveUserConfig(cfg); err != nil {
		return NewAPIImportResult{}, err
	}
	return NewAPIImportResult{Imported: imported, Skipped: skipped}, nil
}

// NewAPILogout 清除配置中的 token，保留 BaseURL/DisplayName 以便下次快速登录。
func (s *ProxyService) NewAPILogout() error {
	if s == nil {
		return nil
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return err
	}
	cfg.NewAPI.Token = ""
	return s.SaveUserConfig(cfg)
}

// NewAPIOpenTopup 在系统浏览器打开 newapi 充值页面。
// newapi 没有充值 API，统一走外链跳转。
func (s *ProxyService) NewAPIOpenTopup() error {
	if s == nil {
		return errors.New("服务未初始化")
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return err
	}
	if cfg.NewAPI.BaseURL == "" {
		return errors.New("未配置 newapi 实例地址")
	}
	url := strings.TrimRight(cfg.NewAPI.BaseURL, "/") + "/topup"
	return browser.OpenURL(url)
}

// --- helpers ---

func pickDisplayName(info newapi.UserInfo) string {
	if name := strings.TrimSpace(info.DisplayName); name != "" {
		return name
	}
	if name := strings.TrimSpace(info.Username); name != "" {
		return name
	}
	return "newapi 用户"
}

func isInvalidAccessTokenError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid access token") || strings.Contains(msg, "user id mismatch")
}

// pickTokenDisplayName 从 GetUserInfo 返回的信息中选择显示名，优先使用 info 中的字段。
func pickTokenDisplayName(info newapi.UserInfo, fallback string) string {
	if name := strings.TrimSpace(info.DisplayName); name != "" {
		return name
	}
	return fallback
}

// quotaToUSD 将 newapi 内部 quota 换算为美元。
func quotaToUSD(quota int64) float64 {
	if quota <= 0 {
		return 0
	}
	return float64(quota) / NewAPIQuotaPerUSD
}

// buildImportDisplayName 给导入的模型生成显示名。
func buildImportDisplayName(modelID string) string {
	return strings.TrimSpace(modelID)
}

func buildImportTooltip(tokenName string) string {
	tokenName = strings.TrimSpace(tokenName)
	if tokenName == "" {
		return "来自 newapi"
	}
	return "来自 newapi · 令牌：" + tokenName
}

func isUserTokenUsable(token newapi.UserTokenListItem) bool {
	return token.Status == 1 && (token.ExpiredTime == -1 || token.ExpiredTime == 0 || token.ExpiredTime > time.Now().Unix())
}

func toClientModelItemsForToken(input []newapi.ModelItem, tokenID int, tokenName string) []NewAPIModelItem {
	items := make([]NewAPIModelItem, 0, len(input))
	for _, item := range input {
		items = append(items, NewAPIModelItem{
			ID:        strings.TrimSpace(item.ID),
			Object:    strings.TrimSpace(item.Object),
			OwnedBy:   strings.TrimSpace(item.OwnedBy),
			TokenID:   tokenID,
			TokenName: strings.TrimSpace(tokenName),
		})
	}
	return items
}

func toClientLogRecords(input []newapi.LogRecord) []NewAPILogRecord {
	records := make([]NewAPILogRecord, 0, len(input))
	for _, item := range input {
		records = append(records, NewAPILogRecord{
			ID:               item.ID,
			CreatedAt:        formatUnixTime(item.CreatedAt),
			ModelName:        item.ModelName,
			TokenName:        item.TokenName,
			Quota:            item.Quota,
			PromptTokens:     item.PromptTokens,
			CompletionTokens: item.CompletionTokens,
			Content:          item.Content,
		})
	}
	return records
}

// modelAdapterFingerprint 与 NormalizeModelAdapterConfigs 中 BuildChannelID 的输入保持一致，
// 用于判重。这里不直接用 ID 字段，因为新构造的 adapter 还没有 ID。
func modelAdapterFingerprint(a config.ModelAdapterConfig) string {
	return strings.Join([]string{
		strings.TrimSpace(a.BaseURL),
		strings.TrimSpace(a.ModelID),
		strings.TrimSpace(a.APIKey),
		strings.TrimSpace(a.DisplayName),
		strings.TrimSpace(a.OpenAIEndpoint),
	}, "\n")
}

// formatUnixTime 将 Unix 时间戳格式化为前端友好的字符串。
func formatUnixTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).In(time.Local).Format("2006-01-02 15:04:05")
}
