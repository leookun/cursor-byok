//! Stores plugin-owned JSON with private permissions and atomic replacement.
use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::Arc,
};

use parking_lot::Mutex;
use tokio::sync::{Mutex as AsyncMutex, OwnedMutexGuard};

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
        Self::read_locked(&path).await
    }

    /// Reads and mutates related JSON documents under one shared plugin lock.
    /// The callback is synchronous: callers cannot perform network work while locked.
    /// Each changed document is atomically replaced; this is not a crash-atomic
    /// transaction across multiple files.
    pub async fn modify<T>(
        &self,
        plugin_id: &str,
        keys: &[String],
        change: impl FnOnce(&mut [serde_json::Value]) -> Result<T>,
    ) -> Result<T> {
        let paths = keys
            .iter()
            .map(|key| self.path(plugin_id, key))
            .collect::<Result<Vec<_>>>()?;
        let guard = self.lock(plugin_id).lock_owned().await;
        let mut values = Vec::with_capacity(paths.len());
        for path in &paths {
            values.push(Self::read_locked(path).await?);
        }
        let previous = values.clone();
        let result = change(&mut values)?;
        let mut writes = Vec::new();
        for (((path, key), value), old) in paths.into_iter().zip(keys).zip(&values).zip(&previous) {
            if value != old {
                writes.push((path, key.clone(), serde_json::to_vec_pretty(value)?));
            }
        }
        Self::write_locked(writes, guard).await?;
        Ok(result)
    }

    async fn read_locked(path: &Path) -> Result<serde_json::Value> {
        match tokio::fs::read(path).await {
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

    /// Blocking IO owns the lock until every replacement completes, even if the
    /// requesting future is cancelled. Handles are closed before each rename.
    async fn write_locked(
        writes: Vec<(PathBuf, String, Vec<u8>)>,
        guard: OwnedMutexGuard<()>,
    ) -> Result<()> {
        if writes.is_empty() {
            return Ok(());
        }
        tokio::task::spawn_blocking(move || {
            let _guard = guard;
            for (target, key, bytes) in writes {
                write_document(&target, &key, &bytes)?;
            }
            Ok(())
        })
        .await
        .expect("plugin data write task panicked")
    }

    pub async fn clear(&self, plugin_id: &str) -> Result<()> {
        validate_component(plugin_id, "plugin id")?;
        let guard = self.lock(plugin_id).lock_owned().await;
        let path = self.root.join(plugin_id);
        tokio::task::spawn_blocking(move || {
            let _guard = guard;
            match std::fs::remove_dir_all(&path) {
                Ok(()) => Ok(()),
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
                Err(error) => Err(Error::Config(format!(
                    "plugin data cleanup failed at {}: {error}",
                    path.display()
                ))),
            }
        })
        .await
        .expect("plugin data cleanup task panicked")
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

fn write_document(target: &Path, key: &str, bytes: &[u8]) -> Result<()> {
    let directory = target.parent().expect("plugin data path has a parent");
    let temporary = directory.join(format!(".{key}.{}.tmp", uuid::Uuid::new_v4()));
    // Windows scanners can briefly hold new files open; retry the whole sequence.
    let mut attempts = 0;
    loop {
        match write_once(directory, &temporary, target, bytes) {
            Ok(()) => return Ok(()),
            Err((step, error)) if attempts < 20 && transient(&error) => {
                attempts += 1;
                tracing::debug!(step, attempts, %error, "retrying plugin data write");
                std::thread::sleep(std::time::Duration::from_millis(100));
            }
            Err((step, error)) => {
                let _ = std::fs::remove_file(&temporary);
                tracing::warn!(
                    path = %target.display(), step, attempts, %error,
                    "plugin data write failed"
                );
                return Err(Error::Config(format!(
                    "plugin data write failed at {}: {step}: {error}",
                    target.display()
                )));
            }
        }
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
    // std::fs::rename replaces an existing file on supported platforms (including
    // Windows). Never delete the target first: readers must see old or new JSON.
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
    #[test]
    fn cancelled_write_keeps_lock_until_disk_work_finishes() {
        let root = tempfile::tempdir().unwrap();
        let store = PluginDataStore::new(root.path().join("data")).unwrap();
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        runtime.block_on(async {
            // Occupy the only blocking thread so the write is deterministically queued.
            let (release, wait) = std::sync::mpsc::channel();
            let blocker = tokio::task::spawn_blocking(move || {
                let _ = wait.recv();
            });
            let lock = store.lock("com.example");
            let guard = lock.clone().lock_owned().await;
            let path = store.path("com.example", "counter").unwrap();
            let (started, queued) = tokio::sync::oneshot::channel();
            let writer = tokio::spawn(async move {
                started.send(()).unwrap();
                PluginDataStore::write_locked(vec![(path, "counter".into(), b"1".to_vec())], guard)
                    .await
            });
            queued.await.unwrap();
            writer.abort();
            assert!(writer.await.unwrap_err().is_cancelled());
            assert!(
                lock.try_lock().is_err(),
                "queued disk work must retain the lock"
            );
            release.send(()).unwrap();
            blocker.await.unwrap();
            assert_eq!(store.read("com.example", "counter").await.unwrap(), 1);
        });
    }

    #[tokio::test]
    async fn concurrent_read_modify_write_keeps_every_update() {
        let root = tempfile::tempdir().unwrap();
        let store = PluginDataStore::new(root.path().join("data")).unwrap();
        let barrier = Arc::new(tokio::sync::Barrier::new(17));
        let mut tasks = Vec::new();
        for _ in 0..16 {
            let store = store.clone();
            let barrier = barrier.clone();
            tasks.push(tokio::spawn(async move {
                barrier.wait().await;
                store
                    .modify("com.example", &["counter".into()], |values| {
                        let count = values[0].as_u64().unwrap_or_default();
                        values[0] = serde_json::json!(count + 1);
                        Ok(())
                    })
                    .await
                    .unwrap();
            }));
        }
        barrier.wait().await;
        for task in tasks {
            task.await.unwrap();
        }
        assert_eq!(store.read("com.example", "counter").await.unwrap(), 16);
    }

    #[tokio::test]
    async fn rejected_mutation_does_not_write_any_document() {
        let root = tempfile::tempdir().unwrap();
        let store = PluginDataStore::new(root.path().join("data")).unwrap();
        let result: Result<()> = store
            .modify("com.example", &["a".into(), "b".into()], |values| {
                values[0] = serde_json::json!("changed");
                Err(Error::Config("rejected".into()))
            })
            .await;
        assert!(result.is_err());
        assert!(store.read("com.example", "a").await.unwrap().is_null());
        assert!(store.read("com.example", "b").await.unwrap().is_null());
    }

    #[tokio::test]
    async fn writes_reads_and_removes_json() {
        let root = tempfile::tempdir().unwrap();
        let store = PluginDataStore::new(root.path().join("data")).unwrap();
        store
            .modify("com.example", &["state".into()], |values| {
                values[0] = serde_json::json!({"token":"secret"});
                Ok(())
            })
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
