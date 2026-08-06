// retry.go 对 provider HTTP 请求的瞬断做服务端透明重试。
//
// 背景：opencode.ai/zen 等免费代理连接不稳定，会间歇性地出现 DNS 解析失败
// （getaddrinfo EAI_AGAIN）、连接中途断开、或把"上游瞬断"包装成 400
// invalid_request_error "Upstream request failed"。如果直接把这类错误抛回客户端，
// 客户端会重新发起整轮请求并重置已生成的文本，表现为"输出到一半突然截断又重来"。
//
// 因此这里在请求建立阶段（尚未向客户端吐出任何 token）做指数退避重试，
// 把瞬断在服务端消化掉。流已经开始的阶段不重试——重试会重复已吐出的 token。
package modeladapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cursor/internal/logger"
)

const (
	// providerMaxRetries 最多重试 10 次。
	providerMaxRetries = 10
	// providerBaseRetryDelay 指数退避基准：首跳 800ms，之后 1.6s / 3.2s / 6.4s ...
	providerBaseRetryDelay = 800 * time.Millisecond
	// providerMaxRetryDelay 退避上限，避免单次等待过久。
	providerMaxRetryDelay = 20 * time.Second
	// providerMaxErrorBodyBytes 判断 4xx 是否"瞬断型"时最多读取的响应体字节数。
	providerMaxErrorBodyBytes = 64 * 1024
)

// DoProviderRequestWithRetry 保留旧入口名；对瞬断/5xx/上游瞬断型 4xx 做指数退避重试。
func DoProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	return doProviderRequestWithRetry(ctx, client, provider, requestID, modelCallID, buildRequest)
}

func doProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}

	var lastErr error
	for attempt := 0; attempt <= providerMaxRetries; attempt++ {
		if attempt > 0 {
			delay := backoffDelay(attempt)
			logger.Infof("provider retry scheduled provider=%s request_id=%s model_call_id=%s attempt=%d next_attempt=%d delay_ms=%d err=%v",
				provider, requestID, modelCallID, attempt, attempt+1, delay.Milliseconds(), lastErr)
			select {
			case <-ctx.Done():
				if lastErr == nil {
					lastErr = ctx.Err()
				}
				return nil, lastErr
			case <-time.After(delay):
			}
		}

		httpReq, err := buildRequest(ctx)
		if err != nil {
			// 请求构造失败是确定性的（如序列化错误），不重试。
			return nil, err
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			// 网络层错误（DNS 解析失败 EAI_AGAIN、连接被重置、超时等）属于瞬断，可重试。
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		switch {
		case resp.StatusCode >= 500:
			// 5xx 通常是上游瞬时故障，重试。
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("provider %s returned status %d", provider, resp.StatusCode)
			continue
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			// 4xx 多为确定性非法请求，不应重试；但 opencode/zen 等代理会把"上游瞬断"
			// 包装成 400 invalid_request_error "Upstream request failed"，对此类做重试。
			transient, body := classifyUpstream4xx(resp)
			if transient {
				_ = resp.Body.Close()
				lastErr = fmt.Errorf("provider %s returned transient 4xx status %d", provider, resp.StatusCode)
				continue
			}
			// 非瞬断型 4xx：还原响应体后交回调用方（调用方会构造错误返回客户端）。
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		default:
			// 2xx：成功。
			return resp, nil
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("provider %s request failed after %d retries", provider, providerMaxRetries)
	}
	logger.Infof("provider error after retries provider=%s request_id=%s model_call_id=%s err=%v",
		provider, requestID, modelCallID, lastErr)
	return nil, lastErr
}

// backoffDelay 计算第 attempt 次重试前的等待时长（指数退避，带上限）。
func backoffDelay(attempt int) time.Duration {
	delay := providerBaseRetryDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	if delay > providerMaxRetryDelay {
		delay = providerMaxRetryDelay
	}
	return delay
}

// classifyUpstream4xx 读取有限长度响应体，判断 4xx 是否属于"上游瞬断被代理误标"的情况。
// 返回 (是否瞬断, 已读取的原始响应体)；调用方据此决定是否还原 body。
func classifyUpstream4xx(resp *http.Response) (bool, []byte) {
	if resp.Body == nil {
		return false, nil
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, providerMaxErrorBodyBytes))
	if err != nil {
		_ = resp.Body.Close()
		return false, nil
	}
	_ = resp.Body.Close()
	lower := strings.ToLower(string(buf))
	transient := strings.Contains(lower, "upstream request failed") ||
		strings.Contains(lower, "eai_again") ||
		strings.Contains(lower, "upstream connect") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "temporarily unavailable") ||
		strings.Contains(lower, "bad gateway") ||
		strings.Contains(lower, "gateway timeout")
	return transient, buf
}

// ProviderRetryAttemptSummary 返回空值；provider 请求不再有服务端内部重试摘要
// （保持与 http_error.go / model_adapter_benchmark.go 的既有调用兼容）。
func ProviderRetryAttemptSummary(resp *http.Response) string {
	return ""
}