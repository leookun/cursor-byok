//! Implements settings management endpoints.
use crate::Result;
use axum::{
    extract::State,
    http::{header, HeaderMap},
    Json,
};
use serde::{Deserialize, Serialize};

use crate::store::{
    CommitPromptLocale, CommitSettings, DesktopSettings, PortSettings, ProxySettings,
    ProxySettingsInput, StatisticsStorage, StatisticsStorageScope, TabSettings,
    TokenPricingSettings,
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
    input: Option<Json<ClearStorageInput>>,
) -> Result<Json<StatisticsStorage>> {
    let scope = input.map(|Json(input)| input.scope).unwrap_or_default();
    let storage = match scope {
        StatisticsStorageScope::Details => service.clear_statistics_storage().await?,
        StatisticsStorageScope::All => service.clear_all_statistics_storage().await?,
    };
    Ok(Json(storage))
}

#[derive(Deserialize)]
pub struct ClearStorageInput {
    #[serde(default)]
    pub scope: StatisticsStorageScope,
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

/// Settings view for commit message generation. Empty `model_id` means 直连
/// (forward the original Cursor RPC). A non-empty value is a configured
/// built-in or plugin model identifier. Empty `prompt` means "use the built-in default".
#[derive(Serialize)]
pub struct CommitSettingsView {
    pub model_id: String,
    pub prompt: String,
    pub prompt_locale: CommitPromptLocale,
    pub default_prompt: &'static str,
}

impl CommitSettingsView {
    fn new(settings: CommitSettings, default_locale: CommitPromptLocale) -> Self {
        Self {
            model_id: settings.model_id,
            prompt: settings.prompt,
            prompt_locale: settings.prompt_locale,
            default_prompt: default_locale.default_prompt(),
        }
    }
}

pub async fn get_commit(
    State(service): State<ControlService>,
    headers: HeaderMap,
) -> Result<Json<CommitSettingsView>> {
    let settings = service.commit_settings().await?;
    Ok(Json(CommitSettingsView::new(
        settings,
        requested_commit_locale(&headers),
    )))
}

pub async fn update_commit(
    State(service): State<ControlService>,
    Json(settings): Json<CommitSettings>,
) -> Result<Json<CommitSettingsView>> {
    let saved = service.set_commit_settings(settings).await?;
    let default_locale = saved.prompt_locale;
    Ok(Json(CommitSettingsView::new(saved, default_locale)))
}

pub async fn get_pricing_settings(
    State(service): State<ControlService>,
) -> Result<Json<TokenPricingSettings>> {
    Ok(Json(service.pricing_settings().await?))
}

pub async fn update_pricing_settings(
    State(service): State<ControlService>,
    Json(settings): Json<TokenPricingSettings>,
) -> Result<Json<TokenPricingSettings>> {
    Ok(Json(service.set_pricing_settings(settings).await?))
}

fn requested_commit_locale(headers: &HeaderMap) -> CommitPromptLocale {
    headers
        .get(header::ACCEPT_LANGUAGE)
        .and_then(|value| value.to_str().ok())
        .map(CommitPromptLocale::from_interface_language)
        .unwrap_or(CommitPromptLocale::EnUs)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn commit_default_prompt_locale_comes_from_interface_language() {
        let mut headers = HeaderMap::new();
        headers.insert(header::ACCEPT_LANGUAGE, "zh-CN".parse().unwrap());
        assert_eq!(requested_commit_locale(&headers), CommitPromptLocale::ZhCn);

        headers.insert(header::ACCEPT_LANGUAGE, "en-US".parse().unwrap());
        assert_eq!(requested_commit_locale(&headers), CommitPromptLocale::EnUs);

        headers.insert(header::ACCEPT_LANGUAGE, "pt-BR".parse().unwrap());
        assert_eq!(requested_commit_locale(&headers), CommitPromptLocale::EnUs);
    }
}
