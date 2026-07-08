package server

import (
	"cursor/internal/logger"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"

	serverconfig "cursor/internal/backend/server/config"
	legacyruntime "cursor/internal/runtime"
)

func Recover() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) (err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("panic: %v\n%s", recovered, string(debug.Stack()))
				}
			}()
			return next(ctx)
		}
	}
}

func ServerContext() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			if ctx == nil {
				return fmt.Errorf("server context is nil")
			}
			if err := ctx.ParseUpstreamURL(); err != nil {
				return err
			}
			return next(ctx)
		}
	}
}

func PolicyMiddleware(configs *serverconfig.Manager) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			ctx.Mode = parseExecutionMode(configs.RouteMode(ctx.UpstreamURL != nil))
			logger.Infof("ctx.Mode=%s upstream=%t", ctx.Mode, ctx.UpstreamURL != nil)
			return next(ctx)
		}
	}
}

func ErrorEncoder() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			if ctx != nil {
				ctx.LastError = nil
			}
			if err := next(ctx); err != nil {
				if ctx != nil {
					ctx.LastError = err
				}
				if ctx == nil || ctx.Writer == nil {
					return err
				}
				writeServerError(ctx.Writer, err)
				return nil
			}
			return nil
		}
	}
}

func writeServerError(writer http.ResponseWriter, err error) {
	if responseWriterHasWrittenHeader(writer) {
		return
	}
	status := http.StatusBadGateway
	message := "bad gateway"
	switch {
	case err == nil:
		status = http.StatusOK
		message = ""
	case strings.TrimSpace(err.Error()) == "empty raw url":
		status = http.StatusBadRequest
		message = "invalid raw url"
	case errors.Is(err, ErrInvalidBidiAppendPayload):
		status = http.StatusBadRequest
		message = "invalid bidi append payload"
	case errors.Is(err, legacyruntime.ErrInvalidSystemSetting):
		status = http.StatusInternalServerError
		message = "invalid system setting"
	case errors.Is(err, legacyruntime.ErrChannelNotAvailable):
		status = http.StatusServiceUnavailable
		message = "no available channel"
	}
	http.Error(writer, message, status)
}
