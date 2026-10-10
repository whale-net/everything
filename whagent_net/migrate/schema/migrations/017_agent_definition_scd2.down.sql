-- Restore agent_version from the pinned definition before making it NOT NULL.
UPDATE session_agent sa
    SET agent_version = d.version
    FROM agent_definition d
    WHERE sa.agent_version IS NULL
      AND sa.agent_definition_id = d.id;

ALTER TABLE session_agent ALTER COLUMN agent_version SET NOT NULL;

ALTER TABLE turn_usage DROP COLUMN IF EXISTS agent_definition_id;
ALTER TABLE session_agent DROP COLUMN IF EXISTS agent_definition_id;

DROP INDEX IF EXISTS idx_agent_definition_current;

ALTER TABLE agent_definition
    DROP COLUMN IF EXISTS valid_to,
    DROP COLUMN IF EXISTS valid_from;
