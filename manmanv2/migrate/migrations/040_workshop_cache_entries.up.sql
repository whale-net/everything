-- workshop_cache_entries / workshop_cache_host_presence: the content-addressed
-- Workshop cache's identity and metadata (#2181, plan #2175, FR5/FR9).
--
-- Identity is workshop_id + content_version ONLY (NFR1) -- no sgc_id,
-- server_id, deployment_id, library_id, or game_id column exists here, and
-- cache_key never embeds any of them (see //manmanv2/api/workshop/cachekey.go).
-- workshop_cache_host_presence records *where a copy currently sits*, as an
-- observation keyed on (cache_entry_id, server_id); it is deliberately a
-- separate table rather than a column on the entry so presence can never
-- leak into the entry's identity or its UNIQUE(cache_key) constraint.
--
-- Deliberately not SCD2 (AGENTS.md § SCD2, FR9): this is an append-only
-- version history, not a dimension. When an addon's source changes, a new
-- row is inserted; the prior row is never closed out via valid_from/
-- valid_to, an is_current flag, or any other SCD2 apparatus -- it just sits
-- there until explicit Admin eviction (FR12). No garbage collection of
-- superseded entries is built here; unbounded growth is accepted for M4.
--
-- No SGC-scoped uniqueness constraint exists anywhere in this layer (NFR2).

CREATE TABLE IF NOT EXISTS workshop_cache_entries (
    cache_entry_id BIGSERIAL PRIMARY KEY,
    workshop_id TEXT NOT NULL,
    content_version TEXT NOT NULL,
    cache_key TEXT NOT NULL,
    s3_key TEXT NOT NULL,
    size_bytes BIGINT,
    last_verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT workshop_cache_entries_cache_key_key UNIQUE (cache_key)
);

-- ListCacheEntriesForWorkshopID's lookup path: every version of a given
-- addon's content, oldest and newest alike (append-only history, FR9).
CREATE INDEX IF NOT EXISTS idx_workshop_cache_entries_workshop_id
    ON workshop_cache_entries(workshop_id);

CREATE TABLE IF NOT EXISTS workshop_cache_host_presence (
    cache_entry_id BIGINT NOT NULL REFERENCES workshop_cache_entries(cache_entry_id) ON DELETE CASCADE,
    server_id BIGINT NOT NULL REFERENCES servers(server_id),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (cache_entry_id, server_id)
);
