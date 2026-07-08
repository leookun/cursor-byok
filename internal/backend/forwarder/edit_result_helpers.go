package forwarder

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sergi/go-diff/diffmatchpatch"
	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

const editReadBinaryContentLimit = 32 * 1024

type editComputation struct {
	BeforeContent string
	AfterContent  string
	DiffString    string
	LinesAdded    int32
	LinesRemoved  int32
	Message       string
}

type editResultPayload struct {
	BeforeContent string
	AfterContent  string
	DiffString    string
	LinesAdded    int32
	LinesRemoved  int32
	Message       string
}

type editMatch struct {
	Start int
	End   int
}

type normalizedEditContent struct {
	Text            string
	OriginalOffsets []int
}

func computeEditDiff(beforeContent string, afterContent string) (string, int32, int32) {
	dmp := diffmatchpatch.New()
	charsBefore, charsAfter, lineArray := dmp.DiffLinesToChars(beforeContent, afterContent)
	diffs := dmp.DiffMain(charsBefore, charsAfter, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)

	linesAdded := int32(0)
	linesRemoved := int32(0)
	for _, diff := range diffs {
		switch diff.Type {
		case diffmatchpatch.DiffInsert:
			linesAdded += countChangedLines(diff.Text)
		case diffmatchpatch.DiffDelete:
			linesRemoved += countChangedLines(diff.Text)
		}
	}
	return dmp.PatchToText(dmp.PatchMake(beforeContent, diffs)), linesAdded, linesRemoved
}

func normalizeEditContentLineEndings(content string) normalizedEditContent {
	var builder strings.Builder
	builder.Grow(len(content))
	offsets := make([]int, 1, len(content)+1)
	offsets[0] = 0
	for index := 0; index < len(content); {
		if content[index] == '\r' && index+1 < len(content) && content[index+1] == '\n' {
			builder.WriteByte('\n')
			index += 2
			offsets = append(offsets, index)
			continue
		}
		if content[index] == '\r' {
			builder.WriteByte('\n')
			index++
			offsets = append(offsets, index)
			continue
		}
		builder.WriteByte(content[index])
		index++
		offsets = append(offsets, index)
	}
	return normalizedEditContent{
		Text:            builder.String(),
		OriginalOffsets: offsets,
	}
}

func normalizeLineEndingsToLF(value string) string {
	if !strings.ContainsAny(value, "\r\n") {
		return value
	}
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(normalized, "\r", "\n")
}

func detectPreferredFileLineEnding(content string) string {
	return dominantLineEnding(content, "\n")
}

func preferredMatchLineEnding(content string, match editMatch, defaultLineEnding string) string {
	return dominantLineEnding(content[match.Start:match.End], defaultLineEnding)
}

func normalizeReplacementLineEndings(newString string, lineEnding string) string {
	if !strings.ContainsAny(newString, "\r\n") {
		return newString
	}
	normalized := normalizeLineEndingsToLF(newString)
	switch lineEnding {
	case "\r\n":
		return strings.ReplaceAll(normalized, "\n", "\r\n")
	case "\r":
		return strings.ReplaceAll(normalized, "\n", "\r")
	default:
		return normalized
	}
}

func dominantLineEnding(content string, fallback string) string {
	if fallback == "" {
		fallback = "\n"
	}
	counts := map[string]int{
		"\n":   0,
		"\r\n": 0,
		"\r":   0,
	}
	first := ""
	for index := 0; index < len(content); {
		switch {
		case content[index] == '\r' && index+1 < len(content) && content[index+1] == '\n':
			counts["\r\n"]++
			if first == "" {
				first = "\r\n"
			}
			index += 2
		case content[index] == '\r':
			counts["\r"]++
			if first == "" {
				first = "\r"
			}
			index++
		case content[index] == '\n':
			counts["\n"]++
			if first == "" {
				first = "\n"
			}
			index++
		default:
			index++
		}
	}
	best := ""
	bestCount := 0
	for _, candidate := range []string{"\r\n", "\n", "\r"} {
		if counts[candidate] > bestCount {
			best = candidate
			bestCount = counts[candidate]
		}
	}
	if bestCount == 0 {
		return fallback
	}
	for _, candidate := range []string{"\r\n", "\n", "\r"} {
		if counts[candidate] == bestCount && candidate == first {
			return candidate
		}
	}
	return best
}

func countChangedLines(text string) int32 {
	if text == "" {
		return 0
	}
	count := int32(strings.Count(text, "\n"))
	if !strings.HasSuffix(text, "\n") {
		count++
	}
	return count
}

func buildCompletedEditToolCall(path string, result *agentv1.EditResult) *agentv1.ToolCall {
	args := &agentv1.EditArgs{Path: strings.TrimSpace(path)}
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{
				Args:   args,
				Result: result,
			},
		},
	}
}

func buildSuccessfulEditResult(path string, beforeContent string, afterContent string, diffString string, linesAdded int32, linesRemoved int32, message string) *agentv1.EditResult {
	success := &agentv1.EditSuccess{
		Path:                 strings.TrimSpace(path),
		AfterFullFileContent: afterContent,
	}
	if beforeContent != "" {
		success.BeforeFullFileContent = stringPtr(beforeContent)
	}
	if diffString != "" {
		success.DiffString = stringPtr(diffString)
	}
	if linesAdded != 0 {
		success.LinesAdded = int32Ptr(linesAdded)
	}
	if linesRemoved != 0 {
		success.LinesRemoved = int32Ptr(linesRemoved)
	}
	if message != "" {
		success.Message = stringPtr(message)
	}
	return &agentv1.EditResult{
		Result: &agentv1.EditResult_Success{Success: success},
	}
}

func buildFinalEditSuccessResult(path string, afterContent string, payload editResultPayload) *agentv1.EditResult {
	diffString := payload.DiffString
	linesAdded := payload.LinesAdded
	linesRemoved := payload.LinesRemoved
	if afterContent != payload.AfterContent {
		diffString, linesAdded, linesRemoved = computeEditDiff(payload.BeforeContent, afterContent)
	}
	return buildSuccessfulEditResult(path, payload.BeforeContent, afterContent, diffString, linesAdded, linesRemoved, payload.Message)
}

func buildEditErrorResult(path string, errorMessage string) *agentv1.EditResult {
	trimmedPath := strings.TrimSpace(path)
	trimmedError := strings.TrimSpace(errorMessage)
	return &agentv1.EditResult{
		Result: &agentv1.EditResult_Error{
			Error: &agentv1.EditError{
				Path:              trimmedPath,
				Error:             trimmedError,
				ModelVisibleError: stringPtr(trimmedError),
			},
		},
	}
}

func decodeReadDataAsEditableText(data []byte) (string, bool, string) {
	if len(data) > editReadBinaryContentLimit {
		return "", false, fmt.Sprintf("Read returned binary data larger than %d bytes", editReadBinaryContentLimit)
	}
	if !utf8.Valid(data) {
		return "", false, "Read returned binary data that is not valid UTF-8 text"
	}
	return string(data), true, ""
}

func buildEditResultFromReadResult(path string, result *agentv1.ReadResult) *agentv1.EditResult {
	if result == nil {
		return buildEditErrorResult(path, "read result missing")
	}
	switch item := result.GetResult().(type) {
	case *agentv1.ReadResult_FileNotFound:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_FileNotFound{
				FileNotFound: &agentv1.EditFileNotFound{Path: firstNonEmpty(item.FileNotFound.GetPath(), path)},
			},
		}
	case *agentv1.ReadResult_PermissionDenied:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_ReadPermissionDenied{
				ReadPermissionDenied: &agentv1.EditReadPermissionDenied{Path: firstNonEmpty(item.PermissionDenied.GetPath(), path)},
			},
		}
	case *agentv1.ReadResult_Rejected:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_Rejected{
				Rejected: &agentv1.EditRejected{
					Path:   firstNonEmpty(item.Rejected.GetPath(), path),
					Reason: item.Rejected.GetReason(),
				},
			},
		}
	case *agentv1.ReadResult_InvalidFile:
		return buildEditErrorResult(firstNonEmpty(item.InvalidFile.GetPath(), path), item.InvalidFile.GetReason())
	case *agentv1.ReadResult_Error:
		return buildEditErrorResult(firstNonEmpty(item.Error.GetPath(), path), item.Error.GetError())
	case *agentv1.ReadResult_Success:
		return buildEditErrorResult(firstNonEmpty(item.Success.GetPath(), path), "read result did not include editable text content")
	default:
		return buildEditErrorResult(path, "unknown read result")
	}
}

func buildEditResultFromWriteResult(path string, result *agentv1.WriteResult) *agentv1.EditResult {
	if result == nil {
		return buildEditErrorResult(path, "write result missing")
	}
	switch item := result.GetResult().(type) {
	case *agentv1.WriteResult_PermissionDenied:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_WritePermissionDenied{
				WritePermissionDenied: &agentv1.EditWritePermissionDenied{
					Path:  firstNonEmpty(item.PermissionDenied.GetPath(), path),
					Error: item.PermissionDenied.GetError(),
				},
			},
		}
	case *agentv1.WriteResult_Rejected:
		return &agentv1.EditResult{
			Result: &agentv1.EditResult_Rejected{
				Rejected: &agentv1.EditRejected{
					Path:   firstNonEmpty(item.Rejected.GetPath(), path),
					Reason: item.Rejected.GetReason(),
				},
			},
		}
	case *agentv1.WriteResult_NoSpace:
		return buildEditErrorResult(firstNonEmpty(item.NoSpace.GetPath(), path), "no space left")
	case *agentv1.WriteResult_Error:
		return buildEditErrorResult(firstNonEmpty(item.Error.GetPath(), path), item.Error.GetError())
	case *agentv1.WriteResult_Success:
		afterContent := item.Success.GetFileContentAfterWrite()
		return buildSuccessfulEditResult(firstNonEmpty(item.Success.GetPath(), path), "", afterContent, "", 0, 0, "")
	default:
		return buildEditErrorResult(path, "unknown write result")
	}
}

func extractReadContentForEdit(result *agentv1.ReadResult) (string, bool) {
	success := result.GetSuccess()
	if success == nil {
		return "", false
	}
	switch output := success.GetOutput().(type) {
	case *agentv1.ReadSuccess_Content:
		return normalizeLineEndingsToLF(output.Content), true
	case *agentv1.ReadSuccess_Data:
		text, ok, _ := decodeReadDataAsEditableText(output.Data)
		return normalizeLineEndingsToLF(text), ok
	}
	if success.GetOutputBlobId() != nil {
		return "", false
	}
	return "", true
}

func summarizeEditResult(result *agentv1.EditResult) string {
	if result == nil {
		return "edit result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.EditResult_Success:
		return item.Success.GetAfterFullFileContent()
	case *agentv1.EditResult_FileNotFound:
		return fmt.Sprintf("file not found: %s", item.FileNotFound.GetPath())
	case *agentv1.EditResult_ReadPermissionDenied:
		return fmt.Sprintf("permission denied: %s", item.ReadPermissionDenied.GetPath())
	case *agentv1.EditResult_WritePermissionDenied:
		return item.WritePermissionDenied.GetError()
	case *agentv1.EditResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.EditResult_Error:
		return item.Error.GetError()
	default:
		return "unknown edit result"
	}
}

func summarizeEditResultWithoutPath(result *agentv1.EditResult) string {
	if result == nil {
		return "edit result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.EditResult_Success:
		return item.Success.GetAfterFullFileContent()
	case *agentv1.EditResult_FileNotFound:
		return "file not found"
	case *agentv1.EditResult_ReadPermissionDenied:
		return "permission denied"
	case *agentv1.EditResult_WritePermissionDenied:
		return item.WritePermissionDenied.GetError()
	case *agentv1.EditResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.EditResult_Error:
		return item.Error.GetError()
	default:
		return "unknown edit result"
	}
}

func editResultWithoutPath(result *agentv1.EditResult) *agentv1.EditResult {
	if result == nil {
		return nil
	}
	switch item := result.GetResult().(type) {
	case *agentv1.EditResult_Success:
		return &agentv1.EditResult{Result: &agentv1.EditResult_Success{Success: &agentv1.EditSuccess{
			AfterFullFileContent:  item.Success.GetAfterFullFileContent(),
			BeforeFullFileContent: item.Success.BeforeFullFileContent,
			DiffString:            item.Success.DiffString,
			LinesAdded:            item.Success.LinesAdded,
			LinesRemoved:          item.Success.LinesRemoved,
			Message:               item.Success.Message,
		}}}
	case *agentv1.EditResult_FileNotFound:
		return &agentv1.EditResult{Result: &agentv1.EditResult_FileNotFound{FileNotFound: &agentv1.EditFileNotFound{}}}
	case *agentv1.EditResult_ReadPermissionDenied:
		return &agentv1.EditResult{Result: &agentv1.EditResult_ReadPermissionDenied{ReadPermissionDenied: &agentv1.EditReadPermissionDenied{}}}
	case *agentv1.EditResult_WritePermissionDenied:
		return &agentv1.EditResult{Result: &agentv1.EditResult_WritePermissionDenied{WritePermissionDenied: &agentv1.EditWritePermissionDenied{Error: item.WritePermissionDenied.GetError()}}}
	case *agentv1.EditResult_Rejected:
		return &agentv1.EditResult{Result: &agentv1.EditResult_Rejected{Rejected: &agentv1.EditRejected{Reason: item.Rejected.GetReason()}}}
	case *agentv1.EditResult_Error:
		return &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{
			Error:             item.Error.GetError(),
			ModelVisibleError: item.Error.ModelVisibleError,
		}}}
	default:
		return result
	}
}

func summarizeStructuredEditResult(result *agentv1.EditResult) string {
	if result == nil {
		return "edit result missing"
	}
	if _, ok := result.GetResult().(*agentv1.EditResult_Success); ok {
		encoded, err := protojson.Marshal(result)
		if err == nil {
			return string(encoded)
		}
	}
	return summarizeEditResult(result)
}

func hiddenWriteControlError(message *agentv1.ExecClientControlMessage) string {
	if message == nil {
		return "write operation failed"
	}
	switch item := message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_Throw:
		return firstNonEmpty(strings.TrimSpace(item.Throw.GetError()), "write operation failed")
	case *agentv1.ExecClientControlMessage_StreamClose:
		return "write operation closed unexpectedly"
	default:
		return "write operation failed"
	}
}

func readJSONStringAny(args map[string]any, keys ...string) (string, bool, bool) {
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		typed, ok := value.(string)
		if !ok {
			return "", true, false
		}
		return typed, true, true
	}
	return "", false, false
}

func readJSONIntAny(args map[string]any, keys ...string) (int, bool, bool) {
	value, found, err := runtimecore.ReadIntArg(args, keys...)
	if err != nil {
		return 0, true, false
	}
	return value, found, found
}

func readJSONBoolAny(args map[string]any, keys ...string) (bool, bool, bool) {
	for _, key := range keys {
		value, ok := args[key]
		if !ok {
			continue
		}
		typed, ok := value.(bool)
		if !ok {
			return false, true, false
		}
		return typed, true, true
	}
	return false, false, false
}

func int32Ptr(value int32) *int32 {
	return &value
}
