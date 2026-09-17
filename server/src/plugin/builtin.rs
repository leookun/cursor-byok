//! Pre-installs bundled built-in plugins into the user's installed directory.
use std::path::Path;

use super::definition::write_if_changed;
use crate::Result;

/// 随二进制打包的内置插件文件;发布构建没有源码目录,靠这里预装。
const CODEX_AUTH: &[(&str, &str)] = &[
    (
        "plugin.json",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/codex-auth/plugin.json"
        )),
    ),
    (
        "main.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/codex-auth/main.ts"
        )),
    ),
    (
        "provider.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/codex-auth/provider.ts"
        )),
    ),
    (
        "models.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/codex-auth/models.ts"
        )),
    ),
    (
        "oauth.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/codex-auth/oauth.ts"
        )),
    ),
    (
        "resources.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/codex-auth/resources.ts"
        )),
    ),
    (
        "assets/codex.svg",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/codex-auth/assets/codex.svg"
        )),
    ),
];

const GROK_AUTH: &[(&str, &str)] = &[
    (
        "plugin.json",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/grok-auth/plugin.json"
        )),
    ),
    (
        "main.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/grok-auth/main.ts"
        )),
    ),
    (
        "provider.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/grok-auth/provider.ts"
        )),
    ),
    (
        "models.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/grok-auth/models.ts"
        )),
    ),
    (
        "oauth.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/grok-auth/oauth.ts"
        )),
    ),
    (
        "resources.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/grok-auth/resources.ts"
        )),
    ),
    (
        "assets/grok.svg",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/grok-auth/assets/grok.svg"
        )),
    ),
];

const ANTIGRAVITY_AUTH: &[(&str, &str)] = &[
    (
        "plugin.json",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/plugin.json"
        )),
    ),
    (
        "main.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/main.ts"
        )),
    ),
    (
        "provider.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/provider.ts"
        )),
    ),
    (
        "models.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/models.ts"
        )),
    ),
    (
        "model_routes.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/model_routes.ts"
        )),
    ),
    (
        "public_models.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/public_models.ts"
        )),
    ),
    (
        "oauth.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/oauth.ts"
        )),
    ),
    (
        "google_oauth.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/google_oauth.ts"
        )),
    ),
    (
        "resources.ts",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/resources.ts"
        )),
    ),
    (
        "assets/antigravity.svg",
        include_str!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/plugins/build-in/antigravity-auth/assets/antigravity.svg"
        )),
    ),
];

const PLUGINS: &[(&str, &[(&str, &str)])] = &[
    ("codex-auth", CODEX_AUTH),
    ("grok-auth", GROK_AUTH),
    ("antigravity-auth", ANTIGRAVITY_AUTH),
];

/// 把内置插件预装到 installed 目录。manifest 的 version 是缓存键:
/// 版本一致时零写盘;版本变化时整目录同步并清理旧版本残留文件。
pub(super) fn install(installed: &Path) -> Result<()> {
    for (name, files) in PLUGINS {
        let directory = installed.join(name);
        if disk_version(&directory) == Some(embedded_version(files)?) {
            continue;
        }
        write_plugin(&directory, files)?;
    }
    Ok(())
}

fn embedded_version(files: &[(&str, &str)]) -> Result<String> {
    let manifest = files
        .iter()
        .find(|(name, _)| *name == "plugin.json")
        .map(|(_, content)| *content)
        .expect("built-in plugin bundles plugin.json");
    let value: serde_json::Value = serde_json::from_str(manifest)?;
    value
        .get("version")
        .and_then(serde_json::Value::as_str)
        .map(str::to_owned)
        .ok_or_else(|| crate::Error::Config("built-in plugin manifest requires version".into()))
}

fn disk_version(directory: &Path) -> Option<String> {
    let manifest = std::fs::read_to_string(directory.join("plugin.json")).ok()?;
    let value: serde_json::Value = serde_json::from_str(&manifest).ok()?;
    Some(value.get("version")?.as_str()?.to_owned())
}

fn write_plugin(directory: &Path, files: &[(&str, &str)]) -> Result<()> {
    for (relative, content) in files {
        let path = directory.join(relative);
        let parent = path.parent().expect("plugin file path has a parent");
        std::fs::create_dir_all(parent)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(parent, std::fs::Permissions::from_mode(0o700))?;
        }
        write_if_changed(&path, content)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
        }
    }
    prune_unknown_files(directory, directory, files)?;
    Ok(())
}

/// 删除插件目录中不在嵌入清单里的文件与空目录(旧版本残留)。
fn prune_unknown_files(root: &Path, directory: &Path, files: &[(&str, &str)]) -> Result<()> {
    for entry in std::fs::read_dir(directory)? {
        let entry = entry?;
        let path = entry.path();
        if entry.file_type()?.is_dir() {
            prune_unknown_files(root, &path, files)?;
            if std::fs::read_dir(&path)?.next().is_none() {
                std::fs::remove_dir(&path)?;
            }
            continue;
        }
        let known = files
            .iter()
            .any(|(relative, _)| root.join(relative) == path);
        if !known {
            std::fs::remove_file(&path)?;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn embedded_main() -> &'static str {
        CODEX_AUTH
            .iter()
            .find(|(name, _)| *name == "main.ts")
            .unwrap()
            .1
    }

    #[test]
    fn install_is_version_gated_and_syncs_on_version_change() {
        let root = tempfile::tempdir().unwrap();
        let plugin = root.path().join("codex-auth");

        install(root.path()).unwrap();
        assert_eq!(
            std::fs::read_to_string(plugin.join("main.ts")).unwrap(),
            embedded_main()
        );
        assert!(root
            .path()
            .join("antigravity-auth/assets/antigravity.svg")
            .is_file());

        // 版本一致:本地改动与额外文件保持原样,不发生任何写盘。
        std::fs::write(plugin.join("main.ts"), "edited").unwrap();
        std::fs::write(plugin.join("stale.ts"), "extra").unwrap();
        install(root.path()).unwrap();
        assert_eq!(
            std::fs::read_to_string(plugin.join("main.ts")).unwrap(),
            "edited"
        );
        assert!(plugin.join("stale.ts").exists());

        // 版本变化:整目录同步回嵌入内容并清理残留。
        let manifest = std::fs::read_to_string(plugin.join("plugin.json")).unwrap();
        let mut value: serde_json::Value = serde_json::from_str(&manifest).unwrap();
        value["version"] = serde_json::Value::String("0.0.1".into());
        std::fs::write(plugin.join("plugin.json"), value.to_string()).unwrap();
        install(root.path()).unwrap();
        assert_eq!(
            std::fs::read_to_string(plugin.join("main.ts")).unwrap(),
            embedded_main()
        );
        assert!(!plugin.join("stale.ts").exists());
    }
}
