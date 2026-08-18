// http_error.go 负责把非 2xx HTTP 响应整理成带响应体摘要的错误。
package modeladapter

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// maxErrorBodyBytes 表示错误响应体最多读取的字节数。
	maxErrorBodyBytes = 8192
)

// HTTPStatusError preserves the provider HTTP status so higher layers can
// distinguish transient overloads from permanent request failures.
type HTTPStatusError struct {
	Prefix        string
	StatusCode    int
	RetrySummary  string
	Body          string
	BodyReadError error
}

func (err *HTTPStatusError) Error() string {
	if err == nil {
		return "provider HTTP error"
	}
	result := fmt.Sprintf("%s status=%d", strings.TrimSpace(err.Prefix), err.StatusCode)
	if summary := strings.TrimSpace(err.RetrySummary); summary != "" {
		result += " " + summary
	}
	if err.BodyReadError != nil {
		return fmt.Sprintf("%s body_read_error=%v", result, err.BodyReadError)
	}
	if body := strings.TrimSpace(err.Body); body != "" {
		result += " body=" + body
	}
	return result
}

// ProviderHTTPStatus returns the original provider response status when the
// error came from a non-2xx HTTP response.
func ProviderHTTPStatus(err error) (int, bool) {
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr == nil || statusErr.StatusCode <= 0 {
		return 0, false
	}
	return statusErr.StatusCode, true
}

// buildHTTPStatusError 读取响应体摘要并生成带状态码的错误。
func buildHTTPStatusError(prefix string, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("%s response is nil", strings.TrimSpace(prefix))
	}

	limitedBody, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return &HTTPStatusError{
			Prefix:        prefix,
			StatusCode:    resp.StatusCode,
			RetrySummary:  ProviderRetryAttemptSummary(resp),
			BodyReadError: err,
		}
	}
	return &HTTPStatusError{
		Prefix:       prefix,
		StatusCode:   resp.StatusCode,
		RetrySummary: ProviderRetryAttemptSummary(resp),
		Body:         strings.TrimSpace(string(limitedBody)),
	}
}
