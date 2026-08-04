package upstream

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cursor/internal/backend/server"
)

type CompatRouteConfig struct {
	Name          string
	StatusCode    int
	JSONBody      map[string]any
	MockProtoType string
	MockBuilder   func(*RequestContext) (map[string]any, error)
	ConsoleLog    bool
}

func ForwardAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleDirect(reqCtx, route)
	}
}

// AuthenticatedForwardAction forwards a Cursor control-plane request with the
// independent desktop account after the local-mode identity rewrite has run.
func AuthenticatedForwardAction(deps Dependencies, cfg CompatRouteConfig, authorizationProvider AuthorizationProvider) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, _, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		if reqCtx == nil || reqCtx.Request == nil {
			return fmt.Errorf("Cursor 控制面请求上下文无效")
		}
		if authorizationProvider == nil {
			return fmt.Errorf("Cursor 账号服务未初始化")
		}
		authorization, err := authorizationProvider.Authorization(reqCtx.Request.Context())
		if err != nil {
			return err
		}
		_, err = ForwardToUpstream(reqCtx, ForwardOptions{
			PatchHeaders: func(headers http.Header) {
				headers.Set("Authorization", authorization)
				headers.Set("x-cursor-checksum", BuildCursorChecksum(authorization))
			},
		})
		return err
	}
}

func FixedStatusAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleFixedStatus(reqCtx, route)
	}
}

func MockJSONAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockJSON(reqCtx, route)
	}
}

func MockOAuthAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockOAuth(reqCtx, route)
	}
}

func MockAuthFullStripeProfileAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockAuthFullStripeProfile(reqCtx, route)
	}
}

func MockAuthStripeProfileAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockAuthStripeProfile(reqCtx, route)
	}
}

func MockAuthPollAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockAuthPoll(reqCtx, route)
	}
}

func MockAuthEmailAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockAuthEmail(reqCtx, route)
	}
}

func MockProtoAction(deps Dependencies, cfg CompatRouteConfig) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, route, err := newCompatRouteObjects(ctx, deps, cfg)
		if err != nil {
			return err
		}
		return handleMockProto(reqCtx, route)
	}
}

func newCompatRouteObjects(ctx *server.Context, deps Dependencies, cfg CompatRouteConfig) (*RequestContext, *Route, error) {
	if ctx == nil || ctx.Request == nil {
		return nil, nil, nil
	}
	body, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		return nil, nil, err
	}
	ctx.Request.Body = io.NopCloser(bytes.NewReader(body))
	targetURL := ctx.UpstreamURL
	if targetURL == nil && ctx.Request.URL != nil {
		copyURL := *ctx.Request.URL
		targetURL = &copyURL
	}
	reqCtx := &RequestContext{
		ResponseWriter: ctx.Writer,
		Request:        ctx.Request,
		StartedAt:      ctx.StartedAt,
		RawURL:         strings.TrimSpace(ctx.Request.Header.Get(server.HeaderServerUpstreamURL)),
		TargetURL:      targetURL,
		Method:         strings.ToUpper(strings.TrimSpace(ctx.Request.Method)),
		Headers:        ctx.Request.Header.Clone(),
		ContentType:    strings.TrimSpace(ctx.Request.Header.Get("content-type")),
		RequestBody:    body,
		Deps:           &deps,
		HTTPRequestID:  resolveHTTPRequestID(ctx.Request),
	}
	route := &Route{
		Name:               cfg.Name,
		Pattern:            ctx.Request.URL.Path,
		StatusCode:         cfg.StatusCode,
		JSONBody:           cfg.JSONBody,
		MockProtoType:      cfg.MockProtoType,
		MockPayloadBuilder: cfg.MockBuilder,
		ConsoleLog:         cfg.ConsoleLog,
	}
	return reqCtx, route, nil
}

func ServerTimeMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildServerTimePayload(reqCtx)
}

func ServerConfigMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildServerConfigPayload(reqCtx)
}

func AvailableModelsMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildAvailableModelsPayload(reqCtx)
}

func DefaultModelNudgeMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDefaultModelNudgeDataPayload(reqCtx)
}

func UsableModelsMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildUsableModelsPayload(reqCtx)
}

func DefaultModelForCliMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDefaultModelForCliPayload(reqCtx)
}

func DefaultModelMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDefaultModelPayload(reqCtx)
}

func BootstrapStatsigMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildBootstrapStatsigPayload(reqCtx)
}

func FirstWindowStatsigDecisionMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildFirstWindowStatsigDecisionPayload(reqCtx)
}

func DashboardCurrentPeriodUsageMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardCurrentPeriodUsagePayload(reqCtx)
}

func DashboardTeamsMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardTeamsPayload(reqCtx)
}

func DashboardManagedSkillsMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardManagedSkillsPayload(reqCtx)
}

// EmptyMockBuilder возвращает пустой proto-ответ для ручек, где клиенту
// достаточно успешного "пусто": нет team-настроек, нет репозиториев,
// нет маркетплейсов/плагинов/команд, телеметрия принята без обработки.
func EmptyMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return map[string]any{}, nil
}

// SubmitLogsMockBuilder подтверждает приём логов телеметрии без обработки.
func SubmitLogsMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return map[string]any{"success": true}, nil
}

func DashboardGetMeMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardGetMePayload(reqCtx)
}

func DashboardUserPrivacyModeMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardUserPrivacyModePayload(reqCtx)
}

func DashboardPlanInfoMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardPlanInfoPayload(reqCtx)
}

func DashboardUsageLimitStatusAndActiveGrantsMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardUsageLimitStatusAndActiveGrantsPayload(reqCtx)
}

func DashboardIsOnNewPricingMockBuilder(reqCtx *RequestContext) (map[string]any, error) {
	return buildDashboardIsOnNewPricingPayload(reqCtx)
}

func resolveHTTPRequestID(request *http.Request) string {
	requestID := strings.TrimSpace(request.Header.Get("x-request-id"))
	if requestID != "" {
		return requestID
	}
	return strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339Nano), ":", "-")
}
