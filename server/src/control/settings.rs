use crate::Result;
use axum::{extract::State, Json};

use crate::store::{
    CompactionSettings, DesktopSettings, PortSettings, ProxySettings, ProxySettingsInput,
    StatisticsStorage, TabSettings,
};

use super::{ControlService, ObservabilitySettings};

pub async fn get(State(service): State<ControlService>) -> Result<Json<ObservabilitySettings>> {
    Ok(Json(service.observability().await?))
}

pub async fn update(
    State(service): State<ControlService>,
    Json(settings): Json<ObservabilitySettings>,
) -> Result<Json<ObservabilitySettings>> {
    Ok(Json(service.set_observability(settings).await?))
}

pub async fn get_ports(State(service): State<ControlService>) -> Result<Json<PortSettings>> {
    Ok(Json(service.ports().await?))
}

pub async fn update_ports(
    State(service): State<ControlService>,
    Json(settings): Json<PortSettings>,
) -> Result<Json<PortSettings>> {
    Ok(Json(service.set_ports(settings).await?))
}

pub async fn get_storage(State(service): State<ControlService>) -> Result<Json<StatisticsStorage>> {
    Ok(Json(service.statistics_storage().await?))
}

pub async fn clear_storage(
    State(service): State<ControlService>,
) -> Result<Json<StatisticsStorage>> {
    Ok(Json(service.clear_statistics_storage().await?))
}

pub async fn get_proxy(State(service): State<ControlService>) -> Result<Json<ProxySettings>> {
    Ok(Json(service.proxy_settings().await?))
}

pub async fn update_proxy(
    State(service): State<ControlService>,
    Json(settings): Json<ProxySettingsInput>,
) -> Result<Json<ProxySettings>> {
    Ok(Json(service.set_proxy_settings(settings).await?))
}

pub async fn get_tab(State(service): State<ControlService>) -> Result<Json<TabSettings>> {
    Ok(Json(service.tab_settings().await?))
}

pub async fn update_tab(
    State(service): State<ControlService>,
    Json(settings): Json<TabSettings>,
) -> Result<Json<TabSettings>> {
    Ok(Json(service.set_tab_settings(settings).await?))
}

pub async fn get_desktop(State(service): State<ControlService>) -> Result<Json<DesktopSettings>> {
    Ok(Json(service.desktop_settings().await?))
}

pub async fn update_desktop(
    State(service): State<ControlService>,
    Json(settings): Json<DesktopSettings>,
) -> Result<Json<DesktopSettings>> {
    service.set_desktop_settings(settings).await?;
    get_desktop(State(service)).await
}

pub async fn get_compaction(
    State(service): State<ControlService>,
) -> Result<Json<CompactionSettings>> {
    Ok(Json(service.compaction_settings().await?))
}

pub async fn update_compaction(
    State(service): State<ControlService>,
    Json(settings): Json<CompactionSettings>,
) -> Result<Json<CompactionSettings>> {
    Ok(Json(service.set_compaction_settings(settings).await?))
}
