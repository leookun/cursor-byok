package proxydebugger

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	agentprotocol "cursor/internal/backend/agent/protocol"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const maxConnectFrameBytes = 64 << 20

type connectFrameDecoder struct {
	buffer      []byte
	messageType string
	codec       string
	maxFrames   int
	frameCount  int
	onFrame     func(FrameView)
}

func newConnectFrameDecoder(messageType string, codec string, maxFrames int, onFrame func(FrameView)) *connectFrameDecoder {
	return &connectFrameDecoder{
		messageType: messageType,
		codec:       strings.TrimSpace(codec),
		maxFrames:   maxFrames,
		onFrame:     onFrame,
	}
}

func (decoder *connectFrameDecoder) Write(payload []byte) {
	if len(payload) == 0 || decoder.frameCount >= decoder.maxFrames {
		return
	}
	decoder.buffer = append(decoder.buffer, payload...)
	for len(decoder.buffer) >= 5 && decoder.frameCount < decoder.maxFrames {
		flags := decoder.buffer[0]
		length := int(binary.BigEndian.Uint32(decoder.buffer[1:5]))
		if length < 0 || length > maxConnectFrameBytes {
			decoder.emit(FrameView{Flags: flags, Length: length, Error: "Connect 帧长度异常"})
			decoder.buffer = nil
			return
		}
		if len(decoder.buffer) < 5+length {
			return
		}
		framePayload := append([]byte(nil), decoder.buffer[5:5+length]...)
		decoder.buffer = decoder.buffer[5+length:]
		decoder.emit(decoder.decode(flags, framePayload))
	}
}

func (decoder *connectFrameDecoder) Close() {
	if len(decoder.buffer) > 0 && decoder.frameCount < decoder.maxFrames {
		decoder.emit(FrameView{
			Length: len(decoder.buffer),
			RawHex: clippedHex(decoder.buffer, 4096),
			Error:  "流结束时仍有不完整的 Connect 帧",
		})
	}
	decoder.buffer = nil
}

func (decoder *connectFrameDecoder) emit(frame FrameView) {
	frame.Index = decoder.frameCount
	decoder.frameCount++
	if decoder.onFrame != nil {
		decoder.onFrame(frame)
	}
}

func (decoder *connectFrameDecoder) decode(flags uint8, payload []byte) FrameView {
	frame := FrameView{
		Flags:      flags,
		Length:     len(payload),
		Compressed: flags&0x01 != 0,
		EndStream:  flags&0x02 != 0,
		RawHex:     clippedHex(payload, 4096),
	}
	decoded := payload
	if frame.Compressed {
		var err error
		decoded, err = decompressPayload(payload, decoder.codec)
		if err != nil {
			frame.Error = err.Error()
			return frame
		}
	}
	if frame.EndStream {
		frame.Kind = "end_stream"
		frame.MessageType = "connect.error.v1.EndStreamResponse"
		frame.JSON = prettyJSON(decoded)
		return frame
	}

	message := newMessage(decoder.messageType)
	if message == nil {
		frame.Error = "未知的 protobuf 消息类型"
		return frame
	}
	if err := proto.Unmarshal(decoded, message); err != nil {
		frame.Error = fmt.Sprintf("protobuf 解码失败：%v", err)
		return frame
	}
	frame.MessageType = decoder.messageType
	frame.Kind = activeOneofName(message)
	if requestID, ok := message.(*aiserverv1.BidiRequestId); ok {
		frame.RequestID = strings.TrimSpace(requestID.GetRequestId())
	}
	frame.JSON = marshalProtoJSON(message)
	return frame
}

func decompressPayload(payload []byte, codec string) ([]byte, error) {
	if codec != "" && !strings.EqualFold(codec, "gzip") {
		return nil, fmt.Errorf("暂不支持压缩算法 %q", codec)
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gzip 解压失败：%w", err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, maxConnectFrameBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 gzip 内容失败：%w", err)
	}
	if len(decoded) > maxConnectFrameBytes {
		return nil, fmt.Errorf("gzip 解压后超过 %d 字节限制", maxConnectFrameBytes)
	}
	return decoded, nil
}

func decodeUnary(path string, payload []byte) (decodedJSON string, kind string, requestID string, err error) {
	var message proto.Message
	switch path {
	case "/aiserver.v1.BidiService/BidiAppend":
		request := &aiserverv1.BidiAppendRequest{}
		if err := proto.Unmarshal(payload, request); err != nil {
			return "", "", "", err
		}
		requestID := strings.TrimSpace(request.GetRequestId().GetRequestId())
		outer := marshalProtoJSON(request)
		clientMessage, clientKind, decodeErr := agentprotocol.DecodeAgentClientMessage(request.GetData())
		if decodeErr != nil || clientMessage == nil {
			return outer, "bidi_append", requestID, decodeErr
		}
		combined := struct {
			BidiAppendRequest json.RawMessage `json:"bidi_append_request"`
			AgentClientKind   string          `json:"agent_client_kind"`
			AgentClient       json.RawMessage `json:"agent_client_message"`
		}{
			BidiAppendRequest: json.RawMessage(outer),
			AgentClientKind:   clientKind,
			AgentClient:       json.RawMessage(marshalProtoJSON(clientMessage)),
		}
		formatted, marshalErr := json.MarshalIndent(combined, "", "  ")
		return string(formatted), clientKind, requestID, marshalErr
	default:
		message = nil
	}
	if message == nil {
		return "", "", "", nil
	}
	return marshalProtoJSON(message), activeOneofName(message), "", nil
}

func newMessage(messageType string) proto.Message {
	switch messageType {
	case "aiserver.v1.BidiRequestId":
		return &aiserverv1.BidiRequestId{}
	case "agent.v1.AgentServerMessage":
		return &agentv1.AgentServerMessage{}
	default:
		return nil
	}
}

func marshalProtoJSON(message proto.Message) string {
	if message == nil {
		return ""
	}
	payload, err := (protojson.MarshalOptions{
		UseProtoNames:   true,
		EmitUnpopulated: false,
		Indent:          "  ",
	}).Marshal(message)
	if err != nil {
		return ""
	}
	return string(payload)
}

func activeOneofName(message proto.Message) string {
	if message == nil {
		return ""
	}
	reflected := message.ProtoReflect()
	oneofs := reflected.Descriptor().Oneofs()
	for index := 0; index < oneofs.Len(); index++ {
		oneof := oneofs.Get(index)
		field := reflected.WhichOneof(oneof)
		if field != nil {
			return string(field.Name())
		}
	}
	return string(reflected.Descriptor().Name())
}

func prettyJSON(payload []byte) string {
	var target any
	if err := json.Unmarshal(payload, &target); err != nil {
		return string(payload)
	}
	formatted, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return string(payload)
	}
	return string(formatted)
}

func clippedHex(payload []byte, max int) string {
	if len(payload) > max {
		return hex.EncodeToString(payload[:max]) + "..."
	}
	return hex.EncodeToString(payload)
}
