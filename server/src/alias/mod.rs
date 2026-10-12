//! Alias configuration, catalog and provider-independent routing policy.
mod catalog;
pub mod configuration;
mod resolver;
pub mod state;

use std::sync::Arc;

use crate::{plugin::PluginRegistry, store::Store};
pub use catalog::{AliasSource, AliasView};
pub use configuration::*;

#[derive(Clone)]
pub struct AliasResolver {
    pub(crate) store: Store,
    pub(crate) plugins: PluginRegistry,
    pub state: Arc<state::RoutingState>,
}

impl AliasResolver {
    pub fn new(store: Store, plugins: PluginRegistry) -> Self {
        Self {
            store,
            plugins,
            state: Arc::new(state::RoutingState::default()),
        }
    }
}
