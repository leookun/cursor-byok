//! Discovers plugin manifests and evaluates serializable TypeScript definitions.
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
};

use base64::{engine::general_purpose::STANDARD, Engine};

use super::{
    definition::PluginDefinitionLoader,
    descriptor::PluginModuleDefinition,
    manifest::{validate_id, PluginManifest},
};
use crate::{config, Error, Result};

const MANIFEST_FILE_NAME: &str = "plugin.json";
const MAX_ICON_BYTES: u64 = 1024 * 1024;

#[derive(Clone)]
pub struct PluginCatalog {
    roots: Vec<PathBuf>,
    definition_loader: PluginDefinitionLoader,
    app_version: String,
}

#[derive(Clone)]
pub(crate) struct PluginEntry {
    pub directory: PathBuf,
    pub entry: PathBuf,
    pub manifest: PluginManifest,
    pub definition: PluginModuleDefinition,
    pub icon: String,
}

impl PluginCatalog {
    pub fn managed(app_version: String) -> Result<Self> {
        let installed = config::managed_data_dir()?.join("plugins/installed");
        fs::create_dir_all(&installed)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(&installed, fs::Permissions::from_mode(0o700))?;
        }
        // 内置插件按版本预装进 installed;版本一致时不写盘。
        super::builtin::install(&installed)?;
        // 扫描顺序即优先级:debug 下源码目录优先,保证内置插件热改生效;
        // 发布构建只有 installed 一个根。
        #[cfg(debug_assertions)]
        let roots = vec![
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("plugins/build-in"),
            installed,
        ];
        #[cfg(not(debug_assertions))]
        let roots = vec![installed];
        Ok(Self {
            roots,
            definition_loader: PluginDefinitionLoader::managed()?,
            app_version,
        })
    }

    pub(crate) fn loader(&self) -> &PluginDefinitionLoader {
        &self.definition_loader
    }

    pub(crate) async fn entries(&self, executable: &Path) -> Vec<PluginEntry> {
        let mut plugins = BTreeMap::new();
        for root in &self.roots {
            let mut directories = match child_directories(root) {
                Ok(value) => value,
                Err(error) => {
                    tracing::warn!(path = %root.display(), %error, "failed to scan plugin directory");
                    continue;
                }
            };
            directories.sort();
            for directory in directories {
                match load_plugin(
                    &directory,
                    &self.definition_loader,
                    executable,
                    &self.app_version,
                )
                .await
                {
                    Ok(entry) => {
                        if plugins.contains_key(&entry.manifest.id) {
                            tracing::debug!(
                                plugin = %entry.manifest.id,
                                path = %directory.display(),
                                "duplicate plugin ignored; earlier plugin source has priority"
                            );
                        } else {
                            plugins.insert(entry.manifest.id.clone(), entry);
                        }
                    }
                    Err(error) => {
                        tracing::warn!(path = %directory.display(), %error, "ignoring invalid plugin")
                    }
                }
            }
        }
        plugins.into_values().collect()
    }

    pub(crate) fn manifests(&self) -> Vec<(PluginManifest, String)> {
        let mut plugins = BTreeMap::new();
        for root in &self.roots {
            let Ok(mut directories) = child_directories(root) else {
                continue;
            };
            directories.sort();
            for directory in directories {
                let loaded = (|| -> Result<_> {
                    let manifest: PluginManifest =
                        serde_json::from_slice(&fs::read(directory.join(MANIFEST_FILE_NAME))?)?;
                    manifest.validate(&directory)?;
                    require_app_version(&manifest, &self.app_version)?;
                    let icon = icon_data_url(&directory, &manifest.icon)?;
                    Ok((manifest, icon))
                })();
                if let Ok((manifest, icon)) = loaded {
                    plugins
                        .entry(manifest.id.clone())
                        .or_insert_with(|| (manifest, icon));
                }
            }
        }
        plugins.into_values().collect()
    }
}

fn child_directories(root: &Path) -> Result<Vec<PathBuf>> {
    if !root.exists() {
        return Ok(Vec::new());
    }
    let mut directories = Vec::new();
    for entry in fs::read_dir(root)? {
        let entry = entry?;
        if entry.file_type()?.is_dir() && !entry.file_name().to_string_lossy().starts_with('.') {
            directories.push(entry.path());
        }
    }
    Ok(directories)
}

/// 应用过旧时拒绝加载,让插件的 minAppVersion 声明生效。
fn require_app_version(manifest: &PluginManifest, app_version: &str) -> Result<()> {
    let Some(minimum) = &manifest.min_app_version else {
        return Ok(());
    };
    if super::manifest::version_at_least(app_version, minimum) {
        return Ok(());
    }
    Err(Error::Config(format!(
        "plugin '{}' requires app version {minimum} or newer (current {app_version})",
        manifest.id
    )))
}

async fn load_plugin(
    directory: &Path,
    loader: &PluginDefinitionLoader,
    executable: &Path,
    app_version: &str,
) -> Result<PluginEntry> {
    let manifest: PluginManifest =
        serde_json::from_slice(&fs::read(directory.join(MANIFEST_FILE_NAME))?)?;
    manifest.validate(directory)?;
    require_app_version(&manifest, app_version)?;
    let icon = icon_data_url(directory, &manifest.icon)?;
    let entry = directory.join(&manifest.entry).canonicalize()?;
    let definition = loader.load(executable, directory, &entry).await?;
    validate_definition(&manifest.id, &definition)?;
    Ok(PluginEntry {
        directory: directory.to_path_buf(),
        entry,
        manifest,
        definition,
        icon,
    })
}

/// 显示文本必须是非空字符串,或全为非空字符串的 locale 映射。
fn validate_localized_text(value: &serde_json::Value, label: &str) -> Result<()> {
    match value {
        serde_json::Value::String(text) if !text.trim().is_empty() => Ok(()),
        serde_json::Value::Object(map)
            if !map.is_empty()
                && map
                    .values()
                    .all(|entry| entry.as_str().is_some_and(|text| !text.trim().is_empty())) =>
        {
            Ok(())
        }
        _ => Err(Error::Config(format!(
            "{label} must be a non-empty string or a locale map of non-empty strings"
        ))),
    }
}

fn validate_definition(plugin_id: &str, definition: &PluginModuleDefinition) -> Result<()> {
    if definition.providers.is_empty() {
        return Err(Error::Config(format!(
            "plugin '{plugin_id}' must define at least one provider"
        )));
    }
    let mut provider_ids = std::collections::HashSet::new();
    for provider in &definition.providers {
        validate_id(&provider.id, "plugin provider id")?;
        validate_localized_text(
            &provider.display_name,
            &format!(
                "plugin '{plugin_id}' provider '{}' displayName",
                provider.id
            ),
        )?;
        if provider.provider_type.trim().is_empty() {
            return Err(Error::Config(format!(
                "plugin '{plugin_id}' provider '{}' requires providerType",
                provider.id
            )));
        }
        if !provider_ids.insert(provider.id.clone()) {
            return Err(Error::Config(format!(
                "plugin '{plugin_id}' contains duplicate provider '{}'",
                provider.id
            )));
        }
        if let Some(resource_type) = &provider.resource_type {
            if !definition
                .resources
                .iter()
                .any(|resource| &resource.resource_type == resource_type)
            {
                return Err(Error::Config(format!(
                    "plugin '{plugin_id}' provider '{}' consumes undeclared resource '{resource_type}'",
                    provider.id
                )));
            }
        }
    }
    let mut resource_types = std::collections::HashSet::new();
    for resource in &definition.resources {
        validate_id(&resource.resource_type, "plugin resource type")?;
        validate_localized_text(
            &resource.display_name,
            &format!(
                "plugin '{plugin_id}' resource '{}' displayName",
                resource.resource_type
            ),
        )?;
        if !resource_types.insert(resource.resource_type.clone()) {
            return Err(Error::Config(format!(
                "plugin '{plugin_id}' contains duplicate resource type '{}'",
                resource.resource_type
            )));
        }
        for method in &resource.add {
            validate_id(&method.id, "plugin add method id")?;
            if !matches!(
                method.method_type.as_str(),
                super::descriptor::OAUTH2_ADD_METHOD
                    | super::descriptor::OAUTH2_AUTHORIZATION_CODE_ADD_METHOD
            ) {
                return Err(Error::Config(format!(
                    "plugin '{plugin_id}' add method '{}' uses unsupported type '{}'",
                    method.id, method.method_type
                )));
            }
            if method.method_type == super::descriptor::OAUTH2_ADD_METHOD
                && method.callback.is_some()
            {
                return Err(Error::Config(format!(
                    "plugin '{plugin_id}' device OAuth method '{}' cannot declare callback settings",
                    method.id
                )));
            }
            if let Some(callback) = &method.callback {
                if callback.port == Some(0) {
                    return Err(Error::Config(format!(
                        "plugin '{plugin_id}' OAuth callback port must be greater than zero"
                    )));
                }
                if let Some(path) = callback.path.as_deref() {
                    if !path.starts_with('/')
                        || path.len() > 128
                        || path.contains('?')
                        || path.contains('#')
                        || path.contains("//")
                    {
                        return Err(Error::Config(format!(
                            "plugin '{plugin_id}' contains invalid OAuth callback path '{path}'"
                        )));
                    }
                }
            }
        }
    }
    Ok(())
}

fn icon_data_url(directory: &Path, relative: &str) -> Result<String> {
    let root = directory.canonicalize()?;
    let path = directory.join(relative).canonicalize()?;
    if !path.starts_with(&root) {
        return Err(Error::Config(format!(
            "plugin icon escapes its directory: {relative}"
        )));
    }
    if fs::metadata(&path)?.len() > MAX_ICON_BYTES {
        return Err(Error::Config(format!(
            "plugin icon exceeds {MAX_ICON_BYTES} bytes: {relative}"
        )));
    }
    let extension = path
        .extension()
        .and_then(|value| value.to_str())
        .unwrap_or_default()
        .to_ascii_lowercase();
    let mime = match extension.as_str() {
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "webp" => "image/webp",
        _ => {
            return Err(Error::Config(format!(
                "unsupported plugin icon: {relative}"
            )))
        }
    };
    Ok(format!(
        "data:{mime};base64,{}",
        STANDARD.encode(fs::read(path)?)
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn repository_examples_have_valid_static_manifests() {
        let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("plugins/build-in");
        let sdk = tempfile::tempdir().unwrap();
        let catalog = PluginCatalog {
            roots: vec![root],
            definition_loader: PluginDefinitionLoader::for_test(sdk.path()).unwrap(),
            app_version: env!("CARGO_PKG_VERSION").into(),
        };
        assert!(!catalog.manifests().is_empty());
    }
}
