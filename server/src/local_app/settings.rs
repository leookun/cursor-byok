//! Integrates local application settings.
use std::{collections::BTreeMap, fs, path::PathBuf};

use serde_json::Value;

use crate::{Error, Result};

const NO_PROXY_KEY: &str = "http.noProxy";
const KEYS: [&str; 5] = [
    "http.proxy",
    "http.proxyKerberosServicePrincipal",
    "http.proxySupport",
    "cursor.general.disableHttp2",
    "http.experimental.systemCertificatesV2",
];

fn path() -> Result<PathBuf> {
    let home = dirs::home_dir()
        .ok_or_else(|| Error::Config("cannot resolve user home directory".into()))?;
    match std::env::consts::OS {
        "macos" => Ok(home.join("Library/Application Support/Cursor/User/settings.json")),
        "windows" => Ok(std::env::var_os("APPDATA")
            .map(PathBuf::from)
            .unwrap_or_else(|| home.join("AppData/Roaming"))
            .join("Cursor/User/settings.json")),
        "linux" => Ok(std::env::var_os("XDG_CONFIG_HOME")
            .map(PathBuf::from)
            .unwrap_or_else(|| home.join(".config"))
            .join("Cursor/User/settings.json")),
        platform => Err(Error::Config(format!(
            "Cursor settings are unsupported on {platform}"
        ))),
    }
}

fn read() -> Result<BTreeMap<String, Value>> {
    let path = path()?;
    let data = match fs::read_to_string(path) {
        Ok(data) => data,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(BTreeMap::new()),
        Err(error) => return Err(error.into()),
    };
    if data.trim().is_empty() {
        return Ok(BTreeMap::new());
    }
    json5::from_str(&data)
        .map_err(|error| Error::Config(format!("parse Cursor settings JSONC: {error}")))
}

fn write(settings: &BTreeMap<String, Value>) -> Result<()> {
    let path = path()?;
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    let data = serde_json::to_vec_pretty(settings)?;
    let temp = path.with_extension("json.tmp");
    fs::write(&temp, [data.as_slice(), b"\n"].concat())?;
    fs::rename(temp, path)?;
    Ok(())
}

pub fn write_proxy_settings(proxy_url: &str) -> Result<()> {
    let mut settings = read()?;
    apply_proxy_settings(&mut settings, proxy_url);
    write(&settings)
}

fn apply_proxy_settings(settings: &mut BTreeMap<String, Value>, proxy_url: &str) {
    settings.remove(NO_PROXY_KEY);
    settings.insert(KEYS[0].into(), Value::String(proxy_url.into()));
    settings.insert(KEYS[1].into(), Value::String(proxy_url.into()));
    settings.insert(KEYS[2].into(), Value::String("on".into()));
    settings.insert(KEYS[3].into(), Value::Bool(true));
    settings.insert(KEYS[4].into(), Value::Bool(true));
}

pub fn clear_proxy_settings() -> Result<()> {
    let mut settings = read()?;
    if remove_proxy_settings(&mut settings) {
        write(&settings)?;
    }
    Ok(())
}

fn remove_proxy_settings(settings: &mut BTreeMap<String, Value>) -> bool {
    let before = settings.len();
    for key in KEYS {
        settings.remove(key);
    }
    settings.len() != before
}

pub fn settings_match(proxy_url: &str) -> Result<bool> {
    Ok(proxy_settings_match(&read()?, proxy_url))
}

fn proxy_settings_match(settings: &BTreeMap<String, Value>, proxy_url: &str) -> bool {
    settings.get(KEYS[0]) == Some(&Value::String(proxy_url.into()))
        && settings.get(KEYS[1]) == Some(&Value::String(proxy_url.into()))
        && settings.get(KEYS[2]) == Some(&Value::String("on".into()))
        && settings.get(KEYS[3]) == Some(&Value::Bool(true))
        && settings.get(KEYS[4]) == Some(&Value::Bool(true))
}

pub fn clear_stale_managed_settings() -> Result<()> {
    let settings = read()?;
    let managed_signature = settings.get(KEYS[2]) == Some(&Value::String("on".into()))
        && settings.get(KEYS[3]) == Some(&Value::Bool(true))
        && settings.get(KEYS[4]) == Some(&Value::Bool(true));
    let loopback = settings
        .get(KEYS[0])
        .and_then(Value::as_str)
        .and_then(|value| value.parse::<reqwest::Url>().ok())
        .and_then(|url| url.host_str().map(str::to_owned))
        .is_some_and(|host| matches!(host.as_str(), "127.0.0.1" | "localhost" | "::1"));
    if managed_signature && loopback {
        clear_proxy_settings()?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const REMOTE_ROUTE: &str = "cursorAgentHost.remoteInferenceRoute";

    #[test]
    fn applying_and_removing_proxy_preserves_user_remote_route_and_unrelated_settings() {
        for route in [None, Some(json!("always")), Some(json!("never"))] {
            let mut settings = BTreeMap::from([
                ("editor.fontSize".into(), json!(14)),
                ("remote.SSH.configFile".into(), json!("/custom/ssh/config")),
            ]);
            if let Some(value) = &route {
                settings.insert(REMOTE_ROUTE.into(), value.clone());
            }
            let original = settings.clone();
            apply_proxy_settings(&mut settings, "http://127.0.0.1:43123");
            assert!(proxy_settings_match(&settings, "http://127.0.0.1:43123"));
            assert_eq!(settings.get(REMOTE_ROUTE), route.as_ref());
            assert!(remove_proxy_settings(&mut settings));
            assert_eq!(settings, original);
            assert!(!remove_proxy_settings(&mut settings));
        }
    }

    #[test]
    fn local_status_requires_every_proxy_setting_and_current_port() {
        let mut settings = BTreeMap::new();
        apply_proxy_settings(&mut settings, "http://127.0.0.1:43123");
        for key in KEYS {
            let mut incomplete = settings.clone();
            incomplete.remove(key);
            assert!(!proxy_settings_match(&incomplete, "http://127.0.0.1:43123"));
        }
        assert!(!proxy_settings_match(&settings, "http://127.0.0.1:43124"));
        apply_proxy_settings(&mut settings, "http://127.0.0.1:43124");
        assert!(proxy_settings_match(&settings, "http://127.0.0.1:43124"));
        assert!(!settings.contains_key(REMOTE_ROUTE));
    }

    #[test]
    fn jsonc_settings_keep_nested_values_when_proxy_is_reapplied() {
        let mut settings: BTreeMap<String, Value> = json5::from_str(
            r#"{
                // An existing Cursor user file, including a trailing comma.
                "http.noProxy": ["example.com"],
                "[rust]": {"editor.formatOnSave": true},
                "cursorAgentHost.remoteInferenceRoute": "always",
            }"#,
        )
        .unwrap();
        apply_proxy_settings(&mut settings, "http://127.0.0.1:43123");
        let once = settings.clone();
        apply_proxy_settings(&mut settings, "http://127.0.0.1:43123");
        assert_eq!(settings, once);
        assert!(!settings.contains_key(NO_PROXY_KEY));
        assert_eq!(settings["[rust]"], json!({"editor.formatOnSave": true}));
        assert_eq!(settings[REMOTE_ROUTE], json!("always"));
    }
}
