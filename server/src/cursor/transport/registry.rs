//! Maps request IDs to active transport handles.

use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    },
};

use tokio::sync::{mpsc, Mutex, Notify};

use crate::{
    cursor::{
        conversation::ConversationRegistry, prompting::PromptCompiler,
        services::observability::CursorTraceService,
    },
    plugin::PluginRegistry,
    provider::Provider,
    search::WebCache,
    store::Store,
    Result,
};

use super::{OutputHub, TransportHandle};

#[derive(Clone)]
pub struct TransportRegistry {
    inner: Arc<RegistryInner>,
}

struct RegistryInner {
    local: Mutex<HashMap<String, LocalTransport>>,
    next_local_generation: AtomicU64,
    upstream: Mutex<HashMap<String, u64>>,
    conversation_routes: Mutex<HashMap<String, bool>>,
    route_changed: Notify,
    store: Store,
    traces: CursorTraceService,
    web_cache: WebCache,
    plugins: Option<PluginRegistry>,
    conversations: ConversationRegistry,
}

#[derive(Clone)]
struct LocalTransport {
    generation: u64,
    handle: TransportHandle,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TransportRoute {
    Local,
    Upstream(u64),
}

impl TransportRegistry {
    pub fn new(store: Store, provider: Arc<dyn Provider>, compiler: PromptCompiler) -> Self {
        Self::with_web_cache(store, provider, compiler, WebCache::default())
    }

    pub fn with_web_cache(
        store: Store,
        provider: Arc<dyn Provider>,
        compiler: PromptCompiler,
        web_cache: WebCache,
    ) -> Self {
        Self::build(store, provider, compiler, web_cache, None, None)
    }

    /// 附带本地 rules 目录的构造;编译请求上下文时会合并该目录下的 md 规则。
    pub fn with_local_rules(
        store: Store,
        provider: Arc<dyn Provider>,
        compiler: PromptCompiler,
        local_rules_dir: std::path::PathBuf,
    ) -> Self {
        Self::build(
            store,
            provider,
            compiler,
            WebCache::default(),
            None,
            Some(local_rules_dir),
        )
    }

    pub fn with_plugins(
        store: Store,
        provider: Arc<dyn Provider>,
        compiler: PromptCompiler,
        web_cache: WebCache,
        plugins: PluginRegistry,
        local_rules_dir: std::path::PathBuf,
    ) -> Self {
        Self::build(
            store,
            provider,
            compiler,
            web_cache,
            Some(plugins),
            Some(local_rules_dir),
        )
    }

    fn build(
        store: Store,
        provider: Arc<dyn Provider>,
        compiler: PromptCompiler,
        web_cache: WebCache,
        plugins: Option<PluginRegistry>,
        local_rules_dir: Option<std::path::PathBuf>,
    ) -> Self {
        Self {
            inner: Arc::new(RegistryInner {
                local: Mutex::new(HashMap::new()),
                next_local_generation: AtomicU64::new(1),
                upstream: Mutex::new(HashMap::new()),
                conversation_routes: Mutex::new(HashMap::new()),
                route_changed: Notify::new(),
                traces: CursorTraceService::new(store.clone()),
                conversations: ConversationRegistry::new(
                    store.clone(),
                    provider,
                    compiler,
                    web_cache.clone(),
                    local_rules_dir,
                ),
                store,
                web_cache,
                plugins,
            }),
        }
    }

    pub fn store(&self) -> &Store {
        &self.inner.store
    }

    pub fn trace(
        &self,
        request_id: &str,
    ) -> crate::cursor::services::observability::CursorTraceRecorder {
        self.inner.traces.recorder(request_id)
    }

    pub fn web_cache(&self) -> &WebCache {
        &self.inner.web_cache
    }

    pub fn plugins(&self) -> Option<&PluginRegistry> {
        self.inner.plugins.as_ref()
    }

    pub fn conversations(&self) -> &ConversationRegistry {
        &self.inner.conversations
    }

    pub async fn get_or_create(&self, request_id: &str) -> Result<TransportHandle> {
        self.get_or_create_for_append(request_id, false).await
    }

    pub(crate) async fn get_or_create_for_append(
        &self,
        request_id: &str,
        replace_closing: bool,
    ) -> Result<TransportHandle> {
        let mut local = self.inner.local.lock().await;
        if let Some(transport) = local.get(request_id) {
            if transport.handle.accepting_appends() || !replace_closing {
                return Ok(transport.handle.clone());
            }
        }
        local.remove(request_id);
        let (commands, receiver) = mpsc::channel(128);
        let output = Arc::new(OutputHub::default());
        let trace = self.inner.traces.recorder(request_id);
        trace.resume();
        let handle = TransportHandle::new(request_id.into(), commands, output, trace);
        let generation = self
            .inner
            .next_local_generation
            .fetch_add(1, Ordering::Relaxed);
        local.insert(
            request_id.into(),
            LocalTransport {
                generation,
                handle: handle.clone(),
            },
        );
        drop(local);
        self.inner.route_changed.notify_waiters();
        self.inner
            .conversations
            .bind_transport(handle.clone(), receiver);

        let registry = Arc::downgrade(&self.inner);
        let request_id = request_id.to_string();
        let lifecycle = handle.clone();
        tokio::spawn(async move {
            lifecycle.wait_transport_closed().await;
            if let Some(registry) = registry.upgrade() {
                let mut local = registry.local.lock().await;
                if local
                    .get(&request_id)
                    .is_some_and(|transport| transport.generation == generation)
                {
                    local.remove(&request_id);
                }
            }
        });
        Ok(handle)
    }

    pub async fn local(&self, request_id: &str) -> Option<TransportHandle> {
        self.inner
            .local
            .lock()
            .await
            .get(request_id)
            .map(|transport| transport.handle.clone())
    }

    pub async fn mark_upstream(&self, request_id: &str) {
        let mut upstream = self.inner.upstream.lock().await;
        let generation = upstream.get(request_id).copied().unwrap_or_default() + 1;
        upstream.insert(request_id.into(), generation);
        drop(upstream);
        self.inner.route_changed.notify_waiters();
    }

    /// Records whether a conversation's latest model is served locally.
    pub async fn set_conversation_local(&self, conversation_id: &str, local: bool) {
        self.inner
            .conversation_routes
            .lock()
            .await
            .insert(conversation_id.into(), local);
    }

    /// Whether the latest run with a model in this conversation was local.
    pub async fn conversation_local(&self, conversation_id: &str) -> Option<bool> {
        self.inner
            .conversation_routes
            .lock()
            .await
            .get(conversation_id)
            .copied()
    }

    pub async fn upstream(&self, request_id: &str) -> bool {
        self.inner.upstream.lock().await.contains_key(request_id)
    }

    pub async fn wait_route(&self, request_id: &str) -> TransportRoute {
        loop {
            let changed = self.inner.route_changed.notified();
            tokio::pin!(changed);
            changed.as_mut().enable();
            if self.inner.local.lock().await.contains_key(request_id) {
                return TransportRoute::Local;
            }
            if let Some(generation) = self.inner.upstream.lock().await.get(request_id).copied() {
                return TransportRoute::Upstream(generation);
            }
            changed.await;
        }
    }

    pub fn finish_upstream(&self, request_id: String, generation: u64) {
        let registry = self.clone();
        tokio::spawn(async move {
            let mut upstream = registry.inner.upstream.lock().await;
            if upstream.get(&request_id) == Some(&generation) {
                upstream.remove(&request_id);
            }
        });
    }

    pub async fn shutdown(&self) {
        self.inner.conversations.shutdown().await;
        let handles = std::mem::take(&mut *self.inner.local.lock().await);
        self.inner.upstream.lock().await.clear();
        self.inner.conversation_routes.lock().await.clear();
        for transport in handles.into_values() {
            transport.handle.disconnect().await;
            let _ = tokio::time::timeout(
                std::time::Duration::from_secs(2),
                transport.handle.wait_transport_closed(),
            )
            .await;
        }
    }
}
