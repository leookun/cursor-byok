package proxydebugger

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"

	"google.golang.org/protobuf/proto"
)

func TestDecodeForkTrafficRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		message  proto.Message
		kind     string
		contains []string
	}{
		{
			name: "fork background composer",
			path: forkBackgroundComposerPath,
			message: &aiserverv1.ForkBackgroundComposerRequest{
				SourceBcId: "bc-source",
				Mode:       aiserverv1.ForkBackgroundComposerMode_FORK_BACKGROUND_COMPOSER_MODE_CONVERSATION,
				Name:       proto.String("forked chat"),
				TurnCount:  proto.Uint32(4),
			},
			kind:     "fork_background_composer_request",
			contains: []string{`"source_bc_id":"bc-source"`, `"turn_count":4`},
		},
		{
			name: "notify conversation clone",
			path: notifyConversationClonePath,
			message: &agentv1.NotifyConversationCloneRequest{
				ConversationId:       "new-conversation",
				SourceConversationId: "source-conversation",
				SourceRequestId:      "source-request",
			},
			kind:     "notify_conversation_clone_request",
			contains: []string{`"conversation_id":"new-conversation"`, `"source_conversation_id":"source-conversation"`},
		},
		{
			name: "upload conversation blobs",
			path: uploadConversationBlobsPath,
			message: &agentv1.UploadConversationBlobsRequest{
				ConversationId: "new-conversation",
				Blobs: []*agentv1.BlobEntry{{
					Id:    []byte{1, 2},
					Value: []byte("blob-value"),
				}},
				ChunkIndex:  1,
				TotalChunks: 2,
			},
			kind:     "upload_conversation_blobs_request",
			contains: []string{`"conversation_id":"new-conversation"`, `"total_chunks":2`, `"value":"YmxvYi12YWx1ZQ=="`},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload, err := proto.Marshal(test.message)
			if err != nil {
				t.Fatal(err)
			}
			decoded, kind, requestID, err := decodeUnaryRequest(test.path, payload)
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if kind != test.kind {
				t.Fatalf("kind = %q, want %q", kind, test.kind)
			}
			if requestID != "" {
				t.Fatalf("request ID = %q, want empty", requestID)
			}
			compact := compactJSON(t, decoded)
			for _, expected := range test.contains {
				if !strings.Contains(compact, expected) {
					t.Errorf("decoded JSON does not contain %q:\n%s", expected, decoded)
				}
			}
		})
	}
}

func TestDecodeForkTrafficResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		message  proto.Message
		kind     string
		contains string
	}{
		{
			name: "fork background composer",
			path: forkBackgroundComposerPath,
			message: &aiserverv1.ForkBackgroundComposerResponse{
				BcId:       "bc-fork",
				SourceBcId: "bc-source",
				Mode:       aiserverv1.ForkBackgroundComposerMode_FORK_BACKGROUND_COMPOSER_MODE_CONVERSATION,
			},
			kind:     "fork_background_composer_response",
			contains: `"bc_id":"bc-fork"`,
		},
		{
			name:     "notify conversation clone",
			path:     notifyConversationClonePath,
			message:  &agentv1.NotifyConversationCloneResponse{},
			kind:     "notify_conversation_clone_response",
			contains: `{}`,
		},
		{
			name:     "upload conversation blobs",
			path:     uploadConversationBlobsPath,
			message:  &agentv1.UploadConversationBlobsResponse{},
			kind:     "upload_conversation_blobs_response",
			contains: `{}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload, err := proto.Marshal(test.message)
			if err != nil {
				t.Fatal(err)
			}
			decoded, kind, err := decodeUnaryResponse(test.path, payload)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if kind != test.kind {
				t.Fatalf("kind = %q, want %q", kind, test.kind)
			}
			if !strings.Contains(compactJSON(t, decoded), test.contains) {
				t.Errorf("decoded JSON does not contain %q:\n%s", test.contains, decoded)
			}
		})
	}
}

func TestFinishResponseBodyDecodesCompressedForkResponse(t *testing.T) {
	t.Parallel()

	payload, err := proto.Marshal(&aiserverv1.ForkBackgroundComposerResponse{
		BcId:       "bc-fork",
		SourceBcId: "bc-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: newExchangeStore(1)}
	server.store.create(&Exchange{
		ExchangeSummary: ExchangeSummary{ID: "1", StartedAt: time.Now()},
	})
	server.finishResponseBody("1", forkBackgroundComposerPath, "gzip", compressed.Bytes(), int64(compressed.Len()), false, nil)

	exchange, ok := server.store.get("1")
	if !ok {
		t.Fatal("exchange was not stored")
	}
	if exchange.ResponseKind != "fork_background_composer_response" {
		t.Fatalf("response kind = %q", exchange.ResponseKind)
	}
	if !strings.Contains(compactJSON(t, exchange.Response.DecodedJSON), `"bc_id":"bc-fork"`) {
		t.Fatalf("unexpected decoded response:\n%s", exchange.Response.DecodedJSON)
	}
	if exchange.Response.DecodeError != "" {
		t.Fatalf("decode error = %q", exchange.Response.DecodeError)
	}
}

func TestFinishRequestBodyDecodesCompressedCloneRequest(t *testing.T) {
	t.Parallel()

	payload, err := proto.Marshal(&agentv1.NotifyConversationCloneRequest{
		ConversationId:       "new-conversation",
		SourceConversationId: "source-conversation",
		SourceRequestId:      "source-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: newExchangeStore(1)}
	server.store.create(&Exchange{
		ExchangeSummary: ExchangeSummary{ID: "1", StartedAt: time.Now()},
	})
	server.finishRequestBody("1", notifyConversationClonePath, "gzip", compressed.Bytes(), int64(compressed.Len()), false, nil)

	exchange, ok := server.store.get("1")
	if !ok {
		t.Fatal("exchange was not stored")
	}
	if exchange.RequestKind != "notify_conversation_clone_request" {
		t.Fatalf("request kind = %q", exchange.RequestKind)
	}
	if !strings.Contains(compactJSON(t, exchange.Request.DecodedJSON), `"source_conversation_id":"source-conversation"`) {
		t.Fatalf("unexpected decoded request:\n%s", exchange.Request.DecodedJSON)
	}
	if exchange.Request.DecodeError != "" {
		t.Fatalf("decode error = %q", exchange.Request.DecodeError)
	}
}

func compactJSON(t *testing.T, value string) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(value)); err != nil {
		t.Fatalf("compact JSON: %v\n%s", err, value)
	}
	return compact.String()
}
