use axum::{extract::State, Json};

use crate::{
    harness::{CursorAccountStatus, CursorHarnessStatus, SetEnabled},
    Result,
};

use super::ControlService;

pub async fn status(State(service): State<ControlService>) -> Result<Json<CursorHarnessStatus>> {
    Ok(Json(service.cursor_harness().status().await?))
}

pub async fn initialize_ca(
    State(service): State<ControlService>,
) -> Result<Json<CursorHarnessStatus>> {
    Ok(Json(service.cursor_harness().initialize_ca().await?))
}

pub async fn set_enabled(
    State(service): State<ControlService>,
    Json(input): Json<SetEnabled>,
) -> Result<Json<CursorHarnessStatus>> {
    Ok(Json(
        service.cursor_harness().set_enabled(input.enabled).await?,
    ))
}

pub async fn account(State(service): State<ControlService>) -> Result<Json<CursorAccountStatus>> {
    Ok(Json(service.cursor_harness().cursor_account().await?))
}

pub async fn restore_account(
    State(service): State<ControlService>,
) -> Result<Json<CursorAccountStatus>> {
    Ok(Json(
        service.cursor_harness().restore_cursor_account().await?,
    ))
}
