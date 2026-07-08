package promptengine

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
