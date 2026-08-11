package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestReadImageProjectionIsProviderOnlyAndIdempotent(t *testing.T) {
	imageData := validForwarderTestPNG(t)
	blobID := sha256.Sum256(imageData)
	store := NewContentBlobStore(t.TempDir())
	if err := store.Put(blobID[:], imageData); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	conversation := readImageConversation(t, blobID[:], len(imageData))

	projector := NewHistoryProjector()
	canonical, err := projector.ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	if len(canonical) != 2 {
		t.Fatalf("canonical message count = %d, want 2", len(canonical))
	}
	if len(canonical[1].ContentParts) != 0 {
		t.Fatalf("canonical replay contains image parts: %#v", canonical[1].ContentParts)
	}

	first, err := enrichProviderReadImages(canonical, conversation, store)
	if err != nil {
		t.Fatalf("first enrichment error = %v", err)
	}
	second, err := enrichProviderReadImages(canonical, conversation, store)
	if err != nil {
		t.Fatalf("second enrichment error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("provider enrichment is not idempotent\nfirst=%#v\nsecond=%#v", first, second)
	}
	reenriched, err := enrichProviderReadImages(first, conversation, store)
	if err != nil {
		t.Fatalf("re-enrichment error = %v", err)
	}
	if !reflect.DeepEqual(first, reenriched) {
		t.Fatalf("provider enrichment changed an already enriched projection\nfirst=%#v\nreenriched=%#v", first, reenriched)
	}
	assertProviderReadImageMessage(t, first[1], imageData)
	first[1].ContentParts[1].Image.Data[0] ^= 0xff
	if bytes.Equal(first[1].ContentParts[1].Image.Data, second[1].ContentParts[1].Image.Data) {
		t.Fatal("separate enrichments share mutable image bytes")
	}

	contextJSON, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}
	if bytes.Contains(contextJSON, imageData) || strings.Contains(string(contextJSON), base64.StdEncoding.EncodeToString(imageData)) {
		t.Fatal("canonical conversation contains raw image bytes")
	}
	checkpoint, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	checkpointJSON, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	if bytes.Contains(checkpointJSON, imageData) || strings.Contains(string(checkpointJSON), base64.StdEncoding.EncodeToString(imageData)) {
		t.Fatal("checkpoint contains raw image bytes")
	}
}

func TestProviderReadImageEnrichmentLeavesTextReadUnchanged(t *testing.T) {
	toolCall := &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Args: &agentv1.ReadToolArgs{Path: "notes.txt"},
				Result: &agentv1.ReadToolResult{
					Result: &agentv1.ReadToolResult_Success{
						Success: &agentv1.ReadToolSuccess{
							Path:   "notes.txt",
							Output: &agentv1.ReadToolSuccess_Content{Content: "hello"},
						},
					},
				},
			},
		},
	}
	encoded, err := protojson.Marshal(toolCall)
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	conversation := &ConversationFile{Entries: []HistoryEntry{
		newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"notes.txt"}`, "hello", "", encoded),
	}}
	messages := []modeladapter.Message{{Role: "tool", ToolCallID: "call-1", Name: "Read", Content: "hello"}}
	got, err := enrichProviderReadImages(messages, conversation, NewContentBlobStore(t.TempDir()))
	if err != nil {
		t.Fatalf("enrichProviderReadImages() error = %v", err)
	}
	if !reflect.DeepEqual(got, messages) {
		t.Fatalf("text read changed: got=%#v want=%#v", got, messages)
	}
}

func readImageConversation(t *testing.T, blobID []byte, fileSize int) *ConversationFile {
	t.Helper()
	toolCall := &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Args: &agentv1.ReadToolArgs{Path: "diagram.png"},
				Result: &agentv1.ReadToolResult{
					Result: &agentv1.ReadToolResult_Success{
						Success: &agentv1.ReadToolSuccess{
							FileSize: uint32(fileSize),
							Path:     "diagram.png",
							Output:   &agentv1.ReadToolSuccess_DataBlobId{DataBlobId: append([]byte(nil), blobID...)},
						},
					},
				},
			},
		},
	}
	encoded, err := protojson.Marshal(toolCall)
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	return &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"diagram.png"}`, "read binary bytes", "", encoded),
		},
	}
}

func assertProviderReadImageMessage(t *testing.T, message modeladapter.Message, imageData []byte) {
	t.Helper()
	if message.Role != "tool" || message.ToolCallID != "call-1" || message.Name != "Read" {
		t.Fatalf("tool message metadata = %#v", message)
	}
	if len(message.ContentParts) != 2 {
		t.Fatalf("content part count = %d, want text and image", len(message.ContentParts))
	}
	if message.ContentParts[0].Type != "text" || message.ContentParts[0].Text != message.Content {
		t.Fatalf("text content part = %#v", message.ContentParts[0])
	}
	imagePart := message.ContentParts[1]
	if imagePart.Type != "image" || imagePart.Image == nil {
		t.Fatalf("image content part = %#v", imagePart)
	}
	if imagePart.Image.MIMEType != "image/png" || imagePart.Image.Path != "diagram.png" || !bytes.Equal(imagePart.Image.Data, imageData) {
		t.Fatalf("image content = %#v", imagePart.Image)
	}
}

func validForwarderTestPNG(t *testing.T) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, 2, 2))
	value.Set(0, 0, color.RGBA{R: 0x44, G: 0x88, B: 0xcc, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return encoded.Bytes()
}
