use std::path::{Path, PathBuf};

use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};
use serde::Serialize;
use serde_json::json;
use sqlx::{Connection, SqliteConnection};

use crate::{
    store::{CursorAccountBackup, Store, TabMode},
    Error, Result,
};

const EMAIL: &str = "cursor@ai.com";
const SIGN_UP_TYPE: &str = "Google";
const SUBJECT: &str = "cursor-local-user";
const ISSUER: &str = "cursor-client";
const MEMBERSHIP_TYPE: &str = "ultra";
const SUBSCRIPTION_STATUS: &str = "active";

const ACCESS_TOKEN_KEY: &str = "cursorAuth/accessToken";
const REFRESH_TOKEN_KEY: &str = "cursorAuth/refreshToken";
const EMAIL_KEY: &str = "cursorAuth/cachedEmail";
const SIGN_UP_TYPE_KEY: &str = "cursorAuth/cachedSignUpType";
const MEMBERSHIP_TYPE_KEY: &str = "cursorAuth/stripeMembershipType";
const SUBSCRIPTION_STATUS_KEY: &str = "cursorAuth/stripeSubscriptionStatus";

const AUTH_KEYS: [&str; 6] = [
    ACCESS_TOKEN_KEY,
    REFRESH_TOKEN_KEY,
    EMAIL_KEY,
    SIGN_UP_TYPE_KEY,
    MEMBERSHIP_TYPE_KEY,
    SUBSCRIPTION_STATUS_KEY,
];

#[derive(Clone, Copy, Debug, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum CursorAccountSource {
    Original,
    Local,
    None,
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct CursorAccountStatus {
    pub source: CursorAccountSource,
    pub email: Option<String>,
    pub has_backup: bool,
    pub backup_email: Option<String>,
}

#[derive(Clone, Debug, Default)]
struct CursorAuthSnapshot {
    access_token: String,
    refresh_token: String,
    email: String,
    sign_up_type: String,
    membership_type: String,
    subscription_status: String,
}

impl CursorAuthSnapshot {
    fn is_local(&self) -> bool {
        is_local_token(&self.access_token) || self.email.trim() == EMAIL
    }

    fn has_session(&self) -> bool {
        !self.access_token.trim().is_empty()
    }

    fn into_backup(self) -> Option<CursorAccountBackup> {
        let backup = CursorAccountBackup {
            email: self.email,
            access_token: self.access_token,
            refresh_token: self.refresh_token,
            sign_up_type: self.sign_up_type,
            membership_type: self.membership_type,
            subscription_status: self.subscription_status,
        };
        backup.is_original().then_some(backup)
    }
}

impl CursorAccountBackup {
    fn email_or_token_claim(&self) -> Option<String> {
        let email = self.email.trim();
        if !email.is_empty() {
            return Some(email.to_owned());
        }
        jwt_claim(&self.access_token, "email").filter(|value| !value.is_empty())
    }
}

pub async fn status(store: &Store) -> Result<CursorAccountStatus> {
    status_at(store, &state_db_path()?).await
}

pub async fn apply_for_tab_mode(store: &Store, mode: TabMode) -> Result<CursorAccountStatus> {
    apply_for_tab_mode_at(store, mode, &state_db_path()?).await
}

pub async fn restore_original(store: &Store) -> Result<CursorAccountStatus> {
    restore_original_at(store, &state_db_path()?, true).await
}

pub async fn restore_original_or_clear_local(store: &Store) -> Result<CursorAccountStatus> {
    restore_original_at(store, &state_db_path()?, false).await
}

async fn apply_for_tab_mode_at(
    store: &Store,
    mode: TabMode,
    path: &Path,
) -> Result<CursorAccountStatus> {
    capture_original_backup(store, path).await?;
    match mode {
        TabMode::Direct => restore_original_at(store, path, false).await,
        TabMode::Public | TabMode::Custom => {
            inject_if_missing_at(path).await?;
            status_at(store, path).await
        }
    }
}

async fn restore_original_at(
    store: &Store,
    path: &Path,
    require_backup: bool,
) -> Result<CursorAccountStatus> {
    capture_original_backup(store, path).await?;
    let backup = load_backup(store).await?;
    let current = read_snapshot(path).await?;
    if current
        .as_ref()
        .is_some_and(|snapshot| snapshot.has_session() && !snapshot.is_local())
    {
        return status_at(store, path).await;
    }
    if let Some(backup) = backup {
        write_snapshot(path, &snapshot_from_backup(&backup)).await?;
        tracing::info!(email = %backup.email, "restored original Cursor account");
        return status_at(store, path).await;
    }
    if require_backup {
        return Err(Error::Config(
            "没有可恢复的 Cursor 原账号备份。请先在 Cursor 中登录原账号，或关闭 BYOK 后重新登录。"
                .into(),
        ));
    }
    if current.as_ref().is_some_and(|snapshot| snapshot.is_local()) {
        clear_injected_account(path).await?;
        tracing::info!("cleared local Cursor account mock");
    }
    status_at(store, path).await
}

async fn status_at(store: &Store, path: &Path) -> Result<CursorAccountStatus> {
    let backup = load_backup(store).await?;
    let snapshot = read_snapshot(path).await?;
    let source = match snapshot.as_ref() {
        Some(snapshot) if snapshot.is_local() => CursorAccountSource::Local,
        Some(snapshot) if snapshot.has_session() => CursorAccountSource::Original,
        _ => CursorAccountSource::None,
    };
    Ok(CursorAccountStatus {
        source,
        email: snapshot.and_then(|snapshot| {
            let email = snapshot.email.trim();
            (!email.is_empty()).then(|| email.to_owned())
        }),
        has_backup: backup.is_some(),
        backup_email: backup.and_then(|backup| backup.email_or_token_claim()),
    })
}

async fn capture_original_backup(store: &Store, path: &Path) -> Result<()> {
    if let Some(backup) = read_snapshot(path)
        .await?
        .and_then(CursorAuthSnapshot::into_backup)
    {
        store.set_cursor_account_backup(&backup).await?;
        return Ok(());
    }
    if store.cursor_account_backup().await?.is_some() {
        return Ok(());
    }
    if let Some(backup) = read_legacy_backup() {
        store.set_cursor_account_backup(&backup).await?;
        tracing::info!(email = %backup.email, "imported legacy Cursor account backup");
    }
    Ok(())
}

async fn load_backup(store: &Store) -> Result<Option<CursorAccountBackup>> {
    if let Some(backup) = store.cursor_account_backup().await? {
        return Ok(Some(backup));
    }
    if let Some(backup) = read_legacy_backup() {
        store.set_cursor_account_backup(&backup).await?;
        return Ok(Some(backup));
    }
    Ok(None)
}

fn snapshot_from_backup(backup: &CursorAccountBackup) -> CursorAuthSnapshot {
    CursorAuthSnapshot {
        access_token: backup.access_token.clone(),
        refresh_token: if backup.refresh_token.trim().is_empty() {
            backup.access_token.clone()
        } else {
            backup.refresh_token.clone()
        },
        email: backup.email.clone(),
        sign_up_type: if backup.sign_up_type.trim().is_empty() {
            SIGN_UP_TYPE.to_owned()
        } else {
            backup.sign_up_type.clone()
        },
        membership_type: backup.membership_type.clone(),
        subscription_status: backup.subscription_status.clone(),
    }
}

fn state_db_path() -> Result<PathBuf> {
    let home = dirs::home_dir()
        .ok_or_else(|| Error::Config("cannot resolve user home directory".into()))?;
    match std::env::consts::OS {
        "macos" => {
            Ok(home.join("Library/Application Support/Cursor/User/globalStorage/state.vscdb"))
        }
        "windows" => Ok(std::env::var_os("APPDATA")
            .map(PathBuf::from)
            .unwrap_or_else(|| home.join("AppData/Roaming"))
            .join("Cursor/User/globalStorage/state.vscdb")),
        "linux" => Ok(std::env::var_os("XDG_CONFIG_HOME")
            .map(PathBuf::from)
            .unwrap_or_else(|| home.join(".config"))
            .join("Cursor/User/globalStorage/state.vscdb")),
        platform => Err(Error::Config(format!(
            "Cursor account injection is unsupported on {platform}"
        ))),
    }
}

async fn inject_if_missing_at(path: &Path) -> Result<()> {
    if read_snapshot(path)
        .await?
        .is_some_and(|snapshot| snapshot.has_session())
    {
        return Ok(());
    }
    let token = local_token()?;
    write_snapshot(
        path,
        &CursorAuthSnapshot {
            access_token: token.clone(),
            refresh_token: token,
            email: EMAIL.to_owned(),
            sign_up_type: SIGN_UP_TYPE.to_owned(),
            membership_type: MEMBERSHIP_TYPE.to_owned(),
            subscription_status: SUBSCRIPTION_STATUS.to_owned(),
        },
    )
    .await?;
    tracing::info!(
        email = EMAIL,
        subject = SUBJECT,
        "injected local Cursor account"
    );
    Ok(())
}

async fn write_snapshot(path: &Path, snapshot: &CursorAuthSnapshot) -> Result<()> {
    let mut connection = open_state_db(path).await?;
    let mut transaction = connection.begin().await?;
    let values = [
        (ACCESS_TOKEN_KEY, snapshot.access_token.as_str()),
        (REFRESH_TOKEN_KEY, snapshot.refresh_token.as_str()),
        (EMAIL_KEY, snapshot.email.as_str()),
        (SIGN_UP_TYPE_KEY, snapshot.sign_up_type.as_str()),
        (MEMBERSHIP_TYPE_KEY, snapshot.membership_type.as_str()),
        (
            SUBSCRIPTION_STATUS_KEY,
            snapshot.subscription_status.as_str(),
        ),
    ];
    for (key, value) in values {
        if value.trim().is_empty() && matches!(key, MEMBERSHIP_TYPE_KEY | SUBSCRIPTION_STATUS_KEY) {
            sqlx::query("DELETE FROM ItemTable WHERE key = ?")
                .bind(key)
                .execute(&mut *transaction)
                .await?;
            continue;
        }
        sqlx::query("INSERT OR REPLACE INTO ItemTable(key, value) VALUES(?, ?)")
            .bind(key)
            .bind(value)
            .execute(&mut *transaction)
            .await?;
    }
    transaction.commit().await?;
    Ok(())
}

async fn clear_injected_account(path: &Path) -> Result<()> {
    if !path.exists() {
        return Ok(());
    }
    let mut connection = open_state_db(path).await?;
    let mut transaction = connection.begin().await?;
    for key in AUTH_KEYS {
        sqlx::query("DELETE FROM ItemTable WHERE key = ?")
            .bind(key)
            .execute(&mut *transaction)
            .await?;
    }
    transaction.commit().await?;
    Ok(())
}

async fn read_snapshot(path: &Path) -> Result<Option<CursorAuthSnapshot>> {
    if !path.exists() {
        return Ok(None);
    }
    let mut connection = open_state_db(path).await?;
    let mut snapshot = CursorAuthSnapshot::default();
    for key in AUTH_KEYS {
        let value = sqlx::query_scalar::<_, String>(
            "SELECT CAST(value AS TEXT) FROM ItemTable WHERE key = ?",
        )
        .bind(key)
        .fetch_optional(&mut connection)
        .await?
        .unwrap_or_default();
        match key {
            ACCESS_TOKEN_KEY => snapshot.access_token = value,
            REFRESH_TOKEN_KEY => snapshot.refresh_token = value,
            EMAIL_KEY => snapshot.email = value,
            SIGN_UP_TYPE_KEY => snapshot.sign_up_type = value,
            MEMBERSHIP_TYPE_KEY => snapshot.membership_type = value,
            SUBSCRIPTION_STATUS_KEY => snapshot.subscription_status = value,
            _ => {}
        }
    }
    if snapshot.has_session() || !snapshot.email.trim().is_empty() {
        Ok(Some(snapshot))
    } else {
        Ok(None)
    }
}

async fn open_state_db(path: &Path) -> Result<SqliteConnection> {
    if let Some(parent) = path.parent() {
        tokio::fs::create_dir_all(parent).await?;
    }
    let options = sqlx::sqlite::SqliteConnectOptions::new()
        .filename(path)
        .create_if_missing(true)
        .busy_timeout(std::time::Duration::from_secs(2));
    let mut connection = SqliteConnection::connect_with(&options).await?;
    sqlx::query(
        "CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)",
    )
    .execute(&mut connection)
    .await?;
    Ok(connection)
}

fn local_token() -> Result<String> {
    let header = URL_SAFE_NO_PAD.encode(br#"{"alg":"HS256","typ":"JWT"}"#);
    let payload = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&json!({
        "sub": SUBJECT,
        "email": EMAIL,
        "type": "session",
        "iss": ISSUER,
        "scope": "openid profile email",
        "exp": 4070908800_u64
    }))?);
    Ok(format!("{header}.{payload}.{SUBJECT}"))
}

fn is_local_token(token: &str) -> bool {
    let token = token.trim();
    if token.is_empty() {
        return false;
    }
    jwt_claim(token, "iss").as_deref() == Some(ISSUER)
        || jwt_claim(token, "sub").as_deref() == Some(SUBJECT)
        || jwt_claim(token, "email").as_deref() == Some(EMAIL)
}

fn jwt_claim(token: &str, name: &str) -> Option<String> {
    let payload = token.split('.').nth(1)?;
    let decoded = URL_SAFE_NO_PAD.decode(payload).ok()?;
    let value = serde_json::from_slice::<serde_json::Value>(&decoded).ok()?;
    value.get(name)?.as_str().map(str::to_owned)
}

fn read_legacy_backup() -> Option<CursorAccountBackup> {
    for path in legacy_backup_paths() {
        let Ok(raw) = std::fs::read_to_string(&path) else {
            continue;
        };
        if let Some(backup) = parse_legacy_backup(&raw) {
            return Some(backup);
        }
    }
    None
}

fn legacy_backup_paths() -> Vec<PathBuf> {
    if cfg!(test) {
        return Vec::new();
    }
    let mut paths = Vec::new();
    let Some(home) = dirs::home_dir() else {
        return paths;
    };
    let root = home.join(".cursor-local-assistant-v2/data");
    paths.push(root.join("cursor-auth-backup.json"));
    let accounts = root.join("cursor-accounts/accounts");
    if let Ok(entries) = std::fs::read_dir(accounts) {
        for entry in entries.flatten() {
            let path = entry.path();
            if path.extension().is_some_and(|ext| ext == "json") {
                paths.push(path);
            }
        }
    }
    paths
}

fn parse_legacy_backup(raw: &str) -> Option<CursorAccountBackup> {
    let value = serde_json::from_str::<serde_json::Value>(raw).ok()?;
    let email = value_string(&value, &["email", "cachedEmail", "cursorAuth/cachedEmail"]);
    let access_token = value_string(
        &value,
        &["accessToken", "access_token", "cursorAuth/accessToken"],
    );
    let refresh_token = value_string(
        &value,
        &["refreshToken", "refresh_token", "cursorAuth/refreshToken"],
    );
    let backup = CursorAccountBackup {
        email,
        access_token,
        refresh_token,
        sign_up_type: value_string(
            &value,
            &[
                "signUpType",
                "cachedSignUpType",
                "cursorAuth/cachedSignUpType",
            ],
        ),
        membership_type: value_string(
            &value,
            &[
                "membershipType",
                "stripeMembershipType",
                "cursorAuth/stripeMembershipType",
            ],
        ),
        subscription_status: value_string(
            &value,
            &[
                "subscriptionStatus",
                "stripeSubscriptionStatus",
                "cursorAuth/stripeSubscriptionStatus",
            ],
        ),
    };
    backup.is_original().then_some(backup)
}

fn value_string(value: &serde_json::Value, keys: &[&str]) -> String {
    keys.iter()
        .find_map(|key| value.get(*key)?.as_str())
        .unwrap_or_default()
        .trim()
        .to_owned()
}

#[cfg(test)]
mod tests {
    use sqlx::Row;

    use super::*;

    async fn memory_store() -> Store {
        Store::connect("sqlite::memory:").await.unwrap()
    }

    fn original_backup() -> CursorAccountBackup {
        CursorAccountBackup {
            email: "user@example.com".into(),
            access_token: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJnb29nbGUtb2F1dGgyfHVzZXIiLCJlbWFpbCI6InVzZXJAZXhhbXBsZS5jb20iLCJpc3MiOiJodHRwczovL2F1dGhlbnRpY2F0aW9uLmN1cnNvci5zaCJ9.sig".into(),
            refresh_token: "refresh-token".into(),
            sign_up_type: "Google".into(),
            membership_type: "pro".into(),
            subscription_status: "active".into(),
        }
    }

    #[tokio::test]
    async fn injects_the_local_account_only_when_missing() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.vscdb");

        inject_if_missing_at(&path).await.unwrap();

        let options = sqlx::sqlite::SqliteConnectOptions::new().filename(&path);
        let mut connection = SqliteConnection::connect_with(&options).await.unwrap();
        let values = sqlx::query("SELECT key, CAST(value AS TEXT) AS value FROM ItemTable")
            .fetch_all(&mut connection)
            .await
            .unwrap()
            .into_iter()
            .map(|row| (row.get::<String, _>("key"), row.get::<String, _>("value")))
            .collect::<std::collections::HashMap<_, _>>();
        let token = &values[ACCESS_TOKEN_KEY];
        assert_eq!(values[REFRESH_TOKEN_KEY], *token);
        assert_eq!(values[EMAIL_KEY], EMAIL);
        assert_eq!(values[SIGN_UP_TYPE_KEY], SIGN_UP_TYPE);
        assert_eq!(values[MEMBERSHIP_TYPE_KEY], MEMBERSHIP_TYPE);
        assert_eq!(values[SUBSCRIPTION_STATUS_KEY], SUBSCRIPTION_STATUS);
        let payload = token.split('.').nth(1).unwrap();
        let payload: serde_json::Value =
            serde_json::from_slice(&URL_SAFE_NO_PAD.decode(payload).unwrap()).unwrap();
        assert_eq!(payload["sub"], SUBJECT);
        assert_eq!(payload["email"], EMAIL);
        assert_eq!(payload["exp"], 4070908800_u64);

        sqlx::query("UPDATE ItemTable SET value = 'existing-token' WHERE key = ?")
            .bind(ACCESS_TOKEN_KEY)
            .execute(&mut connection)
            .await
            .unwrap();
        drop(connection);
        inject_if_missing_at(&path).await.unwrap();

        let options = sqlx::sqlite::SqliteConnectOptions::new().filename(&path);
        let mut connection = SqliteConnection::connect_with(&options).await.unwrap();
        let token: String =
            sqlx::query_scalar("SELECT CAST(value AS TEXT) FROM ItemTable WHERE key = ?")
                .bind(ACCESS_TOKEN_KEY)
                .fetch_one(&mut connection)
                .await
                .unwrap();
        assert_eq!(token, "existing-token");
    }

    #[tokio::test]
    async fn direct_tab_restores_the_backed_up_original_account() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.vscdb");
        let store = memory_store().await;
        let backup = original_backup();
        write_snapshot(path.as_path(), &snapshot_from_backup(&backup))
            .await
            .unwrap();

        apply_for_tab_mode_at(&store, TabMode::Public, &path)
            .await
            .unwrap();
        inject_if_missing_at(&path).await.unwrap();
        write_snapshot(
            &path,
            &CursorAuthSnapshot {
                access_token: local_token().unwrap(),
                refresh_token: local_token().unwrap(),
                email: EMAIL.into(),
                sign_up_type: SIGN_UP_TYPE.into(),
                membership_type: MEMBERSHIP_TYPE.into(),
                subscription_status: SUBSCRIPTION_STATUS.into(),
            },
        )
        .await
        .unwrap();

        let restored = apply_for_tab_mode_at(&store, TabMode::Direct, &path)
            .await
            .unwrap();
        assert_eq!(restored.source, CursorAccountSource::Original);
        assert_eq!(restored.email.as_deref(), Some("user@example.com"));
        assert!(restored.has_backup);

        let snapshot = read_snapshot(&path).await.unwrap().unwrap();
        assert_eq!(snapshot.email, "user@example.com");
        assert_eq!(snapshot.access_token, backup.access_token);
        assert!(!snapshot.is_local());
    }

    #[tokio::test]
    async fn restore_without_backup_clears_the_local_mock() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.vscdb");
        let store = memory_store().await;
        inject_if_missing_at(&path).await.unwrap();

        let status = restore_original_at(&store, &path, false).await.unwrap();
        assert_eq!(status.source, CursorAccountSource::None);
        assert!(!status.has_backup);
        assert!(read_snapshot(&path).await.unwrap().is_none());
    }

    #[tokio::test]
    async fn explicit_restore_requires_an_original_backup() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.vscdb");
        let store = memory_store().await;
        inject_if_missing_at(&path).await.unwrap();
        let error = restore_original_at(&store, &path, true)
            .await
            .unwrap_err()
            .to_string();
        assert!(error.contains("没有可恢复的 Cursor 原账号备份"));
    }

    #[test]
    fn parses_legacy_cursor_auth_backup_json() {
        let backup = parse_legacy_backup(
            r#"{
                "cursorAuth/accessToken": "eyJhbGciOiJIUzI1NiJ9.eyJpc3MiOiJodHRwczovL2F1dGhlbnRpY2F0aW9uLmN1cnNvci5zaCIsImVtYWlsIjoidXNlckBleGFtcGxlLmNvbSJ9.sig",
                "cursorAuth/cachedEmail": "user@example.com",
                "cursorAuth/refreshToken": "refresh"
            }"#,
        )
        .unwrap();
        assert_eq!(backup.email, "user@example.com");
        assert!(backup.is_original());
        assert!(parse_legacy_backup(r#"{"email":"cursor@ai.com","accessToken":"x"}"#).is_none());
    }
}
