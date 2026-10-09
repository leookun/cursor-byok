//! Replays the offline journal to upstream and mirrors upstream list state.
use axum::{
    body::{Body, Bytes},
    http::{header, HeaderMap, HeaderValue, Method, Request},
};
use prost::Message;

use crate::{api::cursor::proxy, cursor::protocol::connect, Result};

use super::{
    store::{JournalOp, RuleRecord, RuleStore},
    KnowledgeBaseAddRequest, KnowledgeBaseAddResponse, KnowledgeBaseListItem,
    KnowledgeBaseRemoveRequest, KnowledgeBaseRemoveResponse, KnowledgeBaseUpdateRequest,
    KnowledgeBaseUpdateResponse,
};

const ADD_PATH: &str = "/aiserver.v1.AiService/KnowledgeBaseAdd";
const UPDATE_PATH: &str = "/aiserver.v1.AiService/KnowledgeBaseUpdate";
const REMOVE_PATH: &str = "/aiserver.v1.AiService/KnowledgeBaseRemove";

/// 逐条把离线日志推送到上游。返回 true 表示日志已清空(上游可用),
/// false 表示上游不可达,剩余日志保留、调用方应降级到本地。
pub async fn replay(
    upstream: &proxy::CursorProxy,
    headers: &HeaderMap,
    store: &RuleStore,
) -> Result<bool> {
    while let Some(entry) = store.journal_front()? {
        let advanced = match entry.op {
            JournalOp::Add => replay_add(upstream, headers, store, &entry.id).await?,
            JournalOp::Update => replay_update(upstream, headers, store, &entry.id).await?,
            JournalOp::Remove => replay_remove(upstream, headers, store, &entry.id).await?,
        };
        if !advanced {
            return Ok(false);
        }
    }
    Ok(true)
}

/// 用上游返回的完整列表覆盖本地镜像。仅应在日志已清空时调用。
pub fn mirror(store: &RuleStore, items: Vec<KnowledgeBaseListItem>) -> Result<()> {
    tracing::info!(
        count = items.len(),
        items = ?items
            .iter()
            .map(|item| (item.id.clone(), item.title.clone()))
            .collect::<Vec<_>>(),
        "KB mirror from upstream"
    );
    let records = items
        .into_iter()
        .map(|item| RuleRecord {
            id: item.id,
            knowledge: item.knowledge,
            title: item.title,
            created_at: item.created_at,
            is_generated: item.is_generated,
            git_origin: String::new(),
        })
        .collect::<Vec<_>>();
    store.replace_all(&records)
}

async fn replay_add(
    upstream: &proxy::CursorProxy,
    headers: &HeaderMap,
    store: &RuleStore,
    id: &str,
) -> Result<bool> {
    let Some(record) = store.get(id)? else {
        // 规则文件已不在(被手动删除等),日志作废。
        store.pop_journal()?;
        return Ok(true);
    };
    let message = KnowledgeBaseAddRequest {
        knowledge: record.knowledge,
        title: record.title,
        git_origin: record.git_origin,
        composer_id: None,
    };
    let Some(body) = send(upstream, headers, ADD_PATH, &message).await else {
        return Ok(false);
    };
    let Ok(reply) = connect::decode_unary::<KnowledgeBaseAddResponse>(&body) else {
        return Ok(false);
    };
    if !reply.success || reply.id.is_empty() {
        tracing::warn!(
            id,
            "rules upstream declined replayed add; dropping journal entry"
        );
        store.pop_journal()?;
        return Ok(true);
    }
    store.promote(id, &reply.id)?;
    store.pop_journal()?;
    tracing::info!(
        local_id = id,
        upstream_id = reply.id,
        "replayed offline rule add to upstream"
    );
    Ok(true)
}

async fn replay_update(
    upstream: &proxy::CursorProxy,
    headers: &HeaderMap,
    store: &RuleStore,
    id: &str,
) -> Result<bool> {
    let Some(record) = store.get(id)? else {
        store.pop_journal()?;
        return Ok(true);
    };
    let message = KnowledgeBaseUpdateRequest {
        id: id.into(),
        knowledge: record.knowledge,
        title: record.title,
    };
    let Some(body) = send(upstream, headers, UPDATE_PATH, &message).await else {
        return Ok(false);
    };
    let Ok(reply) = connect::decode_unary::<KnowledgeBaseUpdateResponse>(&body) else {
        return Ok(false);
    };
    if !reply.success {
        tracing::warn!(
            id,
            "rules upstream declined replayed update; dropping journal entry"
        );
    }
    store.pop_journal()?;
    Ok(true)
}

async fn replay_remove(
    upstream: &proxy::CursorProxy,
    headers: &HeaderMap,
    store: &RuleStore,
    id: &str,
) -> Result<bool> {
    let message = KnowledgeBaseRemoveRequest { id: id.into() };
    let Some(body) = send(upstream, headers, REMOVE_PATH, &message).await else {
        return Ok(false);
    };
    let Ok(reply) = connect::decode_unary::<KnowledgeBaseRemoveResponse>(&body) else {
        return Ok(false);
    };
    if !reply.success {
        tracing::warn!(
            id,
            "rules upstream declined replayed remove; dropping journal entry"
        );
    }
    store.pop_journal()?;
    Ok(true)
}

/// 以当前请求的头为模板向上游发起一次 unary RPC。
/// 成功(2xx)返回响应体;不可达或被拒绝返回 None,由调用方保留日志。
async fn send(
    upstream: &proxy::CursorProxy,
    template: &HeaderMap,
    path: &str,
    message: &impl Message,
) -> Option<Bytes> {
    let mut headers = template.clone();
    // 模板里的上游 URL 头指向原始 RPC 路径,必须移除才能命中回放路径。
    headers.remove(proxy::UPSTREAM_URL_HEADER);
    headers.remove(header::CONTENT_LENGTH);
    headers.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/proto"),
    );
    let mut request = Request::new(Body::from(message.encode_to_vec()));
    *request.method_mut() = Method::POST;
    *request.uri_mut() = path.parse().expect("replay path is a valid URI");
    *request.headers_mut() = headers;

    match proxy::forward_buffered(upstream, request).await {
        Ok(response) if response.status.is_success() => Some(response.body),
        Ok(response) => {
            tracing::warn!(path, status = %response.status, "rules journal replay rejected by upstream");
            None
        }
        Err(error) => {
            tracing::warn!(path, %error, "rules journal replay cannot reach upstream");
            None
        }
    }
}
