-- Load-bearing-decision -> Feature 'protects' edge. An association, not a
-- parent: a decision stays single-parent under its FeatureSet. Rows are never
-- deleted; withdrawal stamps withdrawn_at and the LB4 acting / on_behalf_of
-- pair, mirroring entity_milestone (migration 029). decision_id and feature_id
-- are SCD2 surrogate ids, so no FK targets them; the store validates both.
CREATE TABLE lb_protects_feature (
    id                         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                   UUID        NOT NULL REFERENCES scope(id),
    decision_id                UUID        NOT NULL,
    feature_id                 UUID        NOT NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    withdrawn_at               TIMESTAMPTZ,
    withdrawn_by_acting_iss    TEXT,
    withdrawn_by_acting_sub    TEXT,
    withdrawn_by_acting_kind   TEXT,
    withdrawn_by_on_behalf_iss  TEXT,
    withdrawn_by_on_behalf_sub  TEXT,
    withdrawn_by_on_behalf_kind TEXT,
    CONSTRAINT lb_protects_feature_withdrawal_complete CHECK (
        (withdrawn_at IS NULL
            AND withdrawn_by_acting_iss IS NULL AND withdrawn_by_acting_sub IS NULL AND withdrawn_by_acting_kind IS NULL
            AND withdrawn_by_on_behalf_iss IS NULL AND withdrawn_by_on_behalf_sub IS NULL AND withdrawn_by_on_behalf_kind IS NULL)
        OR
        (withdrawn_at IS NOT NULL
            AND withdrawn_by_acting_iss IS NOT NULL AND withdrawn_by_acting_sub IS NOT NULL AND withdrawn_by_acting_kind IN ('human', 'service', 'agent')
            AND withdrawn_by_on_behalf_iss IS NOT NULL AND withdrawn_by_on_behalf_sub IS NOT NULL AND withdrawn_by_on_behalf_kind IN ('human', 'service', 'agent'))
    )
);

-- At most one active edge per pair; a withdrawn pair may be re-added.
CREATE UNIQUE INDEX lb_protects_feature_active_idx
    ON lb_protects_feature(decision_id, feature_id) WHERE withdrawn_at IS NULL;
CREATE INDEX lb_protects_feature_feature_idx ON lb_protects_feature(feature_id);

-- The one active-edge filter every reader goes through.
CREATE VIEW lb_protects_feature_active AS
    SELECT id, scope_id, decision_id, feature_id, created_at
    FROM lb_protects_feature
    WHERE withdrawn_at IS NULL;
