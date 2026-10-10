//! Server library root; exposes the application, API, runtime, persistence, and integration layers.
pub mod alias;
pub mod api;
pub mod app;
pub mod config;
pub mod control;
pub mod cursor;
pub mod error;
pub mod local_app;
pub mod model;
pub mod network;
pub mod plugin;
pub mod provider;
pub mod run;
pub mod search;
pub mod store;

pub use app::App;
pub use config::Config;
pub use error::{Error, Result};
