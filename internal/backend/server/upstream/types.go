package upstream

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cursor/internal/backend/server"
	legacyruntime "cursor/internal/runtime"
)

const (
	HeaderRawServerURL = server.HeaderServerUpstreamURL
)

type SystemSettingService interface {
	ResolveModelAdapters(context.Context) ([]legacyruntime.ModelAdapterConfig, error)
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Dependencies struct {
	SystemSettingService SystemSettingService
	HTTPClient           HTTPClient
	LogRoot              string
	Routes               []Route
}

type RequestContext struct {
	ResponseWriter http.ResponseWriter
	Request        *http.Request
	StartedAt      time.Time
	RawURL         string
	TargetURL      *url.URL
	Method         string
	Headers        http.Header
	ContentType    string
	RequestBody    []byte
	Mode           server.ExecutionMode
	Deps           *Dependencies
	HTTPRequestID  string
}

type ForwardOptions struct {
	BodyOverride []byte
	PatchHeaders func(headers http.Header)
}

type ForwardMeta struct {
	StatusCode   int
	Status       string
	ContentType  string
	ResponseSize int64
}

type Matcher interface {
	Match(path string) bool
}

type Exact string

func (m Exact) Match(path string) bool { return path == string(m) }

type Prefix string

func (m Prefix) Match(path string) bool {
	value := string(m)
	return value != "" && strings.HasPrefix(path, value)
}

type Wildcard struct{}

func (Wildcard) Match(string) bool { return true }

type RouteHandler func(reqCtx *RequestContext, route *Route) error

type Route struct {
	Name               string
	Pattern            string
	Matcher            Matcher
	ConsoleLog         bool
	StatusCode         int
	JSONBody           map[string]any
	MockProtoType      string
	MockPayloadBuilder func(*RequestContext) (map[string]any, error)
	Handler            RouteHandler
}

func BuildChannelCallError(statusCode int, forwardErr error) (string, string) {
	if forwardErr != nil {
		return "UPSTREAM_REQUEST_FAILED", strings.TrimSpace(forwardErr.Error())
	}
	if statusCode >= 200 && statusCode < 300 {
		return "", ""
	}
	if statusCode <= 0 {
		return "UPSTREAM_STATUS_UNKNOWN", ""
	}
	return "UPSTREAM_STATUS_" + strconv.Itoa(statusCode), ""
}

func ReadStringAny(data map[string]any, keys ...string) string {
	if data == nil {
		return ""
	}
	for _, key := range keys {
		value, ok := data[key]
		if !ok || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}

func ReadMapAny(data map[string]any, keys ...string) map[string]any {
	if data == nil {
		return nil
	}
	for _, key := range keys {
		value, ok := data[key]
		if !ok || value == nil {
			continue
		}
		if mapped, ok := value.(map[string]any); ok {
			return mapped
		}
	}
	return nil
}

func CloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
