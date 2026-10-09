//! Exposes the local desktop application integration.
mod account;
mod agent_tls;
mod ca;
mod process;
mod proxy;
mod settings;

use std::{net::SocketAddr, sync::Arc};

use parking_lot::RwLock;
use serde::{Deserialize, Serialize};
use tokio::sync::Mutex;

use crate::{
    store::{Store, TabMode, TabSettings},
    Error, Result,
};

use self::{agent_tls::AgentEndpoint, ca::CaManager, proxy::ProxyRuntime};

pub(crate) fn proxy_host_allowed(host: &str) -> bool {
    proxy::is_cursor_host(host)
}

pub(crate) fn request_uses_local_cursor_token(headers: &axum::http::HeaderMap) -> bool {
    headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .is_some_and(account::is_local_cursor_authorization)
}

#[cfg(test)]
pub(crate) fn local_cursor_authorization() -> String {
    format!("Bearer {}", account::local_token().unwrap())
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CaState {
    Missing,
    Untrusted,
    Ready,
    Invalid,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum IntegrationState {
    Disabled,
    Enabled,
    Degraded,
}

#[derive(Clone, Debug, Serialize)]
pub struct CursorHarnessStatus {
    pub platform: &'static str,
    pub ca: CaState,
    pub configured_models: usize,
    pub enabled_models: usize,
    pub integration: IntegrationState,
    pub settings_applied: bool,
    pub proxy_url: Option<String>,
    pub ca_install_command: Option<String>,
}

#[derive(Clone, Copy, Debug, Deserialize)]
pub struct SetEnabled {
    pub enabled: bool,
}

#[derive(Clone)]
pub struct CursorHarness {
    inner: Arc<Inner>,
}

struct Inner {
    store: Store,
    ca: CaManager,
    ca_initialization: Mutex<()>,
    backend_addr: RwLock<Option<SocketAddr>>,
    agent_router: RwLock<Option<axum::Router>>,
    tab_mode: Arc<RwLock<TabMode>>,
    proxy: Mutex<ProxyRuntime>,
    agent: Mutex<Option<AgentEndpoint>>,
}

impl CursorHarness {
    pub fn new(store: Store) -> Result<Self> {
        Ok(Self {
            inner: Arc::new(Inner {
                store,
                ca: CaManager::managed()?,
                ca_initialization: Mutex::new(()),
                backend_addr: RwLock::new(None),
                agent_router: RwLock::new(None),
                tab_mode: Arc::new(RwLock::new(TabMode::default())),
                proxy: Mutex::new(ProxyRuntime::default()),
                agent: Mutex::new(None),
            }),
        })
    }

    pub fn set_backend_addr(&self, addr: SocketAddr) {
        *self.inner.backend_addr.write() = Some(addr);
    }

    /// Sets the routes served on Cursor's own HTTP/2 agent connection.
    pub fn set_agent_router(&self, router: axum::Router) {
        *self.inner.agent_router.write() = Some(router);
    }

    pub async fn proxy_port(&self) -> Option<u16> {
        self.inner.proxy.lock().await.port()
    }

    pub async fn cleanup_stale_settings(&self) -> Result<()> {
        settings::clear_stale_managed_settings()
    }

    pub async fn status(&self) -> Result<CursorHarnessStatus> {
        let models = self.inner.store.models().await?;
        let configured_models = models.len();
        let enabled_models = configured_models;
        let ca = self.inner.ca.state()?;
        if self.inner.store.cursor_takeover_enabled().await?
            && matches!(ca, CaState::Ready)
            && self.inner.backend_addr.read().is_some()
        {
            self.enable().await?;
        }
        let proxy = self.inner.proxy.lock().await;
        let proxy_url = proxy.url();
        let settings_applied = proxy_url
            .as_deref()
            .map(settings::settings_match)
            .transpose()?
            .unwrap_or(false);
        let integration = match (proxy.running(), settings_applied) {
            (false, false) => IntegrationState::Disabled,
            (true, true) => IntegrationState::Enabled,
            _ => IntegrationState::Degraded,
        };
        Ok(CursorHarnessStatus {
            platform: std::env::consts::OS,
            ca,
            configured_models,
            enabled_models,
            integration,
            settings_applied,
            proxy_url,
            ca_install_command: self.inner.ca.install_command(),
        })
    }

    pub async fn initialize_ca(&self) -> Result<CursorHarnessStatus> {
        let _initialization = self.inner.ca_initialization.lock().await;
        let manager = self.inner.ca.clone();
        tokio::task::spawn_blocking(move || manager.initialize_local())
            .await
            .map_err(|error| Error::Store(format!("CA initialization task failed: {error}")))??;
        self.status().await
    }

    pub async fn set_enabled(&self, enabled: bool) -> Result<CursorHarnessStatus> {
        if enabled {
            self.inner.store.set_cursor_takeover_enabled(true).await?;
            self.enable().await?;
        } else {
            self.inner.store.set_cursor_takeover_enabled(false).await?;
            // Cursor caches the listener as its agent URL and only refetches
            // server config every few minutes; a restart drops it now.
            if self.disable().await? {
                if let Err(error) = process::terminate_cursor().await {
                    tracing::warn!(%error, "could not terminate Cursor after disabling takeover");
                }
            }
        }
        self.status().await
    }

    pub async fn set_tab_settings(&self, settings: TabSettings) -> Result<TabSettings> {
        let saved = self.inner.store.set_tab_settings(settings).await?;
        *self.inner.tab_mode.write() = saved.mode;
        Ok(saved)
    }

    async fn enable(&self) -> Result<()> {
        if !matches!(self.inner.ca.state()?, CaState::Ready) {
            return Err(Error::Config(
                "initialize and trust the CA before enabling Cursor".into(),
            ));
        }
        let backend_addr = self
            .inner
            .backend_addr
            .read()
            .ok_or_else(|| Error::Config("desktop management server is not ready".into()))?;
        let mut proxy = self.inner.proxy.lock().await;
        let settings_applied = proxy
            .url()
            .as_deref()
            .map(settings::settings_match)
            .transpose()?
            .unwrap_or(false);
        if !settings_applied {
            // Terminating Cursor only makes the freshly written http.proxy take effect
            // sooner; it is optional, so a failed probe or kill must not block takeover.
            if let Err(error) = process::terminate_cursor().await {
                tracing::warn!(%error, "could not terminate Cursor before applying proxy settings");
            }
        }
        if proxy.running() {
            if let Some(url) = proxy.url() {
                apply_cursor_configuration(&url).await?;
            }
            drop(proxy);
            self.start_agent_listener().await;
            return Ok(());
        }
        let ca = self.inner.ca.load()?;
        let requested_port = self.inner.store.port_settings().await?.proxy_port;
        *self.inner.tab_mode.write() = self.inner.store.tab_settings().await?.mode;
        let (url, actual_port) = proxy
            .start(
                backend_addr,
                ca,
                requested_port,
                self.inner.tab_mode.clone(),
            )
            .await?;
        if let Err(error) = self.inner.store.set_proxy_port(actual_port).await {
            proxy.stop().await;
            return Err(error);
        }
        if let Err(error) = apply_cursor_configuration(&url).await {
            proxy.stop().await;
            return Err(error);
        }
        drop(proxy);
        self.start_agent_listener().await;
        Ok(())
    }

    async fn start_agent_listener(&self) {
        let mut agent = self.inner.agent.lock().await;
        if agent.as_ref().is_some_and(AgentEndpoint::running) {
            return;
        }
        // Re-check under the lock so a concurrent disable cannot be undone.
        match self.inner.store.cursor_takeover_enabled().await {
            Ok(true) => {}
            Ok(false) => return,
            Err(error) => {
                tracing::warn!(%error, "HTTP/2 agent listener skipped; takeover state unreadable");
                return;
            }
        }
        let Some(router) = self.inner.agent_router.read().clone() else {
            tracing::warn!("HTTP/2 agent listener skipped; agent routes are not ready");
            return;
        };
        let ca = match self.inner.ca.load() {
            Ok(ca) => ca,
            Err(error) => {
                tracing::warn!(%error, "HTTP/2 agent listener skipped; CA is not loaded");
                return;
            }
        };
        match agent_tls::start(ca, router).await {
            Ok(endpoint) => *agent = Some(endpoint),
            Err(error) => tracing::warn!(%error, "HTTP/2 agent listener did not start"),
        }
    }

    /// Returns whether a running agent listener was stopped.
    pub async fn disable(&self) -> Result<bool> {
        settings::clear_proxy_settings()?;
        // Holding the lock keeps a concurrent enable from starting a listener
        // whose origin this stop would then clear.
        let mut agent = self.inner.agent.lock().await;
        let stopped = agent.take();
        let was_running = stopped.as_ref().is_some_and(AgentEndpoint::running);
        if let Some(endpoint) = stopped {
            endpoint.stop().await;
        }
        drop(agent);
        self.inner.proxy.lock().await.stop().await;
        Ok(was_running)
    }
}

async fn apply_cursor_configuration(proxy_url: &str) -> Result<()> {
    account::inject_if_missing().await?;
    settings::write_proxy_settings(proxy_url)
}
