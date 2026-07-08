package forwarder

import "strings"

// prepareWriteContentsForClient applies a narrow Windows compatibility shim for
// whole-file Write. Some Windows clients write CRLF payloads as CRCRLF on disk,
// so we send LF-only content when the target path is clearly a Windows path and
// the logical contents already use CRLF. The client's text-mode write then
// expands LF back to the intended CRLF on disk.
func prepareWriteContentsForClient(path string, contents string) string {
	if !looksLikeWindowsPath(path) || !strings.Contains(contents, "\r\n") {
		return contents
	}
	return normalizeCRLFToLF(contents)
}

// preparePatchEditWriteContentsForClient sends LF-only transport for PatchEdit.
// The client write path may expand LF to the platform newline, so sending CRLF
// can become CRCRLF on disk.
func preparePatchEditWriteContentsForClient(contents string) string {
	if !strings.Contains(contents, "\r") {
		return contents
	}
	return normalizeObservedLineEndingsToLF(contents)
}

// reconcilePostWriteObservedContent only corrects one known Windows anomaly:
// every line ending in the expected content was duplicated during write-back
// verification, which makes the file appear to contain a blank line between
// each real line. Some read paths normalize line endings to LF, so the
// comparison also checks the normalized representation.
func reconcilePostWriteObservedContent(expected string, observed string) (string, bool) {
	if observed == expected {
		return observed, false
	}
	if observed == systematicallyDoubledLineEndings(expected) {
		return expected, true
	}
	expectedNormalized := normalizeObservedLineEndingsToLF(expected)
	observedNormalized := normalizeObservedLineEndingsToLF(observed)
	if observedNormalized == expectedNormalized {
		return expected, true
	}
	if observedNormalized == systematicallyDoubledLineEndings(expectedNormalized) {
		return expected, true
	}
	return observed, false
}

func systematicallyDoubledLineEndings(content string) string {
	if content == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(content) * 2)
	for index := 0; index < len(content); {
		switch {
		case content[index] == '\r' && index+1 < len(content) && content[index+1] == '\n':
			builder.WriteString("\r\n\r\n")
			index += 2
		case content[index] == '\n':
			builder.WriteString("\n\n")
			index++
		default:
			builder.WriteByte(content[index])
			index++
		}
	}
	return builder.String()
}

func normalizeObservedLineEndingsToLF(content string) string {
	if content == "" {
		return ""
	}
	normalized := normalizeCRLFToLF(content)
	return strings.ReplaceAll(normalized, "\r", "\n")
}

func normalizeCRLFToLF(content string) string {
	if content == "" || !strings.Contains(content, "\r\n") {
		return content
	}
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func looksLikeWindowsPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if len(trimmed) >= 3 && isASCIILetter(trimmed[0]) && trimmed[1] == ':' {
		return trimmed[2] == '\\' || trimmed[2] == '/'
	}
	return strings.HasPrefix(trimmed, `\\`)
}

func isASCIILetter(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}
