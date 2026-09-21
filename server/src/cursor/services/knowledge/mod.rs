//! Serves Cursor user rules: upstream-first with an offline markdown cache.
//!
//! 每个请求先回放离线日志再尝试上游;上游成功时把结果写穿到本地镜像,
//! 上游不可达时降级为本地 md 存储并记录日志等待回放。
mod store;
mod sync;

use std::sync::Arc;

use axum::{
    body::{to_bytes, Body, Bytes},
    extract::Extension,
    http::{header, Request, Response},
};
use prost::Message;

use crate::{api::cursor::proxy, config, cursor::protocol::connect, Result};

pub(crate) use store::{RuleRecord, RuleStore};

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseAddRequest {
    #[prost(string, tag = "1")]
    knowledge: String,
    #[prost(string, tag = "2")]
    title: String,
    #[prost(string, tag = "3")]
    git_origin: String,
    #[prost(string, optional, tag = "4")]
    composer_id: Option<String>,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseAddResponse {
    #[prost(bool, tag = "1")]
    success: bool,
    #[prost(string, tag = "2")]
    id: String,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseListRequest {
    #[prost(int32, optional, tag = "1")]
    limit: Option<i32>,
    #[prost(string, optional, tag = "2")]
    git_origin: Option<String>,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseListResponse {
    #[prost(bool, tag = "1")]
    success: bool,
    #[prost(message, repeated, tag = "2")]
    all_results: Vec<KnowledgeBaseListItem>,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseListItem {
    #[prost(string, tag = "1")]
    id: String,
    #[prost(string, tag = "2")]
    knowledge: String,
    #[prost(string, tag = "3")]
    title: String,
    #[prost(string, tag = "4")]
    created_at: String,
    #[prost(bool, tag = "5")]
    is_generated: bool,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseUpdateRequest {
    #[prost(string, tag = "1")]
    id: String,
    #[prost(string, tag = "2")]
    knowledge: String,
    #[prost(string, tag = "3")]
    title: String,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseUpdateResponse {
    #[prost(bool, tag = "1")]
    success: bool,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseRemoveRequest {
    #[prost(string, tag = "1")]
    id: String,
}

#[derive(Clone, PartialEq, Message)]
pub(crate) struct KnowledgeBaseRemoveResponse {
    #[prost(bool, tag = "1")]
    success: bool,
}

/// 规则存储与并发锁;经 axum Extension 注入四个 handler。
#[derive(Clone)]
pub struct KnowledgeService {
    inner: Arc<Inner>,
}

struct Inner {
    store: RuleStore,
    lock: tokio::sync::Mutex<()>,
}

impl KnowledgeService {
    pub fn managed() -> Result<Self> {
        Self::with_root(config::managed_data_dir()?.join("rules"))
    }

    /// 指定存储根目录构造;managed() 与集成测试共用。
    pub fn with_root(root: std::path::PathBuf) -> Result<Self> {
        Ok(Self {
            inner: Arc::new(Inner {
                store: RuleStore::open(root)?,
                lock: tokio::sync::Mutex::new(()),
            }),
        })
    }
}

pub async fn add(
    Extension(upstream): Extension<proxy::CursorProxy>,
    Extension(service): Extension<KnowledgeService>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let (parts, body) = buffered(request).await?;
    let message: KnowledgeBaseAddRequest = connect::decode_unary(&body)?;
    tracing::info!(
        title = %message.title,
        knowledge = %message.knowledge,
        git_origin = %message.git_origin,
        composer_id = ?message.composer_id,
        "KB add request"
    );
    let _guard = service.inner.lock.lock().await;
    let store = &service.inner.store;

    if store.is_blocked(&message.knowledge, &message.title) {
        tracing::warn!(
            title = %message.title,
            knowledge = %message.knowledge,
            "KB add blocked by .blocklist; not storing, not forwarding upstream"
        );
        return proto(KnowledgeBaseAddResponse {
            success: true,
            id: "blocked".into(),
        });
    }

    if sync::replay(&upstream, &parts.headers, store).await? {
        match proxy::forward_buffered(&upstream, Request::from_parts(parts, Body::from(body))).await
        {
            Ok(response) if response.status.is_success() => {
                if let Ok(reply) = connect::decode_unary::<KnowledgeBaseAddResponse>(&response.body)
                {
                    if reply.success && !reply.id.is_empty() {
                        store.upsert(&RuleRecord {
                            id: reply.id,
                            knowledge: message.knowledge,
                            title: message.title,
                            created_at: now(),
                            is_generated: false,
                            git_origin: message.git_origin,
                        })?;
                    }
                }
                return Ok(response.into_response());
            }
            Ok(response) => {
                tracing::warn!(status = %response.status, "rules upstream rejected add; storing locally");
            }
            Err(error) => {
                tracing::warn!(%error, "rules upstream unavailable for add; storing locally");
            }
        }
    }

    let id = format!("{}{}", store::LOCAL_ID_PREFIX, uuid::Uuid::new_v4());
    store.upsert(&RuleRecord {
        id: id.clone(),
        knowledge: message.knowledge,
        title: message.title,
        created_at: now(),
        is_generated: false,
        git_origin: message.git_origin,
    })?;
    store.record_add(&id)?;
    proto(KnowledgeBaseAddResponse { success: true, id })
}

pub async fn list(
    Extension(upstream): Extension<proxy::CursorProxy>,
    Extension(service): Extension<KnowledgeService>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let (parts, body) = buffered(request).await?;
    let message: KnowledgeBaseListRequest = connect::decode_unary(&body)?;
    let _guard = service.inner.lock.lock().await;
    let store = &service.inner.store;
    let git_origin = message.git_origin.unwrap_or_default();

    if sync::replay(&upstream, &parts.headers, store).await? {
        match proxy::forward_buffered(&upstream, Request::from_parts(parts, Body::from(body))).await
        {
            Ok(response) if response.status.is_success() => {
                if let Ok(reply) =
                    connect::decode_unary::<KnowledgeBaseListResponse>(&response.body)
                {
                    // 带 git_origin 过滤的列表只是子集,整体覆盖会误删其他规则。
                    if reply.success && git_origin.is_empty() {
                        sync::mirror(store, reply.all_results)?;
                    }
                }
                return Ok(response.into_response());
            }
            Ok(response) => {
                tracing::warn!(status = %response.status, "rules upstream rejected list; serving local cache");
            }
            Err(error) => {
                tracing::warn!(%error, "rules upstream unavailable for list; serving local cache");
            }
        }
    }

    let mut records = store.list()?;
    if !git_origin.is_empty() {
        records.retain(|record| record.git_origin == git_origin);
    }
    if let Some(limit) = message.limit {
        if limit >= 0 {
            records.truncate(limit as usize);
        }
    }
    proto(KnowledgeBaseListResponse {
        success: true,
        all_results: records
            .into_iter()
            .map(|record| KnowledgeBaseListItem {
                id: record.id,
                knowledge: record.knowledge,
                title: record.title,
                created_at: record.created_at,
                is_generated: record.is_generated,
            })
            .collect(),
    })
}

pub async fn update(
    Extension(upstream): Extension<proxy::CursorProxy>,
    Extension(service): Extension<KnowledgeService>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let (parts, body) = buffered(request).await?;
    let message: KnowledgeBaseUpdateRequest = connect::decode_unary(&body)?;
    tracing::info!(
        id = %message.id,
        title = %message.title,
        knowledge = %message.knowledge,
        "KB update request"
    );
    let _guard = service.inner.lock.lock().await;
    let store = &service.inner.store;

    if store.is_blocked(&message.knowledge, &message.title) {
        tracing::warn!(
            id = %message.id,
            title = %message.title,
            knowledge = %message.knowledge,
            "KB update blocked by .blocklist; not storing, not forwarding upstream"
        );
        let _ = store.remove(&message.id);
        return proto(KnowledgeBaseUpdateResponse { success: true });
    }

    if sync::replay(&upstream, &parts.headers, store).await? {
        match proxy::forward_buffered(&upstream, Request::from_parts(parts, Body::from(body))).await
        {
            Ok(response) if response.status.is_success() => {
                if let Ok(reply) =
                    connect::decode_unary::<KnowledgeBaseUpdateResponse>(&response.body)
                {
                    if reply.success {
                        let existing = store.get(&message.id)?;
                        store.upsert(&RuleRecord {
                            id: message.id,
                            knowledge: message.knowledge,
                            title: message.title,
                            created_at: existing
                                .as_ref()
                                .map_or_else(now, |record| record.created_at.clone()),
                            is_generated: existing
                                .as_ref()
                                .is_some_and(|record| record.is_generated),
                            git_origin: existing
                                .map(|record| record.git_origin)
                                .unwrap_or_default(),
                        })?;
                    }
                }
                return Ok(response.into_response());
            }
            Ok(response) => {
                tracing::warn!(status = %response.status, "rules upstream rejected update; storing locally");
            }
            Err(error) => {
                tracing::warn!(%error, "rules upstream unavailable for update; storing locally");
            }
        }
    }

    let Some(mut record) = store.get(&message.id)? else {
        return proto(KnowledgeBaseUpdateResponse { success: false });
    };
    record.knowledge = message.knowledge;
    record.title = message.title;
    store.upsert(&record)?;
    store.record_update(&message.id)?;
    proto(KnowledgeBaseUpdateResponse { success: true })
}

pub async fn remove(
    Extension(upstream): Extension<proxy::CursorProxy>,
    Extension(service): Extension<KnowledgeService>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let (parts, body) = buffered(request).await?;
    let message: KnowledgeBaseRemoveRequest = connect::decode_unary(&body)?;
    tracing::info!(id = %message.id, "KB remove request");
    let _guard = service.inner.lock.lock().await;
    let store = &service.inner.store;

    if sync::replay(&upstream, &parts.headers, store).await? {
        match proxy::forward_buffered(&upstream, Request::from_parts(parts, Body::from(body))).await
        {
            Ok(response) if response.status.is_success() => {
                if let Ok(reply) =
                    connect::decode_unary::<KnowledgeBaseRemoveResponse>(&response.body)
                {
                    if reply.success {
                        store.remove(&message.id)?;
                    }
                }
                return Ok(response.into_response());
            }
            Ok(response) => {
                tracing::warn!(status = %response.status, "rules upstream rejected remove; removing locally");
            }
            Err(error) => {
                tracing::warn!(%error, "rules upstream unavailable for remove; removing locally");
            }
        }
    }

    store.remove(&message.id)?;
    store.record_remove(&message.id)?;
    proto(KnowledgeBaseRemoveResponse { success: true })
}

async fn buffered(request: Request<Body>) -> Result<(axum::http::request::Parts, Bytes)> {
    let (parts, body) = request.into_parts();
    let body = to_bytes(body, usize::MAX)
        .await
        .map_err(|error| crate::Error::Protocol(format!("cannot read request body: {error}")))?;
    Ok((parts, body))
}

fn now() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true)
}

fn proto(message: impl Message) -> Result<Response<Body>> {
    let body = message.encode_to_vec();
    let length = body.len();
    let mut response = Response::new(Body::from(body));
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        axum::http::HeaderValue::from_static("application/proto"),
    );
    response.headers_mut().insert(
        header::CONTENT_LENGTH,
        length
            .to_string()
            .parse()
            .expect("body length is always a valid header value"),
    );
    Ok(response)
}
