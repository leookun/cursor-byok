//! Conversation-owned fetched content, persisted by opaque ID and read in bounded pages.
use std::{
    collections::HashMap,
    fs,
    path::PathBuf,
    sync::Arc,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::{config::managed_data_dir, Error, Result};

pub const WEB_CACHE_PAGE_BYTES: usize = 16 * 1024;
const TTL: Duration = Duration::from_secs(24 * 60 * 60);
const MAX_ENTRY_BYTES: usize = 16 * 1024 * 1024;
const MAX_CACHE_BYTES: usize = 64 * 1024 * 1024;
const MAX_ENTRIES: usize = 256;
// JSON escaping can expand a byte to six bytes.
const MAX_RECORD_BYTES: u64 = (MAX_ENTRY_BYTES * 6 + 16 * 1024) as u64;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WebCacheEntry {
    pub content_id: String,
    pub total_bytes: usize,
    pub offset: usize,
    pub next_offset: Option<usize>,
}

#[derive(Clone, Default)]
pub struct WebCache {
    inner: Arc<Mutex<CacheState>>,
}

#[derive(Default)]
struct CacheState {
    directory: Option<PathBuf>,
    entries: HashMap<Uuid, Record>,
}

#[derive(Serialize, Deserialize)]
struct Record {
    conversation_id: String,
    url: String,
    created_at: u64,
    content: String,
}

impl WebCache {
    pub fn managed() -> Result<Self> {
        Self::at(managed_data_dir()?.join("cache").join("web"))
    }

    pub fn at(directory: PathBuf) -> Result<Self> {
        fs::create_dir_all(&directory)?;
        let mut state = CacheState {
            directory: Some(directory.clone()),
            ..Default::default()
        };
        // Recover ownership and content together after restart. Never follow paths from records.
        for entry in fs::read_dir(&directory)? {
            let entry = entry?;
            if !entry.file_type()?.is_file() {
                continue;
            }
            let path = entry.path();
            let Some(id) = path
                .file_stem()
                .and_then(|s| s.to_str())
                .and_then(|s| parse_id(s).ok())
            else {
                continue;
            };
            // Remove obsolete public-download files rather than retaining a second access path.
            if path.extension().is_some_and(|s| s == "txt" || s == "tmp") {
                fs::remove_file(path)?;
                continue;
            }
            if !path.extension().is_some_and(|s| s == "json") {
                continue;
            }
            let record = if entry.metadata()?.len() <= MAX_RECORD_BYTES {
                serde_json::from_slice::<Record>(&fs::read(&path)?).ok()
            } else {
                None
            };
            match record {
                Some(record)
                    if record.content.len() <= MAX_ENTRY_BYTES
                        && !expired(record.created_at, now()) =>
                {
                    state.entries.insert(id, record);
                    state.cleanup(now())?;
                }
                _ => fs::remove_file(path)?,
            }
        }
        state.cleanup(now())?;
        Ok(Self {
            inner: Arc::new(Mutex::new(state)),
        })
    }

    pub async fn store(
        &self,
        conversation_id: &str,
        url: &str,
        content: String,
    ) -> Result<(String, WebCacheEntry)> {
        if conversation_id.is_empty() {
            return Err(invalid("WebFetch requires a conversation ID"));
        }
        if content.len() > MAX_ENTRY_BYTES {
            return Err(invalid("fetched content exceeds the 16 MiB cache limit"));
        }
        let cache = self.clone();
        let conversation_id = conversation_id.to_owned();
        let url = url.to_owned();
        blocking(move || {
            let mut state = cache.inner.lock();
            state.cleanup_to(now(), MAX_CACHE_BYTES - content.len(), MAX_ENTRIES - 1)?;
            let id = Uuid::new_v4();
            let record = Record {
                conversation_id,
                url,
                created_at: now(),
                content,
            };
            if let Some(directory) = &state.directory {
                let path = directory.join(format!("{id}.json"));
                // Atomic publication prevents partial records from being recovered after restart.
                let temporary = directory.join(format!("{id}.tmp"));
                fs::write(&temporary, serde_json::to_vec(&record)?)?;
                if let Err(error) = fs::rename(&temporary, &path) {
                    let _ = fs::remove_file(temporary);
                    return Err(error.into());
                }
            }
            let page = page(id, &record.content, 0, WEB_CACHE_PAGE_BYTES)?;
            state.entries.insert(id, record);
            state.cleanup(now())?;
            Ok(page)
        })
        .await
    }

    pub async fn read(
        &self,
        conversation_id: &str,
        content_id: &str,
        offset: usize,
        limit: usize,
    ) -> Result<(String, String, WebCacheEntry)> {
        let id = parse_id(content_id)?;
        if !(4..=WEB_CACHE_PAGE_BYTES).contains(&limit) {
            return Err(invalid("WebFetch limit must be between 4 and 16384 bytes"));
        }
        let conversation_id = conversation_id.to_owned();
        let cache = self.clone();
        blocking(move || {
            let mut state = cache.inner.lock();
            state.cleanup(now())?;
            let record = state
                .entries
                .get(&id)
                .filter(|r| !conversation_id.is_empty() && r.conversation_id == conversation_id)
                .ok_or_else(|| {
                    invalid(
                        "cached content is unavailable or expired; fetch the original URL again",
                    )
                })?;
            let (content, entry) = page(id, &record.content, offset, limit)?;
            Ok((record.url.clone(), content, entry))
        })
        .await
    }
}

impl CacheState {
    fn cleanup(&mut self, timestamp: u64) -> Result<()> {
        self.cleanup_to(timestamp, MAX_CACHE_BYTES, MAX_ENTRIES)
    }

    fn cleanup_to(&mut self, timestamp: u64, budget: usize, max_entries: usize) -> Result<()> {
        let mut ordered = self
            .entries
            .iter()
            .map(|(id, r)| (*id, r.created_at, r.content.len()))
            .collect::<Vec<_>>();
        ordered.sort_by_key(|(id, created_at, _)| (*created_at, *id));
        let mut total: usize = ordered.iter().map(|(_, _, bytes)| bytes).sum();
        for (id, created_at, bytes) in ordered {
            if expired(created_at, timestamp) || total > budget || self.entries.len() > max_entries
            {
                if let Some(directory) = &self.directory {
                    match fs::remove_file(directory.join(format!("{id}.json"))) {
                        Ok(()) => {}
                        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
                        Err(error) => return Err(error.into()),
                    }
                }
                self.entries.remove(&id);
                total -= bytes;
            }
        }
        Ok(())
    }
}

fn page(id: Uuid, content: &str, offset: usize, limit: usize) -> Result<(String, WebCacheEntry)> {
    if offset > content.len() || !content.is_char_boundary(offset) {
        return Err(invalid(
            "WebFetch offset must be a UTF-8 byte boundary within the content",
        ));
    }
    let mut end = offset.saturating_add(limit).min(content.len());
    while !content.is_char_boundary(end) {
        end -= 1;
    }
    Ok((
        content[offset..end].to_owned(),
        WebCacheEntry {
            content_id: id.to_string(),
            total_bytes: content.len(),
            offset,
            next_offset: (end < content.len()).then_some(end),
        },
    ))
}

fn parse_id(value: &str) -> Result<Uuid> {
    let id = Uuid::parse_str(value)
        .map_err(|_| invalid("WebFetch content_id must be a canonical UUID"))?;
    if id.to_string() != value {
        return Err(invalid("WebFetch content_id must be a canonical UUID"));
    }
    Ok(id)
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}
fn expired(created_at: u64, timestamp: u64) -> bool {
    created_at > timestamp || timestamp.saturating_sub(created_at) >= TTL.as_secs()
}
fn invalid(message: &str) -> Error {
    Error::Protocol(message.into())
}
async fn blocking<T: Send + 'static>(
    work: impl FnOnce() -> Result<T> + Send + 'static,
) -> Result<T> {
    tokio::task::spawn_blocking(work)
        .await
        .map_err(|error| Error::Store(format!("web cache task failed: {error}")))?
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn large_utf8_content_round_trips_through_bounded_pages_and_restart() {
        let directory = tempfile::tempdir().unwrap();
        let cache = WebCache::at(directory.path().into()).unwrap();
        let original = "α日本🦀\n".repeat(12_000);
        let (mut actual, first) = cache
            .store("conversation-a", "https://example.com", original.clone())
            .await
            .unwrap();
        assert!(actual.len() <= WEB_CACHE_PAGE_BYTES);
        let cache = WebCache::at(directory.path().into()).unwrap();
        let mut next = first.next_offset;
        while let Some(offset) = next {
            let (url, text, entry) = cache
                .read("conversation-a", &first.content_id, offset, 997)
                .await
                .unwrap();
            assert_eq!(url, "https://example.com");
            assert!(text.len() <= 997);
            assert_eq!(entry.offset, actual.len());
            actual.push_str(&text);
            next = entry.next_offset;
        }
        assert_eq!(actual, original);
        assert!(cache
            .read("conversation-b", &first.content_id, 0, 100)
            .await
            .is_err());
        assert!(cache.read("", &first.content_id, 0, 100).await.is_err());
        assert!(cache
            .read("conversation-a", &first.content_id, 1, 100)
            .await
            .is_err());
        assert!(cache
            .read("conversation-a", &first.content_id, usize::MAX, 100)
            .await
            .is_err());
        assert!(cache
            .read("conversation-a", &first.content_id, 0, usize::MAX)
            .await
            .is_err());
        assert!(cache
            .read("conversation-a", &first.content_id, 0, 0)
            .await
            .is_err());
        assert!(cache
            .read("conversation-a", "../../secret", 0, 100)
            .await
            .is_err());
        assert!(cache
            .read("conversation-a", &first.content_id.replace('-', ""), 0, 100)
            .await
            .is_err());
    }

    #[tokio::test]
    async fn capacity_evicts_old_records_without_losing_new_pages() {
        let cache = WebCache::default();
        for _ in 0..=MAX_ENTRIES {
            let (_, entry) = cache
                .store("a", "https://example.com", "content".into())
                .await
                .unwrap();
            assert_eq!(
                cache.read("a", &entry.content_id, 0, 100).await.unwrap().1,
                "content"
            );
        }
        assert_eq!(cache.inner.lock().entries.len(), MAX_ENTRIES);
        let mut state = cache.inner.lock();
        state.cleanup_to(now(), 14, MAX_ENTRIES).unwrap();
        assert_eq!(state.entries.len(), 2);
    }

    #[test]
    fn startup_removes_expired_partial_and_obsolete_records() {
        let directory = tempfile::tempdir().unwrap();
        let id = Uuid::new_v4();
        let record = Record {
            conversation_id: "owner".into(),
            url: "https://example.com".into(),
            created_at: now() - TTL.as_secs(),
            content: "expired".into(),
        };
        fs::write(
            directory.path().join(format!("{id}.json")),
            serde_json::to_vec(&record).unwrap(),
        )
        .unwrap();
        fs::write(directory.path().join(format!("{id}.tmp")), "partial").unwrap();
        fs::write(
            directory.path().join(format!("{id}.txt")),
            "obsolete public file",
        )
        .unwrap();
        let cache = WebCache::at(directory.path().into()).unwrap();
        assert!(cache.inner.lock().entries.is_empty());
        assert_eq!(fs::read_dir(directory.path()).unwrap().count(), 0);
    }

    #[tokio::test]
    async fn expiry_removes_persisted_content_and_default_cache_is_usable() {
        let directory = tempfile::tempdir().unwrap();
        let cache = WebCache::at(directory.path().into()).unwrap();
        let (_, entry) = cache
            .store("a", "https://example.com", "complete".into())
            .await
            .unwrap();
        let id = parse_id(&entry.content_id).unwrap();
        cache.inner.lock().entries.get_mut(&id).unwrap().created_at = now() - TTL.as_secs();
        assert!(cache.read("a", &entry.content_id, 0, 100).await.is_err());
        assert!(!directory.path().join(format!("{id}.json")).exists());
        let memory = WebCache::default();
        let (_, entry) = memory
            .store("a", "https://example.com", "complete".into())
            .await
            .unwrap();
        assert_eq!(
            memory.read("a", &entry.content_id, 0, 100).await.unwrap().1,
            "complete"
        );
    }
}
