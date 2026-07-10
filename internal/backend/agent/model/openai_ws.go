// openai_ws.go 实现 OpenAI Responses 的 WebSocket 直连传输。
//
// 背景：sub2api 等订阅网关只在“客户端以 WebSocket 入站”时才会把请求转发到
// 上游的 Responses WebSocket 通道，而 OpenAI 仅在该通道上兑现
// service_tier=priority（Codex fast 模式，约 1.5x 生成速度）。本文件在
// HTTP 层之上加一条可选的 WS 传输：命中条件时先尝试 WS，任何失败都静默
// 回退到原有 HTTP/SSE 路径，并对该主机做短时负缓存避免反复握手。
//
// 命中条件（全部满足）：
//   - 端点路径以 /responses 结尾（chat/completions 等不受影响）
//   - 请求体携带非空 service_tier 字段（即用户显式要求 fast/flex）
//   - 环境变量 BYOK_OPENAI_WS 未设置为 off/0/false
//
// BYOK_OPENAI_WS=always 时放宽第二条（所有 /responses 请求都尝试 WS）。
package modeladapter

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"cursor/internal/netproxy"
)

const (
	openAIWSDialTimeout       = 15 * time.Second
	openAIWSFirstFrameTimeout = 30 * time.Second
	openAIWSReadLimitBytes    = 64 * 1024 * 1024
	// openAIWSPermanentCooldown 用于「永不重试」策略：本进程内不再尝试该主机的 WS
	//（重启即复位，因为负缓存只在内存）。
	openAIWSPermanentCooldown = 100000 * time.Hour

	// OpenAIWSCodexOriginator / OpenAIWSCodexUserAgent 让 WS 握手以官方 Codex
	// CLI 身份出现，从而使网关向上游转发 codex_cli_rs 并激活 fast 通道。
	OpenAIWSCodexOriginator = "codex_cli_rs"
	OpenAIWSCodexUserAgent  = "codex_cli_rs/0.5.0"

	// OpenAIWSFallback* 为「WS 失败回退」策略取值。失败后先退 HTTP，再按此决定
	// 多久重新探测 WS。
	OpenAIWSFallbackRetry5m  = "retry_5m"
	OpenAIWSFallbackRetry10m = "retry_10m"
	OpenAIWSFallbackNever    = "never"
	OpenAIWSFallbackDefault  = OpenAIWSFallbackRetry10m
)

// openAIWSCooldown 把回退策略解析为负缓存冷却时长。
func openAIWSCooldown(strategy string) time.Duration {
	switch strings.TrimSpace(strategy) {
	case OpenAIWSFallbackRetry5m:
		return 5 * time.Minute
	case OpenAIWSFallbackNever:
		return openAIWSPermanentCooldown
	default: // retry_10m 或空值
		return 10 * time.Minute
	}
}

// openAIWSHostFailures 记录 WS 握手失败的主机及其负缓存过期时间。
var openAIWSHostFailures sync.Map // string -> time.Time

var (
	openAIWSDialClientOnce sync.Once
	openAIWSDialClient     *http.Client
)

// doProviderRequestMaybeWS 与 doProviderRequestWithRetry 等价，但在命中
// 条件时优先尝试 Responses WebSocket 传输；未命中或失败则走原 HTTP 路径。
func doProviderRequestMaybeWS(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	requestURL string,
	apiKey string,
	payload []byte,
	req StreamRequest,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	if resp, ok := tryOpenAIResponsesWebSocket(ctx, requestURL, apiKey, payload, req); ok {
		return resp, nil
	}
	return doProviderRequestWithRetry(ctx, client, provider, requestID, modelCallID, buildRequest)
}

func openAIWSMode() string {
	return strings.TrimSpace(strings.ToLower(os.Getenv("BYOK_OPENAI_WS")))
}

func openAIWSEligible(requestURL string, payload []byte, forceWS bool) bool {
	mode := openAIWSMode()
	switch mode {
	case "off", "0", "false", "disable", "disabled":
		// 环境变量显式关闭时，优先级最高，直接禁用 WS。
		return false
	}
	parsed, err := url.Parse(requestURL)
	if err != nil || parsed == nil {
		return false
	}
	if !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/responses") {
		return false
	}
	// 环境变量 always，或模型配置里打开了「强制 WS 加速」开关：所有 /responses 走 WS。
	if mode == "always" || forceWS {
		return true
	}
	// 默认：仅当请求体带非空 service_tier 时才走 WS（避免影响普通请求）。
	if !bytes.Contains(payload, []byte(`"service_tier"`)) {
		return false
	}
	var probe struct {
		ServiceTier string `json:"service_tier"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return strings.TrimSpace(probe.ServiceTier) != ""
}

func openAIWSURLFor(requestURL string) (wsURL string, hostKey string) {
	parsed, err := url.Parse(requestURL)
	if err != nil || parsed == nil {
		return "", ""
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", ""
	}
	return parsed.String(), parsed.Host
}

func openAIWSHostRecentlyFailed(host string) bool {
	value, ok := openAIWSHostFailures.Load(host)
	if !ok {
		return false
	}
	expiry, ok := value.(time.Time)
	if !ok || time.Now().After(expiry) {
		openAIWSHostFailures.Delete(host)
		return false
	}
	return true
}

func openAIWSMarkHostFailed(host string, cooldown time.Duration) {
	openAIWSHostFailures.Store(host, time.Now().Add(cooldown))
}

// getOpenAIWSDialClient 返回用于 WS 握手的 HTTP/1.1 客户端。
// WebSocket 升级要求 HTTP/1.1，因此在 netproxy 传输上禁用 HTTP/2 协商。
func getOpenAIWSDialClient() *http.Client {
	openAIWSDialClientOnce.Do(func() {
		transport := netproxy.NewTransport(nil)
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		openAIWSDialClient = &http.Client{Transport: transport}
	})
	return openAIWSDialClient
}

// openAICodexWSUnsupportedFields 是 Codex 订阅（OAuth）上游在 Responses WS 通道
// 不接受的请求字段，需在发送前剔除。镜像 sub2api HTTP 路径
// applyCodexOAuthTransform 的剥离列表——WS ingress 不做这层清洗，故必须由客户端
// 负责，否则上游返回 "Unsupported parameter: max_output_tokens" 等
// invalid_request_error。
var openAICodexWSUnsupportedFields = []string{
	"max_output_tokens",
	"max_completion_tokens",
	"temperature",
	"top_p",
	"frequency_penalty",
	"presence_penalty",
	"user",
	"metadata",
	"prompt_cache_retention",
	"safety_identifier",
	"stream_options",
}

// openAIWSCreateFrame 把 HTTP 请求体转换为 WS 首帧（response.create），
// 并剔除 Codex 订阅上游不支持的字段。
func openAIWSCreateFrame(payload []byte) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	for _, field := range openAICodexWSUnsupportedFields {
		delete(body, field)
	}
	body["type"] = "response.create"
	return json.Marshal(body)
}

// tryOpenAIResponsesWebSocket 尝试通过 WS 完成请求。返回 (resp, true) 表示
// WS 已接管（resp.Body 输出合成的 SSE 流）；(nil, false) 表示调用方应走 HTTP。
func tryOpenAIResponsesWebSocket(
	ctx context.Context,
	requestURL string,
	apiKey string,
	payload []byte,
	req StreamRequest,
) (*http.Response, bool) {
	if !openAIWSEligible(requestURL, payload, req.OpenAIForceWS) {
		return nil, false
	}
	wsURL, host := openAIWSURLFor(requestURL)
	if wsURL == "" || openAIWSHostRecentlyFailed(host) {
		return nil, false
	}
	cooldown := openAIWSCooldown(req.OpenAIWSFallback)
	firstFrame, err := openAIWSCreateFrame(payload)
	if err != nil {
		return nil, false
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+apiKey)
	// 网关（如 sub2api）仅在识别到官方 Codex 客户端时才向上游转发
	// originator=codex_cli_rs，而 OpenAI 也仅对该 originator 兑现
	// service_tier=priority。因此 WS 通道必须以 Codex 官方客户端身份握手，
	// 否则会被降级为 opencode、priority 静默失效（生成速度回落到 1x）。
	header.Set("User-Agent", OpenAIWSCodexUserAgent)
	header.Set("originator", OpenAIWSCodexOriginator)
	applyCustomHeadersToWSHeader(header, req.CustomHeadersEnabled, req.CustomHeadersJSON)

	dialCtx, cancelDial := context.WithTimeout(ctx, openAIWSDialTimeout)
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPClient: getOpenAIWSDialClient(),
		HTTPHeader: header,
	})
	cancelDial()
	if err != nil {
		openAIWSMarkHostFailed(host, cooldown)
		return nil, false
	}
	conn.SetReadLimit(openAIWSReadLimitBytes)

	if err := conn.Write(ctx, websocket.MessageText, firstFrame); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "write failed")
		openAIWSMarkHostFailed(host, cooldown)
		return nil, false
	}

	// 读取第一帧再决定是否接管：若在收到任何内容前连接就异常关闭（典型如网关
	// "upstream websocket proxy failed" 直接回 Close 帧），说明"零内容已发给客户端"，
	// 可安全回退到原 HTTP 路径（由调用方走 doProviderRequestWithRetry，返回真实状态码），
	// 不会产生重复输出。应用层错误（以 data 帧形式返回，如 invalid_request_error）
	// 则原样转发给客户端，不回退、不重复执行。
	firstCtx, cancelFirst := context.WithTimeout(ctx, openAIWSFirstFrameTimeout)
	firstType, firstData, firstErr := conn.Read(firstCtx)
	cancelFirst()
	if firstErr != nil || (firstType != websocket.MessageText && firstType != websocket.MessageBinary) {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		openAIWSMarkHostFailed(host, cooldown)
		return nil, false
	}

	pr, pw := io.Pipe()
	go pumpOpenAIWSFrames(ctx, conn, pw, firstData)

	resp := &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &openAIWSResponseBody{pr: pr, conn: conn},
	}
	return resp, true
}

// pumpOpenAIWSFrames 把 WS 事件帧转写为 SSE data 行，供既有解析逻辑消费。
// firstData 是调用方已提前读取的第一帧（用于首帧探活），此处先行写出再继续循环。
func pumpOpenAIWSFrames(ctx context.Context, conn *websocket.Conn, pw *io.PipeWriter, firstData []byte) {
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()
	terminalSeen := false

	// writeFrame 把一帧写成 SSE data 行。返回 stop=是否应终止 pump，terminal=是否为终止事件。
	writeFrame := func(data []byte) (stop bool, terminal bool) {
		compact := &bytes.Buffer{}
		if err := json.Compact(compact, data); err != nil {
			return false, false // 跳过畸形帧
		}
		line := append([]byte("data: "), compact.Bytes()...)
		line = append(line, '\n', '\n')
		if _, err := pw.Write(line); err != nil {
			return true, false
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &probe); err == nil {
			switch probe.Type {
			case "response.completed", "response.failed", "response.incomplete", "error":
				return false, true
			}
		}
		return false, false
	}

	if len(firstData) > 0 {
		stop, terminal := writeFrame(firstData)
		if stop {
			return
		}
		if terminal {
			terminalSeen = true
			_ = pw.Close()
			return
		}
	}

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			// 终止帧之后的连接关闭属于正常收尾，按 EOF 结束 SSE 流。
			if terminalSeen {
				_ = pw.Close()
			} else {
				_ = pw.CloseWithError(err)
			}
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			continue
		}
		stop, terminal := writeFrame(data)
		if stop {
			return
		}
		if terminal {
			terminalSeen = true
			_ = pw.Close()
			return
		}
	}
}

// openAIWSResponseBody 让合成的 http.Response 关闭时同步关闭底层 WS 连接。
type openAIWSResponseBody struct {
	pr   *io.PipeReader
	conn *websocket.Conn
	once sync.Once
}

func (b *openAIWSResponseBody) Read(p []byte) (int, error) {
	return b.pr.Read(p)
}

func (b *openAIWSResponseBody) Close() error {
	b.once.Do(func() {
		_ = b.conn.Close(websocket.StatusNormalClosure, "")
		_ = b.pr.Close()
	})
	return nil
}

func applyCustomHeadersToWSHeader(header http.Header, enabled bool, headersJSON string) {
	if !enabled {
		return
	}
	headers, err := parseStringJSONMap(headersJSON, "custom headers json")
	if err != nil {
		return
	}
	for key, value := range headers {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		header.Set(name, value)
	}
}
