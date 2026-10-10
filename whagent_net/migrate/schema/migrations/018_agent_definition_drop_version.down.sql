DROP INDEX IF EXISTS idx_turn_usage_agent_definition_created;

ALTER TABLE agent_definition ADD COLUMN IF NOT EXISTS version INT;

UPDATE agent_definition d
    SET version = n.rn
    FROM (
        SELECT id, ROW_NUMBER() OVER (PARTITION BY agent_id ORDER BY valid_from, id) AS rn
        FROM agent_definition
    ) n
    WHERE d.id = n.id AND d.version IS NULL;

ALTER TABLE agent_definition ALTER COLUMN version SET NOT NULL;
ALTER TABLE agent_definition DROP CONSTRAINT IF EXISTS agent_definition_agent_id_version_key;
ALTER TABLE agent_definition
    ADD CONSTRAINT agent_definition_agent_id_version_key UNIQUE (agent_id, version);

-- Nullable, matching 017.
ALTER TABLE session_agent ADD COLUMN IF NOT EXISTS agent_version INT;

UPDATE session_agent sa
    SET agent_version = d.version
    FROM agent_definition d
    WHERE sa.agent_version IS NULL
      AND sa.agent_definition_id = d.id;

ALTER TABLE session_agent ALTER COLUMN agent_definition_id DROP NOT NULL;
ALTER TABLE turn_usage ALTER COLUMN agent_definition_id DROP NOT NULL;
