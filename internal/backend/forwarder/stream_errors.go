// stream_errors.go extracts error builder functions from service.go (TD-002).
// Contains: buildTerminalStreamError, buildRunSSEProviderError,
// buildRunSSECustomError, buildRunSSEStructuredErrorWithDetail.
package forwarder

import (
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"cursor/gen/aiserverv1"
)

// buildTerminalStreamError 把 broker 终态事件转换成 Connect endstream 错误。
func buildTerminalStreamError(event StreamEvent) error {
	if !event.End {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(event.TerminalErrorCode)) {
	case "":
		return nil
	case "canceled":
		return connect.NewError(connect.CodeCanceled, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "invalid_argument":
		return connect.NewError(connect.CodeInvalidArgument, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "failed_precondition":
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case compactionOverflowTerminalCode:
		return buildRunSSECustomError(connect.CodeInvalidArgument, "Context Too Large After Compaction", errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "provider_error":
		return buildRunSSEProviderError(errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	default:
		return connect.NewError(connect.CodeUnknown, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	}
}

// buildRunSSEProviderError 构造 provider 专用的 RunSSE 错误包。
func buildRunSSEProviderError(cause error) error {
	return buildRunSSEStructuredErrorWithDetail(
		connect.CodeUnavailable,
		"Server Error",
		"",
		cause,
		aiserverv1.ErrorDetails_ERROR_PROVIDER_ERROR,
		false,
	)
}

// buildRunSSECustomError 构造带有 CustomErrorDetails 的 RunSSE 结构化错误。
func buildRunSSECustomError(code connect.Code, title string, cause error) error {
	return buildRunSSEStructuredErrorWithDetail(code, title, "", cause, aiserverv1.ErrorDetails_ERROR_CUSTOM_MESSAGE, false)
}

// buildRunSSEStructuredError 统一构造带有 ErrorDetails 的 Connect endstream 错误。
func buildRunSSEStructuredErrorWithDetail(code connect.Code, title string, detailText string, cause error, errorKind aiserverv1.ErrorDetails_Error, expected bool) error {
	if cause == nil {
		cause = fmt.Errorf("unknown RunSSE error")
	}
	trimmedDetail := strings.TrimSpace(detailText)
	if trimmedDetail == "" {
		trimmedDetail = cause.Error()
	}
	isRetryable := true
	allowUnsafeCommandLinks := true
	showRequestID := true
	shouldShowImmediateError := false
	isExpected := expected
	payload := &aiserverv1.ErrorDetails{
		Error: errorKind,
		Details: &aiserverv1.CustomErrorDetails{
			Title:       strings.TrimSpace(title),
			Detail:      trimmedDetail,
			IsRetryable: &isRetryable,
			AllowCommandLinksPotentiallyUnsafePleaseOnlyUseForHandwrittenTrustedMarkdown: &allowUnsafeCommandLinks,
			ShowRequestId:            &showRequestID,
			ShouldShowImmediateError: &shouldShowImmediateError,
		},
		IsExpected: &isExpected,
	}
	result := connect.NewError(code, cause)
	detail, detailErr := connect.NewErrorDetail(payload)
	if detailErr == nil {
		result.AddDetail(detail)
	}
	return result
}