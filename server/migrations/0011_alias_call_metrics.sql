-- Record alias routing on existing per-attempt session metrics without storing credentials.
ALTER TABLE llm_calls ADD COLUMN alias_id TEXT;
ALTER TABLE llm_calls ADD COLUMN alias_name TEXT;
ALTER TABLE llm_calls ADD COLUMN alias_target_id TEXT;
ALTER TABLE llm_calls ADD COLUMN alias_switch_count INTEGER NOT NULL DEFAULT 0 CHECK (alias_switch_count >= 0);
