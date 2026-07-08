package upstream

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cursor/gen/aiserverv1"
	"cursor/internal/backend/server"
	"cursor/internal/logger"
	"cursor/internal/netproxy"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

func ForwardToUpstream(reqCtx *RequestContext, options ForwardOptions) (*ForwardMeta, error) {
	requestBody := reqCtx.RequestBody
	if options.BodyOverride != nil {
		requestBody = options.BodyOverride
	}
	if !shouldRequestCarryBody(reqCtx.Method) {
		requestBody = []byte{}
	}

	upstreamRequest, upstreamClient, err := buildUpstreamRequest(reqCtx, requestBody, options)
	if err != nil {
		return nil, err
	}

	upstreamResponse, err := upstreamClient.Do(upstreamRequest)
	if err != nil {
		return nil, err
	}
	defer upstreamResponse.Body.Close()

	copyResponseHeadersToClient(reqCtx.ResponseWriter.Header(), upstreamResponse.Header)
	reqCtx.ResponseWriter.WriteHeader(upstreamResponse.StatusCode)

	written, copyErr := copyResponse(reqCtx.ResponseWriter, upstreamResponse.Body)
	meta := &ForwardMeta{
		StatusCode:   upstreamResponse.StatusCode,
		Status:       upstreamResponse.Status,
		ContentType:  upstreamResponse.Header.Get("content-type"),
		ResponseSize: written,
	}
	if copyErr != nil {
		return meta, copyErr
	}
	return meta, nil
}

func buildUpstreamRequest(reqCtx *RequestContext, body []byte, options ForwardOptions) (*http.Request, HTTPClient, error) {
	upstreamRequest, err := http.NewRequestWithContext(reqCtx.Request.Context(), reqCtx.Method, reqCtx.TargetURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("create upstream request failed: %w", err)
	}

	copyRequestHeadersForUpstream(upstreamRequest.Header, reqCtx.Headers)
	upstreamRequest.Header.Del(HeaderRawServerURL)
	if !shouldRequestCarryBody(reqCtx.Method) {
		upstreamRequest.Header.Del("content-length")
	} else {
		upstreamRequest.Header.Set("content-length", strconv.Itoa(len(body)))
	}
	upstreamRequest.Host = reqCtx.TargetURL.Host

	if reqCtx.Mode == server.ModeLocal && shouldRewriteHost(reqCtx.TargetURL.Hostname()) {
		auth := formatBearerAuthorization(legacyruntime.LocalRelayToken)
		if auth == "" {
			return nil, nil, legacyruntime.ErrInvalidSystemSetting
		}
		upstreamRequest.Header.Set("Authorization", auth)
		upstreamRequest.Header.Set("x-cursor-checksum", BuildCursorChecksum(auth))
	}
	if options.PatchHeaders != nil {
		options.PatchHeaders(upstreamRequest.Header)
	}

	upstreamClient := reqCtx.Deps.HTTPClient
	if upstreamClient == nil {
		upstreamClient = netproxy.NewHTTPClient(0)
	}

	return upstreamRequest, upstreamClient, nil
}

func copyResponse(writer io.Writer, reader io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64

	for {
		readCount, readErr := reader.Read(buffer)
		if readCount > 0 {
			chunk := buffer[:readCount]
			written, writeErr := writer.Write(chunk)
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written < len(chunk) {
				return total, io.ErrShortWrite
			}
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

func ParseAndValidateRawURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, fmt.Errorf("empty raw url")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, fmt.Errorf("empty host")
	}
	return parsed, nil
}

func copyRequestHeadersForUpstream(target http.Header, source http.Header) {
	for key, values := range source {
		lowerKey := strings.ToLower(key)
		if _, exists := hopByHopHeaders[lowerKey]; exists {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

func copyResponseHeadersToClient(target http.Header, source http.Header) {
	for key, values := range source {
		lowerKey := strings.ToLower(key)
		if _, exists := hopByHopHeaders[lowerKey]; exists {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

func shouldRewriteHost(host string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if normalized == "" {
		return false
	}
	return normalized == "cursor.sh" || strings.HasSuffix(normalized, ".cursor.sh")
}

func BuildCursorChecksum(authorization string) string {
	const (
		checksumTimestampDivisor = 1_000_000
		checksumInitialSeed      = 165
	)
	timestamp := time.Now().UnixMilli() / checksumTimestampDivisor
	timestampBytes := make([]byte, 6)
	timestampBigInt := big.NewInt(timestamp)
	for index := 0; index < len(timestampBytes); index++ {
		shift := uint((len(timestampBytes) - 1 - index) * 8)
		timestampBytes[index] = byte(new(big.Int).Rsh(timestampBigInt, shift).Uint64() & 0xff)
	}
	seed := checksumInitialSeed
	for index := 0; index < len(timestampBytes); index++ {
		current := int(timestampBytes[index]^byte(seed)) + (index % 256)
		current &= 0xff
		timestampBytes[index] = byte(current)
		seed = current
	}
	prefix := strings.TrimRight(base64.StdEncoding.EncodeToString(timestampBytes), "=")
	hashBytes := sha256.Sum256([]byte(strings.TrimSpace(authorization)))
	hash := fmt.Sprintf("%x", hashBytes)
	return prefix + hash[:32]
}

func formatBearerAuthorization(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return value
	}
	return "Bearer " + value
}

func shouldRequestCarryBody(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		return false
	default:
		return true
	}
}

func marshalJSONBody(payload map[string]any) ([]byte, error) {
	if payload == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(payload)
}

func handleMockJSON(reqCtx *RequestContext, route *Route) error {
	responseBody, err := marshalJSONBody(route.JSONBody)
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/json")
	reqCtx.ResponseWriter.WriteHeader(route.StatusCode)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

func handleMockProto(reqCtx *RequestContext, route *Route) error {
	payload := map[string]any{}
	if route.MockPayloadBuilder != nil {
		built, err := route.MockPayloadBuilder(reqCtx)
		if err != nil {
			return err
		}
		payload = built
	}
	responseBody, err := encodeMockProto(route.MockProtoType, payload)
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/proto")
	reqCtx.ResponseWriter.Header().Del("content-encoding")
	reqCtx.ResponseWriter.Header().Set("content-length", strconv.Itoa(len(responseBody)))
	reqCtx.ResponseWriter.WriteHeader(route.StatusCode)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

func handleMockOAuth(reqCtx *RequestContext, route *Route) error {
	payload := struct {
		RefreshToken string `json:"refresh_token"`
	}{}
	_ = json.Unmarshal(reqCtx.RequestBody, &payload)
	responseBody, err := marshalJSONBody(map[string]any{
		"access_token": payload.RefreshToken,
		"id_token":     payload.RefreshToken,
		"shouldLogout": false,
	})
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/json")
	reqCtx.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

func handleMockAuthFullStripeProfile(reqCtx *RequestContext, route *Route) error {
	_ = route
	responseBody, err := marshalJSONBody(map[string]any{
		"membershipType":          localUltraMembershipType,
		"subscriptionStatus":      localUltraSubscriptionStatus,
		"lastPaymentFailed":       false,
		"pendingCancellationDate": "",
		"daysRemainingOnTrial":    0,
		"paymentId":               localUltraPaymentID,
	})
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/json")
	reqCtx.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

func handleMockAuthStripeProfile(reqCtx *RequestContext, route *Route) error {
	_ = route
	responseBody, err := json.Marshal(localUltraPaymentID)
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/json")
	reqCtx.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

func handleMockAuthPoll(reqCtx *RequestContext, route *Route) error {
	_ = route
	responseBody, err := marshalJSONBody(map[string]any{
		"accessToken":  legacyruntime.InjectAuthToken,
		"refreshToken": legacyruntime.InjectAuthToken,
		"authId":       "local_auth",
	})
	if err != nil {
		return err
	}
	reqCtx.ResponseWriter.Header().Set("content-type", "application/json")
	reqCtx.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

func handleMockAuthEmail(reqCtx *RequestContext, route *Route) error {
	_ = route
	responseBody := encodeAuthGetEmailResponse(legacyruntime.InjectAccountEmail)
	reqCtx.ResponseWriter.Header().Set("content-type", "application/proto")
	reqCtx.ResponseWriter.Header().Set("content-length", strconv.Itoa(len(responseBody)))
	reqCtx.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = reqCtx.ResponseWriter.Write(responseBody)
	return nil
}

func encodeAuthGetEmailResponse(email string) []byte {
	output := make([]byte, 0, len(email)+8)
	output = append(output, 0x0a)
	output = appendProtoVarint(output, uint64(len(email)))
	output = append(output, []byte(email)...)
	output = append(output, 0x10, 0x03) // GetEmailResponse.SignUpType.SIGN_UP_TYPE_GOOGLE
	return output
}

func appendProtoVarint(output []byte, value uint64) []byte {
	for value >= 0x80 {
		output = append(output, byte(value)|0x80)
		value >>= 7
	}
	return append(output, byte(value))
}

func handleFixedStatus(reqCtx *RequestContext, route *Route) error {
	if route != nil && route.ConsoleLog {
		logger.Infof("backend server fixed-status route hit name=%s method=%s path=%s raw_url=%s status=%d", route.Name, reqCtx.Method, reqCtx.TargetURL.Path, reqCtx.RawURL, route.StatusCode)
	}
	writeFixedStatus(reqCtx, route.StatusCode)
	return nil
}

func writeFixedStatus(reqCtx *RequestContext, statusCode int) {
	if reqCtx == nil || reqCtx.ResponseWriter == nil {
		return
	}
	reqCtx.ResponseWriter.WriteHeader(statusCode)
}

func handleDirect(reqCtx *RequestContext, route *Route) error {
	_ = route
	_, err := ForwardToUpstream(reqCtx, ForwardOptions{})
	return err
}

func encodeMockProto(typeName string, payload map[string]any) ([]byte, error) {
	message, err := newProtoMessage(typeName)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, message); err != nil {
		return nil, fmt.Errorf("mock proto json decode failed: %w", err)
	}
	return proto.Marshal(message)
}

func newProtoMessage(typeName string) (proto.Message, error) {
	switch strings.TrimSpace(typeName) {
	case "aiserver.v1.ServerTimeResponse":
		return &aiserverv1.ServerTimeResponse{}, nil
	case "aiserver.v1.GetServerConfigResponse":
		return &aiserverv1.GetServerConfigResponse{}, nil
	case "aiserver.v1.AvailableModelsResponse":
		return &aiserverv1.AvailableModelsResponse{}, nil
	case "aiserver.v1.GetDefaultModelNudgeDataResponse":
		return &aiserverv1.GetDefaultModelNudgeDataResponse{}, nil
	case "aiserver.v1.BootstrapStatsigResponse":
		return &aiserverv1.BootstrapStatsigResponse{}, nil
	case "aiserver.v1.GetFirstWindowStatsigDecisionResponse":
		return &aiserverv1.GetFirstWindowStatsigDecisionResponse{}, nil
	case "aiserver.v1.GetCurrentPeriodUsageResponse":
		return &aiserverv1.GetCurrentPeriodUsageResponse{}, nil
	case "aiserver.v1.GetTeamsResponse":
		return &aiserverv1.GetTeamsResponse{}, nil
	case "aiserver.v1.GetMeResponse":
		return &aiserverv1.GetMeResponse{}, nil
	case "aiserver.v1.GetUserPrivacyModeResponse":
		return &aiserverv1.GetUserPrivacyModeResponse{}, nil
	case "aiserver.v1.GetPlanInfoResponse":
		return &aiserverv1.GetPlanInfoResponse{}, nil
	case "aiserver.v1.GetUsageLimitStatusAndActiveGrantsResponse":
		return &aiserverv1.GetUsageLimitStatusAndActiveGrantsResponse{}, nil
	case "aiserver.v1.IsOnNewPricingResponse":
		return &aiserverv1.IsOnNewPricingResponse{}, nil
	default:
		return nil, fmt.Errorf("unsupported proto message type %q", typeName)
	}
}
