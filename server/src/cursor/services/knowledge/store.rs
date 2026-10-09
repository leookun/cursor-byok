//! Persists rules as markdown files with a JSON metadata sidecar.
use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize};

use crate::{Error, Result};

const META_FILE: &str = "meta.json";
const RULE_EXTENSION: &str = "md";
const BLOCKLIST_FILE: &str = ".blocklist";
pub const LOCAL_ID_PREFIX: &str = "local-";

/// 一条规则的完整视图:knowledge 来自 md 文件,其余字段来自 meta.json。
#[derive(Clone, Debug, PartialEq)]
pub struct RuleRecord {
    pub id: String,
    pub knowledge: String,
    pub title: String,
    pub created_at: String,
    pub is_generated: bool,
    pub git_origin: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum JournalOp {
    Add,
    Update,
    Remove,
}

/// 离线期间未同步到上游的一次变更。
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct JournalEntry {
    pub op: JournalOp,
    pub id: String,
}

#[derive(Default, Serialize, Deserialize)]
struct Meta {
    #[serde(default)]
    rules: BTreeMap<String, RuleMeta>,
    #[serde(default)]
    journal: Vec<JournalEntry>,
}

#[derive(Clone, Default, Serialize, Deserialize)]
struct RuleMeta {
    #[serde(default)]
    title: String,
    #[serde(default)]
    created_at: String,
    #[serde(default)]
    is_generated: bool,
    #[serde(default)]
    git_origin: String,
}

/// md 文件为核心的规则存储;调用方需自行串行化并发访问。
pub struct RuleStore {
    root: PathBuf,
}

impl RuleStore {
    pub fn open(root: PathBuf) -> Result<Self> {
        std::fs::create_dir_all(&root)?;
        Ok(Self { root })
    }

    /// 读取 rules 目录下的 .blocklist:每行一个大小写不敏感的子串,
    /// 命中 knowledge 或 title 的规则会被拦截(不写盘、不注入、列表不可见)。
    /// 用于挡住反复自动再生成的污染规则。文件不存在时为空列表。
    pub fn blocklist(&self) -> Vec<String> {
        let Ok(content) = std::fs::read_to_string(self.root.join(BLOCKLIST_FILE)) else {
            return Vec::new();
        };
        content
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty() && !line.starts_with('#'))
            .map(str::to_lowercase)
            .collect()
    }

    pub fn is_blocked(&self, knowledge: &str, title: &str) -> bool {
        let blocklist = self.blocklist();
        if blocklist.is_empty() {
            return false;
        }
        let knowledge = knowledge.to_lowercase();
        let title = title.to_lowercase();
        blocklist
            .iter()
            .any(|needle| knowledge.contains(needle) || title.contains(needle))
    }

    pub fn list(&self) -> Result<Vec<RuleRecord>> {
        let meta = self.read_meta();
        let mut records = Vec::new();
        for entry in std::fs::read_dir(&self.root)? {
            let path = entry?.path();
            if path.extension().and_then(|value| value.to_str()) != Some(RULE_EXTENSION) {
                continue;
            }
            let Some(id) = path.file_stem().and_then(|value| value.to_str()) else {
                continue;
            };
            if validate_id(id).is_err() {
                continue;
            }
            let knowledge = std::fs::read_to_string(&path)?;
            records.push(assemble(id, knowledge, meta.rules.get(id), &path));
        }
        records.retain(|record| !self.is_blocked(&record.knowledge, &record.title));
        records.sort_by(|left, right| {
            timestamp(&right.created_at)
                .cmp(&timestamp(&left.created_at))
                .then_with(|| left.id.cmp(&right.id))
        });
        Ok(records)
    }

    pub fn get(&self, id: &str) -> Result<Option<RuleRecord>> {
        validate_id(id)?;
        let path = self.rule_path(id);
        let knowledge = match std::fs::read_to_string(&path) {
            Ok(knowledge) => knowledge,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(error) => return Err(error.into()),
        };
        let meta = self.read_meta();
        Ok(Some(assemble(id, knowledge, meta.rules.get(id), &path)))
    }

    pub fn upsert(&self, record: &RuleRecord) -> Result<()> {
        validate_id(&record.id)?;
        if self.is_blocked(&record.knowledge, &record.title) {
            tracing::warn!(
                id = %record.id,
                title = %record.title,
                knowledge = %record.knowledge,
                "KB rule write blocked by .blocklist; removing any on-disk residue"
            );
            remove_file_if_exists(&self.rule_path(&record.id))?;
            let mut meta = self.read_meta();
            if meta.rules.remove(&record.id).is_some() {
                self.write_meta(&meta)?;
            }
            return Ok(());
        }
        write_atomic(&self.rule_path(&record.id), record.knowledge.as_bytes())?;
        let mut meta = self.read_meta();
        meta.rules.insert(record.id.clone(), rule_meta(record));
        self.write_meta(&meta)
    }

    pub fn remove(&self, id: &str) -> Result<()> {
        validate_id(id)?;
        remove_file_if_exists(&self.rule_path(id))?;
        let mut meta = self.read_meta();
        if meta.rules.remove(id).is_some() {
            self.write_meta(&meta)?;
        }
        Ok(())
    }

    /// 离线新增的规则在上游落地后,把本地临时 id 换成上游分配的真实 id。
    pub fn promote(&self, old_id: &str, new_id: &str) -> Result<()> {
        validate_id(old_id)?;
        validate_id(new_id)?;
        let source = self.rule_path(old_id);
        let target = self.rule_path(new_id);
        #[cfg(windows)]
        remove_file_if_exists(&target)?;
        std::fs::rename(&source, &target)?;
        let mut meta = self.read_meta();
        if let Some(rule) = meta.rules.remove(old_id) {
            meta.rules.insert(new_id.into(), rule);
        }
        for entry in &mut meta.journal {
            if entry.id == old_id {
                entry.id = new_id.into();
            }
        }
        self.write_meta(&meta)
    }

    /// 用上游的完整列表覆盖本地镜像;仅应在日志为空(已全部回放)时调用。
    pub fn replace_all(&self, records: &[RuleRecord]) -> Result<()> {
        let mut meta = self.read_meta();
        meta.rules.clear();
        for record in records {
            validate_id(&record.id)?;
            if self.is_blocked(&record.knowledge, &record.title) {
                tracing::warn!(
                    id = %record.id,
                    title = %record.title,
                    "KB mirror skipped blocked rule (matches .blocklist)"
                );
                continue;
            }
            write_atomic(&self.rule_path(&record.id), record.knowledge.as_bytes())?;
            meta.rules.insert(record.id.clone(), rule_meta(record));
        }
        for entry in std::fs::read_dir(&self.root)? {
            let path = entry?.path();
            if path.extension().and_then(|value| value.to_str()) != Some(RULE_EXTENSION) {
                continue;
            }
            let keep = path
                .file_stem()
                .and_then(|value| value.to_str())
                .is_some_and(|id| meta.rules.contains_key(id));
            if !keep {
                remove_file_if_exists(&path)?;
            }
        }
        self.write_meta(&meta)
    }

    pub fn journal_front(&self) -> Result<Option<JournalEntry>> {
        Ok(self.read_meta().journal.first().cloned())
    }

    pub fn pop_journal(&self) -> Result<()> {
        let mut meta = self.read_meta();
        if !meta.journal.is_empty() {
            meta.journal.remove(0);
            self.write_meta(&meta)?;
        }
        Ok(())
    }

    pub fn record_add(&self, id: &str) -> Result<()> {
        let mut meta = self.read_meta();
        meta.journal.push(JournalEntry {
            op: JournalOp::Add,
            id: id.into(),
        });
        self.write_meta(&meta)
    }

    pub fn record_update(&self, id: &str) -> Result<()> {
        let mut meta = self.read_meta();
        if journal_contains(&meta.journal, id, JournalOp::Add) {
            // 回放 add 时会读取最新内容,无需单独的 update 日志。
            return Ok(());
        }
        let op = if id.starts_with(LOCAL_ID_PREFIX) {
            // 本地临时 id 没有对应的 add 日志(如镜像覆盖后的残留),按新增回放。
            JournalOp::Add
        } else {
            JournalOp::Update
        };
        if !journal_contains(&meta.journal, id, op) {
            meta.journal.push(JournalEntry { op, id: id.into() });
            self.write_meta(&meta)?;
        }
        Ok(())
    }

    pub fn record_remove(&self, id: &str) -> Result<()> {
        let mut meta = self.read_meta();
        let never_synced = journal_contains(&meta.journal, id, JournalOp::Add);
        meta.journal.retain(|entry| entry.id != id);
        if !never_synced && !id.starts_with(LOCAL_ID_PREFIX) {
            meta.journal.push(JournalEntry {
                op: JournalOp::Remove,
                id: id.into(),
            });
        }
        self.write_meta(&meta)
    }

    fn rule_path(&self, id: &str) -> PathBuf {
        self.root.join(format!("{id}.{RULE_EXTENSION}"))
    }

    fn meta_path(&self) -> PathBuf {
        self.root.join(META_FILE)
    }

    fn read_meta(&self) -> Meta {
        match std::fs::read(self.meta_path()) {
            Ok(bytes) => serde_json::from_slice(&bytes).unwrap_or_else(|error| {
                tracing::warn!(%error, "rules meta.json is corrupt; starting from empty metadata");
                Meta::default()
            }),
            Err(_) => Meta::default(),
        }
    }

    fn write_meta(&self, meta: &Meta) -> Result<()> {
        write_atomic(&self.meta_path(), &serde_json::to_vec_pretty(meta)?)
    }
}

fn assemble(id: &str, knowledge: String, meta: Option<&RuleMeta>, path: &Path) -> RuleRecord {
    match meta {
        Some(meta) => RuleRecord {
            id: id.into(),
            knowledge,
            title: meta.title.clone(),
            created_at: meta.created_at.clone(),
            is_generated: meta.is_generated,
            git_origin: meta.git_origin.clone(),
        },
        // 用户手放的 md 文件没有元数据,用文件名当标题、修改时间当创建时间。
        None => RuleRecord {
            id: id.into(),
            knowledge,
            title: id.into(),
            created_at: file_modified_at(path),
            is_generated: false,
            git_origin: String::new(),
        },
    }
}

fn rule_meta(record: &RuleRecord) -> RuleMeta {
    RuleMeta {
        title: record.title.clone(),
        created_at: record.created_at.clone(),
        is_generated: record.is_generated,
        git_origin: record.git_origin.clone(),
    }
}

fn journal_contains(journal: &[JournalEntry], id: &str, op: JournalOp) -> bool {
    journal.iter().any(|entry| entry.id == id && entry.op == op)
}

fn timestamp(created_at: &str) -> i64 {
    chrono::DateTime::parse_from_rfc3339(created_at)
        .map(|time| time.timestamp_millis())
        .unwrap_or(0)
}

fn file_modified_at(path: &Path) -> String {
    let modified = std::fs::metadata(path)
        .and_then(|meta| meta.modified())
        .unwrap_or_else(|_| std::time::SystemTime::now());
    chrono::DateTime::<chrono::Utc>::from(modified)
        .to_rfc3339_opts(chrono::SecondsFormat::Millis, true)
}

fn validate_id(id: &str) -> Result<()> {
    if id.is_empty()
        || id.len() > 128
        || !id
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
    {
        return Err(Error::Protocol(format!("invalid rule id: {id:?}")));
    }
    Ok(())
}

fn remove_file_if_exists(path: &Path) -> Result<()> {
    match std::fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error.into()),
    }
}

fn write_atomic(path: &Path, bytes: &[u8]) -> Result<()> {
    use std::io::Write;
    let directory = path.parent().expect("rule path has a parent");
    let temporary = directory.join(format!(".{}.tmp", uuid::Uuid::new_v4()));
    let mut file = std::fs::File::create(&temporary)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    drop(file);
    #[cfg(windows)]
    remove_file_if_exists(path)?;
    std::fs::rename(&temporary, path).inspect_err(|_| {
        let _ = std::fs::remove_file(&temporary);
    })?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn record(id: &str, knowledge: &str, created_at: &str) -> RuleRecord {
        RuleRecord {
            id: id.into(),
            knowledge: knowledge.into(),
            title: format!("title-{id}"),
            created_at: created_at.into(),
            is_generated: false,
            git_origin: String::new(),
        }
    }

    fn journal(store: &RuleStore) -> Vec<JournalEntry> {
        store.read_meta().journal
    }

    #[test]
    fn upserts_lists_and_removes_rules() {
        let root = tempfile::tempdir().unwrap();
        let store = RuleStore::open(root.path().join("rules")).unwrap();
        store
            .upsert(&record("100", "older", "2026-01-01T00:00:00.000Z"))
            .unwrap();
        store
            .upsert(&record("200", "newer", "2026-02-01T00:00:00.000Z"))
            .unwrap();

        let listed = store.list().unwrap();
        assert_eq!(
            listed
                .iter()
                .map(|rule| rule.id.as_str())
                .collect::<Vec<_>>(),
            ["200", "100"],
            "list is sorted by created_at descending"
        );
        assert_eq!(listed[0].knowledge, "newer");
        assert_eq!(listed[0].title, "title-200");

        store.remove("200").unwrap();
        assert!(store.get("200").unwrap().is_none());
        assert_eq!(store.list().unwrap().len(), 1);
    }

    #[test]
    fn rejects_path_traversal_ids() {
        let root = tempfile::tempdir().unwrap();
        let store = RuleStore::open(root.path().join("rules")).unwrap();
        assert!(store.get("../escape").is_err());
        assert!(store.get("a/b").is_err());
        assert!(store.get("").is_err());
    }

    #[test]
    fn compacts_offline_journal() {
        let root = tempfile::tempdir().unwrap();
        let store = RuleStore::open(root.path().join("rules")).unwrap();

        // 离线新增后再更新:回放 add 即可携带最新内容,不产生 update 日志。
        store
            .upsert(&record("local-a", "v1", "2026-01-01T00:00:00.000Z"))
            .unwrap();
        store.record_add("local-a").unwrap();
        store.record_update("local-a").unwrap();
        assert_eq!(
            journal(&store),
            vec![JournalEntry {
                op: JournalOp::Add,
                id: "local-a".into()
            }]
        );

        // 离线新增后又删除:上游从未见过它,日志清空。
        store.record_remove("local-a").unwrap();
        assert!(journal(&store).is_empty());

        // 更新上游已有规则:多次更新合并为一条;删除后 update 日志被顶替。
        store.record_update("42").unwrap();
        store.record_update("42").unwrap();
        assert_eq!(
            journal(&store),
            vec![JournalEntry {
                op: JournalOp::Update,
                id: "42".into()
            }]
        );
        store.record_remove("42").unwrap();
        assert_eq!(
            journal(&store),
            vec![JournalEntry {
                op: JournalOp::Remove,
                id: "42".into()
            }]
        );
    }

    #[test]
    fn promote_renames_rule_and_journal_ids() {
        let root = tempfile::tempdir().unwrap();
        let store = RuleStore::open(root.path().join("rules")).unwrap();
        store
            .upsert(&record("local-a", "content", "2026-01-01T00:00:00.000Z"))
            .unwrap();
        store.record_add("local-a").unwrap();

        store.promote("local-a", "17353272").unwrap();

        assert!(store.get("local-a").unwrap().is_none());
        let promoted = store.get("17353272").unwrap().unwrap();
        assert_eq!(promoted.knowledge, "content");
        assert_eq!(promoted.title, "title-local-a");
        assert_eq!(journal(&store)[0].id, "17353272");
    }

    #[test]
    fn replace_all_mirrors_upstream_state() {
        let root = tempfile::tempdir().unwrap();
        let store = RuleStore::open(root.path().join("rules")).unwrap();
        store
            .upsert(&record("stale", "gone soon", "2026-01-01T00:00:00.000Z"))
            .unwrap();

        store
            .replace_all(&[record(
                "17353272",
                "from upstream",
                "2026-02-01T00:00:00.000Z",
            )])
            .unwrap();

        let listed = store.list().unwrap();
        assert_eq!(listed.len(), 1);
        assert_eq!(listed[0].id, "17353272");
        assert_eq!(listed[0].knowledge, "from upstream");
        assert!(store.get("stale").unwrap().is_none());
    }

    #[test]
    fn lists_hand_written_markdown_without_metadata() {
        let root = tempfile::tempdir().unwrap();
        let store = RuleStore::open(root.path().join("rules")).unwrap();
        std::fs::write(root.path().join("rules/manual_rule.md"), "hand written").unwrap();

        let listed = store.list().unwrap();
        assert_eq!(listed.len(), 1);
        assert_eq!(listed[0].id, "manual_rule");
        assert_eq!(listed[0].title, "manual_rule");
        assert_eq!(listed[0].knowledge, "hand written");
    }
}
