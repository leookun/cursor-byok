package cursor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"cursor/internal/logger"

	_ "modernc.org/sqlite"
)

const (
	cursorStateMembershipType      = "ultra"
	cursorStateSubscriptionStatus  = "active"
	cursorStateDefaultSignUpType   = "Google"
	cursorStateSQLiteBusyTimeoutMS = 2000
	cursorStateDBRelativePath      = "Cursor/User/globalStorage/state.vscdb"
	cursorStateDarwinRelativePath  = "Library/Application Support/Cursor/User/globalStorage/state.vscdb"
	cursorStateLinuxRelativePath   = ".config/Cursor/User/globalStorage/state.vscdb"
	cursorStateStatsigBootstrapKey = "workbench.experiments.statsigBootstrap"
)

var cursorStateDisabledStatsigGates = []string{
	"decompose_always_local_ext_host",
	"cursor_extensions_isolation_v2",
}

// InjectCursorUserInfo synchronizes the Cursor user-level auth cache used by the
// Settings page. It does not modify the installed Cursor app bundle.
func InjectCursorUserInfo(email, token string) error {
	stateDBPath, err := resolveCursorStateDBPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(stateDBPath), 0o755); err != nil {
		return fmt.Errorf("创建 Cursor 状态目录失败: %w", err)
	}

	values := buildCursorAuthStateValues(email, token)
	if err := syncCursorAuthStateDB(stateDBPath, values); err != nil {
		return fmt.Errorf("同步 Cursor 状态库失败 path=%s: %w", stateDBPath, err)
	}

	logger.Infof(
		"injectCursorUserInfo synced path=%s email=%s membership=%s subscription=%s disabled_statsig_gates=%s",
		stateDBPath,
		values["cursorAuth/cachedEmail"],
		values["cursorAuth/stripeMembershipType"],
		values["cursorAuth/stripeSubscriptionStatus"],
		strings.Join(cursorStateDisabledStatsigGates, ","),
	)
	return nil
}

func buildCursorAuthStateValues(email, token string) map[string]string {
	email = strings.TrimSpace(email)
	token = strings.TrimSpace(token)

	return map[string]string{
		"cursorAuth/accessToken":              token,
		"cursorAuth/cachedEmail":              email,
		"cursorAuth/cachedSignUpType":         cursorStateDefaultSignUpType,
		"cursorAuth/refreshToken":             token,
		"cursorAuth/stripeMembershipType":     cursorStateMembershipType,
		"cursorAuth/stripeSubscriptionStatus": cursorStateSubscriptionStatus,
	}
}

func syncCursorAuthStateDB(path string, values map[string]string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", cursorStateSQLiteBusyTimeoutMS)); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	stmt, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO ItemTable(key, value) VALUES(?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, key := range keys {
		if _, err := stmt.ExecContext(ctx, key, values[key]); err != nil {
			return err
		}
	}

	if err := disableCursorStatsigGates(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func disableCursorStatsigGates(ctx context.Context, tx *sql.Tx) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", cursorStateStatsigBootstrapKey).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("解析 Cursor Statsig bootstrap 失败: %w", err)
	}

	featureGates, _ := payload["feature_gates"].(map[string]any)
	if featureGates == nil {
		featureGates = map[string]any{}
		payload["feature_gates"] = featureGates
	}

	hashUsed, _ := payload["hash_used"].(string)
	for _, gate := range cursorStateDisabledStatsigGates {
		disableCursorStatsigGate(featureGates, gate)
		if strings.EqualFold(hashUsed, "djb2") {
			disableCursorStatsigGate(featureGates, cursorStateDJB2Hash(gate))
		}
	}

	updated, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码 Cursor Statsig bootstrap 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE ItemTable SET value = ? WHERE key = ?", updated, cursorStateStatsigBootstrapKey); err != nil {
		return err
	}
	return nil
}

func disableCursorStatsigGate(featureGates map[string]any, key string) {
	gate, _ := featureGates[key].(map[string]any)
	if gate == nil {
		gate = map[string]any{
			"name":       key,
			"rule_id":    "local_disabled",
			"ruleID":     "local_disabled",
			"group_name": "local_disabled",
			"groupName":  "local_disabled",
			"id_type":    "userID",
			"idType":     "userID",
		}
		featureGates[key] = gate
	}
	gate["value"] = false
}

func cursorStateDJB2Hash(value string) string {
	var hash uint32
	for _, b := range []byte(value) {
		hash = hash*31 + uint32(b)
	}
	return fmt.Sprintf("%d", hash)
}

func resolveCursorStateDBPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, filepath.FromSlash(cursorStateDarwinRelativePath)), nil
	case "windows":
		appData := strings.TrimSpace(os.Getenv("APPDATA"))
		if appData == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb"), nil
	case "linux":
		configDir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if configDir == "" {
			return filepath.Join(homeDir, filepath.FromSlash(cursorStateLinuxRelativePath)), nil
		}
		return filepath.Join(configDir, filepath.FromSlash(cursorStateDBRelativePath)), nil
	default:
		return "", fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}
}
