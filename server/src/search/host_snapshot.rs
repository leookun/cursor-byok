//! Cursor-host workspace snapshot for Semble absolute-path indexing.

use std::{
    fs,
    path::{Component, Path, PathBuf},
};

use base64::{engine::general_purpose::STANDARD as BASE64, Engine as _};
use serde::Deserialize;
use serde_json::Value;
use sha2::{Digest, Sha256};

use crate::config;

/// Soft bounds for one Cursor-host dump. Exceeding them fails the tool call.
pub(crate) const MAX_FILES: usize = 4_000;
pub(crate) const MAX_TOTAL_BYTES: u64 = 24 * 1024 * 1024;
pub(crate) const MAX_FILE_BYTES: u64 = 1_000_000;
pub(crate) const SNAPSHOT_TIMEOUT_MS: i32 = 180_000;
pub(crate) const SNAPSHOT_OUTPUT_THRESHOLD_BYTES: u64 = 64 * 1024 * 1024;

const DUMP_SCRIPT: &str = r#"
import base64, json, os, sys
ROOT = os.path.realpath(base64.b64decode(sys.argv[1]).decode())
MAX_FILES = 4000
MAX_BYTES = 24 * 1024 * 1024
MAX_FILE = 1000000
SKIP = {
    ".git", ".hg", ".svn", "node_modules", "target", ".venv", "venv", ".tox",
    "__pycache__", ".next", "dist", "build", ".cache", ".semble",
}

def fail(code, message):
    print(json.dumps({"v": 1, "ok": False, "error": message}), flush=True)
    sys.exit(code)

if not os.path.isdir(ROOT):
    fail(2, "not a directory")

files = []
total = 0
for dirpath, dirnames, filenames in os.walk(ROOT, followlinks=False):
    dirnames[:] = [
        name for name in dirnames
        if name not in SKIP and not os.path.islink(os.path.join(dirpath, name))
    ]
    for name in filenames:
        path = os.path.join(dirpath, name)
        if os.path.islink(path):
            continue
        try:
            real = os.path.realpath(path)
        except OSError:
            continue
        if real != ROOT and not real.startswith(ROOT + os.sep):
            continue
        try:
            st = os.stat(path, follow_symlinks=False)
        except OSError as error:
            fail(2, f"stat failed: {error}")
        if (not os.path.isfile(path)) or st.st_size == 0 or st.st_size > MAX_FILE:
            continue
        if len(files) >= MAX_FILES:
            fail(3, "file limit exceeded")
        if total + st.st_size > MAX_BYTES:
            fail(3, "byte limit exceeded")
        try:
            with open(path, "rb") as handle:
                data = handle.read()
            data.decode("utf-8")
        except Exception:
            continue
        rel = os.path.relpath(path, ROOT).replace("\\", "/")
        files.append((rel, data))
        total += len(data)

print(json.dumps({"v": 1, "ok": True, "files": len(files), "bytes": total}), flush=True)
for rel, data in files:
    print(json.dumps({"p": rel, "b": base64.b64encode(data).decode("ascii")}), flush=True)
print(json.dumps({"v": 1, "done": True, "files": len(files), "bytes": total}), flush=True)
"#;

#[derive(Debug, Deserialize)]
struct Header {
    v: u32,
    #[serde(default)]
    ok: bool,
    #[serde(default)]
    done: bool,
    #[serde(default)]
    files: usize,
    #[serde(default)]
    bytes: u64,
    #[serde(default)]
    error: Option<String>,
}

#[derive(Debug, Deserialize)]
struct FileLine {
    p: String,
    b: String,
}

#[derive(Debug)]
pub(crate) struct SnapshotDump {
    pub files: Vec<(String, Vec<u8>)>,
}

pub(crate) fn is_http_repo(repo: &str) -> bool {
    let trimmed = repo.trim();
    trimmed.starts_with("https://") || trimmed.starts_with("http://")
}

pub(crate) fn is_absolute_fs_repo(repo: &str) -> bool {
    let trimmed = repo.trim();
    if trimmed.is_empty() || is_http_repo(trimmed) {
        return false;
    }
    Path::new(trimmed).is_absolute()
}

pub(crate) fn snapshot_shell_command(remote_root: &str) -> String {
    let script = BASE64.encode(DUMP_SCRIPT.as_bytes());
    let root = BASE64.encode(remote_root.as_bytes());
    format!(
        "python3 -c 'import base64,sys;exec(base64.b64decode(sys.argv[1]))' {script} {root}"
    )
}

pub(crate) fn parse_dump(stdout: &str) -> Result<SnapshotDump, String> {
    let mut lines = stdout.lines().filter(|line| !line.trim().is_empty());
    let header_line = lines
        .next()
        .ok_or_else(|| "Semble host snapshot produced no output".to_string())?;
    let header: Header = serde_json::from_str(header_line)
        .map_err(|error| format!("invalid Semble host snapshot header: {error}"))?;
    if header.v != 1 {
        return Err(format!(
            "unsupported Semble host snapshot version {}",
            header.v
        ));
    }
    if !header.ok {
        return Err(header
            .error
            .unwrap_or_else(|| "Semble host snapshot failed".into()));
    }
    if header.files > MAX_FILES || header.bytes > MAX_TOTAL_BYTES {
        return Err("Semble host snapshot exceeds configured bounds".into());
    }

    let mut files = Vec::with_capacity(header.files);
    let mut total_bytes = 0_u64;
    let mut done = None;
    for line in lines {
        if let Ok(marker) = serde_json::from_str::<Header>(line) {
            if marker.done {
                done = Some(marker);
                break;
            }
            if marker.error.is_some() || !marker.ok {
                return Err(marker
                    .error
                    .unwrap_or_else(|| "Semble host snapshot failed".into()));
            }
        }
        let file: FileLine = serde_json::from_str(line)
            .map_err(|error| format!("invalid Semble host snapshot file line: {error}"))?;
        validate_relative_path(&file.p)?;
        let bytes = BASE64
            .decode(file.b.as_bytes())
            .map_err(|error| format!("invalid Semble host snapshot payload: {error}"))?;
        if bytes.is_empty() || bytes.len() as u64 > MAX_FILE_BYTES {
            return Err(format!(
                "Semble host snapshot file {} has an invalid size",
                file.p
            ));
        }
        total_bytes = total_bytes.saturating_add(bytes.len() as u64);
        if files.len() >= MAX_FILES || total_bytes > MAX_TOTAL_BYTES {
            return Err("Semble host snapshot exceeds configured bounds".into());
        }
        files.push((file.p, bytes));
    }
    let done = done.ok_or_else(|| "Semble host snapshot ended before the done marker".to_string())?;
    if done.files != files.len() || done.bytes != total_bytes || done.files != header.files {
        return Err("Semble host snapshot counts are incomplete or inconsistent".into());
    }
    if files.is_empty() {
        return Err("Semble host snapshot contained no indexable text files".into());
    }
    Ok(SnapshotDump { files })
}

pub(crate) fn materialize_dump(
    conversation_id: &str,
    call_id: &str,
    remote_root: &str,
    dump: &SnapshotDump,
) -> Result<PathBuf, String> {
    let root = config::managed_data_dir()
        .map_err(|error| error.to_string())?
        .join("semble-host")
        .join(safe_segment(conversation_id))
        .join(safe_segment(call_id));
    if root.exists() {
        fs::remove_dir_all(&root).map_err(|error| {
            format!(
                "failed to clear Semble host snapshot directory {}: {error}",
                root.display()
            )
        })?;
    }
    fs::create_dir_all(&root).map_err(|error| {
        format!(
            "failed to create Semble host snapshot directory {}: {error}",
            root.display()
        )
    })?;
    let identity = fs::write(
        root.join(".semble-host-source"),
        format!("{remote_root}\n"),
    );
    identity.map_err(|error| format!("failed to record Semble host source: {error}"))?;

    for (relative, bytes) in &dump.files {
        let destination = join_under_root(&root, relative)?;
        if let Some(parent) = destination.parent() {
            fs::create_dir_all(parent).map_err(|error| {
                format!(
                    "failed to create snapshot path {}: {error}",
                    parent.display()
                )
            })?;
        }
        fs::write(&destination, bytes).map_err(|error| {
            format!(
                "failed to write snapshot file {}: {error}",
                destination.display()
            )
        })?;
    }
    Ok(root)
}

pub(crate) fn rewrite_result_paths(mut value: Value, remote_root: &str) -> Value {
    let Some(results) = value
        .as_object_mut()
        .and_then(|object| object.get_mut("results"))
        .and_then(Value::as_array_mut)
    else {
        return value;
    };
    for result in results {
        let Some(object) = result.as_object_mut() else {
            continue;
        };
        let Some(path) = object.get("file_path").and_then(Value::as_str).map(str::to_owned) else {
            continue;
        };
        object.insert(
            "file_path".into(),
            Value::String(absolutize_path(remote_root, &path)),
        );
    }
    value
}

pub(crate) fn relativize_path(remote_root: &str, file_path: &str) -> String {
    let normalized = file_path.replace('\\', "/");
    let root = remote_root.trim_end_matches('/').replace('\\', "/");
    normalized
        .strip_prefix(&root)
        .map(|rest| rest.trim_start_matches('/').to_string())
        .filter(|rest| !rest.is_empty())
        .unwrap_or(normalized)
}

pub(crate) fn absolutize_path(remote_root: &str, relative: &str) -> String {
    let root = remote_root.trim_end_matches('/').replace('\\', "/");
    let relative = relative.replace('\\', "/").trim_start_matches('/').to_string();
    if relative.is_empty() {
        root
    } else {
        format!("{root}/{relative}")
    }
}

pub(crate) fn source_identity(remote_root: &str) -> String {
    let digest = hex::encode(Sha256::digest(remote_root.as_bytes()));
    format!("cursor-host:{digest}")
}

fn safe_segment(value: &str) -> String {
    let mut output = value
        .chars()
        .map(|character| {
            if character.is_ascii_alphanumeric() || character == '-' || character == '_' {
                character
            } else {
                '_'
            }
        })
        .collect::<String>();
    if output.is_empty() {
        output.push('_');
    }
    output.truncate(120);
    output
}

fn validate_relative_path(path: &str) -> Result<(), String> {
    if path.is_empty() || path.starts_with('/') || path.contains('\0') {
        return Err(format!("unsafe Semble snapshot path: {path}"));
    }
    let path = Path::new(path);
    if path
        .components()
        .any(|component| matches!(component, Component::ParentDir | Component::RootDir))
    {
        return Err(format!("unsafe Semble snapshot path: {}", path.display()));
    }
    Ok(())
}

fn join_under_root(root: &Path, relative: &str) -> Result<PathBuf, String> {
    validate_relative_path(relative)?;
    let joined = root.join(relative);
    let normalized = joined.components().collect::<PathBuf>();
    if !normalized.starts_with(root) {
        return Err(format!("unsafe Semble snapshot path: {relative}"));
    }
    Ok(normalized)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample_dump(relative: &str, content: &str) -> String {
        let encoded = BASE64.encode(content.as_bytes());
        format!(
            "{}\n{}\n{}\n",
            serde_json::json!({"v":1,"ok":true,"files":1,"bytes":content.len()}).to_string(),
            serde_json::json!({"p":relative,"b":encoded}).to_string(),
            serde_json::json!({"v":1,"done":true,"files":1,"bytes":content.len()}).to_string(),
        )
    }

    #[test]
    fn classifies_http_and_absolute_repos() {
        assert!(is_http_repo("https://github.com/owner/repo.git"));
        assert!(is_absolute_fs_repo("/home/user/project"));
        assert!(!is_absolute_fs_repo("relative/path"));
        assert!(!is_absolute_fs_repo("https://example.com/repo"));
    }

    #[test]
    fn snapshot_command_base64_encodes_root_without_interpolation() {
        let command = snapshot_shell_command("/tmp/project; rm -rf /");
        assert!(command.starts_with("python3 -c "));
        assert!(!command.contains("/tmp/project; rm -rf /"));
        assert!(command.contains(&BASE64.encode("/tmp/project; rm -rf /".as_bytes())));
    }

    #[test]
    fn parse_and_materialize_round_trip() {
        let dump = parse_dump(&sample_dump("src/auth.rs", "pub fn authenticate() {}\n")).unwrap();
        assert_eq!(dump.files.len(), 1);
        let root =
            materialize_dump("conversation-materialize", "call-1", "/remote/project", &dump)
                .unwrap();
        let written = fs::read_to_string(root.join("src/auth.rs")).unwrap();
        assert_eq!(written, "pub fn authenticate() {}\n");
        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn rejects_path_escape_and_incomplete_dumps() {
        assert!(parse_dump(r#"{"v":1,"ok":true,"files":1,"bytes":1}
{"p":"../escape.rs","b":"YQ=="}
{"v":1,"done":true,"files":1,"bytes":1}
"#)
        .is_err());
        assert!(parse_dump(r#"{"v":1,"ok":true,"files":1,"bytes":1}
{"p":"a.rs","b":"YQ=="}
"#)
        .is_err());
    }

    #[test]
    fn rewrites_result_paths_to_remote_root() {
        let value = rewrite_result_paths(
            serde_json::json!({
                "query": "auth",
                "results": [{"file_path": "src/auth.rs", "start_line": 1, "end_line": 2, "score": 1.0}]
            }),
            "/remote/project",
        );
        assert_eq!(
            value["results"][0]["file_path"],
            "/remote/project/src/auth.rs"
        );
        assert_eq!(
            relativize_path("/remote/project", "/remote/project/src/auth.rs"),
            "src/auth.rs"
        );
    }
}
