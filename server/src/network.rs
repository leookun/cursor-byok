//! Owns reusable outbound HTTP clients configured from persisted proxy settings.

use std::{
    hash::{Hash, Hasher},
    sync::Arc,
    time::{Duration, Instant},
};

use tokio::sync::RwLock;

use crate::{
    store::{ProxySettingsSecret, Store},
    Error, Result,
};

const LOCAL_NO_PROXY: &str = "localhost,127.0.0.0/8,::1";

/// How often cached clients re-check the effective proxy configuration.
///
/// reqwest resolves the OS system proxy once when a `Client` is built and
/// freezes it into the connector, so a running process never notices when the
/// user toggles a system-wide proxy (e.g. Clash). Comparing a cheap
/// fingerprint on this interval lets stale clients be rebuilt within seconds
/// instead of surviving until the next process restart.
const FINGERPRINT_CHECK_INTERVAL: Duration = Duration::from_secs(2);

#[derive(Clone)]
pub struct NetworkClients {
    store: Store,
    cache: Arc<RwLock<ClientCache>>,
}

#[derive(Default)]
struct ClientCache {
    default: Option<reqwest::Client>,
    cursor: Option<reqwest::Client>,
    provider: Option<(Duration, reqwest::Client)>,
    /// Fingerprint of the proxy configuration the cached clients were built
    /// with, and when the fingerprint was last recomputed.
    fingerprint: Option<(u64, Instant)>,
}

impl NetworkClients {
    pub fn new(store: Store) -> Self {
        Self {
            store,
            cache: Arc::new(RwLock::new(ClientCache::default())),
        }
    }

    pub async fn default_client(&self) -> Result<reqwest::Client> {
        self.invalidate_if_proxy_changed().await;
        if let Some(client) = self.cache.read().await.default.clone() {
            return Ok(client);
        }
        let mut cache = self.cache.write().await;
        if let Some(client) = cache.default.clone() {
            return Ok(client);
        }
        let client = client_builder(&self.store).await?.build()?;
        cache.default = Some(client.clone());
        Ok(client)
    }

    pub async fn cursor_client(&self) -> Result<reqwest::Client> {
        self.invalidate_if_proxy_changed().await;
        if let Some(client) = self.cache.read().await.cursor.clone() {
            return Ok(client);
        }
        let mut cache = self.cache.write().await;
        if let Some(client) = cache.cursor.clone() {
            return Ok(client);
        }
        let client = client_builder(&self.store)
            .await?
            .redirect(reqwest::redirect::Policy::none())
            .build()?;
        cache.cursor = Some(client.clone());
        Ok(client)
    }

    pub async fn provider_client(&self, timeout: Duration) -> Result<reqwest::Client> {
        self.invalidate_if_proxy_changed().await;
        if let Some((_, client)) = self
            .cache
            .read()
            .await
            .provider
            .as_ref()
            .filter(|(cached_timeout, _)| *cached_timeout == timeout)
        {
            return Ok(client.clone());
        }
        let mut cache = self.cache.write().await;
        if let Some((_, client)) = cache
            .provider
            .as_ref()
            .filter(|(cached_timeout, _)| *cached_timeout == timeout)
        {
            return Ok(client.clone());
        }
        let client = client_builder(&self.store)
            .await?
            .timeout(timeout)
            .build()?;
        cache.provider = Some((timeout, client.clone()));
        Ok(client)
    }

    pub async fn invalidate(&self) {
        *self.cache.write().await = ClientCache::default();
    }

    /// Drops cached clients when the effective proxy configuration changed
    /// since they were built. Rate-limited to one fingerprint computation per
    /// `FINGERPRINT_CHECK_INTERVAL`; fingerprint read failures keep the
    /// current cache rather than flushing it spuriously.
    async fn invalidate_if_proxy_changed(&self) {
        {
            let cache = self.cache.read().await;
            if let Some((_, checked_at)) = cache.fingerprint {
                if checked_at.elapsed() < FINGERPRINT_CHECK_INTERVAL {
                    return;
                }
            }
        }
        let Some(fingerprint) = proxy_fingerprint(&self.store).await else {
            return;
        };
        let mut cache = self.cache.write().await;
        match cache.fingerprint {
            Some((cached, _)) if cached == fingerprint => {
                cache.fingerprint = Some((fingerprint, Instant::now()));
            }
            previous => {
                if previous.is_some() {
                    tracing::info!("proxy configuration changed; rebuilding cached HTTP clients");
                }
                *cache = ClientCache {
                    fingerprint: Some((fingerprint, Instant::now())),
                    ..ClientCache::default()
                };
            }
        }
    }
}

/// Hashes everything that influences how outbound clients route requests:
/// the persisted custom proxy settings and, on macOS, the live system proxy
/// (the same HTTP/HTTPS entries hyper-util's `Matcher::from_system` freezes
/// into each reqwest client at build time).
async fn proxy_fingerprint(store: &Store) -> Option<u64> {
    let settings = store.proxy_settings_secret().await.ok()?;
    let mut hasher = std::collections::hash_map::DefaultHasher::new();
    settings.mode.is_custom().hash(&mut hasher);
    if settings.mode.is_custom() {
        settings.address.hash(&mut hasher);
        settings.auth_enabled.hash(&mut hasher);
        settings.username.hash(&mut hasher);
        settings.password.hash(&mut hasher);
    }
    #[cfg(target_os = "macos")]
    macos::hash_system_proxy(&mut hasher);
    Some(hasher.finish())
}

#[cfg(target_os = "macos")]
mod macos {
    use std::hash::Hasher;

    use system_configuration::core_foundation::base::CFType;
    use system_configuration::core_foundation::dictionary::CFDictionary;
    use system_configuration::core_foundation::number::CFNumber;
    use system_configuration::core_foundation::string::{CFString, CFStringRef};
    use system_configuration::dynamic_store::SCDynamicStoreBuilder;
    use system_configuration::sys::schema_definitions::{
        kSCPropNetProxiesHTTPEnable, kSCPropNetProxiesHTTPPort, kSCPropNetProxiesHTTPProxy,
        kSCPropNetProxiesHTTPSEnable, kSCPropNetProxiesHTTPSPort, kSCPropNetProxiesHTTPSProxy,
    };

    pub fn hash_system_proxy(hasher: &mut impl Hasher) {
        let Some(store) = SCDynamicStoreBuilder::new("cursor-byok").build() else {
            return;
        };
        let Some(proxies) = store.get_proxies() else {
            return;
        };
        unsafe {
            hash_setting(
                &proxies,
                kSCPropNetProxiesHTTPEnable,
                kSCPropNetProxiesHTTPProxy,
                kSCPropNetProxiesHTTPPort,
                hasher,
            );
            hash_setting(
                &proxies,
                kSCPropNetProxiesHTTPSEnable,
                kSCPropNetProxiesHTTPSProxy,
                kSCPropNetProxiesHTTPSPort,
                hasher,
            );
        }
    }

    fn hash_setting(
        proxies: &CFDictionary<CFString, CFType>,
        enable_key: CFStringRef,
        host_key: CFStringRef,
        port_key: CFStringRef,
        hasher: &mut impl Hasher,
    ) {
        let enabled = proxies
            .find(enable_key)
            .and_then(|flag| flag.downcast::<CFNumber>())
            .and_then(|flag| flag.to_i32())
            .unwrap_or(0);
        hasher.write_i32(enabled);
        if enabled == 1 {
            if let Some(host) = proxies
                .find(host_key)
                .and_then(|host| host.downcast::<CFString>())
            {
                hasher.write(host.to_string().as_bytes());
            }
            if let Some(port) = proxies
                .find(port_key)
                .and_then(|port| port.downcast::<CFNumber>())
                .and_then(|port| port.to_i32())
            {
                hasher.write_i32(port);
            }
        }
    }
}

pub async fn client_builder(store: &Store) -> Result<reqwest::ClientBuilder> {
    let settings = store.proxy_settings_secret().await?;
    // Use the platform TLS stack for compatibility with provider gateways that
    // only offer legacy TLS 1.2 cipher suites unsupported by rustls.
    let mut builder = reqwest::Client::builder().use_native_tls();
    if settings.mode.is_custom() {
        builder = builder.proxy(custom_proxy(&settings)?);
    }
    Ok(builder)
}

pub async fn client(store: &Store) -> Result<reqwest::Client> {
    Ok(client_builder(store).await?.build()?)
}

pub async fn blocking_client_builder(store: &Store) -> Result<reqwest::blocking::ClientBuilder> {
    let settings = store.proxy_settings_secret().await?;
    let mut builder = reqwest::blocking::Client::builder().use_native_tls();
    if settings.mode.is_custom() {
        builder = builder.proxy(custom_proxy(&settings)?);
    }
    Ok(builder)
}

fn custom_proxy(settings: &ProxySettingsSecret) -> Result<reqwest::Proxy> {
    let mut proxy = reqwest::Proxy::all(&settings.address)?
        .no_proxy(reqwest::NoProxy::from_string(LOCAL_NO_PROXY));
    if settings.auth_enabled {
        proxy = proxy.basic_auth(&settings.username, &settings.password);
    }
    Ok(proxy)
}

pub fn reject_self_proxy(address: &str, local_proxy_port: u16) -> Result<()> {
    if local_proxy_port == 0 {
        return Ok(());
    }
    let url = url::Url::parse(address)
        .map_err(|error| Error::Config(format!("invalid proxy address: {error}")))?;
    if url.port_or_known_default() == Some(local_proxy_port) && url_host_is_loopback(&url) {
        return Err(Error::Config(
            "proxy address cannot point to the Cursor BYOK local proxy".into(),
        ));
    }
    Ok(())
}

fn url_host_is_loopback(url: &url::Url) -> bool {
    match url.host() {
        Some(url::Host::Domain(host)) => {
            host.trim_end_matches('.').eq_ignore_ascii_case("localhost")
        }
        Some(url::Host::Ipv4(address)) => address.is_loopback(),
        Some(url::Host::Ipv6(address)) => address.is_loopback(),
        None => false,
    }
}

#[cfg(test)]
mod tests {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    use super::*;
    use crate::store::ProxyMode;

    fn custom_settings(address: String) -> ProxySettingsSecret {
        ProxySettingsSecret {
            mode: ProxyMode::Custom,
            address,
            auth_enabled: false,
            username: String::new(),
            password: String::new(),
        }
    }

    #[test]
    fn rejects_only_own_loopback_proxy_port() {
        for address in [
            "http://localhost:15721",
            "http://localhost.:15721",
            "http://127.0.0.2:15721",
            "http://[::1]:15721",
        ] {
            assert!(reject_self_proxy(address, 15721).is_err(), "{address}");
        }
        assert!(reject_self_proxy("http://127.0.0.1:7890", 15721).is_ok());
        assert!(reject_self_proxy("http://192.168.1.2:15721", 15721).is_ok());
        assert!(reject_self_proxy("http://127.0.0.1:15721", 0).is_ok());
    }

    #[tokio::test]
    async fn custom_proxy_bypasses_loopback_destinations() {
        let proxy_listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let proxy_address = proxy_listener.local_addr().unwrap();
        let target_listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let target_address = target_listener.local_addr().unwrap();
        let target = tokio::spawn(async move {
            let (mut socket, _) = target_listener.accept().await.unwrap();
            let mut request = [0_u8; 1024];
            let _ = socket.read(&mut request).await.unwrap();
            socket
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
                .await
                .unwrap();
        });
        let settings = custom_settings(format!("http://{proxy_address}"));
        let client = reqwest::Client::builder()
            .proxy(custom_proxy(&settings).unwrap())
            .build()
            .unwrap();

        let body = client
            .get(format!("http://{target_address}"))
            .send()
            .await
            .unwrap()
            .text()
            .await
            .unwrap();

        assert_eq!(body, "ok");
        target.await.unwrap();
        assert!(
            tokio::time::timeout(Duration::from_millis(50), proxy_listener.accept())
                .await
                .is_err(),
            "loopback destination unexpectedly reached the configured proxy"
        );
    }
}
