DROP INDEX IF EXISTS entity_milestone_entity_milestone_idx;
CREATE UNIQUE INDEX entity_milestone_entity_milestone_idx ON entity_milestone(entity_id, milestone_id);
ALTER TABLE entity_milestone DROP COLUMN IF EXISTS relation;

DROP TABLE IF EXISTS milestone_deferral;

ALTER TABLE milestone_ref
    DROP COLUMN IF EXISTS created_by_on_behalf_of_kind,
    DROP COLUMN IF EXISTS created_by_on_behalf_of_sub,
    DROP COLUMN IF EXISTS created_by_on_behalf_of_iss,
    DROP COLUMN IF EXISTS created_by_acting_kind,
    DROP COLUMN IF EXISTS created_by_acting_sub,
    DROP COLUMN IF EXISTS created_by_acting_iss,
    DROP COLUMN IF EXISTS position,
    DROP COLUMN IF EXISTS fr_budget,
    DROP COLUMN IF EXISTS outcome,
    DROP COLUMN IF EXISTS kind;
