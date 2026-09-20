//! Tracks Deno runtime readiness and coordinates one initialization task.
use std::{
    path::PathBuf,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
};

use parking_lot::{Mutex, RwLock};
use serde::Serialize;
use tokio_util::sync::CancellationToken;

use super::{
    asset::{RuntimeAsset, DENO_VERSION},
    installation,
};
use crate::{config, store::Store, Error, Result};

#[derive(Clone)]
pub struct PluginRuntime {
    inner: Arc<PluginRuntimeInner>,
}

struct PluginRuntimeInner {
    root: PathBuf,
    asset: Option<RuntimeAsset>,
    status: RwLock<PluginRuntimeStatus>,
    initializing: AtomicBool,
    cancellation: Mutex<Option<CancellationToken>>,
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum PluginRuntimeState {
    Uninitialized,
    Initializing,
    Ready,
    Failed,
    Unsupported,
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum PluginRuntimePhase {
    Checking,
    Downloading,
    Verifying,
    Installing,
    Validating,
}

#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct PluginRuntimeStatus {
    pub state: PluginRuntimeState,
    pub version: String,
    pub target: Option<String>,
    pub phase: Option<PluginRuntimePhase>,
    pub downloaded_bytes: u64,
    pub total_bytes: Option<u64>,
    pub error: Option<String>,
}

impl PluginRuntimeStatus {
    fn uninitialized(asset: RuntimeAsset) -> Self {
        Self::new(PluginRuntimeState::Uninitialized, Some(asset))
    }

    fn ready(asset: RuntimeAsset) -> Self {
        Self::new(PluginRuntimeState::Ready, Some(asset))
    }

    fn unsupported() -> Self {
        Self {
            state: PluginRuntimeState::Unsupported,
            version: DENO_VERSION.into(),
            target: None,
            phase: None,
            downloaded_bytes: 0,
            total_bytes: None,
            error: Some(format!(
                "unsupported platform: {}/{}",
                std::env::consts::OS,
                std::env::consts::ARCH
            )),
        }
    }

    fn new(state: PluginRuntimeState, asset: Option<RuntimeAsset>) -> Self {
        Self {
            state,
            version: DENO_VERSION.into(),
            target: asset.map(|value| value.target.into()),
            phase: None,
            downloaded_bytes: 0,
            total_bytes: None,
            error: None,
        }
    }
}

impl PluginRuntime {
    #[cfg(test)]
    pub(super) fn for_test() -> Self {
        Self {
            inner: Arc::new(PluginRuntimeInner {
                root: PathBuf::new(),
                asset: None,
                status: RwLock::new(PluginRuntimeStatus::unsupported()),
                initializing: AtomicBool::new(false),
                cancellation: Mutex::new(None),
            }),
        }
    }
    pub fn managed() -> Result<Self> {
        Self::new(config::managed_data_dir()?.join("plugins").join("runtime"))
    }

    fn new(root: PathBuf) -> Result<Self> {
        std::fs::create_dir_all(&root)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&root, std::fs::Permissions::from_mode(0o700))?;
        }
        let asset = RuntimeAsset::current();
        let status = match asset {
            Some(asset) if installation::runtime_complete(&root, asset) => {
                PluginRuntimeStatus::ready(asset)
            }
            Some(asset) => PluginRuntimeStatus::uninitialized(asset),
            None => PluginRuntimeStatus::unsupported(),
        };
        Ok(Self {
            inner: Arc::new(PluginRuntimeInner {
                root,
                asset,
                status: RwLock::new(status),
                initializing: AtomicBool::new(false),
                cancellation: Mutex::new(None),
            }),
        })
    }

    pub fn status(&self) -> PluginRuntimeStatus {
        let mut status = self.inner.status.write();
        if status.state == PluginRuntimeState::Ready {
            if let Some(asset) = self.inner.asset {
                if !installation::runtime_complete(&self.inner.root, asset) {
                    *status = PluginRuntimeStatus::uninitialized(asset);
                }
            }
        }
        status.clone()
    }

    pub fn executable(&self) -> Option<PathBuf> {
        let asset = self.inner.asset?;
        if self.status().state != PluginRuntimeState::Ready {
            return None;
        }
        Some(installation::runtime_executable(&self.inner.root, asset))
    }

    pub fn initialize(&self, store: Store) -> PluginRuntimeStatus {
        let Some(asset) = self.inner.asset else {
            return self.status();
        };
        if installation::runtime_complete(&self.inner.root, asset) {
            let ready = PluginRuntimeStatus::ready(asset);
            *self.inner.status.write() = ready.clone();
            return ready;
        }
        if self
            .inner
            .initializing
            .compare_exchange(false, true, Ordering::AcqRel, Ordering::Acquire)
            .is_err()
        {
            return self.status();
        }

        let mut initializing =
            PluginRuntimeStatus::new(PluginRuntimeState::Initializing, Some(asset));
        initializing.phase = Some(PluginRuntimePhase::Checking);
        *self.inner.status.write() = initializing.clone();

        let cancellation = CancellationToken::new();
        *self.inner.cancellation.lock() = Some(cancellation.clone());
        let runtime = self.clone();
        tokio::spawn(async move {
            let result = installation::install(
                &runtime.inner.root,
                &store,
                asset,
                cancellation,
                |phase, downloaded, total| {
                    runtime.update_progress(phase, downloaded, total);
                },
            )
            .await;
            let status = match result {
                Ok(()) => PluginRuntimeStatus::ready(asset),
                Err(Error::Cancelled) => PluginRuntimeStatus::uninitialized(asset),
                Err(error) => {
                    tracing::error!(%error, target = asset.target, "plugin runtime initialization failed");
                    let mut failed =
                        PluginRuntimeStatus::new(PluginRuntimeState::Failed, Some(asset));
                    failed.error = Some("plugin runtime initialization failed".into());
                    failed
                }
            };
            *runtime.inner.status.write() = status;
            runtime.inner.cancellation.lock().take();
            runtime.inner.initializing.store(false, Ordering::Release);
        });

        initializing
    }

    pub fn cancel_initialization(&self) -> PluginRuntimeStatus {
        if let Some(cancellation) = self.inner.cancellation.lock().as_ref() {
            cancellation.cancel();
        }
        self.status()
    }

    fn update_progress(
        &self,
        phase: PluginRuntimePhase,
        downloaded_bytes: u64,
        total_bytes: Option<u64>,
    ) {
        let mut status = self.inner.status.write();
        status.state = PluginRuntimeState::Initializing;
        status.phase = Some(phase);
        status.downloaded_bytes = downloaded_bytes;
        status.total_bytes = total_bytes;
        status.error = None;
    }
}
