package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/modelchannel"
)

const (
	modelDiscoveryTimeout       = 30 * time.Second
	modelDiscoveryMaxBodyBytes  = 4 << 20
	modelDiscoveryMaxErrorBytes = 8 << 10
	modelDiscoveryMaxPages      = 20
	modelDiscoveryMaxModels     = 5000
	modelDiscoveryDialTimeout   = 10 * time.Second
	modelDiscoveryFallbackDelay = 200 * time.Millisecond
)

type UpstreamModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type ModelDiscoveryResult struct {
	Models []UpstreamModelInfo `json:"models"`
	Total  int                 `json:"total"`
}

type modelDiscoveryPage struct {
	Models  []UpstreamModelInfo
	HasMore bool
	LastID  string
}

func (s *ProxyService) DiscoverModelAdapterModels(adapterID string) (ModelDiscoveryResult, error) {
	normalized, err := s.resolvePersistedModelAdapter(adapterID)
	if err != nil {
		return ModelDiscoveryResult{}, err
	}
	return s.discoverModelAdapterModels(normalized)
}

func (s *ProxyService) DiscoverModelGroupModels(groupID string) (ModelDiscoveryResult, error) {
	config, err := s.LoadUserConfig()
	if err != nil {
		return ModelDiscoveryResult{}, err
	}
	normalized, err := serverconfig.NormalizeConfig(config)
	if err != nil {
		return ModelDiscoveryResult{}, err
	}
	group, exists := serverconfig.FindModelGroupConfig(normalized, groupID)
	if !exists {
		return ModelDiscoveryResult{}, errors.New("模型分组不存在或已变更")
	}
	adapter := serverconfig.ModelAdapterConfig{
		Type:                 group.Type,
		BaseURL:              group.BaseURL,
		APIKey:               group.APIKey,
		OpenAIEndpoint:       group.OpenAIEndpoint,
		CustomHeadersEnabled: group.CustomHeadersEnabled,
		CustomHeadersJSON:    group.CustomHeadersJSON,
	}
	return s.discoverModelAdapterModels(adapter)
}

func (s *ProxyService) resolvePersistedModelAdapter(adapterID string) (serverconfig.ModelAdapterConfig, error) {
	id := strings.TrimSpace(adapterID)
	if id == "" {
		return serverconfig.ModelAdapterConfig{}, errors.New("模型渠道 ID 不能为空")
	}
	config, err := s.LoadUserConfig()
	if err != nil {
		return serverconfig.ModelAdapterConfig{}, err
	}
	adapters, err := serverconfig.NormalizeModelAdapterConfigs(config.ModelAdapters)
	if err != nil {
		return serverconfig.ModelAdapterConfig{}, err
	}
	for _, adapter := range adapters {
		if strings.TrimSpace(adapter.ID) == id {
			return adapter, nil
		}
	}
	return serverconfig.ModelAdapterConfig{}, errors.New("模型渠道不存在或已变更")
}

func (s *ProxyService) discoverModelAdapterModels(normalized serverconfig.ModelAdapterConfig) (ModelDiscoveryResult, error) {
	if s == nil || s.publicClient == nil {
		return ModelDiscoveryResult{}, errors.New("HTTP 客户端未初始化")
	}

	discoveryURL, err := modelDiscoveryEndpointURL(normalized)
	if err != nil {
		return ModelDiscoveryResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelDiscoveryTimeout)
	defer cancel()
	if err := validateModelDiscoveryTarget(ctx, discoveryURL); err != nil {
		return ModelDiscoveryResult{}, err
	}

	client, err := newModelDiscoveryHTTPClient(s.publicClient)
	if err != nil {
		return ModelDiscoveryResult{}, err
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	models := make([]UpstreamModelInfo, 0)
	seen := make(map[string]struct{})
	seenCursors := make(map[string]struct{})
	nextURL := discoveryURL
	for pageIndex := 0; pageIndex < modelDiscoveryMaxPages; pageIndex++ {
		page, err := fetchModelDiscoveryPage(ctx, client, normalized, nextURL)
		if err != nil {
			return ModelDiscoveryResult{}, err
		}
		for _, model := range page.Models {
			model.ID = strings.TrimSpace(model.ID)
			model.DisplayName = strings.TrimSpace(model.DisplayName)
			if model.ID == "" {
				continue
			}
			if _, exists := seen[model.ID]; exists {
				continue
			}
			if model.DisplayName == "" {
				model.DisplayName = model.ID
			}
			seen[model.ID] = struct{}{}
			models = append(models, model)
			if len(models) > modelDiscoveryMaxModels {
				return ModelDiscoveryResult{}, fmt.Errorf("上游模型数量超过限制 %d", modelDiscoveryMaxModels)
			}
		}
		if !page.HasMore {
			break
		}
		if strings.TrimSpace(page.LastID) == "" {
			return ModelDiscoveryResult{}, errors.New("上游返回 has_more=true，但缺少 last_id")
		}
		cursor := strings.TrimSpace(page.LastID)
		if _, exists := seenCursors[cursor]; exists {
			return ModelDiscoveryResult{}, fmt.Errorf("上游模型分页游标重复: %s", cursor)
		}
		seenCursors[cursor] = struct{}{}
		nextURL, err = modelDiscoveryNextPageURL(discoveryURL, page.LastID)
		if err != nil {
			return ModelDiscoveryResult{}, err
		}
		if pageIndex == modelDiscoveryMaxPages-1 {
			return ModelDiscoveryResult{}, fmt.Errorf("上游模型分页超过限制 %d", modelDiscoveryMaxPages)
		}
	}

	sort.SliceStable(models, func(left int, right int) bool {
		return strings.ToLower(models[left].ID) < strings.ToLower(models[right].ID)
	})
	return ModelDiscoveryResult{Models: models, Total: len(models)}, nil
}

func newModelDiscoveryHTTPClient(base *http.Client) (*http.Client, error) {
	if base == nil {
		return nil, errors.New("HTTP 客户端未初始化")
	}
	client := *base
	client.Timeout = modelDiscoveryTimeout
	transport, ok := base.Transport.(*http.Transport)
	if !ok || transport == nil {
		return nil, errors.New("HTTP transport 不支持安全模型发现")
	}
	clonedTransport := transport.Clone()
	dialer := &net.Dialer{Timeout: modelDiscoveryDialTimeout, KeepAlive: 30 * time.Second}
	clonedTransport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("解析模型服务网络地址失败: %w", err)
		}
		ips, err := resolveSafeModelDiscoveryIPs(ctx, host)
		if err != nil {
			return nil, err
		}
		return dialModelDiscoveryIPs(ctx, network, port, ips, dialer.DialContext)
	}
	client.Transport = clonedTransport
	return &client, nil
}

type modelDiscoveryDialFunc func(context.Context, string, string) (net.Conn, error)

type modelDiscoveryDialResult struct {
	conn net.Conn
	err  error
}

func dialModelDiscoveryIPs(ctx context.Context, network string, port string, ips []net.IP, dial modelDiscoveryDialFunc) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, errors.New("模型服务域名没有可用地址")
	}
	if dial == nil {
		return nil, errors.New("模型服务拨号器未初始化")
	}
	ordered := interleaveModelDiscoveryIPs(ips)
	dialCtx, cancel := context.WithCancel(ctx)
	results := make(chan modelDiscoveryDialResult, len(ordered))
	for index, ip := range ordered {
		go func(delay time.Duration, target net.IP) {
			if delay > 0 {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-dialCtx.Done():
					results <- modelDiscoveryDialResult{err: dialCtx.Err()}
					return
				}
			}
			conn, err := dial(dialCtx, network, net.JoinHostPort(target.String(), port))
			results <- modelDiscoveryDialResult{conn: conn, err: err}
		}(time.Duration(index)*modelDiscoveryFallbackDelay, ip)
	}

	var lastErr error
	for completed := 0; completed < len(ordered); completed++ {
		result := <-results
		if result.err == nil && result.conn != nil {
			cancel()
			go closeExtraModelDiscoveryConnections(results, len(ordered)-completed-1)
			return result.conn, nil
		}
		if result.conn != nil {
			_ = result.conn.Close()
		}
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			lastErr = result.err
		}
	}
	cancel()
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	if lastErr == nil {
		lastErr = errors.New("模型服务没有可用网络地址")
	}
	return nil, lastErr
}

func closeExtraModelDiscoveryConnections(results <-chan modelDiscoveryDialResult, remaining int) {
	for index := 0; index < remaining; index++ {
		result := <-results
		if result.conn != nil {
			_ = result.conn.Close()
		}
	}
}

func interleaveModelDiscoveryIPs(ips []net.IP) []net.IP {
	if len(ips) < 2 {
		return append([]net.IP(nil), ips...)
	}
	firstIPv6 := ips[0].To4() == nil
	preferred := make([]net.IP, 0, len(ips))
	alternate := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if (ip.To4() == nil) == firstIPv6 {
			preferred = append(preferred, ip)
		} else {
			alternate = append(alternate, ip)
		}
	}
	ordered := make([]net.IP, 0, len(ips))
	for index := 0; index < len(preferred) || index < len(alternate); index++ {
		if index < len(preferred) {
			ordered = append(ordered, preferred[index])
		}
		if index < len(alternate) {
			ordered = append(ordered, alternate[index])
		}
	}
	return ordered
}

func validateModelDiscoveryTarget(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("解析模型服务地址失败: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("模型服务地址仅支持 http 或 https")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return errors.New("模型服务地址缺少主机名")
	}
	_, err = resolveSafeModelDiscoveryIPs(ctx, host)
	return err
}

func resolveSafeModelDiscoveryIPs(ctx context.Context, host string) ([]net.IP, error) {
	normalizedHost := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	switch normalizedHost {
	case "metadata.google.internal", "metadata.google", "instance-data.ec2.internal", "metadata.azure.internal":
		return nil, errors.New("模型服务地址不能指向云 metadata 服务")
	}

	var ips []net.IP
	if literal := net.ParseIP(normalizedHost); literal != nil {
		ips = []net.IP{literal}
	} else {
		resolved, err := net.DefaultResolver.LookupIPAddr(ctx, normalizedHost)
		if err != nil {
			return nil, fmt.Errorf("解析模型服务域名失败: %w", err)
		}
		ips = make([]net.IP, 0, len(resolved))
		for _, item := range resolved {
			ips = append(ips, item.IP)
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("模型服务域名没有可用地址")
	}
	for _, ip := range ips {
		if isUnsafeModelDiscoveryIP(ip) {
			return nil, fmt.Errorf("模型服务地址解析到受保护地址 %s", ip.String())
		}
	}
	return ips, nil
}

func isUnsafeModelDiscoveryIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, raw := range []string{"169.254.169.254", "100.100.100.200", "fd00:ec2::254"} {
		if blocked := net.ParseIP(raw); blocked != nil && ip.Equal(blocked) {
			return true
		}
	}
	return false
}

func fetchModelDiscoveryPage(ctx context.Context, client *http.Client, adapter serverconfig.ModelAdapterConfig, endpoint string) (modelDiscoveryPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return modelDiscoveryPage{}, fmt.Errorf("创建模型列表请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cursor-byok/model-discovery")
	switch adapter.Type {
	case "openai":
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(adapter.APIKey))
	case "anthropic":
		modeladapter.ApplyAnthropicCompatibleAuthHeaders(req, adapter.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		return modelDiscoveryPage{}, fmt.Errorf("不支持的模型渠道类型 %q", adapter.Type)
	}
	if err := modeladapter.ApplyCustomHeaders(req, adapter.CustomHeadersEnabled, adapter.CustomHeadersJSON); err != nil {
		return modelDiscoveryPage{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return modelDiscoveryPage{}, fmt.Errorf("获取上游模型失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, modelDiscoveryMaxErrorBytes))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return modelDiscoveryPage{}, fmt.Errorf("获取上游模型失败（HTTP %d）: %s", resp.StatusCode, message)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, modelDiscoveryMaxBodyBytes+1))
	if err != nil {
		return modelDiscoveryPage{}, fmt.Errorf("读取上游模型响应失败: %w", err)
	}
	if len(body) > modelDiscoveryMaxBodyBytes {
		return modelDiscoveryPage{}, fmt.Errorf("上游模型响应超过 %d MiB", modelDiscoveryMaxBodyBytes>>20)
	}
	page, err := parseModelDiscoveryPage(body)
	if err != nil {
		return modelDiscoveryPage{}, err
	}
	return page, nil
}

func parseModelDiscoveryPage(body []byte) (modelDiscoveryPage, error) {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return modelDiscoveryPage{}, fmt.Errorf("上游模型响应不是合法 JSON: %w", err)
	}

	items := payload
	page := modelDiscoveryPage{}
	if object, ok := payload.(map[string]any); ok {
		switch {
		case object["data"] != nil:
			items = object["data"]
		case object["models"] != nil:
			items = object["models"]
		default:
			return modelDiscoveryPage{}, errors.New("上游模型响应缺少 data 或 models 数组")
		}
		page.HasMore, _ = object["has_more"].(bool)
		page.LastID = firstModelDiscoveryString(object, "last_id", "lastId")
	}

	list, ok := items.([]any)
	if !ok {
		return modelDiscoveryPage{}, errors.New("上游模型列表必须是数组")
	}
	page.Models = make([]UpstreamModelInfo, 0, len(list))
	for _, item := range list {
		switch value := item.(type) {
		case string:
			page.Models = append(page.Models, UpstreamModelInfo{ID: value, DisplayName: value})
		case map[string]any:
			id := firstModelDiscoveryString(value, "id", "model", "model_id", "modelId")
			displayName := firstModelDiscoveryString(value, "display_name", "displayName", "name")
			page.Models = append(page.Models, UpstreamModelInfo{ID: id, DisplayName: displayName})
		}
	}
	return page, nil
}

func firstModelDiscoveryString(source map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := source[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func modelDiscoveryEndpointURL(adapter serverconfig.ModelAdapterConfig) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(adapter.BaseURL), "/")
	var requestURL string
	switch adapter.Type {
	case "openai":
		requestURL = modeladapter.OpenAIEndpointURL(baseURL, adapter.OpenAIEndpoint)
	case "anthropic":
		if modeladapter.ProviderURLHasEndpoint(baseURL, "/v1/messages", "/messages") {
			requestURL = baseURL
		} else if modelDiscoveryURLHasTrailingVersionSegment(baseURL) {
			requestURL = baseURL + "/messages"
		} else {
			requestURL = baseURL + "/v1/messages"
		}
	default:
		return "", fmt.Errorf("不支持的模型渠道类型 %q", adapter.Type)
	}

	parsed, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("解析模型服务地址失败: %w", err)
	}
	if parsed.User != nil {
		return "", errors.New("模型服务地址不能包含 userinfo")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("模型服务地址不能包含 query 或 fragment")
	}
	path := strings.TrimRight(parsed.Path, "/")
	lowerPath := strings.ToLower(path)
	trimmedKnownEndpoint := false
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		if strings.HasSuffix(lowerPath, suffix) {
			path = path[:len(path)-len(suffix)]
			trimmedKnownEndpoint = true
			break
		}
	}
	if adapter.Type == "openai" && adapter.OpenAIEndpoint == modelchannel.OpenAIEndpointCustom && !trimmedKnownEndpoint {
		if separator := strings.LastIndex(path, "/"); separator >= 0 {
			path = path[:separator]
		}
	}
	parsed.Path = strings.TrimRight(path, "/") + "/models"
	parsed.RawPath = ""
	if adapter.Type == "anthropic" {
		query := parsed.Query()
		query.Set("limit", "100")
		parsed.RawQuery = query.Encode()
	} else {
		parsed.RawQuery = ""
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func modelDiscoveryURLHasTrailingVersionSegment(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	path := strings.TrimRight(parsed.Path, "/")
	index := strings.LastIndex(path, "/")
	segment := path[index+1:]
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for _, char := range segment[1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func modelDiscoveryNextPageURL(endpoint string, lastID string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("解析模型分页地址失败: %w", err)
	}
	query := parsed.Query()
	query.Set("after_id", strings.TrimSpace(lastID))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
