package server

import (
	"net/http"

	"cursor/internal/logger"
)

func Health() HandlerFunc {
	return func(ctx *Context) error {
		if ctx == nil || ctx.Writer == nil {
			return nil
		}
		if ctx.Request != nil && ctx.Request.Method != http.MethodGet {
			http.Error(ctx.Writer, "method not allowed", http.StatusMethodNotAllowed)
			return nil
		}
		if ctx.Request != nil {
			logger.Infof("内置后端 healthz 命中 remote_addr=%s user_agent=%s", ctx.Request.RemoteAddr, ctx.Request.UserAgent())
		}
		ctx.Writer.WriteHeader(http.StatusOK)
		_, _ = ctx.Writer.Write([]byte("ok"))
		return nil
	}
}

func HTTPHandlerAction(handler http.Handler) HandlerFunc {
	return func(ctx *Context) error {
		if ctx == nil {
			return nil
		}
		if handler == nil {
			return nil
		}
		handler.ServeHTTP(ctx.Writer, ctx.Request)
		return nil
	}
}
