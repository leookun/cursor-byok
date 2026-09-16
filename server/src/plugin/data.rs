//! Stores plugin-owned JSON with private permissions and atomic replacement.
use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::Arc,
};

use parking_lot::Mutex;
use tokio::sync::Mutex as AsyncMutex;

use crate::{config, Error, Result};

#[derive(Clone)]
pub struct PluginDataStore {
    root: PathBuf,
    locks: Arc<Mutex<HashMap<String, Arc<AsyncMutex<()>>>>>,
}

impl PluginDataStore {
    pub fn managed() -> Result<Self> {
        Self::new(config::managed_data_dir()?.join("plugins/data"))
    }

    #[cfg(test)]
    pub(super) fn for_test(root: PathBuf) -> Result<Self> {
        Self::new(root)
    }

    fn new(root: PathBuf) -> Result<Self> {
        std::fs::create_dir_all(&root)?;
        set_directory_permissions(&root)?;
        Ok(Self {
            root,
            locks: Arc::new(Mutex::new(HashMap::new())),
        })
    }

    pub async fn read(&self, plugin_id: &str, key: &str) -> Result<serde_json::Value> {
        let path = self.path(plugin_id, key)?;
        let lock = self.lock(plugin_id);
        let _guard = lock.lock().await;
        match tokio::fs::read(&path).await {
            Ok(bytes) => Ok(serde_json::from_slice(&bytes)?),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                Ok(serde_json::Value::Null)
            }
            Err(error) => Err(Error::Config(format!(
                "plugin data read failed at {}: {error}",
                path.display()
            ))),
        }
    }

    pub async fn update(
        &self,
        plugin_id: &str,
        key: &str,
        value: &serde_json::Value,
    ) -> Result<()> {
        let path = self.path(plugin_id, key)?;
        let lock = self.lock(plugin_id);
        let _guard = lock.lock().await;
        self.write_locked(&path, key, value)
            .await
            // 带上具体路径,Windows 上的拒绝访问才能定位到是哪一步。
            .map_err(|error| {
                Error::Config(format!(
                    "plugin data write failed at {}: {error}",
                    path.display()
                ))
            })
    }

    /// 全程使用同步 IO 在阻塞线程完成:tokio 异步文件的关闭是延迟的,
    /// 替换前句柄可能仍被本进程持有;同步写入保证替换时句柄已确定关闭。
    async fn write_locked(&self, path: &Path, key: &str, value: &serde_json::Value) -> Result<()> {
        let directory = path
            .parent()
            .expect("plugin data path has a parent")
            .to_owned();
        let temporary = directory.join(format!(".{key}.{}.tmp", uuid::Uuid::new_v4()));
        let target = path.to_owned();
        let bytes = serde_json::to_vec_pretty(value)?;
        tokio::task::spawn_blocking(move || {
            // Windows 上杀软或索引器会短暂锁住新建文件,任何一步都可能
            // 拒绝访问,因此把整个序列作为一个整体重试。
            let mut attempts = 0;
            loop {
                match write_once(&directory, &temporary, &target, &bytes) {
                    Ok(()) => return Ok(()),
                    Err((step, error)) if attempts < 20 && transient(&error) => {
                        attempts += 1;
                        tracing::debug!(step, attempts, %error, "retrying plugin data write");
                        std::thread::sleep(std::time::Duration::from_millis(100));
                    }
                    Err((step, error)) => {
                        let _ = std::fs::remove_file(&temporary);
                        tracing::warn!(
                            path = %target.display(),
                            step,
                            attempts,
                            %error,
                            "plugin data write failed"
                        );
                        return Err(Error::Config(format!("{step}: {error}")));
                    }
                }
            }
        })
        .await
        .expect("plugin data write task panicked")
    }

    pub async fn clear(&self, plugin_id: &str) -> Result<()> {
        validate_component(plugin_id, "plugin id")?;
        let lock = self.lock(plugin_id);
        let _guard = lock.lock().await;
        let path = self.root.join(plugin_id);
        match tokio::fs::remove_dir_all(&path).await {
            Ok(()) => Ok(()),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(error) => Err(Error::Config(format!(
                "plugin data cleanup failed at {}: {error}",
                path.display()
            ))),
        }
    }

    fn path(&self, plugin_id: &str, key: &str) -> Result<PathBuf> {
        validate_component(plugin_id, "plugin id")?;
        validate_component(key, "plugin data key")?;
        Ok(self.root.join(plugin_id).join(format!("{key}.json")))
    }

    fn lock(&self, plugin_id: &str) -> Arc<AsyncMutex<()>> {
        self.locks
            .lock()
            .entry(plugin_id.to_owned())
            .or_insert_with(|| Arc::new(AsyncMutex::new(())))
            .clone()
    }
}

/// 单次完整写入:建目录、写临时文件、落盘、原子替换。
/// 失败时返回失败步骤的标签,供上层区分重试与报错。
fn write_once(
    directory: &Path,
    temporary: &Path,
    target: &Path,
    bytes: &[u8],
) -> std::result::Result<(), (&'static str, std::io::Error)> {
    use std::io::Write;
    std::fs::create_dir_all(directory).map_err(|error| ("create data directory", error))?;
    let _ = set_directory_permissions(directory);
    let mut file =
        std::fs::File::create(temporary).map_err(|error| ("create temporary file", error))?;
    file.write_all(bytes)
        .map_err(|error| ("write temporary file", error))?;
    file.sync_all()
        .map_err(|error| ("sync temporary file", error))?;
    drop(file);
    let _ = set_file_permissions(temporary);
    // Replace atomically. Never delete the last valid rotated credential before replacement.
    std::fs::rename(temporary, target).map_err(|error| ("replace target file", error))?;
    let _ = set_file_permissions(target);
    Ok(())
}

/// Windows 下拒绝访问(5)与共享冲突(32)通常是杀软或索引器的
/// 瞬时锁定,值得重试;其余错误与其他平台一律直接失败。
fn transient(error: &std::io::Error) -> bool {
    #[cfg(windows)]
    {
        const ACCESS_DENIED: i32 = 5;
        const SHARING_VIOLATION: i32 = 32;
        matches!(
            error.raw_os_error(),
            Some(ACCESS_DENIED | SHARING_VIOLATION)
        )
    }
    #[cfg(not(windows))]
    {
        let _ = error;
        false
    }
}

fn validate_component(value: &str, label: &str) -> Result<()> {
    if value.is_empty()
        || value.len() > 128
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b'-'))
    {
        return Err(Error::Config(format!("invalid {label}: {value}")));
    }
    Ok(())
}

fn set_directory_permissions(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))?;
    }
    #[cfg(not(unix))]
    {
        let _ = path;
    }
    Ok(())
}

fn set_file_permissions(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))?;
    }
    #[cfg(not(unix))]
    {
        let _ = path;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn credential_replacement_has_no_missing_or_partial_file_window() {
        use std::sync::{
            atomic::{AtomicBool, Ordering},
            Arc,
        };
        let root = tempfile::tempdir().unwrap();
        let store = PluginDataStore::for_test(root.path().join("data")).unwrap();
        store
            .update(
                "test.plugin",
                "tokens",
                &serde_json::json!({"refreshToken":0}),
            )
            .await
            .unwrap();
        let path = store.path("test.plugin", "tokens").unwrap();
        let done = Arc::new(AtomicBool::new(false));
        let reader_done = done.clone();
        let reader = std::thread::spawn(move || {
            while !reader_done.load(Ordering::Relaxed) {
                let bytes =
                    std::fs::read(&path).expect("credentials disappeared during replacement");
                let value: serde_json::Value =
                    serde_json::from_slice(&bytes).expect("partial credential write");
                assert!(value["refreshToken"].is_number());
            }
        });
        for id in 1..100 {
            store
                .update(
                    "test.plugin",
                    "tokens",
                    &serde_json::json!({"refreshToken":id}),
                )
                .await
                .unwrap();
        }
        done.store(true, Ordering::Relaxed);
        reader.join().unwrap();
    }
    #[tokio::test]
    async fn writes_reads_and_removes_json() {
        let root = tempfile::tempdir().unwrap();
        let store = PluginDataStore::new(root.path().join("data")).unwrap();
        store
            .update(
                "com.example",
                "state",
                &serde_json::json!({"token":"secret"}),
            )
            .await
            .unwrap();
        assert_eq!(
            store.read("com.example", "state").await.unwrap()["token"],
            "secret"
        );
        store.clear("com.example").await.unwrap();
        assert!(store.read("com.example", "state").await.unwrap().is_null());
    }
}
