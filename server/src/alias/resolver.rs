//! Alias execution is a single, bounded sequence of existing provider attempts.
use std::{sync::Arc, time::Duration};

use async_stream::try_stream;
use futures_util::StreamExt;
use tokio_util::sync::CancellationToken;

use super::{
    configuration::{Alias, ReturnMode},
    AliasResolver,
};
use crate::{
    model::ModelInvocation,
    provider::{
        failure::{FailureKind, ProviderFailure},
        is_valid_response_event, ModelEvent, Provider, ProviderStream,
    },
    Error,
};

struct CancelAttempt(CancellationToken);
impl Drop for CancelAttempt {
    fn drop(&mut self) {
        self.0.cancel();
    }
}

impl AliasResolver {
    pub fn stream(
        &self,
        alias: Alias,
        invocation: ModelInvocation,
        provider: Arc<dyn Provider>,
        cancellation: CancellationToken,
    ) -> ProviderStream {
        let resolver = self.clone();
        Box::pin(try_stream! {
            if cancellation.is_cancelled() { Err(Error::Cancelled)?; }
            if !alias.config.enabled { Err(Error::Alias(format!("Alias '{}' is disabled",alias.config.name)))?; }
            if !resolver.store.alias_run_is_current(&invocation.conversation_id, &invocation.run_id).await? { Err(Error::Cancelled)?; }
            let sources = resolver.sources().await?;
            let view = resolver.view(alias.clone(), &sources);
            let settings = resolver.store.alias_settings().await?;
            let (pinned, previously_committed) = resolver.state.begin(&invocation.conversation_id, &alias.id, &invocation.run_id);
            let mut candidates = view.target_statuses.iter().enumerate()
                .filter(|(_,status)| matches!(status.status.as_str(), "active" | "available"))
                .map(|(index,_)| index).collect::<Vec<_>>();
            if previously_committed {
                candidates.retain(|index| Some(alias.config.targets[*index].key()) == pinned);
            } else if alias.config.sticky && alias.config.return_mode == ReturnMode::NewSessions {
                if let Some(position) = candidates.iter().position(|index|Some(alias.config.targets[*index].key())==pinned) {
                    let preferred = candidates.remove(position);
                    candidates.insert(0,preferred);
                }
            }
            let mut failures = view.target_statuses.iter().filter(|status|!matches!(status.status.as_str(),"active"|"available"))
                .map(|status|format!("{}: {}",status.key,status.reason.as_deref().unwrap_or(&status.status))).collect::<Vec<_>>();
            let mut attempt_index = 0;
            for index in candidates {
                if cancellation.is_cancelled() { Err(Error::Cancelled)?; }
                let target = &alias.config.targets[index];
                let key = target.key();
                if let Some(health) = resolver.state.health(&key, crate::store::now_ms()) {
                    failures.push(format!("{key}: {}",health.status));
                    continue;
                }
                let source = sources.iter().find(|source|source.target.key()==key).expect("eligible source exists");
                tracing::info!(alias_id=%alias.id, alias=%alias.config.name, target=%key, attempt=attempt_index, "resolved alias target");
                let mut routed = invocation.clone();
                routed.call_id = format!("{}:alias:{}", invocation.call_id, attempt_index);
                routed.request.model.model_id = source.request_model_id.clone();
                routed.request.model.reasoning = Default::default();
                routed.request.model.extra_params = serde_json::json!({});
                routed.request.model.context_window_tokens = super::catalog::clamp(routed.request.model.context_window_tokens, view.parameters.context_window_tokens);
                routed.request.model.max_output_tokens = super::catalog::clamp(routed.request.model.max_output_tokens, view.parameters.max_output_tokens);
                let attempt_call_id = routed.call_id.clone();
                let attempt_started = std::time::Instant::now();
                let attempt_token = cancellation.child_token();
                let guard = CancelAttempt(attempt_token.clone());
                let mut stream = provider.stream(routed, attempt_token.clone());
                let deadline = tokio::time::Instant::now() + Duration::from_secs(settings.first_token_timeout_seconds);
                let mut pending = Vec::new();
                let mut emitted = false;
                let mut done = false;
                let mut recorded = false;
                let failure = loop {
                    let event = tokio::select! {
                        biased;
                        _ = cancellation.cancelled() => break Some(Error::Cancelled),
                        event = stream.next() => event,
                        _ = tokio::time::sleep_until(deadline), if !emitted => break Some(Error::Upstream(ProviderFailure::transient("first response timed out"))),
                    };
                    if !recorded {
                        // The single-target router creates the existing call row on first poll.
                        resolver.store.record_alias_call(&attempt_call_id, &alias.id, &alias.config.name, &key, attempt_index).await?;
                        recorded = true;
                    }
                    let event = match event {
                        Some(Ok(event)) => event,
                        Some(Err(error)) => break Some(error),
                        None if done => break None,
                        None if cancellation.is_cancelled() => break Some(Error::Cancelled),
                        None => break Some(Error::Upstream(ProviderFailure::transient("provider stream ended before completion"))),
                    };
                    if done { break Some(Error::Protocol("provider emitted output after completion".into())); }
                    if matches!(event,ModelEvent::Done(_)) { done = true; }
                    if !emitted {
                        let starts_output = is_valid_response_event(&event) || done;
                        pending.push(event);
                        if pending.len()>128 { break Some(Error::Protocol("too many events before provider content".into())); }
                        if !starts_output { continue; }
                        if cancellation.is_cancelled() || !resolver.store.alias_run_is_current(&invocation.conversation_id,&invocation.run_id).await? { break Some(Error::Cancelled); }
                        resolver.state.commit(&invocation.conversation_id,&alias.id,&invocation.run_id,&key,alias.config.sticky);
                        resolver.state.answered(&invocation.run_id,&key);
                        emitted = true;
                        for buffered in pending.drain(..) { yield buffered; }
                    } else { yield event; }
                };
                // Drop the suspended generator BEFORE touching Store: it can be
                // awaiting a recorder write while still owning the write lock.
                drop(guard);
                drop(stream);
                if cancellation.is_cancelled() { Err(Error::Cancelled)?; }
                if !recorded {
                    resolver.store.record_alias_call(&attempt_call_id, &alias.id, &alias.config.name, &key, attempt_index).await?;
                }
                if let Some(error) = failure.as_ref().filter(|error| !matches!(error,Error::Cancelled)) {
                    resolver.store.finish_alias_failure(&attempt_call_id,attempt_started.elapsed().as_millis().min(i64::MAX as u128) as i64,&error.to_string()).await?;
                }
                let Some(error) = failure else {
                    resolver.state.attempt(&invocation.run_id, &key, None);
                    return;
                };
                if matches!(error,Error::Cancelled) { Err(Error::Cancelled)?; }
                let classified = ProviderFailure::from_error(&error);
                let safe_reason = classified.as_ref().map(|failure| match failure.kind {
                    FailureKind::RateLimit => "rate limit",
                    FailureKind::Authorization => "authorization problem",
                    FailureKind::Transient => "upstream unavailable or timed out",
                    FailureKind::Request => "request rejected",
                }).unwrap_or("provider configuration or protocol error");
                if let Some(failure) = &classified { resolver.state.failed(&key,failure,&settings,crate::store::now_ms()); }
                resolver.state.attempt(&invocation.run_id,&key,Some(safe_reason.into()));
                failures.push(format!("{key}: {safe_reason}"));
                tracing::warn!(alias_id=%alias.id,target=%key,reason=safe_reason,committed=emitted || previously_committed,"alias attempt failed");
                // No repeated candidate, no nested retry, and no switch after any model output in this run.
                if emitted || previously_committed || !classified.as_ref().is_some_and(|failure|failure.kind!=FailureKind::Request) {
                    Err(Error::Alias(format!("Alias '{}', target {}: {}",alias.config.name,key,error)))?;
                }
                attempt_index += 1;
            }
            Err(Error::Alias(format!("Alias '{}' has no available targets: {}",alias.config.name,if failures.is_empty(){"no enabled targets".into()}else{failures.join("; ")})))?;
        })
    }
}
