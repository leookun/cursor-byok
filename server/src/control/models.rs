//! Implements model configuration endpoints.
use axum::{
    extract::{Path, State},
    http::StatusCode,
    Json,
};
use serde::Deserialize;

use crate::{
    model::{ModelConfig, ModelConfigInput},
    Result,
};

use super::{
    ControlService, DiscoveredModels, LegacyModelImportPreview, LegacyModelImportResult,
    ModelConnectivityResult, ModelDiscoveryInput,
};

#[derive(Deserialize)]
pub struct SaveModels {
    pub models: Vec<ModelConfigInput>,
}

#[derive(Deserialize)]
pub struct ModelOrder {
    pub model_hashes: Vec<String>,
}

/// 分组开关的批量发布开关:关闭后这些模型不再出现在 Cursor 模型选择栏。
#[derive(Deserialize)]
pub struct SetModelsEnabled {
    pub model_hashes: Vec<String>,
    pub enabled: bool,
}

pub async fn list(State(service): State<ControlService>) -> Result<Json<Vec<ModelConfig>>> {
    Ok(Json(service.models().await?))
}

pub async fn create(
    State(service): State<ControlService>,
    Json(input): Json<SaveModels>,
) -> Result<(StatusCode, Json<Vec<ModelConfig>>)> {
    Ok((
        StatusCode::CREATED,
        Json(service.create_models(&input.models).await?),
    ))
}

pub async fn reorder(
    State(service): State<ControlService>,
    Json(input): Json<ModelOrder>,
) -> Result<Json<Vec<ModelConfig>>> {
    Ok(Json(service.reorder_models(&input.model_hashes).await?))
}

pub async fn set_enabled(
    State(service): State<ControlService>,
    Json(input): Json<SetModelsEnabled>,
) -> Result<Json<Vec<ModelConfig>>> {
    Ok(Json(
        service
            .set_models_enabled(&input.model_hashes, input.enabled)
            .await?,
    ))
}

pub async fn remove(
    State(service): State<ControlService>,
    Path(model_hash): Path<String>,
) -> Result<StatusCode> {
    service.delete_model(&model_hash).await?;
    Ok(StatusCode::NO_CONTENT)
}

pub async fn update(
    State(service): State<ControlService>,
    Path(model_hash): Path<String>,
    Json(input): Json<ModelConfigInput>,
) -> Result<Json<ModelConfig>> {
    Ok(Json(service.update_model(&model_hash, &input).await?))
}

pub async fn test(
    State(service): State<ControlService>,
    Path((model_hash, test_id)): Path<(String, String)>,
) -> Result<Json<ModelConnectivityResult>> {
    Ok(Json(service.test_model(&model_hash, &test_id).await?))
}

pub async fn cancel(
    State(service): State<ControlService>,
    Path((_model_hash, test_id)): Path<(String, String)>,
) -> Result<StatusCode> {
    service.cancel_model_test(&test_id);
    Ok(StatusCode::NO_CONTENT)
}

pub async fn discover(
    State(service): State<ControlService>,
    Json(input): Json<ModelDiscoveryInput>,
) -> Result<Json<DiscoveredModels>> {
    Ok(Json(service.discover_models(&input).await?))
}

pub async fn import_v0049(
    State(service): State<ControlService>,
) -> Result<Json<LegacyModelImportResult>> {
    Ok(Json(service.import_v0049_models().await?))
}

pub async fn preview_v0049(
    State(service): State<ControlService>,
) -> Result<Json<LegacyModelImportPreview>> {
    Ok(Json(service.preview_v0049_models().await?))
}
