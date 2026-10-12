//! Exposes provider-independent search capabilities.
mod cache;
mod catalog;
mod engine;
mod federation;
mod fetch;
pub(crate) mod host_snapshot;
mod search_provider;

pub use cache::{WebCache, WebCacheEntry};
pub use engine::{HtmlEngine, JsonEngine, SearchEngine, SearchHit};
pub use federation::{SearchError, WebSearch};
pub(crate) use fetch::WebFetchRequest;
pub use fetch::{FetchError, FetchedPage, WebFetch};
pub(crate) use search_provider::{
    execute as execute_semble, execute_on_root as execute_semble_on_root,
};
