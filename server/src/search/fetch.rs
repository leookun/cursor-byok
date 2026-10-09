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

    pub async fn fetch(&self, value: &str) -> Result<FetchedPage, FetchError> {
        let mut page = timeout(FETCH_TIMEOUT, self.fetch_inner(value))
            .await
            .map_err(|_| failure("request timed out"))??;
        page.cache = self
            .cache
            .store(&page.markdown)
            .await
            .map_err(|error| failure(format!("cannot cache fetched content: {error}")))?;
        Ok(page)
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
            FetchClient::Direct => reqwest::Client::builder().use_native_tls().http1_only(),
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

fn failure(message: impl Into<String>) -> FetchError {
    FetchError(message.into())
}
