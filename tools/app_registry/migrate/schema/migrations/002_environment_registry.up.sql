-- App Registry — Environment Registry (AR-3b)
--
-- Adds the `environment` table described in ARCHITECTURE.md's "Data model":
-- environments are managed rows, not a compiled-in enum, so an ephemeral or
-- regional environment is an insert, not a release. `promotion`,
-- `promotion_event`, and `v_current_promotion` are deliberately NOT here —
-- they land in AR-3c's migration, which needs `environment_id` to exist
-- first. See migrate/README.md "Planned migrations".

CREATE TABLE environment (
    environment_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key                  TEXT NOT NULL,
    display_name         TEXT NOT NULL DEFAULT '',
    -- Orders promotion legality (e.g. "must be in stage before prod"). Not
    -- enforced until AR-3c. Left as plain INTEGER, not UNIQUE — nothing in
    -- ARCHITECTURE.md requires ranks to be distinct, and a shared rank
    -- (e.g. two parallel prod-like environments) is a legitimate shape.
    rank                  INTEGER NOT NULL DEFAULT 0,
    -- Honored once the approval gate ships (see ARCHITECTURE.md "Future:
    -- approval gate"). Not enforced by anything in AR-3b.
    requires_approval    BOOLEAN NOT NULL DEFAULT false,
    gitops_path          TEXT NOT NULL DEFAULT '',
    -- Principals permitted to promote here, checked against the caller's
    -- OIDC claims once AR-3c enforces it.
    allowed_principals   TEXT[] NOT NULL DEFAULT '{}',
    archived             BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (key)
);

CREATE INDEX environment_rank_idx ON environment (rank);

-- Seed the three environments every domain promotes through. Ranks leave
-- gaps (0/10/20) so a future environment (e.g. "stage-eu") can be inserted
-- between existing ones without renumbering. requires_approval is left
-- false for all three here — the approval gate is unimplemented (see
-- ARCHITECTURE.md), so defaulting prod to true would be inert metadata a
-- future migration would need to reason about; enabling it is an explicit
-- admin action via UpsertEnvironment once the gate exists.
--
-- ON CONFLICT (key) DO NOTHING makes this safe to apply to a database that
-- already has these rows (e.g. a partially-applied migration retried, or an
-- operator who pre-seeded via UpsertEnvironment before this migration ran)
-- — it will never overwrite live admin edits. See the AR-3b phase report
-- for why seeding lives in the migration rather than server startup.
INSERT INTO environment (key, display_name, rank, requires_approval, gitops_path)
VALUES
    ('dev', 'Development', 0, false, 'environments/dev'),
    ('stage', 'Staging', 10, false, 'environments/stage'),
    ('prod', 'Production', 20, false, 'environments/prod')
ON CONFLICT (key) DO NOTHING;
