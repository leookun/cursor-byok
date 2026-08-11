// retry.go 保留 provider HTTP 请求入口的历史命名；provider 错误交给客户端重连链路处理。
package modeladapter

import (
	"context"
	"net/http"
)

// DoProviderRequestWithRetry 保留旧入口名；本地模式不在服务端重试 provider 请求。
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
	httpReq, err := buildRequest(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	return resp, nil
}

// ProviderRetryAttemptSummary 返回空值；provider 请求不再有服务端内部重试摘要。
func ProviderRetryAttemptSummary(resp *http.Response) string {
	return ""
}
