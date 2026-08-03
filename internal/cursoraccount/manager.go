package cursoraccount

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor/gen/aiserverv1"
	"cursor/internal/backend/server/upstream"

	"github.com/google/uuid"
	"github.com/pkg/browser"
	"google.golang.org/protobuf/proto"
)

const (
	StateSignedOut = "signed_out"
	StateWaiting   = "waiting"
	StateSignedIn  = "signed_in"
	StateError     = "error"

	websiteURL    = "https://cursor.com"
	backendURL    = "https://api2.cursor.sh"
	authClientID  = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
	loginTimeout  = 10 * time.Minute
	pollInterval  = time.Second
	refreshMargin = 2 * time.Minute
)

var ErrNotSignedIn = errors.New("尚未在 cursor-byok 中登录 Cursor 账号")

// Status 是可安全返回给前端的脱敏账号状态。
type Status struct {
	State  string `json:"state"`
	AuthID string `json:"authId"`
	Email  string `json:"email"`
	Error  string `json:"error"`
}

type credentials struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	AuthID       string `json:"authId"`
	Email        string `json:"email,omitempty"`
}

type pollResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	AuthID       string `json:"authId"`
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ShouldLogout bool   `json:"shouldLogout"`
}

// Manager 持有 cursor-byok 自己的 Cursor 登录态，不读写 Cursor 客户端状态库。
type Manager struct {
	path   string
	client *http.Client

	mu              sync.RWMutex
	credentials     credentials
	state           string
	lastError       string
	loginCancel     context.CancelFunc
	loginGeneration uint64

	refreshMu sync.Mutex
}

func NewManager(path string, client *http.Client) *Manager {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	manager := &Manager{
		path:   strings.TrimSpace(path),
		client: client,
		state:  StateSignedOut,
	}
	if err := manager.load(); err != nil {
		manager.state = StateError
		manager.lastError = fmt.Sprintf("读取 Cursor 账号凭据失败: %v", err)
	}
	return manager
}

func (manager *Manager) Status() Status {
	if manager == nil {
		return Status{State: StateSignedOut}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return Status{
		State:  manager.state,
		AuthID: manager.credentials.AuthID,
		Email:  manager.credentials.Email,
		Error:  manager.lastError,
	}
}

// EnsureEmail backfills a human-readable identity for credentials saved by
// builds that only persisted authId. Profile lookup failure does not invalidate
// an otherwise usable control-plane login.
func (manager *Manager) EnsureEmail(ctx context.Context) {
	if manager == nil || !manager.SignedIn() {
		return
	}
	current, generation := manager.snapshotCredentials()
	if strings.TrimSpace(current.Email) != "" {
		return
	}
	authorization, err := manager.Authorization(ctx)
	if err != nil {
		return
	}
	profile, err := manager.fetchProfile(ctx, authorization)
	if err != nil || strings.TrimSpace(profile.GetEmail()) == "" {
		return
	}
	current, currentGeneration := manager.snapshotCredentials()
	if currentGeneration != generation {
		return
	}
	current.Email = strings.TrimSpace(profile.GetEmail())
	_ = manager.commitCredentials(generation, current)
}

func (manager *Manager) SignedIn() bool {
	if manager == nil {
		return false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.state == StateSignedIn && strings.TrimSpace(manager.credentials.AccessToken) != ""
}

// StartLogin 启动官方浏览器 PKCE 登录，并在后台等待登录结果。
func (manager *Manager) StartLogin() (Status, error) {
	if manager == nil {
		return Status{State: StateError}, fmt.Errorf("Cursor 账号服务未初始化")
	}
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return manager.Status(), fmt.Errorf("生成 Cursor 登录校验码失败: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	loginID := uuid.NewString()

	loginURL, err := buildLoginURL(loginID, challenge)
	if err != nil {
		return manager.Status(), err
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)

	manager.mu.Lock()
	if manager.loginCancel != nil {
		manager.loginCancel()
	}
	manager.loginGeneration++
	generation := manager.loginGeneration
	manager.loginCancel = cancel
	manager.state = StateWaiting
	manager.lastError = ""
	manager.mu.Unlock()

	if err := browser.OpenURL(loginURL); err != nil {
		cancel()
		manager.finishWithError(generation, fmt.Sprintf("打开 Cursor 登录页面失败: %v", err))
		return manager.Status(), err
	}

	go manager.pollLogin(ctx, generation, loginID, verifier)
	return manager.Status(), nil
}

// Disconnect 只清除 cursor-byok 自己保存的账号，不调用 Cursor 客户端 logout。
func (manager *Manager) Disconnect() (Status, error) {
	if manager == nil {
		return Status{State: StateSignedOut}, nil
	}
	manager.mu.Lock()
	manager.loginGeneration++
	if manager.loginCancel != nil {
		manager.loginCancel()
		manager.loginCancel = nil
	}
	manager.credentials = credentials{}
	manager.state = StateSignedOut
	manager.lastError = ""
	manager.mu.Unlock()

	err := os.Remove(manager.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		manager.mu.Lock()
		manager.state = StateError
		manager.lastError = fmt.Sprintf("清除 Cursor 账号凭据失败: %v", err)
		manager.mu.Unlock()
		return manager.Status(), err
	}
	return manager.Status(), nil
}

func (manager *Manager) Shutdown() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	manager.loginGeneration++
	if manager.loginCancel != nil {
		manager.loginCancel()
		manager.loginCancel = nil
	}
	manager.mu.Unlock()
}

// Authorization 返回官方控制面请求使用的真实 Cursor Bearer 身份。
func (manager *Manager) Authorization(ctx context.Context) (string, error) {
	if manager == nil {
		return "", ErrNotSignedIn
	}
	manager.refreshMu.Lock()
	defer manager.refreshMu.Unlock()

	creds, generation := manager.snapshotCredentials()
	if strings.TrimSpace(creds.AccessToken) == "" {
		return "", ErrNotSignedIn
	}
	if !tokenNeedsRefresh(creds.AccessToken, time.Now()) {
		return bearer(creds.AccessToken), nil
	}
	if strings.TrimSpace(creds.RefreshToken) == "" {
		manager.setAuthorizationError(generation, "Cursor 登录已过期，请重新登录")
		return "", fmt.Errorf("Cursor 登录已过期且没有刷新令牌")
	}

	updated, shouldLogout, err := manager.refresh(ctx, creds)
	if err != nil {
		manager.setAuthorizationError(generation, fmt.Sprintf("刷新 Cursor 登录失败: %v", err))
		return "", err
	}
	if shouldLogout {
		manager.invalidateAuthorization(generation, "Cursor 登录已失效，请重新登录")
		return "", ErrNotSignedIn
	}
	if err := manager.commitCredentials(generation, updated); err != nil {
		return "", err
	}
	return bearer(updated.AccessToken), nil
}

func (manager *Manager) pollLogin(ctx context.Context, generation uint64, loginID string, verifier string) {
	defer func() {
		manager.mu.Lock()
		if manager.loginGeneration == generation {
			manager.loginCancel = nil
		}
		manager.mu.Unlock()
	}()

	for {
		result, pending, err := manager.pollOnce(ctx, loginID, verifier)
		if err == nil && !pending {
			creds := credentials{
				AccessToken:  strings.TrimSpace(result.AccessToken),
				RefreshToken: strings.TrimSpace(result.RefreshToken),
				AuthID:       strings.TrimSpace(result.AuthID),
			}
			if creds.AccessToken == "" {
				manager.finishWithError(generation, "Cursor 登录响应缺少 access token")
				return
			}
			if profile, profileErr := manager.fetchProfile(ctx, bearer(creds.AccessToken)); profileErr == nil {
				creds.Email = strings.TrimSpace(profile.GetEmail())
			}
			_ = manager.commitCredentials(generation, creds)
			return
		}
		if err != nil && !isRetryablePollError(err) {
			manager.finishWithError(generation, fmt.Sprintf("Cursor 登录失败: %v", err))
			return
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				manager.finishWithError(generation, "Cursor 登录等待超时，请重试")
			}
			return
		case <-time.After(pollInterval):
		}
	}
}

func (manager *Manager) fetchProfile(ctx context.Context, authorization string) (*aiserverv1.GetMeResponse, error) {
	body, err := proto.Marshal(&aiserverv1.GetMeRequest{})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, backendURL+"/aiserver.v1.DashboardService/GetMe", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", authorization)
	req.Header.Set("x-cursor-checksum", upstream.BuildCursorChecksum(authorization))
	req.Header.Set("content-type", "application/proto")
	req.Header.Set("accept", "application/proto")
	req.Header.Set("connect-protocol-version", "1")
	resp, err := manager.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GetMe 返回 HTTP %d", resp.StatusCode)
	}
	profile := &aiserverv1.GetMeResponse{}
	if err := proto.Unmarshal(responseBody, profile); err != nil {
		return nil, err
	}
	return profile, nil
}

func (manager *Manager) pollOnce(ctx context.Context, loginID string, verifier string) (pollResponse, bool, error) {
	endpoint, err := url.Parse(backendURL + "/auth/poll")
	if err != nil {
		return pollResponse{}, false, err
	}
	query := endpoint.Query()
	query.Set("uuid", loginID)
	query.Set("verifier", verifier)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return pollResponse{}, false, err
	}
	resp, err := manager.client.Do(req)
	if err != nil {
		return pollResponse{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return pollResponse{}, true, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return pollResponse{}, false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pollResponse{}, false, fmt.Errorf("登录服务返回 HTTP %d", resp.StatusCode)
	}
	result := pollResponse{}
	if err := json.Unmarshal(body, &result); err != nil {
		return pollResponse{}, false, fmt.Errorf("解析登录响应失败: %w", err)
	}
	return result, false, nil
}

func (manager *Manager) refresh(ctx context.Context, current credentials) (credentials, bool, error) {
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     authClientID,
		"refresh_token": current.RefreshToken,
	})
	if err != nil {
		return credentials{}, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, backendURL+"/oauth/token", bytes.NewReader(payload))
	if err != nil {
		return credentials{}, false, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := manager.client.Do(req)
	if err != nil {
		return credentials{}, false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return credentials{}, false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return credentials{}, false, fmt.Errorf("刷新服务返回 HTTP %d", resp.StatusCode)
	}
	result := refreshResponse{}
	if err := json.Unmarshal(body, &result); err != nil {
		return credentials{}, false, fmt.Errorf("解析刷新响应失败: %w", err)
	}
	if result.ShouldLogout {
		return credentials{}, true, nil
	}
	if strings.TrimSpace(result.AccessToken) == "" {
		return credentials{}, false, fmt.Errorf("刷新响应缺少 access token")
	}
	current.AccessToken = strings.TrimSpace(result.AccessToken)
	if strings.TrimSpace(result.RefreshToken) != "" {
		current.RefreshToken = strings.TrimSpace(result.RefreshToken)
	}
	return current, false, nil
}

func (manager *Manager) load() error {
	if manager.path == "" {
		return fmt.Errorf("Cursor 账号凭据路径为空")
	}
	data, err := os.ReadFile(manager.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	loaded := credentials{}
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	loaded.AccessToken = strings.TrimSpace(loaded.AccessToken)
	loaded.RefreshToken = strings.TrimSpace(loaded.RefreshToken)
	loaded.AuthID = strings.TrimSpace(loaded.AuthID)
	loaded.Email = strings.TrimSpace(loaded.Email)
	if loaded.AccessToken == "" {
		return nil
	}
	manager.credentials = loaded
	manager.state = StateSignedIn
	return nil
}

func (manager *Manager) save(value credentials) error {
	if manager.path == "" {
		return fmt.Errorf("Cursor 账号凭据路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(manager.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tempPath := manager.path + ".tmp"
	if err := os.WriteFile(tempPath, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, manager.path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return os.Chmod(manager.path, 0o600)
}

func (manager *Manager) snapshotCredentials() (credentials, uint64) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.credentials, manager.loginGeneration
}

func (manager *Manager) finishWithError(generation uint64, message string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.loginGeneration != generation {
		return
	}
	manager.state = StateError
	manager.lastError = strings.TrimSpace(message)
}

func (manager *Manager) commitCredentials(generation uint64, value credentials) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.loginGeneration != generation {
		return ErrNotSignedIn
	}
	if err := manager.save(value); err != nil {
		manager.state = StateError
		manager.lastError = fmt.Sprintf("保存 Cursor 登录凭据失败: %v", err)
		return err
	}
	manager.credentials = value
	manager.state = StateSignedIn
	manager.lastError = ""
	return nil
}

func (manager *Manager) setAuthorizationError(generation uint64, message string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.loginGeneration != generation {
		return
	}
	manager.state = StateError
	manager.lastError = strings.TrimSpace(message)
}

func (manager *Manager) invalidateAuthorization(generation uint64, message string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.loginGeneration != generation {
		return
	}
	manager.loginGeneration++
	manager.credentials = credentials{}
	manager.state = StateError
	manager.lastError = strings.TrimSpace(message)
	_ = os.Remove(manager.path)
}

func buildLoginURL(loginID string, challenge string) (string, error) {
	parsed, err := url.Parse(websiteURL + "/loginDeepControl")
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("challenge", challenge)
	query.Set("uuid", loginID)
	query.Set("mode", "login")
	query.Set("supportsSelectedTeamLogin", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func bearer(token string) string {
	value := strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return value
	}
	return "Bearer " + value
}

func tokenNeedsRefresh(token string, now time.Time) bool {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	claims := struct {
		ExpiresAt json.Number `json:"exp"`
	}{}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil || claims.ExpiresAt == "" {
		return false
	}
	expiresAt, err := claims.ExpiresAt.Int64()
	if err != nil {
		return false
	}
	return !now.Add(refreshMargin).Before(time.Unix(expiresAt, 0))
}

func isRetryablePollError(err error) bool {
	if err == nil {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "http 429") || strings.Contains(message, "http 5")
}
