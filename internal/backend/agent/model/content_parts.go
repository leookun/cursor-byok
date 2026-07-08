package modeladapter

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	contentPartTypeText  = "text"
	contentPartTypeImage = "image"
)

// ContentPart 表示一条消息中的结构化内容块。
type ContentPart struct {
	Type  string        `json:"type"`
	Text  string        `json:"text,omitempty"`
	Image *ImageContent `json:"image,omitempty"`
}

// ImageContent 表示消息中携带的一张图片。
type ImageContent struct {
	MIMEType string `json:"mime_type,omitempty"`
	Path     string `json:"path,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

func hasImageContentParts(parts []ContentPart) bool {
	for _, part := range parts {
		if normalizeContentPartType(part.Type) == contentPartTypeImage {
			return true
		}
	}
	return false
}

func collapseTextContentParts(parts []ContentPart) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalizeContentPartType(part.Type) != contentPartTypeText {
			continue
		}
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, "")
}

func normalizeContentPartType(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	switch trimmed {
	case "", contentPartTypeText:
		return contentPartTypeText
	case contentPartTypeImage:
		return contentPartTypeImage
	default:
		return trimmed
	}
}

func openAIContentValue(message Message) (any, error) {
	if !hasImageContentParts(message.ContentParts) {
		content := message.Content
		if strings.TrimSpace(content) == "" && len(message.ContentParts) > 0 {
			content = collapseTextContentParts(message.ContentParts)
		}
		return content, nil
	}

	parts := make([]map[string]any, 0, len(message.ContentParts)+1)
	if len(message.ContentParts) == 0 && strings.TrimSpace(message.Content) != "" {
		parts = append(parts, map[string]any{
			"type": contentPartTypeText,
			"text": message.Content,
		})
	}
	for _, part := range message.ContentParts {
		switch normalizeContentPartType(part.Type) {
		case contentPartTypeText:
			if part.Text == "" {
				continue
			}
			parts = append(parts, map[string]any{
				"type": contentPartTypeText,
				"text": part.Text,
			})
		case contentPartTypeImage:
			dataURL, err := imageContentDataURL(part.Image)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": dataURL,
				},
			})
		default:
			return nil, fmt.Errorf("unsupported openai content part type: %s", strings.TrimSpace(part.Type))
		}
	}
	if len(parts) == 0 {
		return message.Content, nil
	}
	return parts, nil
}

func anthropicContentBlocks(message Message) ([]map[string]any, error) {
	if len(message.ContentParts) == 0 {
		if strings.TrimSpace(message.Content) == "" {
			return nil, nil
		}
		return []map[string]any{{
			"type": contentPartTypeText,
			"text": message.Content,
		}}, nil
	}

	blocks := make([]map[string]any, 0, len(message.ContentParts))
	for _, part := range message.ContentParts {
		switch normalizeContentPartType(part.Type) {
		case contentPartTypeText:
			if part.Text == "" {
				continue
			}
			blocks = append(blocks, map[string]any{
				"type": contentPartTypeText,
				"text": part.Text,
			})
		case contentPartTypeImage:
			payload, mediaType, err := resolveImageContent(part.Image)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, map[string]any{
				"type": contentPartTypeImage,
				"source": map[string]any{
					"type":       "base64",
					"media_type": mediaType,
					"data":       base64.StdEncoding.EncodeToString(payload),
				},
			})
		default:
			return nil, fmt.Errorf("unsupported anthropic content part type: %s", strings.TrimSpace(part.Type))
		}
	}
	if len(blocks) == 0 && strings.TrimSpace(message.Content) != "" {
		blocks = append(blocks, map[string]any{
			"type": contentPartTypeText,
			"text": message.Content,
		})
	}
	return blocks, nil
}

func imageContentDataURL(image *ImageContent) (string, error) {
	payload, mediaType, err := resolveImageContent(image)
	if err != nil {
		return "", err
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(payload), nil
}

func resolveImageContent(image *ImageContent) ([]byte, string, error) {
	if image == nil {
		return nil, "", fmt.Errorf("image content is required")
	}
	if len(image.Data) > 0 {
		return image.Data, normalizeImageMIMEType(image.MIMEType, image.Path, image.Data), nil
	}
	path := strings.TrimSpace(image.Path)
	if path == "" {
		return nil, "", fmt.Errorf("image content is missing data and path")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read image content failed: %w", err)
	}
	return payload, normalizeImageMIMEType(image.MIMEType, path, payload), nil
}

func normalizeImageMIMEType(mimeType string, path string, payload []byte) string {
	trimmed := strings.TrimSpace(strings.ToLower(mimeType))
	if trimmed != "" {
		return trimmed
	}
	if len(payload) > 0 {
		detected := strings.TrimSpace(strings.ToLower(http.DetectContentType(payload)))
		if strings.HasPrefix(detected, "image/") {
			return detected
		}
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}
