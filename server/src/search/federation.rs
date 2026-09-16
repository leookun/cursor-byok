//! Aggregates and ranks results from search sources.
use std::{cmp::Ordering, collections::HashMap};

use futures_util::future::join_all;

use crate::store::Store;

use super::{catalog, SearchEngine, SearchHit};

const RRF_K: f64 = 60.0;
const MAX_RESULTS: usize = 15;

#[derive(Clone)]
pub struct WebSearch {
    client: SearchClient,
    engines: Vec<SearchEngine>,
}

#[derive(Clone)]
enum SearchClient {
    Managed(Store),
    Direct(reqwest::Client),
}

#[derive(Debug, thiserror::Error)]
#[error("web search failed: {0}")]
pub struct SearchError(String);

impl WebSearch {
    pub fn built_in() -> Self {
        Self::with_engines(catalog::engines())
    }

    pub(crate) fn managed(store: Store) -> Self {
        Self {
            client: SearchClient::Managed(store),
            engines: catalog::engines(),
        }
    }

    pub fn with_engines<I, E>(engines: I) -> Self
    where
        I: IntoIterator<Item = E>,
        E: Into<SearchEngine>,
    {
        Self {
            client: SearchClient::Direct(reqwest::Client::new()),
            engines: engines.into_iter().map(Into::into).collect(),
        }
    }

    pub fn engine_ids(&self) -> Vec<&'static str> {
        self.engines.iter().map(SearchEngine::id).collect()
    }

    pub async fn search(&self, query: &str) -> Result<Vec<SearchHit>, SearchError> {
        let query = query.trim();
        if query.is_empty() {
            return Err(SearchError("query is empty".into()));
        }
        let client = match &self.client {
            SearchClient::Managed(store) => crate::network::client(store)
                .await
                .map_err(|error| SearchError(format!("HTTP client failed: {error}")))?,
            SearchClient::Direct(client) => client.clone(),
        };
        let responses = join_all(
            self.engines
                .iter()
                .map(|engine| engine.search(&client, query)),
        )
        .await;
        let mut merged = HashMap::<String, SearchHit>::new();
        let mut failures = Vec::new();
        for (engine, response) in self.engines.iter().zip(responses) {
            match response {
                Ok(results) if !results.is_empty() => {
                    tracing::debug!(
                        engine = engine.id(),
                        results = results.len(),
                        "search engine completed"
                    );
                    merge(&mut merged, engine.id(), results)
                }
                Ok(_) => {
                    tracing::debug!(engine = engine.id(), "search engine returned no results");
                    failures.push(engine.id().to_string());
                }
                Err(error) => {
                    tracing::warn!(engine = engine.id(), %error, "search engine failed");
                    failures.push(engine.id().to_string());
                }
            }
        }
        if merged.is_empty() {
            return Err(SearchError(format!(
                "no results from engines: {}",
                failures.join(", ")
            )));
        }
        let mut results = merged.into_values().collect::<Vec<_>>();
        results.sort_by(|left, right| {
            right
                .score
                .partial_cmp(&left.score)
                .unwrap_or(Ordering::Equal)
                .then_with(|| left.url.cmp(&right.url))
        });
        results.truncate(MAX_RESULTS);
        Ok(results)
    }
}

impl Default for WebSearch {
    fn default() -> Self {
        Self::built_in()
    }
}

fn merge(merged: &mut HashMap<String, SearchHit>, engine: &'static str, results: Vec<SearchHit>) {
    for (rank, mut result) in results.into_iter().enumerate() {
        let score = 1.0 / (RRF_K + rank as f64 + 1.0);
        match merged.get_mut(&result.url) {
            Some(existing) => {
                // Reciprocal Rank Fusion sums one term per ranked list. One
                // engine listing a URL more than once is still one list, so
                // only its best rank counts; a later repeat contributes
                // nothing but its snippet.
                if !existing.engines.contains(&engine) {
                    existing.score += score;
                    existing.engines.push(engine);
                }
                if result.chunk.len() > existing.chunk.len() {
                    existing.chunk = result.chunk;
                }
            }
            None => {
                result.score = score;
                merged.insert(result.url.clone(), result);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn hit(url: &str, engine: &'static str, chunk: &str) -> SearchHit {
        SearchHit::new("title", url, chunk, vec![engine])
    }

    fn rrf(rank: usize) -> f64 {
        1.0 / (RRF_K + rank as f64 + 1.0)
    }

    #[test]
    fn an_engine_that_lists_one_url_twice_contributes_a_single_rrf_term() {
        let mut merged = HashMap::new();
        merge(
            &mut merged,
            "duckduckgo",
            vec![
                hit("https://example.com/p", "duckduckgo", "short"),
                hit("https://example.com/p", "duckduckgo", "longer snippet"),
            ],
        );

        let entry = &merged["https://example.com/p"];
        assert_eq!(entry.engines, vec!["duckduckgo"]);
        assert_eq!(entry.score, rrf(0));
        assert_eq!(entry.chunk, "longer snippet");
    }

    #[test]
    fn two_engines_that_agree_on_a_url_still_sum_both_ranks() {
        let mut merged = HashMap::new();
        merge(
            &mut merged,
            "google",
            vec![hit("https://example.com/p", "google", "a")],
        );
        merge(
            &mut merged,
            "bing",
            vec![
                hit("https://example.com/other", "bing", "b"),
                hit("https://example.com/p", "bing", "c"),
            ],
        );

        let entry = &merged["https://example.com/p"];
        assert_eq!(entry.engines, vec!["google", "bing"]);
        assert_eq!(entry.score, rrf(0) + rrf(1));
    }

    #[test]
    fn agreement_between_engines_outranks_one_engine_repeating_itself() {
        let mut merged = HashMap::new();
        merge(
            &mut merged,
            "duckduckgo",
            vec![
                hit("https://example.com/repeated", "duckduckgo", "a"),
                hit("https://example.com/repeated", "duckduckgo", "b"),
                hit("https://example.com/agreed", "duckduckgo", "c"),
            ],
        );
        merge(
            &mut merged,
            "brave",
            vec![hit("https://example.com/agreed", "brave", "d")],
        );

        assert!(
            merged["https://example.com/agreed"].score
                > merged["https://example.com/repeated"].score
        );
    }
}
