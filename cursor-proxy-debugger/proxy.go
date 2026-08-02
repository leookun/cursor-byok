package proxydebugger

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor/internal/certs"

	"github.com/elazarl/goproxy"
)

type exchangeContext struct {
	id string
}

// Server runs the HTTPS debugging proxy and its local web UI.
type Server struct {
	config      Config
	certManager *certs.Manager
	store       *exchangeStore
	counter     atomic.Uint64
	proxyServer *http.Server
	uiServer    *http.Server
	proxyLn     net.Listener
	uiLn        net.Listener
	runMu       sync.Mutex
}

// New creates a standalone Cursor protocol debugger.
func New(config Config) (*Server, error) {
	config = config.normalized()
	if err := validateLoopbackAddress(config.UIAddr); err != nil {
		return nil, err
	}
	manager, err := certs.NewEmbeddedManager()
	if err != nil {
		return nil, fmt.Errorf("加载 MITM CA 失败：%w", err)
	}
	server := &Server{
		config:      config,
		certManager: manager,
		store:       newExchangeStore(config.MaxExchanges),
	}
	proxyHandler, err := server.newProxyHandler()
	if err != nil {
		return nil, err
	}
	server.proxyServer = &http.Server{
		Handler:  proxyHandler,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	server.uiServer = &http.Server{Handler: server.newUIHandler()}
	return server, nil
}

// Start starts both listeners without modifying Cursor or system proxy settings.
func (server *Server) Start() error {
	server.runMu.Lock()
	defer server.runMu.Unlock()
	if server.proxyLn != nil || server.uiLn != nil {
		return errors.New("调试代理已经启动")
	}
	proxyListener, err := net.Listen("tcp", server.config.ProxyAddr)
	if err != nil {
		return fmt.Errorf("启动代理监听失败：%w", err)
	}
	uiListener, err := net.Listen("tcp", server.config.UIAddr)
	if err != nil {
		_ = proxyListener.Close()
		return fmt.Errorf("启动调试界面失败：%w", err)
	}
	server.proxyLn = proxyListener
	server.uiLn = uiListener
	go func() { _ = server.proxyServer.Serve(proxyListener) }()
	go func() { _ = server.uiServer.Serve(uiListener) }()
	return nil
}

// Close stops both listeners.
func (server *Server) Close(ctx context.Context) error {
	server.runMu.Lock()
	proxyServer := server.proxyServer
	uiServer := server.uiServer
	server.proxyLn = nil
	server.uiLn = nil
	server.runMu.Unlock()
	var errorsList []error
	if proxyServer != nil {
		if err := proxyServer.Shutdown(ctx); err != nil {
			errorsList = append(errorsList, err)
		}
	}
	if uiServer != nil {
		if err := uiServer.Shutdown(ctx); err != nil {
			errorsList = append(errorsList, err)
		}
	}
	return errors.Join(errorsList...)
}

func (server *Server) ProxyAddr() string { return server.config.ProxyAddr }
func (server *Server) UIAddr() string    { return server.config.UIAddr }
func (server *Server) UIURL() string     { return "http://" + browserAddress(server.config.UIAddr) }

func (server *Server) newProxyHandler() (*goproxy.ProxyHttpServer, error) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	proxy.AllowHTTP2 = true
	proxy.Logger = log.New(io.Discard, "", 0)
	proxy.ConnectionErrHandler = func(_ io.Writer, context *goproxy.ProxyCtx, connectionErr error) {
		id := exchangeID(context)
		if id == "" {
			return
		}
		server.store.update(id, func(exchange *Exchange) {
			exchange.State = "error"
			exchange.Error = connectionErr.Error()
			exchange.DurationMS = elapsedMS(exchange.StartedAt)
		})
	}
	proxy.Tr = &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	caCertificate, err := server.certManager.CATLSCertificate()
	if err != nil {
		return nil, fmt.Errorf("读取 MITM CA 失败：%w", err)
	}
	baseTLSConfig := goproxy.TLSConfigFromCA(caCertificate)
	mitmAction := &goproxy.ConnectAction{
		Action: goproxy.ConnectMitm,
		TLSConfig: func(host string, context *goproxy.ProxyCtx) (*tls.Config, error) {
			return baseTLSConfig(host, context)
		},
	}
	proxy.OnRequest().HandleConnectFunc(func(host string, _ *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if server.matchesTargetHost(host) {
			return mitmAction, host
		}
		return goproxy.OkConnect, host
	})

	proxy.OnRequest().DoFunc(server.captureRequest)
	proxy.OnResponse().DoFunc(server.captureResponse)
	return proxy, nil
}

func (server *Server) captureRequest(request *http.Request, context *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if request == nil || request.Method == http.MethodConnect || !server.matchesHTTPRequest(request) {
		return request, nil
	}
	id := strconv.FormatUint(server.counter.Add(1), 10)
	path := request.URL.Path
	requestCodec := requestContentCodec(path, request.Header)
	exchange := &Exchange{
		ExchangeSummary: ExchangeSummary{
			ID:        id,
			StartedAt: time.Now(),
			Method:    request.Method,
			URL:       request.URL.String(),
			Host:      request.URL.Host,
			Path:      path,
			State:     "pending",
		},
		Request: Payload{
			Headers:      sortedHeaders(request.Header),
			ContentType:  request.Header.Get("Content-Type"),
			ContentCodec: requestCodec,
			Frames:       make([]FrameView, 0),
		},
		Response: Payload{Headers: make([]Header, 0), Frames: make([]FrameView, 0)},
	}
	server.store.create(exchange)
	context.UserData = exchangeContext{id: id}

	if request.Body == nil {
		server.finishRequestBody(id, path, requestCodec, nil, 0, false, nil)
		return request, nil
	}
	var frameDecoder *connectFrameDecoder
	if path == "/agent.v1.AgentService/RunSSE" {
		frameDecoder = newConnectFrameDecoder(
			"aiserver.v1.BidiRequestId",
			requestCodec,
			server.config.MaxFrames,
			func(frame FrameView) { server.appendRequestFrame(id, frame) },
		)
	}
	request.Body = newCaptureReadCloser(
		request.Body,
		server.config.MaxCaptureBytes,
		func(chunk []byte) {
			if frameDecoder != nil {
				frameDecoder.Write(chunk)
			}
		},
		func(captured []byte, size int64, truncated bool, readErr error) {
			if frameDecoder != nil {
				frameDecoder.Close()
			}
			server.finishRequestBody(id, path, requestCodec, captured, size, truncated, readErr)
		},
	)
	return request, nil
}

func (server *Server) captureResponse(response *http.Response, context *goproxy.ProxyCtx) *http.Response {
	id := exchangeID(context)
	if id == "" || response == nil {
		return response
	}
	server.store.update(id, func(exchange *Exchange) {
		exchange.Status = response.StatusCode
		exchange.State = "streaming"
		exchange.DurationMS = elapsedMS(exchange.StartedAt)
		exchange.Response.Headers = sortedHeaders(response.Header)
		exchange.Response.ContentType = response.Header.Get("Content-Type")
		exchange.Response.ContentCodec = responseContentCodec(response.Header)
	})
	if response.Body == nil {
		server.finishResponseBody(id, nil, 0, false, nil)
		return response
	}

	path := ""
	if response.Request != nil && response.Request.URL != nil {
		path = response.Request.URL.Path
	}
	var frameDecoder *connectFrameDecoder
	if path == "/agent.v1.AgentService/RunSSE" {
		frameDecoder = newConnectFrameDecoder(
			"agent.v1.AgentServerMessage",
			response.Header.Get("Connect-Content-Encoding"),
			server.config.MaxFrames,
			func(frame FrameView) { server.appendResponseFrame(id, frame) },
		)
	}
	response.Body = newCaptureReadCloser(
		response.Body,
		server.config.MaxCaptureBytes,
		func(chunk []byte) {
			if frameDecoder != nil {
				frameDecoder.Write(chunk)
			}
		},
		func(captured []byte, size int64, truncated bool, readErr error) {
			if frameDecoder != nil {
				frameDecoder.Close()
			}
			server.finishResponseBody(id, captured, size, truncated, readErr)
		},
	)
	return response
}

func (server *Server) finishRequestBody(id, path string, codec string, captured []byte, size int64, truncated bool, readErr error) {
	decodePayload := captured
	var contentDecodeErr error
	if path == "/aiserver.v1.BidiService/BidiAppend" && truncated {
		contentDecodeErr = errors.New("请求正文超过抓取上限，无法完整解码")
	} else if path == "/aiserver.v1.BidiService/BidiAppend" && codec != "" && !strings.EqualFold(codec, "identity") {
		decodePayload, contentDecodeErr = decompressPayload(captured, codec)
	}
	decodedJSON, kind, requestID, decodeErr := "", "", "", contentDecodeErr
	if decodeErr == nil {
		decodedJSON, kind, requestID, decodeErr = decodeUnary(path, decodePayload)
	}
	server.store.update(id, func(exchange *Exchange) {
		exchange.RequestBytes = size
		exchange.Request.Size = size
		exchange.Request.RawHex = rawHex(captured)
		exchange.Request.RawTruncated = truncated
		if decodedJSON != "" {
			exchange.Request.DecodedJSON = decodedJSON
		}
		if kind != "" {
			exchange.RequestKind = kind
		}
		if requestID != "" {
			exchange.RequestID = requestID
		}
		if decodeErr != nil {
			exchange.Request.DecodeError = decodeErr.Error()
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			exchange.Error = readErr.Error()
		}
	})
}

func requestContentCodec(path string, headers http.Header) string {
	if path == "/agent.v1.AgentService/RunSSE" {
		return strings.TrimSpace(headers.Get("Connect-Content-Encoding"))
	}
	return strings.TrimSpace(headers.Get("Content-Encoding"))
}

func responseContentCodec(headers http.Header) string {
	if codec := strings.TrimSpace(headers.Get("Connect-Content-Encoding")); codec != "" {
		return codec
	}
	return strings.TrimSpace(headers.Get("Content-Encoding"))
}

func (server *Server) finishResponseBody(id string, captured []byte, size int64, truncated bool, readErr error) {
	server.store.update(id, func(exchange *Exchange) {
		exchange.ResponseBytes = size
		exchange.Response.Size = size
		exchange.Response.RawHex = rawHex(captured)
		exchange.Response.RawTruncated = truncated
		exchange.DurationMS = elapsedMS(exchange.StartedAt)
		exchange.State = "completed"
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			exchange.State = "error"
			exchange.Error = readErr.Error()
		}
	})
}

func (server *Server) appendRequestFrame(id string, frame FrameView) {
	server.store.update(id, func(exchange *Exchange) {
		if len(exchange.Request.Frames) < server.config.MaxFrames {
			exchange.Request.Frames = append(exchange.Request.Frames, frame)
		}
		if frame.Kind != "" {
			exchange.RequestKind = frame.Kind
		}
		if frame.RequestID != "" {
			exchange.RequestID = frame.RequestID
		}
	})
}

func (server *Server) appendResponseFrame(id string, frame FrameView) {
	server.store.update(id, func(exchange *Exchange) {
		if len(exchange.Response.Frames) < server.config.MaxFrames {
			exchange.Response.Frames = append(exchange.Response.Frames, frame)
		}
		exchange.FrameCount = len(exchange.Response.Frames)
		if frame.Kind != "" && frame.Kind != "end_stream" {
			exchange.ResponseKind = frame.Kind
		}
		if frame.Error != "" {
			exchange.Response.DecodeError = frame.Error
		}
	})
}

func (server *Server) matchesHTTPRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	host := request.Host
	if request.URL != nil && request.URL.Host != "" {
		host = request.URL.Host
	}
	return server.matchesTargetHost(host)
}

func (server *Server) matchesTargetHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	target := strings.TrimSpace(strings.ToLower(server.config.TargetHost))
	return host == target
}

func exchangeID(context *goproxy.ProxyCtx) string {
	if context == nil {
		return ""
	}
	value, ok := context.UserData.(exchangeContext)
	if !ok {
		return ""
	}
	return value.id
}

func browserAddress(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("调试界面监听地址无效：%w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("调试界面只能监听本机回环地址")
	}
	return nil
}
