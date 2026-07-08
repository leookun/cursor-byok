package mitm

import (
	"fmt"
	"io"
	"net/http"
	"sync"
)

// localRelayResponseWriter 定义了当前模块中的 localRelayResponseWriter 类型。
type localRelayResponseWriter struct {
	// header 表示当前声明中的 header。
	header http.Header
	// pipeWriter 表示当前声明中的 pipeWriter。
	pipeWriter *io.PipeWriter
	// ready 表示当前声明中的 ready。
	ready chan struct{}
	// wroteHeader 表示当前声明中的 wroteHeader。
	wroteHeader bool
	// statusCode 表示当前声明中的 statusCode。
	statusCode int
	// once 表示当前声明中的 once。
	once sync.Once
	// mu 表示当前声明中的 mu。
	mu sync.Mutex
}

// newLocalRelayResponseWriter 用于处理与 newLocalRelayResponseWriter 相关的逻辑。
func newLocalRelayResponseWriter(pipeWriter *io.PipeWriter) *localRelayResponseWriter {
	return &localRelayResponseWriter{
		header:     make(http.Header),
		pipeWriter: pipeWriter,
		ready:      make(chan struct{}),
		statusCode: http.StatusOK,
	}
}

// Header 用于处理与 Header 相关的逻辑。
func (w *localRelayResponseWriter) Header() http.Header {
	return w.header
}

// WriteHeader 用于处理与 WriteHeader 相关的逻辑。
func (w *localRelayResponseWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	if !w.wroteHeader {
		w.wroteHeader = true
		w.statusCode = statusCode
		w.once.Do(func() {
			close(w.ready)
		})
	}
	w.mu.Unlock()
}

// Write 用于处理与 Write 相关的逻辑。
func (w *localRelayResponseWriter) Write(body []byte) (int, error) {
	if !w.headerWritten() {
		w.WriteHeader(http.StatusOK)
	}
	return w.pipeWriter.Write(body)
}

// Flush 用于处理与 Flush 相关的逻辑。
func (w *localRelayResponseWriter) Flush() {
	if !w.headerWritten() {
		w.WriteHeader(http.StatusOK)
	}
}

// Finish 用于处理与 Finish 相关的逻辑。
func (w *localRelayResponseWriter) Finish(err error) {
	if err != nil && !w.headerWritten() {
		w.WriteHeader(http.StatusInternalServerError)
	}
	if !w.headerWritten() {
		w.WriteHeader(http.StatusOK)
	}
	if err != nil {
		_ = w.pipeWriter.CloseWithError(err)
		return
	}
	_ = w.pipeWriter.Close()
}

// Ready 用于处理与 Ready 相关的逻辑。
func (w *localRelayResponseWriter) Ready() <-chan struct{} {
	return w.ready
}

// Response 用于处理与 Response 相关的逻辑。
func (w *localRelayResponseWriter) Response(request *http.Request, body io.ReadCloser) *http.Response {
	w.mu.Lock()
	statusCode := w.statusCode
	headers := w.header.Clone()
	w.mu.Unlock()
	return &http.Response{
		StatusCode:    statusCode,
		Status:        fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header:        headers,
		Body:          body,
		ContentLength: -1,
		Request:       request,
	}
}

// headerWritten 用于处理与 headerWritten 相关的逻辑。
func (w *localRelayResponseWriter) headerWritten() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wroteHeader
}
