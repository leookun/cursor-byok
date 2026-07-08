package server

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type HandlerFunc func(*Context) error
type Middleware func(HandlerFunc) HandlerFunc

type Route struct {
	Method     string
	Pattern    string
	Name       string
	Protocol   ProtocolClass
	Middleware []Middleware
	Local      HandlerFunc
	Upstream   HandlerFunc
}

type App struct {
	router            chi.Router
	globalMiddlewares []Middleware
	routes            []Route
	mounts            []mountSpec
}

type mountSpec struct {
	pattern string
	handler http.Handler
}

type Option func(*App)
type RouteOption func(*Route)

func New(options ...Option) http.Handler {
	app := &App{router: chi.NewRouter()}
	for _, option := range options {
		if option != nil {
			option(app)
		}
	}
	for _, route := range app.routes {
		app.registerRoute(route)
	}
	for _, mount := range app.mounts {
		app.router.Mount(mount.pattern, mount.handler)
	}
	return app.router
}

func Use(middlewares ...Middleware) Option {
	return func(app *App) {
		app.globalMiddlewares = append(app.globalMiddlewares, middlewares...)
	}
}

func Mount(pattern string, handler http.Handler) Option {
	return func(app *App) {
		if handler == nil {
			return
		}
		app.mounts = append(app.mounts, mountSpec{pattern: pattern, handler: handler})
	}
}

func GET(pattern string, options ...RouteOption) Option {
	return routeOption(http.MethodGet, pattern, options...)
}
func POST(pattern string, options ...RouteOption) Option {
	return routeOption(http.MethodPost, pattern, options...)
}
func Any(pattern string, options ...RouteOption) Option { return routeOption("", pattern, options...) }

func routeOption(method string, pattern string, options ...RouteOption) Option {
	return func(app *App) {
		route := Route{
			Method:   method,
			Pattern:  pattern,
			Protocol: ProtocolHTTP,
			Local: func(ctx *Context) error {
				return fmt.Errorf("route %s has no local action", pattern)
			},
		}
		for _, option := range options {
			if option != nil {
				option(&route)
			}
		}
		app.routes = append(app.routes, route)
	}
}

func Name(name string) RouteOption {
	return func(route *Route) {
		route.Name = name
	}
}

func HTTP() RouteOption {
	return func(route *Route) {
		route.Protocol = ProtocolHTTP
	}
}

func ConnectUnary() RouteOption {
	return func(route *Route) {
		route.Protocol = ProtocolConnectUnary
	}
}

func ConnectStream() RouteOption {
	return func(route *Route) {
		route.Protocol = ProtocolConnectStream
	}
}

func With(middlewares ...Middleware) RouteOption {
	return func(route *Route) {
		route.Middleware = append(route.Middleware, middlewares...)
	}
}

func Local(action HandlerFunc) RouteOption {
	return func(route *Route) {
		route.Local = action
	}
}

func Upstream(action HandlerFunc) RouteOption {
	return func(route *Route) {
		route.Upstream = action
	}
}

func (app *App) registerRoute(route Route) {
	handler := app.buildRouteHandler(route)
	if route.Method == "" {
		app.router.HandleFunc(route.Pattern, handler)
		return
	}
	app.router.MethodFunc(route.Method, route.Pattern, handler)
}

func (app *App) buildRouteHandler(route Route) http.HandlerFunc {
	chain := append([]Middleware{}, app.globalMiddlewares...)
	chain = append(chain, route.Middleware...)
	final := Chain(chain...)(func(ctx *Context) error {
		if shouldUseUpstreamAction(ctx, route) && route.Upstream != nil {
			return route.Upstream(ctx)
		}
		if shouldUseUpstreamAction(ctx, route) && ctx.UpstreamURL != nil {
			return fmt.Errorf("route %s is missing upstream action while request targets upstream %s", route.Name, ctx.UpstreamURL.String())
		}
		if route.Local != nil {
			return route.Local(ctx)
		}
		return fmt.Errorf("route %s has no executable action", route.Name)
	})
	return func(writer http.ResponseWriter, request *http.Request) {
		trackedWriter := newTrackedResponseWriter(writer)
		ctx := newContext(trackedWriter, request, route)
		if err := final(ctx); err != nil {
			writeServerError(trackedWriter, err)
		}
	}
}

func shouldUseUpstreamAction(ctx *Context, route Route) bool {
	_ = route
	if ctx == nil {
		return false
	}
	return ctx.Mode == ModeUpstream
}

func Chain(middlewares ...Middleware) Middleware {
	return func(final HandlerFunc) HandlerFunc {
		wrapped := final
		for index := len(middlewares) - 1; index >= 0; index-- {
			current := middlewares[index]
			if current == nil {
				continue
			}
			wrapped = current(wrapped)
		}
		return wrapped
	}
}
