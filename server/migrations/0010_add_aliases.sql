-- API channels have a stable identity independent of their mutable configuration hash.
-- Rebuild once so the new UUID is required without leaving an invalid insert default.
CREATE TABLE model_configs_next (
    model_hash TEXT PRIMARY KEY,
    source_id TEXT NOT NULL UNIQUE CHECK(length(source_id) = 36),
    sort_order INTEGER NOT NULL DEFAULT 0,
    display_name TEXT NOT NULL,
    group_name TEXT,
    model_type TEXT NOT NULL CHECK(model_type IN ('openai', 'anthropic')),
    base_url TEXT NOT NULL,
    use_full_url INTEGER NOT NULL DEFAULT 0 CHECK(use_full_url IN (0, 1)),
    api_key TEXT NOT NULL,
    tooltip_data TEXT NOT NULL,
    model_id TEXT NOT NULL,
    reasoning_effort TEXT,
    openai_endpoint TEXT NOT NULL DEFAULT '',
    openai_extra_params_enabled INTEGER NOT NULL DEFAULT 0 CHECK(openai_extra_params_enabled IN (0, 1)),
    openai_extra_params_json TEXT NOT NULL DEFAULT '{}',
    custom_headers_enabled INTEGER NOT NULL DEFAULT 0 CHECK(custom_headers_enabled IN (0, 1)),
    custom_headers_json TEXT NOT NULL DEFAULT '{}',
    anthropic_extra_params_enabled INTEGER NOT NULL DEFAULT 0 CHECK(anthropic_extra_params_enabled IN (0, 1)),
    anthropic_extra_params_json TEXT NOT NULL DEFAULT '{}',
    context_window_tokens INTEGER,
    max_completion_tokens INTEGER,
    anthropic_max_tokens INTEGER,
    anthropic_thinking_effort TEXT,
    thinking_budget_tokens INTEGER,
    supports_images INTEGER CHECK(supports_images IN (0, 1)),
    supports_tools INTEGER CHECK(supports_tools IN (0, 1)),
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
);

INSERT INTO model_configs_next (
    model_hash, source_id, sort_order, display_name, group_name, model_type, base_url,
    use_full_url, api_key, tooltip_data, model_id, reasoning_effort, openai_endpoint,
    openai_extra_params_enabled, openai_extra_params_json, custom_headers_enabled,
    custom_headers_json, anthropic_extra_params_enabled, anthropic_extra_params_json,
    context_window_tokens, max_completion_tokens, anthropic_max_tokens,
    anthropic_thinking_effort, thinking_budget_tokens, created_at_ms, updated_at_ms
)
SELECT
    model_hash,
    lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' ||
        substr(hex(randomblob(2)), 2) || '-' || substr('89ab', (random() & 3) + 1, 1) ||
        substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))),
    sort_order, display_name, group_name, model_type, base_url, use_full_url, api_key,
    tooltip_data, model_id, reasoning_effort, openai_endpoint, openai_extra_params_enabled,
    openai_extra_params_json, custom_headers_enabled, custom_headers_json,
    anthropic_extra_params_enabled, anthropic_extra_params_json, context_window_tokens,
    max_completion_tokens, anthropic_max_tokens, anthropic_thinking_effort,
    thinking_budget_tokens, created_at_ms, updated_at_ms
FROM model_configs;

DROP TABLE model_configs;
ALTER TABLE model_configs_next RENAME TO model_configs;
CREATE INDEX model_configs_sort ON model_configs(sort_order, display_name);

-- The ordered JSON list deliberately has no source foreign keys: missing channels
-- remain visible to the user and can be repaired without losing alias configuration.
CREATE TABLE aliases (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE
        CHECK(length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^a-z0-9._-]*'),
    description TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL CHECK(enabled IN (0, 1)),
    targets_json TEXT NOT NULL CHECK(json_valid(targets_json) AND json_type(targets_json) = 'array'),
    sticky INTEGER NOT NULL DEFAULT 1 CHECK(sticky IN (0, 1)),
    return_mode TEXT NOT NULL DEFAULT 'new_sessions' CHECK(return_mode IN ('new_sessions', 'immediate')),
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    CHECK(enabled = 0 OR json_array_length(targets_json) > 0)
);
