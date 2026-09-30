-- Retired names are local rejection markers, never alternate names for a target.
-- They survive alias deletion so stale Cursor requests cannot fall through remotely.
CREATE TABLE alias_retired_names (
    name TEXT PRIMARY KEY NOT NULL COLLATE NOCASE
        CHECK(length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^a-z0-9._-]*')
);
