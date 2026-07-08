package logger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor/internal/appdata"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

const (
	appLogMaxLines        = 10000
	appLogTrimReserveLine = 1000
)

var (
	initOnce    sync.Once
	logFile     *os.File
	logFilePath string
)

// Init 配置默认 slog logger，并把标准库 log 接到同一输出。
func Init() {
	initOnce.Do(func() {
		handlers := []slog.Handler{tint.NewHandler(colorable.NewColorableStdout(), &tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: "15:04:05.000",
			NoColor:    disableColor(),
		})}
		fileHandler, path, fileErr := buildFileHandler()
		if fileErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[logger] 初始化日志文件失败: %v\n", fileErr)
		} else if fileHandler != nil {
			handlers = append(handlers, fileHandler)
			logFilePath = path
		}
		handler := handlers[0]
		if len(handlers) > 1 {
			handler = &multiHandler{handlers: handlers}
		}
		slog.SetDefault(slog.New(handler))
		stdlog.SetFlags(0)
		if logFilePath != "" {
			slog.Info("应用日志已写入文件", "path", logFilePath, "pid", os.Getpid())
		}
	})
}

// Info 输出 info 级日志。
func Info(msg string, args ...any) {
	Init()
	slog.Info(msg, args...)
}

// Error 输出 error 级日志。
func Error(msg string, args ...any) {
	Init()
	slog.Error(msg, args...)
}

// Infof 输出格式化的 info 级日志。
func Infof(format string, args ...any) {
	Init()
	slog.Info(formatMessage(format, args...))
}

// Errorf 输出格式化的 error 级日志。
func Errorf(format string, args ...any) {
	Init()
	slog.Error(formatMessage(format, args...))
}

func formatMessage(format string, args ...any) string {
	if len(args) == 0 {
		return strings.TrimSpace(format)
	}
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

func disableColor() bool {
	if strings.TrimSpace(os.Getenv("NO_COLOR")) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return true
	}
	fd := os.Stdout.Fd()
	return !isatty.IsTerminal(fd) && !isatty.IsCygwinTerminal(fd)
}

func buildFileHandler() (slog.Handler, string, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return nil, "", err
	}
	path := filepath.Join(appdata.LogsRootPath(), "app.log")
	writer, err := newLineWindowFileWriter(path, appLogMaxLines, appLogTrimReserveLine)
	if err != nil {
		return nil, "", err
	}
	logFile = writer.file
	return tint.NewHandler(writer, &tint.Options{
		Level:      slog.LevelInfo,
		TimeFormat: time.RFC3339,
		NoColor:    true,
	}), path, nil
}

type lineWindowFileWriter struct {
	mu          sync.Mutex
	path        string
	file        *os.File
	lineCount   int
	openLine    bool
	maxLines    int
	trimReserve int
}

func newLineWindowFileWriter(path string, maxLines int, trimReserve int) (*lineWindowFileWriter, error) {
	writer := &lineWindowFileWriter{
		path:        path,
		maxLines:    maxLines,
		trimReserve: trimReserve,
	}
	if err := writer.openLocked(); err != nil {
		return nil, err
	}
	lineCount, openLine, err := countFileLines(path)
	if err != nil {
		_ = writer.file.Close()
		return nil, err
	}
	writer.lineCount = lineCount
	writer.openLine = openLine
	if maxLines > 0 && lineCount > maxLines {
		if err := writer.trimToLastLinesLocked(maxLines); err != nil {
			_ = writer.file.Close()
			return nil, err
		}
	}
	return writer, nil
}

func (writer *lineWindowFileWriter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer == nil || writer.file == nil {
		return 0, fmt.Errorf("log file writer is not initialized")
	}
	newLines := writer.countIncomingLines(payload)
	if writer.maxLines > 0 && newLines > 0 && writer.lineCount+newLines > writer.maxLines {
		target := writer.maxLines - newLines - writer.trimReserve
		if target < 0 {
			target = writer.maxLines - newLines
		}
		if target < 0 {
			target = 0
		}
		if err := writer.trimToLastLinesLocked(target); err != nil {
			return 0, err
		}
	}
	written, err := writer.file.Write(payload)
	writer.lineCount += writer.countIncomingLines(payload[:written])
	if written > 0 {
		writer.openLine = payload[written-1] != '\n'
	}
	return written, err
}

func (writer *lineWindowFileWriter) openLocked() error {
	file, err := os.OpenFile(writer.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	writer.file = file
	logFile = file
	return nil
}

func (writer *lineWindowFileWriter) trimToLastLinesLocked(targetLines int) error {
	if writer.file != nil {
		if err := writer.file.Close(); err != nil {
			return err
		}
		writer.file = nil
	}
	payload, err := os.ReadFile(writer.path)
	if err != nil {
		if reopenErr := writer.openLocked(); reopenErr != nil {
			return errors.Join(err, reopenErr)
		}
		return err
	}
	trimmed, lineCount := lastLinesBytes(payload, targetLines)
	if err := os.WriteFile(writer.path, trimmed, 0o644); err != nil {
		if reopenErr := writer.openLocked(); reopenErr != nil {
			return errors.Join(err, reopenErr)
		}
		return err
	}
	if err := writer.openLocked(); err != nil {
		return err
	}
	writer.lineCount = lineCount
	writer.openLine = len(trimmed) > 0 && trimmed[len(trimmed)-1] != '\n'
	return nil
}

func (writer *lineWindowFileWriter) countIncomingLines(payload []byte) int {
	if len(payload) == 0 {
		return 0
	}
	newlineCount := bytes.Count(payload, []byte{'\n'})
	endsWithNewline := payload[len(payload)-1] == '\n'
	delta := newlineCount
	switch {
	case writer.openLine && endsWithNewline:
		delta--
	case !writer.openLine && !endsWithNewline:
		delta++
	}
	if delta < 0 {
		return 0
	}
	return delta
}

func countFileLines(path string) (int, bool, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return countBytesLines(payload), len(payload) > 0 && payload[len(payload)-1] != '\n', nil
}

func countBytesLines(payload []byte) int {
	if len(payload) == 0 {
		return 0
	}
	count := bytes.Count(payload, []byte{'\n'})
	if payload[len(payload)-1] != '\n' {
		count++
	}
	return count
}

func lastLinesBytes(payload []byte, targetLines int) ([]byte, int) {
	if len(payload) == 0 || targetLines <= 0 {
		return nil, 0
	}
	lineCount := countBytesLines(payload)
	if lineCount <= targetLines {
		return append([]byte(nil), payload...), lineCount
	}
	dropLines := lineCount - targetLines
	offset := 0
	for i := 0; i < dropLines; i++ {
		next := bytes.IndexByte(payload[offset:], '\n')
		if next < 0 {
			return nil, 0
		}
		offset += next + 1
	}
	trimmed := append([]byte(nil), payload[offset:]...)
	return trimmed, countBytesLines(trimmed)
}

type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	var handleErr error
	for _, handler := range h.handlers {
		if !handler.Enabled(ctx, record.Level) {
			continue
		}
		if err := handler.Handle(ctx, record.Clone()); err != nil {
			handleErr = errors.Join(handleErr, err)
		}
	}
	return handleErr
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithAttrs(attrs))
	}
	return &multiHandler{handlers: next}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithGroup(name))
	}
	return &multiHandler{handlers: next}
}
