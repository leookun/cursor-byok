package proxydebugger

import "time"

const (
	defaultProxyAddr       = "127.0.0.1:9090"
	defaultUIAddr          = "127.0.0.1:9091"
	defaultTargetHost      = "api2.cursor.sh"
	defaultMaxExchanges    = 200
	defaultMaxCaptureBytes = 2 << 20
	defaultMaxFrames       = 2000
)

// Config controls the standalone proxy debugger.
type Config struct {
	ProxyAddr       string
	UIAddr          string
	TargetHost      string
	MaxExchanges    int
	MaxCaptureBytes int
	MaxFrames       int
}

func (config Config) normalized() Config {
	if config.ProxyAddr == "" {
		config.ProxyAddr = defaultProxyAddr
	}
	if config.UIAddr == "" {
		config.UIAddr = defaultUIAddr
	}
	if config.TargetHost == "" {
		config.TargetHost = defaultTargetHost
	}
	if config.MaxExchanges <= 0 {
		config.MaxExchanges = defaultMaxExchanges
	}
	if config.MaxCaptureBytes <= 0 {
		config.MaxCaptureBytes = defaultMaxCaptureBytes
	}
	if config.MaxFrames <= 0 {
		config.MaxFrames = defaultMaxFrames
	}
	return config
}

// ExchangeSummary is the compact request-list representation.
type ExchangeSummary struct {
	ID            string    `json:"id"`
	StartedAt     time.Time `json:"startedAt"`
	Method        string    `json:"method"`
	URL           string    `json:"url"`
	Host          string    `json:"host"`
	Path          string    `json:"path"`
	Status        int       `json:"status"`
	State         string    `json:"state"`
	DurationMS    int64     `json:"durationMs"`
	RequestBytes  int64     `json:"requestBytes"`
	ResponseBytes int64     `json:"responseBytes"`
	RequestID     string    `json:"requestId,omitempty"`
	RequestKind   string    `json:"requestKind,omitempty"`
	ResponseKind  string    `json:"responseKind,omitempty"`
	FrameCount    int       `json:"frameCount"`
	Error         string    `json:"error,omitempty"`
}

// Exchange contains the request and response detail shown by the debugger.
type Exchange struct {
	ExchangeSummary
	Request  Payload `json:"request"`
	Response Payload `json:"response"`
}

// Payload contains headers, captured raw bytes, and decoded protobuf frames.
type Payload struct {
	Headers      []Header    `json:"headers"`
	ContentType  string      `json:"contentType,omitempty"`
	ContentCodec string      `json:"contentCodec,omitempty"`
	Size         int64       `json:"size"`
	RawHex       string      `json:"rawHex,omitempty"`
	RawTruncated bool        `json:"rawTruncated,omitempty"`
	DecodedJSON  string      `json:"decodedJson,omitempty"`
	DecodeError  string      `json:"decodeError,omitempty"`
	Frames       []FrameView `json:"frames,omitempty"`
}

// Header is a stable, sorted HTTP header pair.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// FrameView describes one Connect streaming envelope.
type FrameView struct {
	Index       int    `json:"index"`
	Flags       uint8  `json:"flags"`
	Length      int    `json:"length"`
	Compressed  bool   `json:"compressed"`
	EndStream   bool   `json:"endStream"`
	Kind        string `json:"kind,omitempty"`
	MessageType string `json:"messageType,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
	JSON        string `json:"json,omitempty"`
	RawHex      string `json:"rawHex,omitempty"`
	Error       string `json:"error,omitempty"`
}

type storeEvent struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}
