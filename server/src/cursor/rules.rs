use std::{fs, path::PathBuf};

use axum::{
    body::Body,
    http::{header, HeaderValue, Request, Response},
};
use chrono::{DateTime, Utc};
use prost::Message;
use uuid::Uuid;

use crate::{config, cursor::connect, Error, Result};

const RULE_EXTENSION: &str = "md";

#[derive(Clone, PartialEq, Message)]
struct KnowledgeBaseAddRequest {
    #[prost(string, tag = "1")]
    knowledge: String,
    #[prost(string, tag = "2")]
    title: String,
    #[prost(string, tag = "3")]
    _git_origin: String,
    #[prost(string, optional, tag = "4")]
    _composer_id: Option<String>,
}

#[derive(Clone, PartialEq, Message)]
struct KnowledgeBaseAddResponse {
    #[prost(bool, tag = "1")]
    success: bool,
    #[prost(string, tag = "2")]
    id: String,
}

#[derive(Clone, PartialEq, Message)]
struct KnowledgeBaseListRequest {
    #[prost(int32, optional, tag = "1")]
    limit: Option<i32>,
    #[prost(string, optional, tag = "2")]
    _git_origin: Option<String>,
}

#[derive(Clone, PartialEq, Message)]
struct KnowledgeBaseListResponse {
    #[prost(message, repeated, tag = "2")]
    all_results: Vec<KnowledgeBaseListItem>,
    #[prost(bool, tag = "1")]
    success: bool,
}

#[derive(Clone, PartialEq, Message)]
struct KnowledgeBaseListItem {
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
struct KnowledgeBaseUpdateRequest {
    #[prost(string, tag = "1")]
    id: String,
    #[prost(string, tag = "2")]
    knowledge: String,
    #[prost(string, tag = "3")]
    title: String,
}

#[derive(Clone, PartialEq, Message)]
struct KnowledgeBaseUpdateResponse {
    #[prost(bool, tag = "1")]
    success: bool,
}

#[derive(Clone, PartialEq, Message)]
struct KnowledgeBaseRemoveRequest {
    #[prost(string, tag = "1")]
    id: String,
}

#[derive(Clone, PartialEq, Message)]
struct KnowledgeBaseRemoveResponse {
    #[prost(bool, tag = "1")]
    success: bool,
}

#[derive(Clone, PartialEq, Message)]
struct FetchRelevantKnowledgeForConversationRequest {
    #[prost(string, tag = "1")]
    _request_id: String,
    #[prost(bytes = "vec", repeated, tag = "2")]
    _conversation: Vec<Vec<u8>>,
    #[prost(string, optional, tag = "3")]
    _git_origin: Option<String>,
    #[prost(int32, optional, tag = "4")]
    limit: Option<i32>,
    #[prost(string, repeated, tag = "5")]
    _tagged_filenames: Vec<String>,
}

#[derive(Clone, PartialEq, Message)]
struct FetchRelevantKnowledgeForConversationResponse {
    #[prost(message, repeated, tag = "1")]
    knowledge_items: Vec<RelevantKnowledgeItem>,
}

#[derive(Clone, PartialEq, Message)]
struct RelevantKnowledgeItem {
    #[prost(string, tag = "1")]
    title: String,
    #[prost(string, tag = "2")]
    knowledge: String,
    #[prost(string, tag = "3")]
    knowledge_id: String,
    #[prost(bool, tag = "4")]
    is_generated: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct Rule {
    id: String,
    knowledge: String,
    created_at: String,
}

pub(crate) fn system_prompt_section(path: PathBuf) -> Result<String> {
    let store = RuleStore::open(path)?;
    let rules = store.list()?;
    let mut section = String::from(
        "<shared_user_rules description=\"These shared local rules apply to every conversation. Follow them when relevant.\">",
    );
    for rule in rules {
        let content = rule_content(&rule.knowledge);
        if content.is_empty() {
            continue;
        }
        section.push_str("\n<rule file=\"");
        section.push_str(&escape_xml(&format!("{}.{}", rule.id, RULE_EXTENSION)));
        section.push_str("\">\n");
        section.push_str(&escape_xml(content));
        section.push_str("\n</rule>");
    }
    if section.lines().count() == 1 {
        return Ok(String::new());
    }
    section.push_str("\n</shared_user_rules>");
    Ok(section)
}

pub async fn add(request: Request<Body>) -> Result<Response<Body>> {
    let request = decode::<KnowledgeBaseAddRequest>(request).await?;
    let knowledge = required_knowledge(&request.knowledge)?;
    let store = RuleStore::open(config::global_rules_dir()?)?;
    let rule = store.add(knowledge)?;
    proto(&KnowledgeBaseAddResponse {
        success: true,
        id: rule.id,
    })
}

pub async fn list(request: Request<Body>) -> Result<Response<Body>> {
    let request = decode::<KnowledgeBaseListRequest>(request).await?;
    let store = RuleStore::open(config::global_rules_dir()?)?;
    let mut rules = store.list()?;
    if let Some(limit) = request.limit.filter(|limit| *limit >= 0) {
        rules.truncate(limit as usize);
    }
    proto(&KnowledgeBaseListResponse {
        success: true,
        all_results: rules
            .into_iter()
            .map(|rule| KnowledgeBaseListItem {
                title: rule.id.clone(),
                id: rule.id,
                knowledge: rule.knowledge,
                created_at: rule.created_at,
                is_generated: false,
            })
            .collect(),
    })
}

pub async fn relevant(request: Request<Body>) -> Result<Response<Body>> {
    let request = decode::<FetchRelevantKnowledgeForConversationRequest>(request).await?;
    let store = RuleStore::open(config::global_rules_dir()?)?;
    let rules = store.list()?;
    proto(&FetchRelevantKnowledgeForConversationResponse {
        knowledge_items: relevant_knowledge_items(rules, request.limit),
    })
}

fn relevant_knowledge_items(rules: Vec<Rule>, limit: Option<i32>) -> Vec<RelevantKnowledgeItem> {
    let limit = limit
        .filter(|limit| *limit >= 0)
        .map(|limit| limit as usize);
    rules
        .into_iter()
        .take(limit.unwrap_or(usize::MAX))
        .map(relevant_knowledge_item)
        .collect()
}

fn relevant_knowledge_item(rule: Rule) -> RelevantKnowledgeItem {
    RelevantKnowledgeItem {
        title: rule.id.clone(),
        knowledge: rule.knowledge,
        knowledge_id: rule.id,
        is_generated: false,
    }
}

pub async fn update(request: Request<Body>) -> Result<Response<Body>> {
    let request = decode::<KnowledgeBaseUpdateRequest>(request).await?;
    let knowledge = required_knowledge(&request.knowledge)?;
    let store = RuleStore::open(config::global_rules_dir()?)?;
    store.update(&request.id, knowledge)?;
    proto(&KnowledgeBaseUpdateResponse { success: true })
}

pub async fn remove(request: Request<Body>) -> Result<Response<Body>> {
    let request = decode::<KnowledgeBaseRemoveRequest>(request).await?;
    let store = RuleStore::open(config::global_rules_dir()?)?;
    store.remove(&request.id)?;
    proto(&KnowledgeBaseRemoveResponse { success: true })
}

async fn decode<M: Message + Default>(request: Request<Body>) -> Result<M> {
    let (_, body) = request.into_parts();
    let body = axum::body::to_bytes(body, usize::MAX)
        .await
        .map_err(|error| Error::Protocol(format!("cannot read rule request body: {error}")))?;
    connect::decode_unary(&body)
}

fn proto(message: &impl Message) -> Result<Response<Body>> {
    let body = message.encode_to_vec();
    let mut response = Response::new(Body::from(body));
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/proto"),
    );
    Ok(response)
}

fn rule_content(knowledge: &str) -> &str {
    let trimmed = knowledge.trim();
    let mut lines = trimmed.split_inclusive('\n');
    if lines.next().is_none_or(|line| line.trim() != "---") {
        return trimmed;
    }
    let mut offset = 0;
    for line in trimmed.split_inclusive('\n') {
        offset += line.len();
        if offset > 4 && line.trim() == "---" {
            return trimmed[offset..].trim();
        }
    }
    trimmed
}

fn escape_xml(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('"', "&quot;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

fn required_knowledge(knowledge: &str) -> Result<String> {
    let knowledge = knowledge.trim();
    if knowledge.is_empty() {
        return Err(Error::Protocol("rule content is required".into()));
    }
    Ok(knowledge.to_owned())
}

struct RuleStore {
    directory: PathBuf,
}

impl RuleStore {
    fn open(directory: PathBuf) -> Result<Self> {
        fs::create_dir_all(&directory)?;
        Ok(Self { directory })
    }

    fn add(&self, knowledge: String) -> Result<Rule> {
        let id = Uuid::new_v4().to_string();
        self.write(&id, &knowledge)?;
        self.read(&id)
    }

    fn list(&self) -> Result<Vec<Rule>> {
        let mut rules = fs::read_dir(&self.directory)?
            .filter_map(|entry| entry.ok())
            .filter_map(|entry| {
                let path = entry.path();
                (path.extension().and_then(|extension| extension.to_str()) == Some(RULE_EXTENSION))
                    .then_some(path)
            })
            .map(|path| self.read_path(path))
            .collect::<Result<Vec<_>>>()?;
        rules.sort_by(|left, right| left.created_at.cmp(&right.created_at));
        Ok(rules)
    }

    fn update(&self, id: &str, knowledge: String) -> Result<()> {
        let id = valid_id(id)?;
        let path = self.path(&id);
        if !path.exists() {
            return Err(Error::RunNotFound(format!("global rule {id}")));
        }
        self.write(&id, &knowledge)
    }

    fn remove(&self, id: &str) -> Result<()> {
        let id = valid_id(id)?;
        let path = self.path(&id);
        if !path.exists() {
            return Err(Error::RunNotFound(format!("global rule {id}")));
        }
        fs::remove_file(path)?;
        Ok(())
    }

    fn read(&self, id: &str) -> Result<Rule> {
        self.read_path(self.path(id))
    }

    fn read_path(&self, path: PathBuf) -> Result<Rule> {
        let knowledge = fs::read_to_string(&path)?;
        let metadata = fs::metadata(&path)?;
        let modified_at: DateTime<Utc> = metadata.modified()?.into();
        let id = path
            .file_stem()
            .and_then(|id| id.to_str())
            .ok_or_else(|| Error::Store("global rule path has no valid ID".into()))?
            .to_owned();
        Ok(Rule {
            id,
            knowledge,
            created_at: modified_at.to_rfc3339(),
        })
    }

    fn write(&self, id: &str, knowledge: &str) -> Result<()> {
        let path = self.path(id);
        let temporary = path.with_extension("tmp");
        fs::write(&temporary, knowledge)?;
        fs::rename(temporary, path)?;
        Ok(())
    }

    fn path(&self, id: &str) -> PathBuf {
        self.directory.join(format!("{id}.{RULE_EXTENSION}"))
    }
}

fn valid_id(id: &str) -> Result<String> {
    let id = id.trim();
    Uuid::parse_str(id).map_err(|_| Error::Protocol("invalid global rule ID".into()))?;
    Ok(id.to_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn relevant_knowledge_is_global_across_git_origins() {
        let rules = vec![
            Rule {
                id: "first-rule".into(),
                knowledge: "always write tests".into(),
                created_at: "2026-08-25T00:00:00Z".into(),
            },
            Rule {
                id: "second-rule".into(),
                knowledge: "keep modules small".into(),
                created_at: "2026-08-25T00:01:00Z".into(),
            },
        ];

        let project_a = relevant_knowledge_items(rules.clone(), Some(10));
        let project_b = relevant_knowledge_items(rules, Some(10));

        assert_eq!(project_a, project_b);
        assert_eq!(
            project_b
                .into_iter()
                .map(|item| item.knowledge_id)
                .collect::<Vec<_>>(),
            vec!["first-rule", "second-rule"]
        );
    }

    #[test]
    fn relevant_knowledge_honors_a_non_negative_limit() {
        let rules = vec![Rule {
            id: "rule-id".into(),
            knowledge: "always write tests".into(),
            created_at: "2026-08-25T00:00:00Z".into(),
        }];

        assert!(relevant_knowledge_items(rules, Some(0)).is_empty());
    }

    #[tokio::test]
    async fn rule_response_uses_raw_protobuf_like_the_cursor_connect_handler() {
        let response = proto(&KnowledgeBaseAddResponse {
            success: true,
            id: "rule-id".into(),
        })
        .unwrap();
        let body = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .unwrap();

        assert_eq!(
            KnowledgeBaseAddResponse::decode(body.as_ref()).unwrap(),
            KnowledgeBaseAddResponse {
                success: true,
                id: "rule-id".into(),
            }
        );
        assert_ne!(body.first(), Some(&0));
    }

    #[test]
    fn rule_store_persists_the_full_global_rule_lifecycle() {
        let directory = tempfile::tempdir().unwrap();
        let store = RuleStore::open(directory.path().to_path_buf()).unwrap();
        let added = store.add("always write tests".into()).unwrap();
        assert_eq!(store.list().unwrap(), vec![added.clone()]);

        store
            .update(&added.id, "always write regression tests".into())
            .unwrap();
        assert_eq!(
            store.read(&added.id).unwrap().knowledge,
            "always write regression tests"
        );

        store.remove(&added.id).unwrap();
        assert!(store.list().unwrap().is_empty());
    }
}
