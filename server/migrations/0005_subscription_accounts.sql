CREATE TABLE subscription_accounts (
    account_id TEXT PRIMARY KEY NOT NULL,
    provider TEXT NOT NULL CHECK (provider IN ('grok', 'codex')),
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    display_name TEXT NOT NULL,
    plan_label TEXT,
    remaining_percent REAL,
    used_percent REAL,
    reset_at_ms INTEGER,
    limit_reached INTEGER NOT NULL DEFAULT 0 CHECK (limit_reached IN (0, 1)),
    active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
);

CREATE INDEX subscription_accounts_provider
    ON subscription_accounts (provider, active DESC, updated_at_ms DESC);

CREATE UNIQUE INDEX subscription_accounts_one_active
    ON subscription_accounts (provider)
    WHERE active = 1;

INSERT INTO subscription_accounts (
    account_id, provider, access_token, refresh_token, display_name,
    plan_label, remaining_percent, used_percent, reset_at_ms, limit_reached,
    active, created_at_ms, updated_at_ms
)
SELECT
    'grok:' || MIN(model_hash),
    'grok',
    api_key,
    NULL,
    'Grok',
    NULL,
    NULL,
    NULL,
    NULL,
    0,
    0,
    MIN(created_at_ms),
    MIN(updated_at_ms)
FROM model_configs
WHERE trim(api_key) != ''
  AND (
        instr(base_url, 'api.x.ai') > 0
        OR instr(lower(model_id), 'grok') > 0
        OR instr(tooltip_data, 'xAI') > 0
        OR instr(tooltip_data, 'Grok') > 0
      )
GROUP BY api_key;

INSERT INTO subscription_accounts (
    account_id, provider, access_token, refresh_token, display_name,
    plan_label, remaining_percent, used_percent, reset_at_ms, limit_reached,
    active, created_at_ms, updated_at_ms
)
SELECT
    'codex:' || MIN(model_hash),
    'codex',
    api_key,
    NULL,
    'ChatGPT',
    NULL,
    NULL,
    NULL,
    NULL,
    0,
    0,
    MIN(created_at_ms),
    MIN(updated_at_ms)
FROM model_configs
WHERE trim(api_key) != ''
  AND (
        instr(base_url, 'chatgpt.com') > 0
        OR instr(tooltip_data, 'Codex') > 0
        OR instr(tooltip_data, 'ChatGPT') > 0
      )
GROUP BY api_key;

UPDATE subscription_accounts
SET active = 1
WHERE account_id IN (
    SELECT MIN(account_id)
    FROM subscription_accounts
    GROUP BY provider
);
