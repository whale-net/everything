DROP VIEW IF EXISTS entity_milestone_active;
-- Withdrawn rows would violate the full unique index; drop them first.
DELETE FROM entity_milestone WHERE withdrawn_at IS NOT NULL;
DROP INDEX IF EXISTS entity_milestone_entity_milestone_idx;
CREATE UNIQUE INDEX entity_milestone_entity_milestone_idx ON entity_milestone(entity_id, milestone_id, relation);
ALTER TABLE entity_milestone
    DROP CONSTRAINT IF EXISTS entity_milestone_withdrawal_complete,
    DROP COLUMN IF EXISTS withdrawn_at,
    DROP COLUMN IF EXISTS withdrawn_by_acting_iss,
    DROP COLUMN IF EXISTS withdrawn_by_acting_sub,
    DROP COLUMN IF EXISTS withdrawn_by_acting_kind,
    DROP COLUMN IF EXISTS withdrawn_by_on_behalf_iss,
    DROP COLUMN IF EXISTS withdrawn_by_on_behalf_sub,
    DROP COLUMN IF EXISTS withdrawn_by_on_behalf_kind,
    DROP COLUMN IF EXISTS withdrawal_reason;
