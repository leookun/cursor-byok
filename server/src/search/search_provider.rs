//! Implements the configured external search provider adapter.
use std::sync::Arc;

use semble_core::{ContentType, FindRelatedRequest, SearchEngine, SearchRequest, SembleConfig};
use serde::Deserialize;
use serde_json::Value;
use tokio::sync::OnceCell;

use crate::{store::Store, Error, Result};

use super::host_snapshot::{self, relativize_path, rewrite_result_paths};

static ENGINE: OnceCell<Arc<SearchEngine>> = OnceCell::const_new();

#[cfg(test)]
static TEST_ENGINE: std::sync::Mutex<Option<Arc<SearchEngine>>> = std::sync::Mutex::new(None);

#[derive(Clone, Copy, Debug, Default, Deserialize)]
#[serde(rename_all = "snake_case")]
enum ContentSelection {
    #[default]
    Code,
    Docs,
    Config,
    All,
}

#[derive(Debug, Deserialize)]
struct SearchArguments {
    query: String,
    repo: String,
    #[serde(default = "default_top_k")]
    top_k: usize,
    #[serde(default = "default_snippet_lines")]
    max_snippet_lines: Option<usize>,
    #[serde(default)]
    content: ContentSelection,
}

#[derive(Debug, Deserialize)]
struct FindRelatedArguments {
    repo: String,
    file_path: String,
    line: usize,
    #[serde(default = "default_top_k")]
    top_k: usize,
    #[serde(default = "default_snippet_lines")]
    max_snippet_lines: Option<usize>,
    #[serde(default)]
    content: ContentSelection,
}

enum Operation {
    Search(SearchArguments),
    FindRelated(FindRelatedArguments),
}

pub(crate) async fn execute(
    tool_name: &str,
    arguments: Value,
    store: Option<Store>,
) -> std::result::Result<Value, String> {
    let operation = parse_operation(tool_name, arguments)?;
    let engine = engine(store).await.map_err(|error| error.to_string())?;
    tokio::task::spawn_blocking(move || run(engine.as_ref(), operation, None))
        .await
        .map_err(|error| format!("Semble search worker failed: {error}"))?
}

pub(crate) async fn execute_on_root(
    tool_name: &str,
    arguments: Value,
    root: std::path::PathBuf,
    remote_root: String,
    identity: String,
    store: Option<Store>,
) -> std::result::Result<Value, String> {
    let operation = parse_operation(tool_name, arguments)?;
    let engine = engine(store).await.map_err(|error| error.to_string())?;
    tokio::task::spawn_blocking(move || {
        run(
            engine.as_ref(),
            operation,
            Some(PreparedSource {
                root,
                remote_root,
                identity,
            }),
        )
    })
    .await
    .map_err(|error| format!("Semble search worker failed: {error}"))?
}

struct PreparedSource {
    root: std::path::PathBuf,
    remote_root: String,
    identity: String,
}

fn parse_operation(tool_name: &str, arguments: Value) -> std::result::Result<Operation, String> {
    match tool_name {
        "semblesearch" => Ok(Operation::Search(
            serde_json::from_value(arguments).map_err(|error| error.to_string())?,
        )),
        "semblefindrelated" => Ok(Operation::FindRelated(
            serde_json::from_value(arguments).map_err(|error| error.to_string())?,
        )),
        _ => Err(format!("unsupported Semble tool: {tool_name}")),
    }
}

fn run(
    engine: &SearchEngine,
    operation: Operation,
    prepared: Option<PreparedSource>,
) -> std::result::Result<Value, String> {
    match (operation, prepared) {
        (Operation::Search(arguments), None) => {
            if host_snapshot::is_absolute_fs_repo(&arguments.repo) {
                return Err(
                    "absolute filesystem repos must be indexed through a Cursor host snapshot"
                        .into(),
                );
            }
            engine
                .search(SearchRequest {
                    query: arguments.query,
                    repo: arguments.repo.into(),
                    top_k: arguments.top_k,
                    max_snippet_lines: arguments.max_snippet_lines,
                    content: content(arguments.content),
                })
                .and_then(json_value)
                .map_err(|error| error.to_string())
        }
        (Operation::FindRelated(arguments), None) => {
            if host_snapshot::is_absolute_fs_repo(&arguments.repo) {
                return Err(
                    "absolute filesystem repos must be indexed through a Cursor host snapshot"
                        .into(),
                );
            }
            engine
                .find_related(FindRelatedRequest {
                    repo: arguments.repo.into(),
                    file_path: arguments.file_path,
                    line: arguments.line,
                    top_k: arguments.top_k,
                    max_snippet_lines: arguments.max_snippet_lines,
                    content: content(arguments.content),
                })
                .and_then(json_value)
                .map_err(|error| error.to_string())
        }
        (Operation::Search(arguments), Some(prepared)) => {
            let response = engine
                .search_prepared(
                    &prepared.root,
                    &prepared.identity,
                    SearchRequest {
                        query: arguments.query,
                        repo: prepared.root.clone(),
                        top_k: arguments.top_k,
                        max_snippet_lines: arguments.max_snippet_lines,
                        content: content(arguments.content),
                    },
                )
                .and_then(json_value)
                .map_err(|error| error.to_string())?;
            Ok(rewrite_result_paths(response, &prepared.remote_root))
        }
        (Operation::FindRelated(arguments), Some(prepared)) => {
            let file_path = relativize_path(&prepared.remote_root, &arguments.file_path);
            let response = engine
                .find_related_prepared(
                    &prepared.root,
                    &prepared.identity,
                    FindRelatedRequest {
                        repo: prepared.root.clone(),
                        file_path,
                        line: arguments.line,
                        top_k: arguments.top_k,
                        max_snippet_lines: arguments.max_snippet_lines,
                        content: content(arguments.content),
                    },
                )
                .and_then(json_value)
                .map_err(|error| error.to_string())?;
            Ok(rewrite_result_paths(response, &prepared.remote_root))
        }
    }
}

async fn engine(store: Option<Store>) -> Result<Arc<SearchEngine>> {
    #[cfg(test)]
    if let Some(engine) = TEST_ENGINE
        .lock()
        .unwrap_or_else(|error| error.into_inner())
        .clone()
    {
        return Ok(engine);
    }
    ENGINE
        .get_or_try_init(|| async move {
            let builder = match store {
                Some(store) => crate::network::blocking_client_builder(&store).await?,
                None => reqwest::blocking::Client::builder().use_native_tls(),
            };
            tokio::task::spawn_blocking(move || {
                let client = builder.build()?;
                SearchEngine::load_default_with_client(SembleConfig::default(), &client)
                    .map(Arc::new)
                    .map_err(|error| Error::Config(format!("load Semble search engine: {error}")))
            })
            .await
            .map_err(|error| Error::Config(format!("load Semble search engine: {error}")))?
        })
        .await
        .cloned()
}

fn json_value(response: semble_core::SearchResponse) -> semble_core::Result<Value> {
    serde_json::to_value(response)
        .map_err(|error| semble_core::Error::Serialization(error.to_string()))
}

fn content(selection: ContentSelection) -> Vec<ContentType> {
    match selection {
        ContentSelection::Code => vec![ContentType::Code],
        ContentSelection::Docs => vec![ContentType::Docs],
        ContentSelection::Config => vec![ContentType::Config],
        ContentSelection::All => vec![ContentType::Code, ContentType::Docs, ContentType::Config],
    }
}

fn default_top_k() -> usize {
    5
}

fn default_snippet_lines() -> Option<usize> {
    Some(10)
}

#[cfg(test)]
pub(crate) fn set_test_engine(engine: Option<Arc<SearchEngine>>) {
    *TEST_ENGINE
        .lock()
        .unwrap_or_else(|error| error.into_inner()) = engine;
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use semble_core::{Embedder, SearchEngine, SembleConfig};

    use super::*;
    use crate::search::host_snapshot::{materialize_dump, parse_dump, source_identity};

    struct KeywordEmbedder;

    impl Embedder for KeywordEmbedder {
        fn id(&self) -> &str {
            "keyword-v1"
        }
        fn dimensions(&self) -> usize {
            3
        }
        fn encode(&self, texts: &[String]) -> semble_core::Result<Vec<Vec<f32>>> {
            Ok(texts
                .iter()
                .map(|text| {
                    let text = text.to_ascii_lowercase();
                    let mut vector = vec![
                        usize::from(text.contains("auth")) as f32,
                        usize::from(text.contains("invoice")) as f32,
                        usize::from(text.contains("parse")) as f32,
                    ];
                    let norm = vector.iter().map(|value| value * value).sum::<f32>().sqrt();
                    if norm > 0.0 {
                        vector.iter_mut().for_each(|value| *value /= norm);
                    }
                    vector
                })
                .collect())
        }
    }

    #[tokio::test]
    async fn host_snapshot_search_rewrites_paths_to_remote_root() {
        let cache = tempfile::tempdir().unwrap();
        let engine = Arc::new(SearchEngine::with_embedder(
            SembleConfig::new(cache.path()),
            Arc::new(KeywordEmbedder),
        ));
        set_test_engine(Some(engine));
        let content = "pub fn authenticate_request() {}\n";
        let dump = parse_dump(&format!(
            "{}\n{}\n{}\n",
            serde_json::json!({"v":1,"ok":true,"files":1,"bytes":content.len()}),
            serde_json::json!({"p":"src/auth.rs","b":base64::Engine::encode(&base64::engine::general_purpose::STANDARD, content.as_bytes())}),
            serde_json::json!({"v":1,"done":true,"files":1,"bytes":content.len()}),
        ))
        .unwrap();
        let root =
            materialize_dump("conv-search", "call-search", "/remote/workspace", &dump).unwrap();
        let value = execute_on_root(
            "semblesearch",
            serde_json::json!({
                "query": "authenticate request",
                "repo": "/remote/workspace",
                "top_k": 1,
                "max_snippet_lines": 1
            }),
            root.clone(),
            "/remote/workspace".into(),
            source_identity("/remote/workspace"),
            None,
        )
        .await
        .unwrap();
        assert_eq!(
            value["results"][0]["file_path"],
            "/remote/workspace/src/auth.rs"
        );
        set_test_engine(None);
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn absolutize_helpers_round_trip() {
        assert_eq!(
            host_snapshot::absolutize_path("/remote/root", "src/lib.rs"),
            "/remote/root/src/lib.rs"
        );
        assert_eq!(
            relativize_path("/remote/root", "/remote/root/src/lib.rs"),
            "src/lib.rs"
        );
    }
}
