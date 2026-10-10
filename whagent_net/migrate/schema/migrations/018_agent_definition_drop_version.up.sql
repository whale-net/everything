-- Contract half of moving agent_definition from (agent_id, version) to SCD2.
-- Requires no live binary to read or write version/agent_version. Re-runnable.

-- Rows written by old binaries between 017 and the cutover.
-- Guarded: on a re-run the version columns are already gone.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'session_agent' AND column_name = 'agent_version'
                 AND table_schema = current_schema()) THEN
        EXECUTE 'UPDATE session_agent sa SET agent_definition_id = d.id
                 FROM agent_definition d
                 WHERE sa.agent_definition_id IS NULL
                   AND d.agent_id = sa.agent_id
                   AND d.version = sa.agent_version';
    END IF;
END $$;

UPDATE turn_usage tu
    SET agent_definition_id = sa.agent_definition_id
    FROM session_agent sa
    WHERE tu.agent_definition_id IS NULL
      AND sa.session_id = tu.session_id
      AND sa.valid_from <= tu.created_at
      AND (sa.valid_to IS NULL OR tu.created_at < sa.valid_to);

ALTER TABLE session_agent ALTER COLUMN agent_definition_id SET NOT NULL;
ALTER TABLE turn_usage ALTER COLUMN agent_definition_id SET NOT NULL;

ALTER TABLE agent_definition DROP CONSTRAINT IF EXISTS agent_definition_agent_id_version_key;
ALTER TABLE agent_definition DROP COLUMN IF EXISTS version;
ALTER TABLE session_agent DROP COLUMN IF EXISTS agent_version;

CREATE INDEX IF NOT EXISTS idx_turn_usage_agent_definition_created
    ON turn_usage (agent_definition_id, created_at);
