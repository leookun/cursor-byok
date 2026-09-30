//! Copies a user-selected plugin directory into the installed catalog.
use std::{
    collections::HashSet,
    fs,
    path::{Path, PathBuf},
};

use super::{catalog::load_plugin, definition::PluginDefinitionLoader, manifest::PluginManifest};
use crate::{Error, Result};

const MANIFEST_FILE_NAME: &str = "plugin.json";
const MAX_PLUGIN_FILES: usize = 512;
const MAX_PLUGIN_BYTES: u64 = 32 * 1024 * 1024;

#[derive(Debug)]
pub(super) enum PrepareOutcome {
    Exists { id: String, name: String },
    Pending(PendingInstall),
}

#[derive(Debug)]
pub(super) struct PendingInstall {
    pub id: String,
    pub name: String,
    pub replaced: bool,
    committed: bool,
    staging: PathBuf,
    destination: PathBuf,
}

struct CopyLimits {
    max_files: usize,
    max_bytes: u64,
}

struct CopyState {
    files: usize,
    bytes: u64,
    visiting: HashSet<PathBuf>,
}

pub(super) async fn prepare(
    installed: &Path,
    loader: &PluginDefinitionLoader,
    app_version: &str,
    source: &Path,
    executable: &Path,
    replace: bool,
) -> Result<PrepareOutcome> {
    let source = canonicalize_directory(source)?;
    fs::create_dir_all(installed)?;
    let installed = installed.canonicalize()?;
    let staging_root = staging_root(&installed)?;
    ensure_outside(&source, &installed, "the installed plugin directory")?;
    if staging_root.exists() {
        ensure_outside(&source, &staging_root, "the plugin staging directory")?;
    }

    let manifest = read_manifest(&source)?;
    if super::builtin::is_reserved_plugin(&manifest.id) {
        return Err(Error::Config(format!(
            "plugin '{}' is built in and cannot be replaced",
            manifest.id
        )));
    }
    let destination = installed.join(&manifest.id);
    if destination.exists() && !replace {
        return Ok(PrepareOutcome::Exists {
            id: manifest.id,
            name: existing_name(&destination).unwrap_or(manifest.name),
        });
    }

    let staging = staging_root.join(&manifest.id);
    if staging.exists() {
        fs::remove_dir_all(&staging)?;
    }
    fs::create_dir_all(&staging_root)?;
    let pending = PendingInstall {
        id: manifest.id,
        name: manifest.name,
        replaced: destination.exists(),
        committed: false,
        staging,
        destination,
    };
    copy_plugin_tree(
        &source,
        &pending.staging,
        CopyLimits {
            max_files: MAX_PLUGIN_FILES,
            max_bytes: MAX_PLUGIN_BYTES,
        },
    )?;
    load_plugin(&pending.staging, loader, executable, app_version).await?;
    Ok(PrepareOutcome::Pending(pending))
}

impl PendingInstall {
    pub(super) fn commit(mut self) -> Result<()> {
        swap_directory(&self.staging, &self.destination)?;
        self.committed = true;
        Ok(())
    }
}

impl Drop for PendingInstall {
    fn drop(&mut self) {
        if !self.committed {
            let _ = fs::remove_dir_all(&self.staging);
        }
    }
}

fn staging_root(installed: &Path) -> Result<PathBuf> {
    Ok(installed
        .parent()
        .ok_or_else(|| Error::Config("plugin install directory has no parent".into()))?
        .join("installing"))
}

fn canonicalize_directory(path: &Path) -> Result<PathBuf> {
    let canonical = fs::canonicalize(path).map_err(|error| {
        Error::Config(format!(
            "cannot read plugin directory {}: {error}",
            path.display()
        ))
    })?;
    if !canonical.is_dir() {
        return Err(Error::Config(format!(
            "plugin source is not a directory: {}",
            path.display()
        )));
    }
    Ok(canonical)
}

fn ensure_outside(source: &Path, root: &Path, label: &str) -> Result<()> {
    let root = if root.exists() {
        root.canonicalize()?
    } else {
        return Ok(());
    };
    if source.starts_with(&root) || root.starts_with(source) {
        return Err(Error::Config(format!(
            "plugin source must be outside {label}"
        )));
    }
    Ok(())
}

fn read_manifest(directory: &Path) -> Result<PluginManifest> {
    let manifest: PluginManifest =
        serde_json::from_slice(&fs::read(directory.join(MANIFEST_FILE_NAME))?)?;
    manifest.validate(directory)?;
    Ok(manifest)
}

fn existing_name(directory: &Path) -> Option<String> {
    let manifest = fs::read(directory.join(MANIFEST_FILE_NAME)).ok()?;
    let value: serde_json::Value = serde_json::from_slice(&manifest).ok()?;
    value.get("name")?.as_str().map(str::to_owned)
}

fn copy_plugin_tree(source: &Path, destination: &Path, limits: CopyLimits) -> Result<()> {
    let source = source.canonicalize()?;
    fs::create_dir_all(destination)?;
    restrict(destination, true);
    let mut state = CopyState {
        files: 0,
        bytes: 0,
        visiting: HashSet::from([source.clone()]),
    };
    let result = copy_children(
        &source,
        &source,
        destination,
        Path::new(""),
        &limits,
        &mut state,
    );
    if result.is_err() {
        let _ = fs::remove_dir_all(destination);
    }
    result
}

fn copy_children(
    source_root: &Path,
    current: &Path,
    destination_root: &Path,
    relative: &Path,
    limits: &CopyLimits,
    state: &mut CopyState,
) -> Result<()> {
    for entry in fs::read_dir(current)? {
        let path = entry?.path();
        let name = path
            .file_name()
            .ok_or_else(|| Error::Config("plugin file has no name".into()))?;
        let child_relative = relative.join(name);
        let destination = destination_root.join(&child_relative);
        place_path(
            &path,
            source_root,
            destination_root,
            &destination,
            &child_relative,
            limits,
            state,
        )?;
    }
    Ok(())
}

fn place_path(
    path: &Path,
    source_root: &Path,
    destination_root: &Path,
    destination: &Path,
    relative: &Path,
    limits: &CopyLimits,
    state: &mut CopyState,
) -> Result<()> {
    let metadata = fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink() {
        let target = fs::canonicalize(path).map_err(|_| {
            Error::Config(format!(
                "plugin symlink cannot be resolved: {}",
                relative.display()
            ))
        })?;
        if !target.starts_with(source_root) {
            return Err(Error::Config(format!(
                "plugin file escapes its directory: {}",
                relative.display()
            )));
        }
        let followed = fs::metadata(path)?;
        if followed.is_dir() {
            if !state.visiting.insert(target.clone()) {
                return Err(Error::Config(format!(
                    "plugin contains a symlink cycle: {}",
                    relative.display()
                )));
            }
            fs::create_dir_all(destination)?;
            restrict(destination, true);
            let result = copy_children(
                source_root,
                &target,
                destination_root,
                relative,
                limits,
                state,
            );
            state.visiting.remove(&target);
            return result;
        }
        if followed.is_file() {
            return copy_file(&target, destination, limits, state);
        }
        return Err(Error::Config(format!(
            "unsupported plugin file: {}",
            relative.display()
        )));
    }
    if metadata.is_dir() {
        fs::create_dir_all(destination)?;
        restrict(destination, true);
        return copy_children(source_root, path, destination_root, relative, limits, state);
    }
    if metadata.is_file() {
        return copy_file(path, destination, limits, state);
    }
    Err(Error::Config(format!(
        "unsupported plugin file: {}",
        relative.display()
    )))
}

fn copy_file(
    source: &Path,
    destination: &Path,
    limits: &CopyLimits,
    state: &mut CopyState,
) -> Result<()> {
    let length = fs::metadata(source)?.len();
    state.files += 1;
    state.bytes = state.bytes.saturating_add(length);
    if state.files > limits.max_files {
        return Err(Error::Config(format!(
            "plugin contains more than {} files",
            limits.max_files
        )));
    }
    if state.bytes > limits.max_bytes {
        return Err(Error::Config(format!(
            "plugin is larger than {} bytes",
            limits.max_bytes
        )));
    }
    if let Some(parent) = destination.parent() {
        fs::create_dir_all(parent)?;
    }
    fs::copy(source, destination)?;
    restrict(destination, false);
    Ok(())
}

fn swap_directory(staging: &Path, destination: &Path) -> Result<()> {
    let parent = destination
        .parent()
        .ok_or_else(|| Error::Config("plugin destination has no parent".into()))?;
    let backup = parent.join(format!(
        ".{}-replacing",
        destination
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or("plugin")
    ));
    if backup.exists() {
        fs::remove_dir_all(&backup)?;
    }
    if destination.exists() {
        fs::rename(destination, &backup)?;
    }
    if let Err(error) = fs::rename(staging, destination) {
        if backup.exists() && !destination.exists() {
            let _ = fs::rename(&backup, destination);
        }
        return Err(error.into());
    }
    if backup.exists() {
        if let Err(error) = fs::remove_dir_all(&backup) {
            tracing::warn!(%error, "failed to remove replaced plugin backup");
        }
    }
    Ok(())
}

fn restrict(path: &Path, directory: bool) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = if directory { 0o700 } else { 0o600 };
        let _ = fs::set_permissions(path, fs::Permissions::from_mode(mode));
    }
    #[cfg(not(unix))]
    {
        let _ = (path, directory);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn write_plugin(directory: &Path, id: &str, name: &str) {
        fs::create_dir_all(directory.join("assets")).unwrap();
        fs::write(
            directory.join("plugin.json"),
            format!(
                r#"{{
                  "apiVersion": 1,
                  "id": "{id}",
                  "name": "{name}",
                  "version": "0.1.0",
                  "icon": "assets/icon.svg",
                  "entry": "main.ts"
                }}"#
            ),
        )
        .unwrap();
        fs::write(directory.join("main.ts"), "export {}\n").unwrap();
        fs::write(
            directory.join("assets/icon.svg"),
            "<svg xmlns=\"http://www.w3.org/2000/svg\"/>",
        )
        .unwrap();
    }

    fn loader(root: &Path) -> PluginDefinitionLoader {
        PluginDefinitionLoader::for_test(root).unwrap()
    }

    #[tokio::test]
    async fn rejects_a_built_in_plugin_id() {
        let root = tempfile::tempdir().unwrap();
        let source = root.path().join("source");
        write_plugin(&source, "dev.cursorbyok.examples.codex-auth", "Codex");
        let error = prepare(
            &root.path().join("installed"),
            &loader(root.path()),
            "1.0.1",
            &source,
            Path::new("deno"),
            false,
        )
        .await
        .unwrap_err();
        assert!(error.to_string().contains("built in"));
        assert!(!root.path().join("installing").exists());
    }

    #[tokio::test]
    async fn reports_an_existing_plugin_without_copying() {
        let root = tempfile::tempdir().unwrap();
        let installed = root.path().join("installed");
        let destination = installed.join("example.user.plugin");
        write_plugin(&destination, "example.user.plugin", "Installed");
        fs::write(destination.join("marker.txt"), "keep").unwrap();
        let source = root.path().join("source");
        write_plugin(&source, "example.user.plugin", "Replacement");

        let outcome = prepare(
            &installed,
            &loader(root.path()),
            "1.0.1",
            &source,
            Path::new("deno"),
            false,
        )
        .await
        .unwrap();
        match outcome {
            PrepareOutcome::Exists { name, .. } => assert_eq!(name, "Installed"),
            PrepareOutcome::Pending(_) => panic!("existing plugin was staged"),
        }
        assert_eq!(
            fs::read_to_string(destination.join("marker.txt")).unwrap(),
            "keep"
        );
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn failed_evaluation_keeps_the_installed_copy() {
        use std::os::unix::fs::PermissionsExt;
        let root = tempfile::tempdir().unwrap();
        let installed = root.path().join("installed");
        let destination = installed.join("example.user.plugin");
        write_plugin(&destination, "example.user.plugin", "Installed");
        fs::write(destination.join("marker.txt"), "keep").unwrap();
        let source = root.path().join("source");
        write_plugin(&source, "example.user.plugin", "Replacement");
        let executable = root.path().join("fail.sh");
        fs::write(&executable, "#!/bin/sh\nexit 1\n").unwrap();
        fs::set_permissions(&executable, fs::Permissions::from_mode(0o700)).unwrap();

        let error = prepare(
            &installed,
            &loader(root.path()),
            "1.0.1",
            &source,
            &executable,
            true,
        )
        .await
        .unwrap_err();
        assert!(error.to_string().contains("failed"));
        assert_eq!(
            fs::read_to_string(destination.join("marker.txt")).unwrap(),
            "keep"
        );
        assert!(
            !root.path().join("installing").exists()
                || fs::read_dir(root.path().join("installing"))
                    .unwrap()
                    .next()
                    .is_none()
        );
    }

    #[cfg(unix)]
    #[test]
    fn rejects_a_symlink_that_leaves_the_source_directory() {
        let root = tempfile::tempdir().unwrap();
        let source = root.path().join("source");
        write_plugin(&source, "example.user.plugin", "Example");
        let outside = root.path().join("outside.txt");
        fs::write(&outside, "secret").unwrap();
        std::os::unix::fs::symlink(&outside, source.join("leaked.txt")).unwrap();
        let destination = root.path().join("staged");
        let error = copy_plugin_tree(
            &source,
            &destination,
            CopyLimits {
                max_files: 20,
                max_bytes: 1024,
            },
        )
        .unwrap_err();
        assert!(error.to_string().contains("escapes"));
        assert!(!destination.exists());
    }

    #[cfg(unix)]
    #[test]
    fn copies_an_internal_symlink_as_a_regular_file() {
        let root = tempfile::tempdir().unwrap();
        let source = root.path().join("source");
        write_plugin(&source, "example.user.plugin", "Example");
        std::os::unix::fs::symlink(source.join("main.ts"), source.join("alias.ts")).unwrap();
        let destination = root.path().join("staged");
        copy_plugin_tree(
            &source,
            &destination,
            CopyLimits {
                max_files: 20,
                max_bytes: 1024,
            },
        )
        .unwrap();
        assert!(fs::symlink_metadata(destination.join("alias.ts"))
            .unwrap()
            .file_type()
            .is_file());
        assert_eq!(
            fs::read_to_string(destination.join("alias.ts")).unwrap(),
            fs::read_to_string(source.join("main.ts")).unwrap()
        );
    }

    #[test]
    fn rejects_a_plugin_with_too_many_files() {
        let root = tempfile::tempdir().unwrap();
        let source = root.path().join("source");
        write_plugin(&source, "example.user.plugin", "Example");
        fs::write(source.join("extra.ts"), "export {}\n").unwrap();
        let destination = root.path().join("staged");
        let error = copy_plugin_tree(
            &source,
            &destination,
            CopyLimits {
                max_files: 2,
                max_bytes: 1024,
            },
        )
        .unwrap_err();
        assert!(error.to_string().contains("more than 2 files"));
        assert!(!destination.exists());
    }

    #[test]
    fn swap_replaces_the_destination_and_removes_staging() {
        let root = tempfile::tempdir().unwrap();
        let staging = root.path().join("staging");
        let destination = root.path().join("installed").join("example.user.plugin");
        fs::create_dir_all(&staging).unwrap();
        fs::write(staging.join("main.ts"), "new\n").unwrap();
        fs::create_dir_all(&destination).unwrap();
        fs::write(destination.join("main.ts"), "old\n").unwrap();
        swap_directory(&staging, &destination).unwrap();
        assert_eq!(
            fs::read_to_string(destination.join("main.ts")).unwrap(),
            "new\n"
        );
        assert!(!staging.exists());
        assert!(!root
            .path()
            .join("installed")
            .join(".example.user.plugin-replacing")
            .exists());
    }
}
