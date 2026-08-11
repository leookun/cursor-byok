package execbridge

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestApplyExecClientMessageReturnsContentAddressedReadImage(t *testing.T) {
	imageData := validReadTestPNG(t)
	wantBlobID := sha256.Sum256(imageData)
	result, err := NewBridge().ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_ReadResult{
			ReadResult: &agentv1.ReadResult{
				Result: &agentv1.ReadResult_Success{
					Success: &agentv1.ReadSuccess{
						Path:         "diagram.png",
						FileSize:     int64(len(imageData)),
						OutputBlobId: append([]byte(nil), wantBlobID[:]...),
						Output:       &agentv1.ReadSuccess_Data{Data: imageData},
					},
				},
			},
		},
	}, runtimecore.PendingExec{
		ExecKind:   "read",
		ToolCallID: "call-1",
		ArgsJSON:   []byte(`{"path":"diagram.png"}`),
	})
	if err != nil {
		t.Fatalf("ApplyExecClientMessage() error = %v", err)
	}
	if len(result.ContentBlobs) != 1 {
		t.Fatalf("content blob count = %d, want 1", len(result.ContentBlobs))
	}
	if !bytes.Equal(result.ContentBlobs[0].ID, wantBlobID[:]) || !bytes.Equal(result.ContentBlobs[0].Data, imageData) {
		t.Fatalf("content blob = %#v", result.ContentBlobs[0])
	}
	readSuccess := result.ToolCall.GetReadToolCall().GetResult().GetSuccess()
	if readSuccess == nil {
		t.Fatal("read tool result is not successful")
	}
	if !bytes.Equal(readSuccess.GetDataBlobId(), wantBlobID[:]) {
		t.Fatalf("data_blob_id = %x, want %x", readSuccess.GetDataBlobId(), wantBlobID)
	}
	if len(readSuccess.GetData()) != 0 {
		t.Fatalf("read tool result retained %d image bytes", len(readSuccess.GetData()))
	}
}

func TestApplyExecClientMessageUsesComputedImageBlobID(t *testing.T) {
	imageData := validReadTestPNG(t)
	wantBlobID := sha256.Sum256(imageData)
	result, err := NewBridge().ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_ReadResult{
			ReadResult: &agentv1.ReadResult{
				Result: &agentv1.ReadResult_Success{
					Success: &agentv1.ReadSuccess{
						Path:         "diagram.png",
						OutputBlobId: bytes.Repeat([]byte{0xff}, sha256.Size),
						Output:       &agentv1.ReadSuccess_Data{Data: imageData},
					},
				},
			},
		},
	}, runtimecore.PendingExec{ExecKind: "read", ToolCallID: "call-1"})
	if err != nil {
		t.Fatalf("ApplyExecClientMessage() error = %v", err)
	}
	if !bytes.Equal(result.ContentBlobs[0].ID, wantBlobID[:]) {
		t.Fatalf("content blob id = %x, want computed %x", result.ContentBlobs[0].ID, wantBlobID)
	}
}

func TestConvertReadResultKeepsTextAndLimitsUnsupportedBinary(t *testing.T) {
	textResult := convertReadResultToReadToolResult(&agentv1.ReadResult{
		Result: &agentv1.ReadResult_Success{
			Success: &agentv1.ReadSuccess{
				Path:   "notes.txt",
				Output: &agentv1.ReadSuccess_Content{Content: "hello"},
			},
		},
	})
	if got := textResult.GetSuccess().GetContent(); got != "hello" {
		t.Fatalf("text read content = %q, want hello", got)
	}

	largeBinary := bytes.Repeat([]byte{0xff}, readReplayBinaryLimit+1)
	binaryResult := convertReadResultToReadToolResult(&agentv1.ReadResult{
		Result: &agentv1.ReadResult_Success{
			Success: &agentv1.ReadSuccess{
				Path:   "archive.bin",
				Output: &agentv1.ReadSuccess_Data{Data: largeBinary},
			},
		},
	})
	binarySuccess := binaryResult.GetSuccess()
	if binarySuccess == nil || !binarySuccess.GetExceededLimit() {
		t.Fatal("large non-image binary was not limited")
	}
	if binarySuccess.GetData() != nil || binarySuccess.GetDataBlobId() != nil {
		t.Fatal("large non-image binary was retained")
	}
	if !strings.Contains(binarySuccess.GetContent(), "Read binary data") {
		t.Fatalf("large binary fallback = %q", binarySuccess.GetContent())
	}
}

func validReadTestPNG(t *testing.T) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, 2, 2))
	value.Set(0, 0, color.RGBA{R: 0x44, G: 0x88, B: 0xcc, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return encoded.Bytes()
}
