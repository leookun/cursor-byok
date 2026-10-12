//! Alias management uses the same HTTP boundary as model configuration.
use axum::{
    extract::{Path, State},
    http::StatusCode,
    Json,
};
use serde::Serialize;

use super::{ControlService, ModelConnectivityResult};
use crate::{
    alias::{
        state::RouteReport, Alias, AliasInput, AliasResolver, AliasSettings, AliasSource, AliasView,
    },
    Error, Result,
};

#[derive(Serialize)]
pub struct AliasTestResult {
    pub result: Option<ModelConnectivityResult>,
    #[serde(flatten)]
    pub route: RouteReport,
    pub error: Option<String>,
}

impl ControlService {
    fn aliases(&self) -> Result<AliasResolver> {
        self.provider
            .alias_resolver()
            .ok_or_else(|| Error::Config("alias routing is unavailable".into()))
    }

    async fn save_alias(&self, id: Option<&str>, input: &AliasInput) -> Result<Alias> {
        let name = input.name.trim();
        for source in self.aliases()?.sources().await? {
            if name.eq_ignore_ascii_case(&source.request_model_id)
                || (!source.target.model_id.is_empty()
                    && name.eq_ignore_ascii_case(&source.target.model_id))
            {
                return Err(Error::Config(
                    "alias name conflicts with an existing model ID".into(),
                ));
            }
        }
        self.store.save_alias(id, input).await
    }

    async fn test_alias(&self, id: &str, test_id: &str) -> Result<AliasTestResult> {
        let alias = self
            .store
            .alias(id)
            .await?
            .ok_or_else(|| Error::RunNotFound(format!("alias {id}")))?;
        let resolver = self.aliases()?;
        let run = format!("alias-test-{}", uuid::Uuid::new_v4());
        let cancellation = {
            let mut tests = self
                .model_tests
                .lock()
                .expect("model test registry mutex poisoned");
            tests.entry(test_id.into()).or_default().clone()
        };
        resolver.state.watch_test(&run);
        let result = self
            .run_model_test(&alias.config.name, cancellation.clone(), Some(run.clone()))
            .await;
        cancellation.cancel();
        self.model_tests
            .lock()
            .expect("model test registry mutex poisoned")
            .remove(test_id);
        let route = resolver.state.take_test(&run);
        Ok(match result {
            Ok(result) => AliasTestResult {
                result: Some(result),
                route,
                error: None,
            },
            Err(error) => AliasTestResult {
                result: None,
                route,
                error: Some(error.to_string()),
            },
        })
    }
}

pub async fn list(State(service): State<ControlService>) -> Result<Json<Vec<AliasView>>> {
    Ok(Json(service.aliases()?.views().await?))
}
pub async fn sources(State(service): State<ControlService>) -> Result<Json<Vec<AliasSource>>> {
    Ok(Json(service.aliases()?.sources().await?))
}
pub async fn create(
    State(service): State<ControlService>,
    Json(input): Json<AliasInput>,
) -> Result<Json<Alias>> {
    Ok(Json(service.save_alias(None, &input).await?))
}
pub async fn update(
    State(service): State<ControlService>,
    Path(id): Path<String>,
    Json(input): Json<AliasInput>,
) -> Result<Json<Alias>> {
    Ok(Json(service.save_alias(Some(&id), &input).await?))
}
pub async fn remove(
    State(service): State<ControlService>,
    Path(id): Path<String>,
) -> Result<StatusCode> {
    service.store.delete_alias(&id).await?;
    service.aliases()?.state.remove_alias(&id);
    Ok(StatusCode::NO_CONTENT)
}
pub async fn settings(State(service): State<ControlService>) -> Result<Json<AliasSettings>> {
    Ok(Json(service.store.alias_settings().await?))
}
pub async fn save_settings(
    State(service): State<ControlService>,
    Json(input): Json<AliasSettings>,
) -> Result<Json<AliasSettings>> {
    Ok(Json(service.store.set_alias_settings(&input).await?))
}
pub async fn test(
    State(service): State<ControlService>,
    Path((id, test_id)): Path<(String, String)>,
) -> Result<Json<AliasTestResult>> {
    Ok(Json(service.test_alias(&id, &test_id).await?))
}
pub async fn cancel(
    State(service): State<ControlService>,
    Path((_id, test_id)): Path<(String, String)>,
) -> StatusCode {
    service.cancel_model_test(&test_id);
    StatusCode::NO_CONTENT
}
