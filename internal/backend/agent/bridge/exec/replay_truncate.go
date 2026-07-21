package execbridge

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"cursor/gen/agentv1"
)

func normalizeReadResultForModel(result *agentv1.ReadResult) *agentv1.ReadResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.ReadResult)
	if !ok {
		return result
	}
	success := cloned.GetSuccess()
	if success == nil {
		return cloned
	}
	if output, ok := success.GetOutput().(*agentv1.ReadSuccess_Content); ok {
		normalized := normalizeReadContentLineEndingsToLF(output.Content)
		if normalized != output.Content {
			output.Content = normalized
			success.TotalLines = countLFReadLines(normalized)
		}
	}
	return cloned
}

func normalizeReadContentLineEndingsToLF(content string) string {
	if !strings.ContainsAny(content, "\r\n") {
		return content
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	return strings.ReplaceAll(normalized, "\r", "\n")
}

func countLFReadLines(content string) int32 {
	if content == "" {
		return 0
	}
	count := int32(strings.Count(content, "\n"))
	if !strings.HasSuffix(content, "\n") {
		count++
	}
	return count
}

func truncateReplayText(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateUTF8Bytes(text, limit)
		}
		kept := truncateUTF8Bytes(text, keep)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, len(kept), original)
		output := strings.TrimRight(kept, "\n") + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateReplayTextMiddle(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateUTF8Bytes(text, limit)
		}
		headLimit := keep / 2
		tailLimit := keep - headLimit
		head := truncateUTF8Bytes(text, headLimit)
		tail := truncateUTF8Suffix(text, tailLimit)
		kept := len(head) + len(tail)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", toolName, limit, kept, original)
		output := head + nextNotice + tail
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateReplayLine(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf(" [truncated: %s line exceeded %d bytes; showing %d of %d bytes]", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateUTF8Bytes(text, limit)
		}
		kept := truncateUTF8Bytes(text, keep)
		nextNotice := fmt.Sprintf(" [truncated: %s line exceeded %d bytes; showing %d of %d bytes]", toolName, limit, len(kept), original)
		output := kept + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateReplayLines(toolName string, text string, lineLimit int) string {
	if lineLimit <= 0 || text == "" {
		return text
	}
	parts := strings.SplitAfter(text, "\n")
	for index, part := range parts {
		newline := ""
		body := part
		if strings.HasSuffix(part, "\n") {
			body = strings.TrimSuffix(part, "\n")
			newline = "\n"
		}
		parts[index] = truncateReplayLine(toolName, body, lineLimit) + newline
	}
	return strings.Join(parts, "")
}

func truncateUTF8Bytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit > len(text) {
		limit = len(text)
	}
	truncated := text[:limit]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func truncateUTF8Suffix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	if start < 0 {
		start = 0
	}
	suffix := text[start:]
	for !utf8.ValidString(suffix) && start < len(text) {
		start++
		suffix = text[start:]
	}
	return suffix
}

func truncateByteSlice(value []byte, limit int) ([]byte, bool) {
	if limit <= 0 || len(value) <= limit {
		return value, false
	}
	return append([]byte(nil), value[:limit]...), true
}

func replayTruncationNotice(toolName string, limit int, kept int, original int) string {
	return fmt.Sprintf("[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, kept, original)
}

func truncateMcpToolResultForReplay(result *agentv1.McpToolResult) *agentv1.McpToolResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.McpToolResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	success := cloned.GetSuccess()
	notices := make([]string, 0, 3)
	if structured := success.GetStructuredContent(); structured != nil {
		if encoded, err := protojson.Marshal(structured); err == nil && len(encoded) > mcpReplayStructuredLimit {
			replacement, _ := structpb.NewStruct(map[string]any{
				"_truncated":          true,
				"original_json_bytes": float64(len(encoded)),
				"limit_bytes":         float64(mcpReplayStructuredLimit),
			})
			success.StructuredContent = replacement
			notices = append(notices, replayTruncationNotice("MCP structured_content", mcpReplayStructuredLimit, 0, len(encoded)))
		}
	}
	content := success.GetContent()
	if len(content) > mcpReplayContentItemLimit {
		notices = append(notices, fmt.Sprintf("[truncated: MCP content items exceeded %d items; showing %d of %d items]", mcpReplayContentItemLimit, mcpReplayContentItemLimit, len(content)))
		content = content[:mcpReplayContentItemLimit]
	}
	totalText := 0
	truncatedContent := make([]*agentv1.McpToolResultContentItem, 0, len(content)+len(notices))
	for _, item := range content {
		if item == nil {
			continue
		}
		next, ok := proto.Clone(item).(*agentv1.McpToolResultContentItem)
		if !ok {
			continue
		}
		if text := next.GetText(); text != nil {
			original := text.GetText()
			nextText := truncateReplayText("MCP content item", original, mcpReplayTextItemLimit)
			remaining := mcpReplayTextTotalLimit - totalText
			if remaining <= 0 {
				notices = append(notices, replayTruncationNotice("MCP text", mcpReplayTextTotalLimit, totalText, totalText+len(original)))
				continue
			}
			nextText = truncateReplayText("MCP text", nextText, remaining)
			text.Text = nextText
			totalText += len(nextText)
			truncatedContent = append(truncatedContent, next)
			continue
		}
		if image := next.GetImage(); image != nil && len(image.GetData()) > mcpReplayBinaryLimit {
			original := len(image.GetData())
			image.Data, _ = truncateByteSlice(image.GetData(), mcpReplayBinaryLimit)
			notices = append(notices, replayTruncationNotice("MCP image data", mcpReplayBinaryLimit, len(image.GetData()), original))
		}
		truncatedContent = append(truncatedContent, next)
	}
	for _, notice := range notices {
		truncatedContent = append(truncatedContent, &agentv1.McpToolResultContentItem{
			Content: &agentv1.McpToolResultContentItem_Text{
				Text: &agentv1.McpTextContent{Text: notice},
			},
		})
	}
	success.Content = truncatedContent
	return cloned
}

func truncateListMcpResourcesResultForReplay(result *agentv1.ListMcpResourcesExecResult) *agentv1.ListMcpResourcesExecResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.ListMcpResourcesExecResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	resources := cloned.GetSuccess().GetResources()
	if len(resources) > mcpResourcesReplayCount {
		resources = resources[:mcpResourcesReplayCount]
	}
	trimmed := make([]*agentv1.ListMcpResourcesExecResult_McpResource, 0, len(resources))
	for _, resource := range resources {
		if resource == nil {
			continue
		}
		next, ok := proto.Clone(resource).(*agentv1.ListMcpResourcesExecResult_McpResource)
		if !ok {
			continue
		}
		if next.Description != nil {
			description := truncateReplayText("MCP resource description", next.GetDescription(), mcpResourceDescriptionSize)
			next.Description = stringPtr(description)
		}
		trimmed = append(trimmed, next)
	}
	cloned.GetSuccess().Resources = trimmed
	for len(cloned.GetSuccess().Resources) > 0 {
		encoded, err := protojson.Marshal(cloned)
		if err != nil || len(encoded) <= mcpResourcesReplayLimit {
			break
		}
		cloned.GetSuccess().Resources = cloned.GetSuccess().Resources[:len(cloned.GetSuccess().Resources)-1]
	}
	if len(cloned.GetSuccess().Resources) < len(result.GetSuccess().GetResources()) {
		notice := replayTruncationNotice("ListMcpResources", mcpResourcesReplayLimit, len(cloned.GetSuccess().Resources), len(result.GetSuccess().GetResources()))
		cloned.GetSuccess().Resources = append(cloned.GetSuccess().Resources, &agentv1.ListMcpResourcesExecResult_McpResource{
			Uri:         "truncated:list-mcp-resources",
			Name:        stringPtr("truncated"),
			Description: stringPtr(notice),
		})
	}
	return cloned
}

func truncateReadMcpResourceResultForReplay(result *agentv1.ReadMcpResourceExecResult) *agentv1.ReadMcpResourceExecResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.ReadMcpResourceExecResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	success := cloned.GetSuccess()
	if text := success.GetText(); text != "" {
		success.Content = &agentv1.ReadMcpResourceSuccess_Text{
			Text: truncateReplayText("FetchMcpResource", text, mcpReplayTextTotalLimit),
		}
		return cloned
	}
	if blob := success.GetBlob(); len(blob) > mcpReplayBinaryLimit {
		success.Content = &agentv1.ReadMcpResourceSuccess_Text{
			Text: replayTruncationNotice("FetchMcpResource blob", mcpReplayBinaryLimit, 0, len(blob)),
		}
	}
	return cloned
}

func truncateGlobResultForReplay(result *agentv1.GrepResult) *agentv1.GrepResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.GrepResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	filesResult := firstGrepFilesResult(cloned.GetSuccess())
	if filesResult == nil {
		return cloned
	}
	files := append([]string(nil), filesResult.GetFiles()...)
	totalFiles := int(filesResult.GetTotalFiles())
	if totalFiles <= 0 {
		totalFiles = len(files)
	}
	if len(files) <= maxGlobReplayFiles {
		if filesResult.GetTotalFiles() <= 0 {
			filesResult.TotalFiles = int32(totalFiles)
		}
		return cloned
	}
	filesResult.Files = append([]string(nil), files[:maxGlobReplayFiles]...)
	filesResult.TotalFiles = int32(totalFiles)
	filesResult.ClientTruncated = true
	return cloned
}

func truncateGrepResultForReplay(result *agentv1.GrepResult) *agentv1.GrepResult {
	if result == nil {
		return nil
	}
	cloned, ok := proto.Clone(result).(*agentv1.GrepResult)
	if !ok || cloned == nil || cloned.GetSuccess() == nil {
		return result
	}
	budget := &grepReplayBudget{
		remainingContentBytes: grepReplayContentLimit,
		remainingMatches:      grepReplayTotalMatches,
	}
	success := cloned.GetSuccess()
	for _, union := range success.GetWorkspaceResults() {
		truncateGrepUnionResultForReplay(union, budget)
	}
	truncateGrepUnionResultForReplay(success.GetActiveEditorResult(), budget)
	return cloned
}

func truncateGrepUnionResultForReplay(union *agentv1.GrepUnionResult, budget *grepReplayBudget) {
	if union == nil || budget == nil {
		return
	}
	if content := union.GetContent(); content != nil {
		truncateGrepContentResultForReplay(content, budget)
		return
	}
	if files := union.GetFiles(); files != nil {
		total := int(files.GetTotalFiles())
		if total <= 0 {
			total = len(files.GetFiles())
		}
		if len(files.Files) > grepReplayListLimit {
			files.Files = append([]string(nil), files.Files[:grepReplayListLimit]...)
			files.ClientTruncated = true
		}
		if files.GetTotalFiles() <= 0 {
			files.TotalFiles = int32(total)
		}
		return
	}
	if counts := union.GetCount(); counts != nil {
		totalFiles := int(counts.GetTotalFiles())
		if totalFiles <= 0 {
			totalFiles = len(counts.GetCounts())
		}
		if len(counts.Counts) > grepReplayListLimit {
			counts.Counts = append([]*agentv1.GrepFileCount(nil), counts.Counts[:grepReplayListLimit]...)
			counts.ClientTruncated = true
		}
		if counts.GetTotalFiles() <= 0 {
			counts.TotalFiles = int32(totalFiles)
		}
	}
}

func truncateGrepContentResultForReplay(content *agentv1.GrepContentResult, budget *grepReplayBudget) {
	if content == nil || budget == nil {
		return
	}
	originalBytes := grepContentBytes(content.GetMatches())
	truncated := false
	newFiles := make([]*agentv1.GrepFileMatch, 0, len(content.GetMatches()))
	for _, fileMatch := range content.GetMatches() {
		if fileMatch == nil {
			continue
		}
		if budget.remainingMatches <= 0 || budget.remainingContentBytes <= 0 {
			truncated = true
			break
		}
		nextFile := &agentv1.GrepFileMatch{File: fileMatch.GetFile()}
		perFile := 0
		for _, match := range fileMatch.GetMatches() {
			if match == nil {
				continue
			}
			if perFile >= grepReplayMatchesPerFile || budget.remainingMatches <= 0 || budget.remainingContentBytes <= 0 {
				truncated = true
				break
			}
			nextMatch, ok := proto.Clone(match).(*agentv1.GrepContentMatch)
			if !ok {
				continue
			}
			originalContent := nextMatch.GetContent()
			nextMatch.Content = truncateReplayText("Grep match", originalContent, grepReplayMatchLimit)
			if nextMatch.Content != originalContent {
				nextMatch.ContentTruncated = true
				truncated = true
			}
			if len(nextMatch.Content) > budget.remainingContentBytes {
				nextMatch.Content = truncateReplayText("Grep", nextMatch.Content, budget.remainingContentBytes)
				nextMatch.ContentTruncated = true
				truncated = true
			}
			if strings.TrimSpace(nextMatch.Content) == "" {
				truncated = true
				break
			}
			budget.remainingContentBytes -= len(nextMatch.Content)
			budget.remainingMatches--
			perFile++
			nextFile.Matches = append(nextFile.Matches, nextMatch)
		}
		if len(nextFile.Matches) > 0 {
			newFiles = append(newFiles, nextFile)
		}
		if len(fileMatch.GetMatches()) > perFile {
			truncated = true
		}
	}
	if len(newFiles) < len(content.GetMatches()) {
		truncated = true
	}
	if truncated {
		content.ClientTruncated = true
		newFiles = addGrepContentTruncationNotice(newFiles, originalBytes)
	}
	content.Matches = newFiles
}

func addGrepContentTruncationNotice(files []*agentv1.GrepFileMatch, originalBytes int) []*agentv1.GrepFileMatch {
	used := grepContentBytes(files)
	notice := replayTruncationNotice("Grep", grepReplayContentLimit, used, originalBytes)
	match := &agentv1.GrepContentMatch{
		LineNumber:       0,
		Content:          notice,
		ContentTruncated: true,
		IsContextLine:    true,
	}
	if len(files) == 0 {
		return []*agentv1.GrepFileMatch{{File: "[truncated]", Matches: []*agentv1.GrepContentMatch{match}}}
	}
	files[len(files)-1].Matches = append(files[len(files)-1].Matches, match)
	return files
}

func grepContentBytes(files []*agentv1.GrepFileMatch) int {
	used := 0
	for _, file := range files {
		for _, match := range file.GetMatches() {
			used += len(match.GetContent())
		}
	}
	return used
}
