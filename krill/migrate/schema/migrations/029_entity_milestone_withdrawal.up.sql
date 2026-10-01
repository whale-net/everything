-- Edge withdrawal for entity_milestone (expand step). A withdrawn edge keeps
-- its row (the edge is not SCD2, rows are never deleted); readers see only
-- active edges through the entity_milestone_active view. withdrawn_by_* follow
-- the LB4 acting / on_behalf_of pair and are populated together with withdrawn_at.
ALTER TABLE entity_milestone
    ADD COLUMN withdrawn_at                TIMESTAMPTZ,
    ADD COLUMN withdrawn_by_acting_iss     TEXT,
    ADD COLUMN withdrawn_by_acting_sub     TEXT,
    ADD COLUMN withdrawn_by_acting_kind    TEXT,
    ADD COLUMN withdrawn_by_on_behalf_iss  TEXT,
    ADD COLUMN withdrawn_by_on_behalf_sub  TEXT,
    ADD COLUMN withdrawn_by_on_behalf_kind TEXT,
    ADD COLUMN withdrawal_reason           TEXT,
    ADD CONSTRAINT entity_milestone_withdrawal_complete CHECK (
        (withdrawn_at IS NULL
            AND withdrawn_by_acting_iss IS NULL AND withdrawn_by_acting_sub IS NULL AND withdrawn_by_acting_kind IS NULL
            AND withdrawn_by_on_behalf_iss IS NULL AND withdrawn_by_on_behalf_sub IS NULL AND withdrawn_by_on_behalf_kind IS NULL
            AND withdrawal_reason IS NULL)
        OR
        (withdrawn_at IS NOT NULL
            AND withdrawn_by_acting_iss IS NOT NULL AND withdrawn_by_acting_sub IS NOT NULL AND withdrawn_by_acting_kind IN ('human', 'service', 'agent')
            AND withdrawn_by_on_behalf_iss IS NOT NULL AND withdrawn_by_on_behalf_sub IS NOT NULL AND withdrawn_by_on_behalf_kind IN ('human', 'service', 'agent')
            AND withdrawal_reason IS NOT NULL)
    );

DROP INDEX entity_milestone_entity_milestone_idx;
CREATE UNIQUE INDEX entity_milestone_entity_milestone_idx
    ON entity_milestone(entity_id, milestone_id, relation) WHERE withdrawn_at IS NULL;

-- The one active-edge filter every entity_milestone reader goes through.
CREATE VIEW entity_milestone_active AS
    SELECT id, scope_id, entity_id, milestone_id, relation, created_at
    FROM entity_milestone
    WHERE withdrawn_at IS NULL;
