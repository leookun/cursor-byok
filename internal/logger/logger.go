package logger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"io"
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
		// Route stdlib log.* through the slog facade so all diagnostic
		// output (e.g. log.Printf("[Pet] foo") in pet/bridge packages)
		// appears as structured INFO entries with the same file output.
		RedirectStdLog()
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

// Error ?? error ????
func Error(msg string, args ...any) {
	Init()
	slog.Error(msg, args...)
}

// Warn ?? warn ????
func Warn(msg string, args ...any) {
	Init()
	slog.Warn(msg, args...)
}

// Infof ?????? info ????
func Infof(format string, args ...any) {
	Init()
	slog.Info(formatMessage(format, args...))
}

// Errorf ?????? error ????
func Errorf(format string, args ...any) {
	Init()
	slog.Error(formatMessage(format, args...))
}

// Warnf ?????? warn ????
func Warnf(format string, args ...any) {
	Init()
	slog.Warn(formatMessage(format, args...))
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

// LogFilePath 返回当前日志文件落盘路径（空字符串表示未落盘）。
// 供需要把标准库 log 重定向到同一文件的模块使用。
func LogFilePath() string {
	Init()
	return logFilePath
}

// stdlogWriter is an io.Writer that forwards every Write to slog via Info.
// Each Write produces one INFO slog record per line (multi-line payloads are
// split so each line becomes its own log entry, matching typical log.Printf
// semantics). Trailing partial lines are flushed as their own record.
type stdlogWriter struct{}

func (stdlogWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := len(p)
	// slog already adds a timestamp; strip the stdlib log prefix timestamp
	// (if any) so we don't duplicate. We keep the rest of the line verbatim
	// because callers embed structured prefixes like "[Pet]".
	rest := p
	for len(rest) > 0 {
		nl := bytes.IndexByte(rest, '\n')
		var line []byte
		if nl < 0 {
			line = rest
			rest = nil
		} else {
			line = rest[:nl]
			rest = rest[nl+1:]
		}
		msg := strings.TrimRight(string(line), "\r")
		if strings.TrimSpace(msg) != "" {
			slog.Info(msg)
		}
	}
	return total, nil
}

// origStdLogOutput holds the stdlib log output captured before RedirectStdLog
// swaps it, so RestoreStdLog can revert. It is read once on first redirect.
var (
	origStdLogOutput io.Writer
	origStdLogFlags  int
	stdLogMu         sync.Mutex
)

// RedirectStdLog 把标准库 log（log.Printf 等）的输出重定向到 slog facade，
// 使其作为 INFO 级结构化日志条目落盘。所有诊断日志（pet/bridge 等包中
// 仍使用 log.Printf 的位置）会自动经 slog 处理器输出。
//
// 与原始实现不同：不再直接写文件，而是经 slog，确保所有日志走同一通道
// （控制台 + 文件 + 未来的 hook），并具备级别/字段扩展能力。
func RedirectStdLog() {
	stdLogMu.Lock()
	defer stdLogMu.Unlock()
	// NOTE: do not call Init() here — Init() itself calls RedirectStdLog()
	// during its sync.Once critical section, which would deadlock. The
	// standalone (pre-Init) path simply routes stdlib log through whatever
	// the current slog.Default() is.
	if origStdLogOutput == nil {
		origStdLogOutput = stdlog.Default().Writer()
		origStdLogFlags = stdlog.Default().Flags()
	}
	stdlog.SetOutput(stdlogWriter{})
	stdlog.SetFlags(0)
}

// RestoreStdLog 撤销 RedirectStdLog 的影响，把标准库 log 输出恢复到原始
// 目标（通常是 stderr）。主要用于测试和受控关闭场景。
func RestoreStdLog() {
	stdLogMu.Lock()
	defer stdLogMu.Unlock()
	if origStdLogOutput == nil {
		// Nothing to restore; reset to stderr as a safe default.
		stdlog.SetOutput(os.Stderr)
		stdlog.SetFlags(stdlog.LstdFlags)
		return
	}
	stdlog.SetOutput(origStdLogOutput)
	stdlog.SetFlags(origStdLogFlags)
}
