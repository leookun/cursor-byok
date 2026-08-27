use axum::{
    extract::{Path, State},
    Json,
};
use serde::{Deserialize, Serialize};

use crate::{
    store::SubscriptionAccount,
    subscription::{account_identity, parse_imported_credentials, SubscriptionKind},
    Error, Result,
};

use super::ControlService;

#[derive(Debug, Deserialize)]
pub struct UpsertAccountInput {
    pub access_token: String,
    pub refresh_token: Option<String>,
    pub display_name: Option<String>,
}

#[derive(Debug, Deserialize)]
pub struct ImportAccountsInput {
    pub provider: Option<String>,
    pub files: Vec<ImportAccountFile>,
}

#[derive(Debug, Deserialize)]
pub struct ImportAccountFile {
    pub name: String,
    pub content: serde_json::Value,
}

#[derive(Debug, Serialize)]
pub struct ImportAccountsResult {
    pub imported: usize,
    pub skipped: usize,
    pub imported_names: Vec<String>,
    pub errors: Vec<ImportAccountError>,
}

#[derive(Debug, Serialize)]
pub struct ImportAccountError {
    pub name: String,
    pub message: String,
}

#[derive(Debug, Serialize)]
pub struct AccountResponse {
    pub account_id: String,
    pub provider: String,
    pub display_name: String,
    pub plan_label: Option<String>,
    pub remaining_percent: Option<f64>,
    pub used_percent: Option<f64>,
    pub reset_at_ms: Option<i64>,
    pub session_remaining_percent: Option<f64>,
    pub session_reset_at_ms: Option<i64>,
    pub limit_reached: bool,
    pub active: bool,
}

impl From<SubscriptionAccount> for AccountResponse {
    fn from(account: SubscriptionAccount) -> Self {
        Self {
            account_id: account.account_id,
            provider: account.provider.as_str().into(),
            display_name: account.display_name,
            plan_label: account.plan_label,
            remaining_percent: account.remaining_percent,
            used_percent: account.used_percent,
            reset_at_ms: account.reset_at_ms,
            session_remaining_percent: account.session_remaining_percent,
            session_reset_at_ms: account.session_reset_at_ms,
            limit_reached: account.limit_reached,
            active: account.active,
        }
    }
}

pub async fn list_grok(State(service): State<ControlService>) -> Result<Json<Vec<AccountResponse>>> {
    list(service, SubscriptionKind::Grok).await
}

pub async fn list_codex(State(service): State<ControlService>) -> Result<Json<Vec<AccountResponse>>> {
    list(service, SubscriptionKind::Codex).await
}

pub async fn upsert_grok(
    State(service): State<ControlService>,
    Json(input): Json<UpsertAccountInput>,
) -> Result<Json<AccountResponse>> {
    upsert(service, SubscriptionKind::Grok, input).await
}

pub async fn upsert_codex(
    State(service): State<ControlService>,
    Json(input): Json<UpsertAccountInput>,
) -> Result<Json<AccountResponse>> {
    upsert(service, SubscriptionKind::Codex, input).await
}

pub async fn activate_grok(
    State(service): State<ControlService>,
    Path(account_id): Path<String>,
) -> Result<Json<AccountResponse>> {
    activate(service, SubscriptionKind::Grok, &account_id).await
}

pub async fn activate_codex(
    State(service): State<ControlService>,
    Path(account_id): Path<String>,
) -> Result<Json<AccountResponse>> {
    activate(service, SubscriptionKind::Codex, &account_id).await
}

pub async fn delete_grok(
    State(service): State<ControlService>,
    Path(account_id): Path<String>,
) -> Result<Json<Vec<AccountResponse>>> {
    delete(service, SubscriptionKind::Grok, &account_id).await
}

pub async fn delete_codex(
    State(service): State<ControlService>,
    Path(account_id): Path<String>,
) -> Result<Json<Vec<AccountResponse>>> {
    delete(service, SubscriptionKind::Codex, &account_id).await
}

async fn list(
    service: ControlService,
    kind: SubscriptionKind,
) -> Result<Json<Vec<AccountResponse>>> {
    let accounts = service
        .store()
        .subscription_accounts(kind)
        .await?
        .into_iter()
        .map(AccountResponse::from)
        .collect();
    Ok(Json(accounts))
}

async fn upsert(
    service: ControlService,
    kind: SubscriptionKind,
    input: UpsertAccountInput,
) -> Result<Json<AccountResponse>> {
    let access_token = input.access_token.trim();
    if access_token.is_empty() {
        return Err(Error::Config("missing subscription access token".into()));
    }
    let (account_id, jwt_name) = account_identity(kind, access_token);
    let display_name = optional_text(input.display_name.as_deref()).unwrap_or(jwt_name);
    let refresh = optional_text(input.refresh_token.as_deref());
    let account = service
        .store()
        .upsert_subscription_account(
            kind,
            &account_id,
            access_token,
            refresh.as_deref(),
            &display_name,
            true,
        )
        .await?;
    Ok(Json(account.into()))
}

pub async fn import_accounts(
    State(service): State<ControlService>,
    Json(input): Json<ImportAccountsInput>,
) -> Result<Json<ImportAccountsResult>> {
    let filter = input
        .provider
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(SubscriptionKind::parse)
        .transpose()?;
    let mut imported = 0;
    let mut skipped = 0;
    let mut imported_names = Vec::new();
    let mut errors = Vec::new();
    let mut grok_active = service
        .store()
        .active_subscription_account(SubscriptionKind::Grok)
        .await?
        .is_some();
    let mut codex_active = service
        .store()
        .active_subscription_account(SubscriptionKind::Codex)
        .await?
        .is_some();
    for file in input.files {
        match parse_imported_credentials(&file.name, &file.content) {
            Ok(credentials) => {
                if credentials.is_empty() {
                    skipped += 1;
                    continue;
                }
                for credential in credentials {
                    if filter.is_some_and(|kind| kind != credential.kind) {
                        errors.push(ImportAccountError {
                            name: file.name.clone(),
                            message: format!(
                                "{} is a {} credential",
                                file.name,
                                credential.kind.label()
                            ),
                        });
                        continue;
                    }
                    let (account_id, jwt_name) =
                        account_identity(credential.kind, &credential.access_token);
                    let display_name = credential.display_name.unwrap_or(jwt_name);
                    let make_active = match credential.kind {
                        SubscriptionKind::Grok => !grok_active,
                        SubscriptionKind::Codex => !codex_active,
                    };
                    if let Err(error) = service
                        .store()
                        .upsert_subscription_account(
                            credential.kind,
                            &account_id,
                            &credential.access_token,
                            credential.refresh_token.as_deref(),
                            &display_name,
                            make_active,
                        )
                        .await
                    {
                        errors.push(ImportAccountError {
                            name: file.name.clone(),
                            message: error.to_string(),
                        });
                        continue;
                    }
                    if make_active {
                        match credential.kind {
                            SubscriptionKind::Grok => grok_active = true,
                            SubscriptionKind::Codex => codex_active = true,
                        }
                    }
                    imported += 1;
                    imported_names.push(display_name);
                }
            }
            Err(error) => errors.push(ImportAccountError {
                name: file.name,
                message: error.to_string(),
            }),
        }
    }
    Ok(Json(ImportAccountsResult {
        imported,
        skipped,
        imported_names,
        errors,
    }))
}

fn optional_text(value: Option<&str>) -> Option<String> {
    value
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToString::to_string)
}

async fn activate(
    service: ControlService,
    kind: SubscriptionKind,
    account_id: &str,
) -> Result<Json<AccountResponse>> {
    let account = service
        .store()
        .activate_subscription_account(account_id)
        .await?;
    if account.provider != kind {
        return Err(Error::Config("subscription account provider mismatch".into()));
    }
    Ok(Json(account.into()))
}

async fn delete(
    service: ControlService,
    kind: SubscriptionKind,
    account_id: &str,
) -> Result<Json<Vec<AccountResponse>>> {
    let existing = service
        .store()
        .subscription_account(account_id)
        .await?
        .ok_or_else(|| Error::RunNotFound(format!("subscription account {account_id}")))?;
    if existing.provider != kind {
        return Err(Error::Config("subscription account provider mismatch".into()));
    }
    service.store().delete_subscription_account(account_id).await?;
    list(service, kind).await
}
