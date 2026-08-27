use std::borrow::Cow;

use cursor_server::store::Store;
use sqlx::{migrate::Migrator, sqlite::SqliteConnectOptions, Row};

#[tokio::test]
async fn version_two_database_upgrades_with_cursor_request_mapping() {
    let directory = tempfile::tempdir().unwrap();
    let database = directory.path().join("upgrade.db");
    let pool = sqlx::SqlitePool::connect_with(
        SqliteConnectOptions::new()
            .filename(&database)
            .create_if_missing(true),
    )
    .await
    .unwrap();
    let all = sqlx::migrate!("./migrations");
    let prior = Migrator {
        migrations: Cow::Owned(
            all.iter()
                .filter(|migration| migration.version <= 2)
                .cloned()
                .collect(),
        ),
        ignore_missing: false,
        locking: true,
        no_tx: false,
    };
    prior.run(&pool).await.unwrap();
    drop(pool);

    let store = Store::connect(&format!("sqlite://{}", database.display()))
        .await
        .unwrap();
    let columns = sqlx::query("PRAGMA table_info(runs)")
        .fetch_all(store.pool())
        .await
        .unwrap();

    assert!(columns
        .iter()
        .any(|column| column.get::<String, _>("name") == "cursor_request_id"));
}

#[tokio::test]
async fn provider_and_model_rows_upgrade_to_flat_model_configuration() {
    let directory = tempfile::tempdir().unwrap();
    let database = directory.path().join("flat-model-upgrade.db");
    let pool = sqlx::SqlitePool::connect_with(
        SqliteConnectOptions::new()
            .filename(&database)
            .create_if_missing(true)
            .foreign_keys(true),
    )
    .await
    .unwrap();
    let all = sqlx::migrate!("./migrations");
    let prior = Migrator {
        migrations: Cow::Owned(
            all.iter()
                .filter(|migration| migration.version <= 3)
                .cloned()
                .collect(),
        ),
        ignore_missing: false,
        locking: true,
        no_tx: false,
    };
    prior.run(&pool).await.unwrap();
    sqlx::query(
        r#"INSERT INTO provider_endpoints(
            name, provider_type, base_url, api_key, custom_headers_json, extra_params_json,
            created_at_ms, updated_at_ms
        ) VALUES ('Example', 'openai-chat', 'https://example.com/v1', 'secret',
            '{"x-client":"cursor-byok"}', '{"service_tier":"priority"}', 10, 11)"#,
    )
    .execute(&pool)
    .await
    .unwrap();
    sqlx::query(
        r#"INSERT INTO provider_models(
            model_hash, provider_id, model_id, display_name, endpoint_type, request_url,
            enabled, sort_order, reasoning_enabled, supports_image_generation,
            created_at_ms, updated_at_ms
        ) VALUES
            ('rspns001', 1, 'model-b', 'Model B', 'openai-responses',
                'https://proxy.example.com/arbitrary/generate?api-version=2026-01-01',
                1, 5, 0, 0, 12, 13),
            ('anthr001', 1, 'model-c', 'Model C', 'anthropic', '/proxy/claude',
                1, 6, 0, 0, 12, 13),
            ('anthstd1', 1, 'model-d', 'Model D', 'anthropic', '',
                1, 7, 0, 0, 12, 13)"#,
    )
    .execute(&pool)
    .await
    .unwrap();
    sqlx::query(
        r#"INSERT INTO provider_models(
            model_hash, provider_id, model_id, display_name, endpoint_type, request_url,
            enabled, sort_order, context_window_tokens, max_output_tokens,
            reasoning_enabled, reasoning_effort, supports_image_generation,
            created_at_ms, updated_at_ms
        ) VALUES ('12345678', 1, 'model-a', 'Model A', 'openai-chat', '',
            1, 4, 200000, 8192, 1, 'high', 0, 12, 13)"#,
    )
    .execute(&pool)
    .await
    .unwrap();
    sqlx::query(
        r#"INSERT INTO llm_calls(
            call_id, run_id, conversation_id, provider_call_index, model_hash,
            provider_type, provider_url, request_type, request_url, model_id, display_name,
            status, created_at_ms, message_count, tool_count, detailed
        ) VALUES ('call-1', 'run-1', 'conversation-1', 0, '12345678',
            'openai-chat', 'https://example.com/v1', 'openai-chat',
            'https://example.com/v1/chat/completions', 'model-a', 'Model A',
            'completed', 14, 1, 0, 0)"#,
    )
    .execute(&pool)
    .await
    .unwrap();
    all.run(&pool).await.unwrap();
    drop(pool);

    let store = Store::connect(&format!("sqlite://{}", database.display()))
        .await
        .unwrap();
    let row = sqlx::query(
        r#"SELECT model_hash, sort_order, display_name, model_type, base_url, api_key,
            tooltip_data, model_id, reasoning_effort, openai_endpoint, use_full_url,
            openai_extra_params_enabled, openai_extra_params_json,
            custom_headers_enabled, custom_headers_json, context_window_tokens,
            max_completion_tokens
        FROM model_configs WHERE model_hash = '12345678'"#,
    )
    .fetch_one(store.pool())
    .await
    .unwrap();

    assert_eq!(row.get::<String, _>("model_hash"), "12345678");
    assert_eq!(row.get::<i64, _>("sort_order"), 4);
    assert_eq!(row.get::<String, _>("model_type"), "openai");
    assert_eq!(row.get::<String, _>("base_url"), "https://example.com/v1");
    assert_eq!(row.get::<String, _>("api_key"), "secret");
    assert_eq!(row.get::<i64, _>("use_full_url"), 0);
    assert_eq!(row.get::<String, _>("reasoning_effort"), "high");
    assert_eq!(
        row.get::<String, _>("openai_endpoint"),
        "/v1/chat/completions"
    );
    assert_eq!(row.get::<i64, _>("openai_extra_params_enabled"), 1);
    assert_eq!(
        row.get::<String, _>("openai_extra_params_json"),
        r#"{"service_tier":"priority"}"#
    );
    assert_eq!(row.get::<i64, _>("custom_headers_enabled"), 1);
    assert_eq!(row.get::<i64, _>("context_window_tokens"), 200000);
    assert_eq!(row.get::<i64, _>("max_completion_tokens"), 8192);
    let migrated_rows = sqlx::query(
        "SELECT model_hash, model_type, base_url, use_full_url, openai_endpoint FROM model_configs WHERE model_hash IN ('rspns001', 'anthr001', 'anthstd1') ORDER BY model_hash",
    )
    .fetch_all(store.pool())
    .await
    .unwrap();
    assert_eq!(migrated_rows[0].get::<String, _>("model_hash"), "anthr001");
    assert_eq!(migrated_rows[0].get::<String, _>("model_type"), "anthropic");
    assert_eq!(
        migrated_rows[0].get::<String, _>("base_url"),
        "https://example.com/v1/proxy/claude"
    );
    assert_eq!(migrated_rows[0].get::<i64, _>("use_full_url"), 1);
    assert_eq!(migrated_rows[0].get::<String, _>("openai_endpoint"), "");
    assert_eq!(migrated_rows[1].get::<String, _>("model_hash"), "anthstd1");
    assert_eq!(migrated_rows[1].get::<String, _>("model_type"), "anthropic");
    assert_eq!(
        migrated_rows[1].get::<String, _>("base_url"),
        "https://example.com/v1"
    );
    assert_eq!(migrated_rows[1].get::<i64, _>("use_full_url"), 0);
    assert_eq!(migrated_rows[1].get::<String, _>("openai_endpoint"), "");
    assert_eq!(migrated_rows[2].get::<String, _>("model_hash"), "rspns001");
    assert_eq!(migrated_rows[2].get::<String, _>("model_type"), "openai");
    assert_eq!(
        migrated_rows[2].get::<String, _>("base_url"),
        "https://proxy.example.com/arbitrary/generate?api-version=2026-01-01"
    );
    assert_eq!(migrated_rows[2].get::<i64, _>("use_full_url"), 1);
    assert_eq!(
        migrated_rows[2].get::<String, _>("openai_endpoint"),
        "/v1/responses"
    );
    assert_eq!(
        sqlx::query_scalar::<_, String>(
            "SELECT model_hash FROM llm_calls WHERE call_id = 'call-1'"
        )
        .fetch_one(store.pool())
        .await
        .unwrap(),
        "12345678"
    );
    assert!(sqlx::query("SELECT 1 FROM provider_endpoints")
        .fetch_one(store.pool())
        .await
        .is_err());
    assert!(sqlx::query("SELECT 1 FROM provider_models")
        .fetch_one(store.pool())
        .await
        .is_err());
    assert!(sqlx::query("PRAGMA foreign_key_check")
        .fetch_all(store.pool())
        .await
        .unwrap()
        .is_empty());
}

#[tokio::test]
async fn model_tokens_backfill_subscription_accounts() {
    let directory = tempfile::tempdir().unwrap();
    let database = directory.path().join("subscription-accounts.db");
    let pool = sqlx::SqlitePool::connect_with(
        SqliteConnectOptions::new()
            .filename(&database)
            .create_if_missing(true)
            .foreign_keys(true),
    )
    .await
    .unwrap();
    let all = sqlx::migrate!("./migrations");
    let prior = Migrator {
        migrations: Cow::Owned(
            all.iter()
                .filter(|migration| migration.version <= 4)
                .cloned()
                .collect(),
        ),
        ignore_missing: false,
        locking: true,
        no_tx: false,
    };
    prior.run(&pool).await.unwrap();
    sqlx::query(
        r#"INSERT INTO model_configs(
            model_hash, sort_order, display_name, model_type, base_url, use_full_url, api_key,
            tooltip_data, model_id, openai_endpoint, openai_extra_params_enabled,
            openai_extra_params_json, custom_headers_enabled, custom_headers_json,
            anthropic_extra_params_enabled, anthropic_extra_params_json, created_at_ms, updated_at_ms
        ) VALUES
        ('grokhash', 1, 'grok-4', 'openai', 'https://api.x.ai/v1', 0, 'grok-token',
         'xAI Grok', 'grok-4', '/v1/chat/completions', 0, '{}', 0, '{}', 0, '{}', 1, 1),
        ('codexhash', 2, 'gpt-5.4', 'openai', 'https://chatgpt.com/backend-api/codex/responses', 1, 'codex-token',
         'ChatGPT / OpenAI Codex', 'gpt-5.4', '/v1/responses', 0, '{}', 0, '{}', 0, '{}', 1, 1)"#,
    )
    .execute(&pool)
    .await
    .unwrap();
    drop(pool);

    let store = Store::connect(&format!("sqlite://{}", database.display()))
        .await
        .unwrap();
    let grok = store
        .subscription_accounts(cursor_server::subscription::SubscriptionKind::Grok)
        .await
        .unwrap();
    let codex = store
        .subscription_accounts(cursor_server::subscription::SubscriptionKind::Codex)
        .await
        .unwrap();
    assert_eq!(grok.len(), 1);
    assert_eq!(grok[0].access_token, "grok-token");
    assert!(grok[0].active);
    assert_eq!(codex.len(), 1);
    assert_eq!(codex[0].access_token, "codex-token");
    assert!(codex[0].active);
}
