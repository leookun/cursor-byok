// provider_read_images.go 负责在 provider 请求边界按 blob 引用补全 Read 图片。
package forwarder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

type contentBlobReader interface {
	Get(id []byte) ([]byte, error)
}

type providerReadImageReference struct {
	blobID   []byte
	path     string
	fileSize uint32
}

// enrichProviderReadImages 只为本次 provider 请求加载图片，不修改 canonical history 投影。
func enrichProviderReadImages(messages []modeladapter.Message, conversation *ConversationFile, blobs contentBlobReader) ([]modeladapter.Message, error) {
	cloned := cloneProviderEnrichmentMessages(messages)
	references, err := collectProviderReadImageReferences(conversation)
	if err != nil {
		return nil, err
	}
	if len(references) == 0 {
		return cloned, nil
	}
	for index := range cloned {
		message := &cloned[index]
		if strings.TrimSpace(message.Role) != "tool" {
			continue
		}
		reference, ok := references[strings.TrimSpace(message.ToolCallID)]
		if !ok {
			continue
		}
		if blobs == nil {
			return nil, fmt.Errorf("provider read image blob store is not initialized")
		}
		data, err := blobs.Get(reference.blobID)
		if err != nil {
			return nil, fmt.Errorf("load read image blob for tool call %s: %w", message.ToolCallID, err)
		}
		mimeType := validatedProviderReadImageMIMEType(data)
		if mimeType == "" {
			return nil, fmt.Errorf("read image blob for tool call %s is not a supported image", message.ToolCallID)
		}
		summary := "Read image file: " + reference.path
		message.Content = summary
		message.ContentParts = []modeladapter.ContentPart{
			{Type: "text", Text: summary},
			{
				Type: "image",
				Image: &modeladapter.ImageContent{
					MIMEType: mimeType,
					Path:     reference.path,
					Data:     append([]byte(nil), data...),
				},
			},
		}
	}
	return cloned, nil
}

func collectProviderReadImageReferences(conversation *ConversationFile) (map[string]providerReadImageReference, error) {
	references := make(map[string]providerReadImageReference)
	if conversation == nil {
		return references, nil
	}
	for _, entry := range conversation.Entries {
		if strings.TrimSpace(entry.Kind) != "tool_result" {
			continue
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode read image tool result entry: %w", err)
		}
		if len(payload.ToolCall) == 0 {
			continue
		}
		toolCall := &agentv1.ToolCall{}
		if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
			return nil, fmt.Errorf("decode read image tool call: %w", err)
		}
		readToolCall := toolCall.GetReadToolCall()
		if readToolCall == nil || readToolCall.GetResult().GetSuccess() == nil {
			continue
		}
		success := readToolCall.GetResult().GetSuccess()
		blobID := success.GetDataBlobId()
		if len(blobID) == 0 {
			continue
		}
		toolCallID := strings.TrimSpace(firstNonEmpty(payload.ToolCallID, entry.ToolCallID))
		if toolCallID == "" {
			continue
		}
		reference := providerReadImageReference{
			blobID:   append([]byte(nil), blobID...),
			path:     firstNonEmpty(strings.TrimSpace(success.GetPath()), strings.TrimSpace(readToolCall.GetArgs().GetPath())),
			fileSize: success.GetFileSize(),
		}
		if existing, ok := references[toolCallID]; ok {
			if !bytes.Equal(existing.blobID, reference.blobID) || existing.path != reference.path || existing.fileSize != reference.fileSize {
				return nil, fmt.Errorf("conflicting read image references for tool call %s", toolCallID)
			}
			continue
		}
		references[toolCallID] = reference
	}
	return references, nil
}

func cloneProviderEnrichmentMessages(messages []modeladapter.Message) []modeladapter.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]modeladapter.Message, 0, len(messages))
	for _, message := range messages {
		item := cloneReplayModelMessage(message)
		if len(message.ContentParts) > 0 {
			item.ContentParts = make([]modeladapter.ContentPart, len(message.ContentParts))
			for index, part := range message.ContentParts {
				item.ContentParts[index] = part
				if part.Image != nil {
					imageCopy := *part.Image
					imageCopy.Data = append([]byte(nil), part.Image.Data...)
					item.ContentParts[index].Image = &imageCopy
				}
			}
		}
		cloned = append(cloned, item)
	}
	return cloned
}

func validatedProviderReadImageMIMEType(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	detected := strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	configuration, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || configuration.Width <= 0 || configuration.Height <= 0 {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png":
		if detected == "image/png" {
			return detected
		}
	case "jpeg":
		if detected == "image/jpeg" {
			return detected
		}
	case "gif":
		if detected == "image/gif" {
			return detected
		}
	}
	return ""
}
