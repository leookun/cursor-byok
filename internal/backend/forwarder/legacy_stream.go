// legacy_stream.go 提供兼容 Cursor legacy RunSSE 响应头的 HTTP 包装器。
package forwarder

import (
	"context"
	"net/http"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"

	"connectrpc.com/connect"
)

const legacyRunSSEContentType = "text/event-stream"

// NewLegacyRunSSEHandler 构造一个对外表现为 text/event-stream 的 Connect ServerStream 处理器。
func NewLegacyRunSSEHandler(
	procedure string,
	implementation func(context.Context, *connect.Request[aiserverv1.BidiRequestId], *connect.ServerStream[agentv1.AgentServerMessage]) error,
	options ...connect.HandlerOption,
) http.Handler {
	inner := connect.NewServerStreamHandler(procedure, implementation, options...)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		inner.ServeHTTP(newLegacyRunSSEHeaderWriter(writer), request)
	})
}

// legacyRunSSEHeaderWriter 在真正写出 header 时覆盖 Content-Type。
type legacyRunSSEHeaderWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

// newLegacyRunSSEHeaderWriter 创建一个响应头包装器。
func newLegacyRunSSEHeaderWriter(writer http.ResponseWriter) *legacyRunSSEHeaderWriter {
	return &legacyRunSSEHeaderWriter{ResponseWriter: writer}
}

// WriteHeader 在输出状态码前补齐 legacy 所需响应头。
func (writer *legacyRunSSEHeaderWriter) WriteHeader(statusCode int) {
	writer.applyLegacyHeaders()
	writer.wroteHeader = true
	writer.ResponseWriter.WriteHeader(statusCode)
}

// Write 在首次写 body 时懒加载 header。
func (writer *legacyRunSSEHeaderWriter) Write(payload []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(payload)
}

// Flush 尝试把底层缓冲区立即刷新给客户端。
func (writer *legacyRunSSEHeaderWriter) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap 返回底层 ResponseWriter。
func (writer *legacyRunSSEHeaderWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

// applyLegacyHeaders 设置 Cursor legacy RunSSE 需要的响应头。
func (writer *legacyRunSSEHeaderWriter) applyLegacyHeaders() {
	header := writer.ResponseWriter.Header()
	header.Set("Content-Type", legacyRunSSEContentType)
	if header.Get("Cache-Control") == "" {
		header.Set("Cache-Control", "no-cache")
	}
}
