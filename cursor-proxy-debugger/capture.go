package proxydebugger

import (
	"bytes"
	"encoding/hex"
	"io"
	"sync"
)

type captureReadCloser struct {
	source    io.ReadCloser
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	size      int64
	truncated bool
	done      bool
	onChunk   func([]byte)
	onDone    func(captured []byte, size int64, truncated bool, readErr error)
}

func newCaptureReadCloser(
	source io.ReadCloser,
	limit int,
	onChunk func([]byte),
	onDone func(captured []byte, size int64, truncated bool, readErr error),
) *captureReadCloser {
	return &captureReadCloser{
		source:  source,
		limit:   limit,
		onChunk: onChunk,
		onDone:  onDone,
	}
}

func (reader *captureReadCloser) Read(payload []byte) (int, error) {
	read, err := reader.source.Read(payload)
	if read > 0 {
		chunk := payload[:read]
		reader.mu.Lock()
		reader.size += int64(read)
		remaining := reader.limit - reader.buffer.Len()
		if remaining > 0 {
			captured := read
			if captured > remaining {
				captured = remaining
			}
			_, _ = reader.buffer.Write(chunk[:captured])
		}
		if reader.buffer.Len() >= reader.limit && reader.size > int64(reader.buffer.Len()) {
			reader.truncated = true
		}
		reader.mu.Unlock()
		if reader.onChunk != nil {
			reader.onChunk(append([]byte(nil), chunk...))
		}
	}
	if err != nil {
		reader.finish(err)
	}
	return read, err
}

func (reader *captureReadCloser) Close() error {
	err := reader.source.Close()
	reader.finish(err)
	return err
}

func (reader *captureReadCloser) finish(readErr error) {
	reader.mu.Lock()
	if reader.done {
		reader.mu.Unlock()
		return
	}
	reader.done = true
	captured := append([]byte(nil), reader.buffer.Bytes()...)
	size := reader.size
	truncated := reader.truncated
	reader.mu.Unlock()
	if reader.onDone != nil {
		reader.onDone(captured, size, truncated, readErr)
	}
}

func rawHex(payload []byte) string {
	return hex.EncodeToString(payload)
}
