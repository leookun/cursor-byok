//! Selection, snapshot concurrency, and restart invariants for JSON persistence.
use super::*;

const PLUGIN: &str = "dev.example";
const TYPE: &str = "account";

fn store() -> (tempfile::TempDir, PluginStateStore) {
    let root = tempfile::tempdir().unwrap();
    let data = PluginDataStore::for_test(root.path().join("data")).unwrap();
    (root, PluginStateStore::new(data))
}

fn draft(key: &str, token: &str) -> ResourceDraft {
    ResourceDraft {
        key: key.into(),
        private_data: serde_json::json!({"token": token}),
        state: None,
    }
}

fn invalid() -> ResourcePatch {
    ResourcePatch {
        private_data: None,
        state: Some(ResourceStateInput::Invalid { message: None }),
    }
}

fn cooling() -> ResourcePatch {
    ResourcePatch {
        private_data: None,
        state: Some(ResourceStateInput::Cooling {
            retry_at_ms: None,
            message: None,
        }),
    }
}

async fn patch(store: &PluginStateStore, id: &str, patch: ResourcePatch) -> Result<()> {
    let expected = store
        .resources(PLUGIN, TYPE)
        .await?
        .into_iter()
        .find(|record| record.id == id)
        .expect("test resource exists");
    assert!(
        store
            .apply_patch_if_current(PLUGIN, TYPE, &expected, patch)
            .await?
    );
    Ok(())
}

fn model(id: &str) -> StoredModel {
    StoredModel::from_definition(&serde_json::json!({"id": id, "displayName": id})).unwrap()
}

async fn seed(store: &PluginStateStore) -> Vec<ResourceRecord> {
    store
        .upsert_resources(
            PLUGIN,
            TYPE,
            vec![draft("a", "a"), draft("b", "b"), draft("c", "c")],
        )
        .await
        .unwrap();
    store.resources(PLUGIN, TYPE).await.unwrap()
}

#[tokio::test]
async fn selection_is_manual_persistent_and_scoped() {
    let (root, store) = store();
    assert_eq!(
        store.selection(PLUGIN, TYPE).await.unwrap(),
        ResourceSelection::default()
    );
    let records = seed(&store).await;
    let initial = store.selection(PLUGIN, TYPE).await.unwrap();
    assert_eq!(
        initial.active_resource_id.as_deref(),
        Some(records[0].id.as_str())
    );
    assert!(!initial.automatic_switching);
    let selected = store
        .set_selection(PLUGIN, TYPE, Some(records[1].id.clone()), true)
        .await
        .unwrap();
    assert!(selected.revision > initial.revision);
    assert!(store
        .set_selection(PLUGIN, "other", Some(records[1].id.clone()), false)
        .await
        .is_err());
    assert!(store
        .set_selection("other.plugin", TYPE, Some(records[1].id.clone()), false)
        .await
        .is_err());
    assert_eq!(
        store.selection(PLUGIN, "other").await.unwrap(),
        ResourceSelection::default()
    );
    let value = serde_json::to_value(&selected).unwrap();
    assert_eq!(value["activeResourceId"], records[1].id);
    assert_eq!(value["automaticSwitching"], true);
    assert!(value.get("active_resource_id").is_none());
    drop(store);
    let data = PluginDataStore::for_test(root.path().join("data")).unwrap();
    assert!(data
        .read(PLUGIN, "resources-account")
        .await
        .unwrap()
        .is_array());
    assert_eq!(data.read(PLUGIN, "selection-account").await.unwrap(), value);
    let restarted = PluginStateStore::new(data);
    assert_eq!(restarted.selection(PLUGIN, TYPE).await.unwrap(), selected);
    assert_eq!(
        restarted
            .select_resource(PLUGIN, TYPE, true)
            .await
            .unwrap()
            .0
            .id,
        records[1].id
    );
}

#[tokio::test]
async fn imports_preserve_selection_and_only_empty_collection_selects_first_added() {
    let (_root, store) = store();
    let records = seed(&store).await;
    let selected = store
        .set_selection(PLUGIN, TYPE, Some(records[1].id.clone()), false)
        .await
        .unwrap();
    store
        .upsert_resources(PLUGIN, TYPE, vec![draft("b", "new"), draft("d", "d")])
        .await
        .unwrap();
    let selection = store.selection(PLUGIN, TYPE).await.unwrap();
    assert_eq!(selection.active_resource_id, selected.active_resource_id);
    assert!(!selection.automatic_switching);
    assert!(selection.revision > selected.revision);
    store
        .set_selection(PLUGIN, TYPE, None, false)
        .await
        .unwrap();
    store
        .upsert_resources(PLUGIN, TYPE, vec![draft("e", "e")])
        .await
        .unwrap();
    assert!(store
        .selection(PLUGIN, TYPE)
        .await
        .unwrap()
        .active_resource_id
        .is_none());
    for record in store.resources(PLUGIN, TYPE).await.unwrap() {
        store
            .remove_resource(PLUGIN, TYPE, &record.id)
            .await
            .unwrap();
    }
    store
        .upsert_resources(PLUGIN, TYPE, vec![draft("z", "z"), draft("y", "y")])
        .await
        .unwrap();
    assert_eq!(
        store
            .select_resource(PLUGIN, TYPE, true)
            .await
            .unwrap()
            .0
            .key,
        "z"
    );
}

#[tokio::test]
async fn manual_never_falls_back_and_discovery_only_relaxes_cooling() {
    let (_root, store) = store();
    let records = seed(&store).await;
    patch(&store, &records[0].id, cooling()).await.unwrap();
    assert!(store.select_resource(PLUGIN, TYPE, true).await.is_err());
    assert_eq!(
        store
            .select_resource(PLUGIN, TYPE, false)
            .await
            .unwrap()
            .0
            .id,
        records[0].id
    );
    patch(&store, &records[0].id, invalid()).await.unwrap();
    assert!(store.select_resource(PLUGIN, TYPE, false).await.is_err());
    let current = store.resources(PLUGIN, TYPE).await.unwrap().remove(0);
    let selection = store.selection(PLUGIN, TYPE).await.unwrap();
    assert!(!store
        .fail_resource(PLUGIN, TYPE, &current, &selection, invalid())
        .await
        .unwrap());
    assert_eq!(
        store
            .selection(PLUGIN, TYPE)
            .await
            .unwrap()
            .active_resource_id,
        Some(records[0].id.clone())
    );
}

#[tokio::test]
async fn auto_selects_ready_in_stable_circular_order_and_never_unavailable() {
    let (_root, store) = store();
    let records = seed(&store).await;
    store
        .set_selection(PLUGIN, TYPE, Some(records[1].id.clone()), true)
        .await
        .unwrap();
    patch(&store, &records[1].id, cooling()).await.unwrap();
    patch(&store, &records[2].id, invalid()).await.unwrap();
    assert_eq!(
        store
            .select_resource(PLUGIN, TYPE, true)
            .await
            .unwrap()
            .0
            .id,
        records[0].id
    );
    patch(&store, &records[0].id, invalid()).await.unwrap();
    assert!(store.select_resource(PLUGIN, TYPE, true).await.is_err());
    assert!(store.select_resource(PLUGIN, TYPE, false).await.is_err());
    patch(
        &store,
        &records[1].id,
        ResourcePatch {
            private_data: None,
            state: Some(ResourceStateInput::Cooling {
                retry_at_ms: Some(0),
                message: None,
            }),
        },
    )
    .await
    .unwrap();
    assert_eq!(
        store
            .select_resource(PLUGIN, TYPE, true)
            .await
            .unwrap()
            .0
            .id,
        records[1].id
    );
}

#[tokio::test]
async fn deletion_clears_manual_and_advances_auto_without_readding_records() {
    let (_root, store) = store();
    let records = seed(&store).await;
    store
        .remove_resource(PLUGIN, TYPE, &records[0].id)
        .await
        .unwrap();
    assert!(store
        .selection(PLUGIN, TYPE)
        .await
        .unwrap()
        .active_resource_id
        .is_none());
    assert!(store.select_resource(PLUGIN, TYPE, true).await.is_err());
    assert!(!store
        .apply_patch_if_current(PLUGIN, TYPE, &records[0], invalid())
        .await
        .unwrap());
    store
        .set_selection(PLUGIN, TYPE, Some(records[1].id.clone()), true)
        .await
        .unwrap();
    store
        .remove_resource(PLUGIN, TYPE, &records[1].id)
        .await
        .unwrap();
    assert_eq!(
        store
            .selection(PLUGIN, TYPE)
            .await
            .unwrap()
            .active_resource_id,
        Some(records[2].id.clone())
    );
    store
        .remove_resource(PLUGIN, TYPE, &records[2].id)
        .await
        .unwrap();
    assert!(store
        .selection(PLUGIN, TYPE)
        .await
        .unwrap()
        .active_resource_id
        .is_none());
    assert!(store.resources(PLUGIN, TYPE).await.unwrap().is_empty());
}

#[tokio::test]
async fn auto_delete_skips_unavailable_and_nonactive_delete_preserves_selection() {
    let (_root, store) = store();
    let records = seed(&store).await;
    patch(&store, &records[1].id, cooling()).await.unwrap();
    let selection = store
        .set_selection(PLUGIN, TYPE, Some(records[0].id.clone()), true)
        .await
        .unwrap();
    store
        .remove_resource(PLUGIN, TYPE, &records[1].id)
        .await
        .unwrap();
    assert_eq!(store.selection(PLUGIN, TYPE).await.unwrap(), selection);
    patch(&store, &records[2].id, invalid()).await.unwrap();
    store
        .remove_resource(PLUGIN, TYPE, &records[0].id)
        .await
        .unwrap();
    assert!(store
        .selection(PLUGIN, TYPE)
        .await
        .unwrap()
        .active_resource_id
        .is_none());
}

#[tokio::test]
async fn delayed_refresh_and_failure_cannot_overwrite_reimported_credentials() {
    let (_root, store) = store();
    let records = seed(&store).await;
    let selected = store
        .set_selection(PLUGIN, TYPE, Some(records[0].id.clone()), true)
        .await
        .unwrap();
    let (release, wait) = tokio::sync::oneshot::channel();
    let late_store = store.clone();
    let snapshot = records[0].clone();
    let late = tokio::spawn(async move {
        wait.await.unwrap();
        late_store
            .apply_patch_if_current(
                PLUGIN,
                TYPE,
                &snapshot,
                ResourcePatch {
                    private_data: Some(serde_json::json!({"token": "stale"})),
                    state: None,
                },
            )
            .await
            .unwrap()
    });
    store
        .upsert_resources(PLUGIN, TYPE, vec![draft("a", "imported")])
        .await
        .unwrap();
    release.send(()).unwrap();
    assert!(!late.await.unwrap());
    assert!(!store
        .fail_resource(PLUGIN, TYPE, &records[0], &selected, invalid())
        .await
        .unwrap());
    let current = store.resources(PLUGIN, TYPE).await.unwrap();
    assert_eq!(current[0].private_data["token"], "imported");
    assert_eq!(current[0].state, ResourceState::Ready);
    assert!(current[0].updated_at_ms > records[0].updated_at_ms);
    assert_eq!(
        store
            .selection(PLUGIN, TYPE)
            .await
            .unwrap()
            .active_resource_id,
        selected.active_resource_id
    );
}

#[tokio::test]
async fn concurrent_snapshot_patches_have_exactly_one_winner() {
    let (_root, store) = store();
    let records = seed(&store).await;
    let (a, b) = tokio::join!(
        store.apply_patch_if_current(PLUGIN, TYPE, &records[0], invalid()),
        store.apply_patch_if_current(PLUGIN, TYPE, &records[0], cooling()),
    );
    assert_ne!(a.unwrap(), b.unwrap());
}

#[tokio::test]
async fn concurrent_imports_and_model_edits_do_not_lose_updates() {
    let (_root, store) = store();
    let (a, b) = tokio::join!(
        store.upsert_resources(PLUGIN, TYPE, vec![draft("a", "a")]),
        store.upsert_resources(PLUGIN, TYPE, vec![draft("b", "b")]),
    );
    assert_eq!(a.unwrap().added, 1);
    assert_eq!(b.unwrap().added, 1);
    assert_eq!(store.resources(PLUGIN, TYPE).await.unwrap().len(), 2);
    store
        .replace_models(PLUGIN, "provider", &[model("m")])
        .await
        .unwrap();
    let replacement = [model("m"), model("n")];
    let (replace, toggle) = tokio::join!(
        store.replace_models(PLUGIN, "provider", &replacement),
        store.set_model_enabled(PLUGIN, "provider", "m", false),
    );
    replace.unwrap();
    toggle.unwrap();
    let models = store.models(PLUGIN, "provider").await.unwrap();
    assert_eq!(models.len(), 2);
    assert!(!models[0].enabled);
}

#[tokio::test]
async fn failure_switches_once_and_respects_later_user_edits() {
    let (_root, store) = store();
    let records = seed(&store).await;
    let selection = store
        .set_selection(PLUGIN, TYPE, Some(records[0].id.clone()), true)
        .await
        .unwrap();
    assert!(store
        .fail_resource(PLUGIN, TYPE, &records[0], &selection, invalid())
        .await
        .unwrap());
    let switched = store.selection(PLUGIN, TYPE).await.unwrap();
    assert_eq!(switched.active_resource_id, Some(records[1].id.clone()));
    assert!(store
        .fail_resource(PLUGIN, TYPE, &records[0], &selection, invalid())
        .await
        .unwrap());
    assert_eq!(store.selection(PLUGIN, TYPE).await.unwrap(), switched);

    // A user explicitly reselecting even the same account prevents a late switch.
    let old = store
        .set_selection(PLUGIN, TYPE, Some(records[1].id.clone()), true)
        .await
        .unwrap();
    store
        .set_selection(PLUGIN, TYPE, Some(records[1].id.clone()), true)
        .await
        .unwrap();
    assert!(!store
        .fail_resource(PLUGIN, TYPE, &records[1], &old, invalid())
        .await
        .unwrap());
    assert_eq!(
        store
            .selection(PLUGIN, TYPE)
            .await
            .unwrap()
            .active_resource_id,
        Some(records[1].id.clone())
    );

    let old = store
        .set_selection(PLUGIN, TYPE, Some(records[2].id.clone()), true)
        .await
        .unwrap();
    let manual = store
        .set_selection(PLUGIN, TYPE, None, false)
        .await
        .unwrap();
    assert!(!store
        .fail_resource(PLUGIN, TYPE, &records[2], &old, invalid())
        .await
        .unwrap());
    assert_eq!(store.selection(PLUGIN, TYPE).await.unwrap(), manual);
}

#[tokio::test]
async fn delayed_model_sync_rejects_selection_credentials_and_deletion_changes() {
    let (_root, store) = store();
    let records = seed(&store).await;
    let initial = store.selection(PLUGIN, TYPE).await.unwrap();
    assert!(store
        .replace_models_if_selected(PLUGIN, TYPE, &initial, "provider", &[model("first")])
        .await
        .unwrap());
    store
        .set_model_enabled(PLUGIN, "provider", "first", false)
        .await
        .unwrap();
    assert!(store
        .replace_models_if_selected(PLUGIN, TYPE, &initial, "provider", &[model("first")])
        .await
        .unwrap());
    assert!(!store.models(PLUGIN, "provider").await.unwrap()[0].enabled);
    store
        .upsert_resources(PLUGIN, TYPE, vec![draft("a", "new")])
        .await
        .unwrap();
    assert!(!store
        .replace_models_if_selected(PLUGIN, TYPE, &initial, "provider", &[model("late")])
        .await
        .unwrap());
    let imported = store.selection(PLUGIN, TYPE).await.unwrap();
    store
        .set_selection(PLUGIN, TYPE, Some(records[1].id.clone()), false)
        .await
        .unwrap();
    assert!(!store
        .replace_models_if_selected(PLUGIN, TYPE, &imported, "provider", &[model("late")])
        .await
        .unwrap());
    let selected = store.selection(PLUGIN, TYPE).await.unwrap();
    store
        .remove_resource(PLUGIN, TYPE, &records[1].id)
        .await
        .unwrap();
    assert!(!store
        .replace_models_if_selected(PLUGIN, TYPE, &selected, "provider", &[model("late")])
        .await
        .unwrap());
    assert_eq!(
        store.models(PLUGIN, "provider").await.unwrap()[0].id,
        "first"
    );
}

#[tokio::test]
async fn invalid_import_is_transactional_and_recreated_key_has_new_identity() {
    let (_root, store) = store();
    let records = seed(&store).await;
    let selection = store.selection(PLUGIN, TYPE).await.unwrap();
    assert!(store
        .upsert_resources(PLUGIN, TYPE, vec![draft("a", "bad"), draft("", "bad")])
        .await
        .is_err());
    assert_eq!(
        store.resources(PLUGIN, TYPE).await.unwrap()[0].private_data["token"],
        "a"
    );
    assert_eq!(store.selection(PLUGIN, TYPE).await.unwrap(), selection);
    store
        .remove_resource(PLUGIN, TYPE, &records[0].id)
        .await
        .unwrap();
    store
        .upsert_resources(PLUGIN, TYPE, vec![draft("a", "new")])
        .await
        .unwrap();
    assert!(!store
        .apply_patch_if_current(PLUGIN, TYPE, &records[0], invalid())
        .await
        .unwrap());
    let recreated = store
        .resources(PLUGIN, TYPE)
        .await
        .unwrap()
        .into_iter()
        .find(|record| record.key == "a")
        .unwrap();
    assert_ne!(recreated.id, records[0].id);
    assert_eq!(recreated.private_data["token"], "new");
}

#[tokio::test]
async fn selection_errors_explain_missing_cooling_invalid_and_exhausted_resources() {
    let (_root, store) = store();
    let error = store
        .select_resource(PLUGIN, TYPE, true)
        .await
        .unwrap_err()
        .to_string();
    assert!(error.contains("no plugin resources are configured"));
    let records = seed(&store).await;
    store
        .set_selection(PLUGIN, TYPE, None, false)
        .await
        .unwrap();
    let error = store
        .select_resource(PLUGIN, TYPE, true)
        .await
        .unwrap_err()
        .to_string();
    assert!(error.contains("no plugin resource is selected"));
    store
        .set_selection(PLUGIN, TYPE, Some(records[0].id.clone()), false)
        .await
        .unwrap();
    patch(
        &store,
        &records[0].id,
        ResourcePatch {
            private_data: None,
            state: Some(ResourceStateInput::Cooling {
                retry_at_ms: Some(4_102_444_800_000),
                message: Some("quota exhausted".into()),
            }),
        },
    )
    .await
    .unwrap();
    let error = store
        .select_resource(PLUGIN, TYPE, true)
        .await
        .unwrap_err()
        .to_string();
    assert!(error.contains("cooling down: quota exhausted"));
    assert!(error.contains("retry at 2100-01-01T00:00:00Z"));
    patch(
        &store,
        &records[0].id,
        ResourcePatch {
            private_data: None,
            state: Some(ResourceStateInput::Invalid {
                message: Some("credentials expired".into()),
            }),
        },
    )
    .await
    .unwrap();
    let error = store
        .select_resource(PLUGIN, TYPE, false)
        .await
        .unwrap_err()
        .to_string();
    assert!(error.contains("invalid: credentials expired"));
    assert!(error.contains("refresh or reimport"));
    for record in &records[1..] {
        patch(&store, &record.id, invalid()).await.unwrap();
    }
    store
        .set_selection(PLUGIN, TYPE, Some(records[0].id.clone()), true)
        .await
        .unwrap();
    let error = store
        .select_resource(PLUGIN, TYPE, true)
        .await
        .unwrap_err()
        .to_string();
    assert!(error.contains("no ready alternative resource is available"));
}

#[test]
fn resource_revision_strictly_advances_even_when_clock_is_behind() {
    let mut record = ResourceRecord {
        id: "id".into(),
        key: "key".into(),
        private_data: serde_json::Value::Null,
        state: ResourceState::Ready,
        created_at_ms: 0,
        updated_at_ms: i64::MAX - 2,
    };
    advance_resource(&mut record).unwrap();
    assert_eq!(record.updated_at_ms, i64::MAX - 1);
    advance_resource(&mut record).unwrap();
    assert_eq!(record.updated_at_ms, i64::MAX);
    assert!(advance_resource(&mut record).is_err());
}
