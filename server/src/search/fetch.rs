//! Fetches and extracts web content.
use std::{
    net::{IpAddr, Ipv4Addr, Ipv6Addr},
    time::Duration,
};

use bytes::BytesMut;
use dom_smoothie::{Config, Readability, TextMode};
use futures_util::StreamExt;
use reqwest::{
    header::{ACCEPT, ACCEPT_LANGUAGE, CONTENT_LENGTH, CONTENT_TYPE, LOCATION, USER_AGENT},
    redirect::Policy,
    Response,
};
use tokio::{net::lookup_host, time::timeout};
use url::{Host, Url};

use crate::store::Store;

use super::{WebCache, WebCacheEntry};

const MAX_RESPONSE_SIZE: usize = 5 * 1024 * 1024;
const MAX_REDIRECTS: usize = 5;
const FETCH_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FetchedPage {
    pub url: String,
    pub markdown: String,
    pub cache: Option<WebCacheEntry>,
}

#[derive(Debug, thiserror::Error)]
#[error("web fetch failed: {0}")]
pub struct FetchError(String);

#[derive(Clone, Debug)]
pub(crate) enum WebFetchRequest {
    Url(String),
    Cached {
        url: String,
        content_id: String,
        offset: usize,
        limit: usize,
    },
}

impl WebFetchRequest {
    pub(crate) fn from_arguments(arguments: &serde_json::Value) -> crate::Result<Self> {
        #[derive(serde::Deserialize)]
        struct Arguments {
            url: String,
            content_id: Option<String>,
            offset: Option<usize>,
            limit: Option<usize>,
        }
        let args: Arguments = serde_json::from_value(arguments.clone())?;
        let invalid = |message: &str| crate::Error::Protocol(message.into());
        parse_url(&args.url).map_err(|error| invalid(&error.to_string()))?;
        match args.content_id {
            None if args.offset.is_none() && args.limit.is_none() => Ok(Self::Url(args.url)),
            Some(content_id) => {
                let id = uuid::Uuid::parse_str(&content_id)
                    .map_err(|_| invalid("WebFetch content_id must be a canonical UUID"))?;
                if id.to_string() != content_id {
                    return Err(invalid("WebFetch content_id must be a canonical UUID"));
                }
                let limit = args.limit.unwrap_or(super::cache::WEB_CACHE_PAGE_BYTES);
                if !(4..=super::cache::WEB_CACHE_PAGE_BYTES).contains(&limit) {
                    return Err(invalid("WebFetch limit must be between 4 and 16384 bytes"));
                }
                Ok(Self::Cached {
                    url: args.url,
                    content_id,
                    offset: args.offset.unwrap_or(0),
                    limit,
                })
            }
            _ => Err(invalid("WebFetch offset and limit require content_id")),
        }
    }

    // Keep Cursor's native URL approval unchanged for both operations. Cached reads
    // verify this approved URL against their persisted source before returning content.
    pub(crate) fn approval_target(&self) -> String {
        match self {
            Self::Url(url) | Self::Cached { url, .. } => url.clone(),
        }
    }
}

#[derive(Clone)]
pub struct WebFetch {
    client: FetchClient,
    cache: WebCache,
}

#[derive(Clone)]
enum FetchClient {
    Managed(Store),
    Direct,
}

impl WebFetch {
    pub fn built_in() -> Self {
        Self {
            client: FetchClient::Direct,
            cache: WebCache::default(),
        }
    }

    pub(crate) fn managed(store: Store, cache: WebCache) -> Self {
        Self {
            client: FetchClient::Managed(store),
            cache,
        }
    }

    pub async fn fetch(
        &self,
        conversation_id: &str,
        value: &str,
    ) -> Result<FetchedPage, FetchError> {
        if conversation_id.is_empty() {
            return Err(failure("WebFetch requires a conversation ID"));
        }
        let mut page = timeout(FETCH_TIMEOUT, self.fetch_inner(value))
            .await
            .map_err(|_| failure("request timed out"))??;
        let (markdown, cache) = self
            .cache
            .store(conversation_id, &page.url, page.markdown)
            .await
            .map_err(|error| failure(format!("cannot cache fetched content: {error}")))?;
        page.markdown = markdown;
        page.cache = Some(cache);
        Ok(page)
    }

    pub(crate) async fn execute(
        &self,
        conversation_id: &str,
        request: WebFetchRequest,
    ) -> Result<FetchedPage, FetchError> {
        match request {
            WebFetchRequest::Url(url) => self.fetch(conversation_id, &url).await,
            WebFetchRequest::Cached {
                url: approved_url,
                content_id,
                offset,
                limit,
            } => {
                let (url, markdown, cache) = self
                    .cache
                    .read(conversation_id, &content_id, offset, limit)
                    .await
                    .map_err(|error| failure(error.to_string()))?;
                if url != approved_url {
                    return Err(failure("cached content does not match the approved URL"));
                }
                Ok(FetchedPage {
                    url,
                    markdown,
                    cache: Some(cache),
                })
            }
        }
    }

    async fn fetch_inner(&self, value: &str) -> Result<FetchedPage, FetchError> {
        let mut url = parse_url(value)?;
        for redirect in 0..=MAX_REDIRECTS {
            let response = self.request(&url).await?;
            if response.status().is_redirection() {
                if redirect == MAX_REDIRECTS {
                    return Err(failure("too many redirects"));
                }
                let location = response
                    .headers()
                    .get(LOCATION)
                    .and_then(|value| value.to_str().ok())
                    .ok_or_else(|| failure("redirect is missing Location"))?;
                url = parse_url(
                    url.join(location)
                        .map_err(|error| failure(format!("invalid redirect: {error}")))?
                        .as_str(),
                )?;
                continue;
            }
            if !response.status().is_success() {
                return Err(failure(format!("HTTP {}", response.status())));
            }
            return page(response).await;
        }
        unreachable!("redirect loop always returns")
    }

    async fn request(&self, url: &Url) -> Result<Response, FetchError> {
        let host = url
            .host_str()
            .ok_or_else(|| failure("URL is missing a host"))?;
        let port = url
            .port_or_known_default()
            .ok_or_else(|| failure("URL has no usable port"))?;
        let addresses = lookup_host((host, port))
            .await
            .map_err(|error| failure(format!("DNS lookup failed: {error}")))?
            .collect::<Vec<_>>();
        if addresses.is_empty() {
            return Err(failure("DNS lookup returned no addresses"));
        }
        let domain = matches!(url.host(), Some(Host::Domain(_)));
        if addresses
            .iter()
            .any(|address| !safe_resolution(address.ip(), domain))
        {
            return Err(failure("URL resolves to a non-public address"));
        }

        let builder = match &self.client {
            FetchClient::Managed(store) => crate::network::client_builder(store)
                .await
                .map_err(|error| failure(format!("HTTP client failed: {error}")))?,
            FetchClient::Direct => reqwest::Client::builder().use_native_tls(),
        };
        let mut builder = builder
            .redirect(Policy::none())
            .connect_timeout(Duration::from_secs(10));
        if domain {
            builder = builder.resolve_to_addrs(host, &addresses);
        }
        let client = builder
            .build()
            .map_err(|error| failure(format!("HTTP client failed: {error}")))?;
        client
            .get(url.clone())
            .header(
                USER_AGENT,
                "Mozilla/5.0 (compatible; CursorBYOK/0.1; +https://github.com)",
            )
            .header(
                ACCEPT,
                "text/markdown, text/plain;q=0.9, text/html;q=0.8, application/xhtml+xml;q=0.8, application/json;q=0.7, */*;q=0.1",
            )
            .header(ACCEPT_LANGUAGE, "en-US,en;q=0.9")
            .send()
            .await
            .map_err(|error| failure(format!("request failed: {error}")))
    }
}

impl Default for WebFetch {
    fn default() -> Self {
        Self::built_in()
    }
}

async fn page(response: Response) -> Result<FetchedPage, FetchError> {
    let url = response.url().to_string();
    let content_type = response
        .headers()
        .get(CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .unwrap_or("application/octet-stream")
        .to_string();
    if response
        .headers()
        .get(CONTENT_LENGTH)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse::<usize>().ok())
        .is_some_and(|length| length > MAX_RESPONSE_SIZE)
    {
        return Err(failure("response exceeds 5 MiB"));
    }
    let body = limited_body(response).await?;
    let text = decode(&body, &content_type)?;
    let media_type = content_type
        .split(';')
        .next()
        .unwrap_or_default()
        .trim()
        .to_ascii_lowercase();
    let markdown = match media_type.as_str() {
        "text/html" | "application/xhtml+xml" => {
            let source_url = url.clone();
            tokio::task::spawn_blocking(move || readable_markdown(&text, &source_url))
                .await
                .map_err(|error| failure(format!("content task failed: {error}")))??
        }
        "text/markdown" | "text/x-markdown" | "text/plain" => text,
        "application/json" => format!("```json\n{text}\n```"),
        "application/xml" | "text/xml" => format!("```xml\n{text}\n```"),
        value if value.starts_with("text/") => text,
        _ => return Err(failure(format!("unsupported content type: {media_type}"))),
    };
    if markdown.trim().is_empty() {
        return Err(failure("response contains no readable content"));
    }
    Ok(FetchedPage {
        url,
        markdown,
        cache: None,
    })
}

async fn limited_body(response: Response) -> Result<BytesMut, FetchError> {
    let mut body = BytesMut::new();
    let mut stream = response.bytes_stream();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|error| failure(format!("response failed: {error}")))?;
        if body.len() + chunk.len() > MAX_RESPONSE_SIZE {
            return Err(failure("response exceeds 5 MiB"));
        }
        body.extend_from_slice(&chunk);
    }
    Ok(body)
}

fn readable_markdown(html: &str, url: &str) -> Result<String, FetchError> {
    let mut readability = Readability::new(
        html,
        Some(url),
        Some(Config {
            max_elements_to_parse: 50_000,
            text_mode: TextMode::Markdown,
            ..Default::default()
        }),
    )
    .map_err(|error| failure(format!("HTML parse failed: {error}")))?;
    let article = readability
        .parse()
        .map_err(|error| failure(format!("article extraction failed: {error}")))?;
    let body = article.text_content.trim().to_string();
    let title = article.title.trim();
    let heading = format!("# {title}");
    Ok(if title.is_empty() || body.starts_with(&heading) {
        body
    } else {
        format!("# {title}\n\n{body}")
    })
}

fn decode(bytes: &[u8], content_type: &str) -> Result<String, FetchError> {
    let charset = content_type.split(';').skip(1).find_map(|parameter| {
        let (name, value) = parameter.trim().split_once('=')?;
        name.trim()
            .eq_ignore_ascii_case("charset")
            .then(|| value.trim().trim_matches(['\'', '"']))
    });
    let encoding = match charset {
        Some(label) => encoding_rs::Encoding::for_label(label.as_bytes())
            .ok_or_else(|| failure(format!("unsupported charset: {label}")))?,
        None => encoding_rs::UTF_8,
    };
    let (text, _, malformed) = encoding.decode(bytes);
    if malformed {
        return Err(failure("response contains malformed text"));
    }
    Ok(text.into_owned())
}

fn parse_url(value: &str) -> Result<Url, FetchError> {
    let url = Url::parse(value).map_err(|error| failure(format!("invalid URL: {error}")))?;
    if value.len() > 8192 || url.as_str().len() > 8192 {
        return Err(failure("URL exceeds 8192 bytes"));
    }
    if !matches!(url.scheme(), "http" | "https") {
        return Err(failure("URL must use http or https"));
    }
    if !url.username().is_empty() || url.password().is_some() {
        return Err(failure("URL credentials are not allowed"));
    }
    if url.host_str().is_none() {
        return Err(failure("URL is missing a host"));
    }
    Ok(url)
}

fn is_public(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(ip) => public_v4(ip),
        IpAddr::V6(ip) => public_v6(ip),
    }
}

fn safe_resolution(ip: IpAddr, domain: bool) -> bool {
    is_public(ip) || (domain && is_benchmark_proxy_range(ip))
}

fn is_benchmark_proxy_range(ip: IpAddr) -> bool {
    let IpAddr::V4(ip) = ip else {
        return false;
    };
    u32::from(ip) >> 17 == u32::from(Ipv4Addr::new(198, 18, 0, 0)) >> 17
}

fn public_v4(ip: Ipv4Addr) -> bool {
    let value = u32::from(ip);
    ![
        (0x0000_0000, 8),
        (0x0a00_0000, 8),
        (0x6440_0000, 10),
        (0x7f00_0000, 8),
        (0xa9fe_0000, 16),
        (0xac10_0000, 12),
        (0xc000_0000, 24),
        (0xc000_0200, 24),
        (0xc0a8_0000, 16),
        (0xc612_0000, 15),
        (0xc633_6400, 24),
        (0xcb00_7100, 24),
        (0xe000_0000, 3),
    ]
    .into_iter()
    .any(|(network, prefix)| value >> (32 - prefix) == network >> (32 - prefix))
}

fn public_v6(ip: Ipv6Addr) -> bool {
    let segments = ip.segments();
    segments[0] & 0xe000 == 0x2000 && !(segments[0] == 0x2001 && segments[1] == 0x0db8)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn web_fetch_request_validates_sources_and_paging() {
        let id = "550e8400-e29b-41d4-a716-446655440000";
        let request = WebFetchRequest::from_arguments(
            &json!({"url": "https://example.com", "content_id": id}),
        )
        .unwrap();
        assert!(matches!(
            request,
            WebFetchRequest::Cached {
                offset: 0,
                limit: 16384,
                ..
            }
        ));
        assert!(WebFetchRequest::from_arguments(&json!({"url": "https://example.com"})).is_ok());
        for arguments in [
            json!({}),
            json!({"url": "https://example.com", "content_id": "../etc/passwd"}),
            json!({"content_id": id}),
            json!({"url": "https://example.com", "content_id": id, "offset": -1}),
            json!({"url": "https://example.com", "content_id": id, "offset": 1.5}),
            json!({"url": "https://example.com", "content_id": id, "limit": 0}),
            json!({"url": "https://example.com", "content_id": id, "limit": 16385}),
            json!({"url": "https://example.com", "offset": 10}),
            json!({"url": "file:///etc/passwd"}),
            json!({"url": "https://name:password@example.com"}),
            json!({"url": format!("cursor-byok-cache://{id}")}),
        ] {
            assert!(
                WebFetchRequest::from_arguments(&arguments).is_err(),
                "accepted {arguments}"
            );
        }
    }

    #[tokio::test]
    async fn web_fetch_cached_read_requires_approved_source_url() {
        let fetch = WebFetch::built_in();
        let (_, entry) = fetch
            .cache
            .store(
                "owner",
                "https://example.com/original",
                "cached private text".into(),
            )
            .await
            .unwrap();
        let request = WebFetchRequest::from_arguments(
            &json!({"url": "https://example.com/different", "content_id": entry.content_id}),
        )
        .unwrap();
        assert!(fetch
            .execute("owner", request)
            .await
            .unwrap_err()
            .to_string()
            .contains("does not match the approved URL"));
        let request = WebFetchRequest::from_arguments(
            &json!({"url": "https://example.com/original", "content_id": entry.content_id}),
        )
        .unwrap();
        assert_eq!(
            fetch.execute("owner", request).await.unwrap().markdown,
            "cached private text"
        );
    }

    #[tokio::test]
    async fn web_fetch_cache_does_not_weaken_public_address_protection() {
        let fetch = WebFetch::built_in();
        for url in [
            "http://127.0.0.1/page",
            "http://10.0.0.1/page",
            "http://169.254.169.254/latest/meta-data/",
        ] {
            assert!(fetch
                .fetch("owner", url)
                .await
                .unwrap_err()
                .to_string()
                .contains("non-public"));
        }
        // The existing resolver may reject bracketed IPv6 at lookup rather than at
        // the public-address check; neither path may return localhost content.
        assert!(fetch.fetch("owner", "http://[::1]/page").await.is_err());
        for address in [
            "127.0.0.1",
            "10.0.0.1",
            "::1",
            "::ffff:127.0.0.1",
            "169.254.169.254",
            "192.168.0.1",
        ] {
            assert!(!safe_resolution(address.parse().unwrap(), true));
        }
        assert!(safe_resolution("8.8.8.8".parse().unwrap(), true));
    }

    #[test]
    fn web_fetch_model_schema_exposes_cached_reads() {
        let catalog: serde_json::Value =
            serde_json::from_str(include_str!("../../prompt/cursor/tools.json")).unwrap();
        let tools = catalog
            .as_array()
            .or_else(|| catalog.get("tools").and_then(serde_json::Value::as_array))
            .unwrap();
        let tool = tools
            .iter()
            .find(|tool| tool["function"]["name"] == "WebFetch")
            .unwrap();
        let parameters = &tool["function"]["parameters"];
        assert_eq!(parameters["properties"]["limit"]["maximum"], 16384);
        assert_eq!(parameters["properties"]["offset"]["minimum"], 0);
        assert_eq!(parameters["required"], json!(["url"]));
        assert_eq!(parameters["properties"]["content_id"]["type"], "string");
    }
}

fn failure(message: impl Into<String>) -> FetchError {
    FetchError(message.into())
}
