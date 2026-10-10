-- Expand half of moving agent_definition from (agent_id, version) to SCD2.
-- Additive only: version, UNIQUE (agent_id, version) and
-- session_agent.agent_version stay until the contract migration. Re-runnable.

ALTER TABLE agent_definition
    ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS valid_to   TIMESTAMPTZ NULL;

UPDATE agent_definition
    SET valid_from = created_at
    WHERE valid_from IS NULL;

-- Close each superseded version at the next version's created_at; the newest
-- version per agent_id stays open.
UPDATE agent_definition d
    SET valid_to = n.next_created_at
    FROM (
        SELECT id, LEAD(created_at) OVER (PARTITION BY agent_id ORDER BY version) AS next_created_at
        FROM agent_definition
    ) n
    WHERE d.id = n.id
      AND d.valid_to IS NULL
      AND n.next_created_at IS NOT NULL;

ALTER TABLE agent_definition
    ALTER COLUMN valid_from SET DEFAULT NOW(),
    ALTER COLUMN valid_from SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_definition_current
    ON agent_definition (agent_id) WHERE valid_to IS NULL;

-- Nullable until the contract migration: running binaries still insert
-- session_agent rows without it.
ALTER TABLE session_agent
    ADD COLUMN IF NOT EXISTS agent_definition_id UUID NULL REFERENCES agent_definition(id);

UPDATE session_agent sa
    SET agent_definition_id = d.id
    FROM agent_definition d
    WHERE sa.agent_definition_id IS NULL
      AND d.agent_id = sa.agent_id
      AND d.version = sa.agent_version;

-- New code stops writing agent_version so the contract migration can drop it
-- without breaking pods still mid-rollout.
ALTER TABLE session_agent ALTER COLUMN agent_version DROP NOT NULL;

ALTER TABLE turn_usage
    ADD COLUMN IF NOT EXISTS agent_definition_id UUID NULL REFERENCES agent_definition(id);

-- Each usage row takes the definition assigned to its session at created_at.
UPDATE turn_usage tu
    SET agent_definition_id = sa.agent_definition_id
    FROM session_agent sa
    WHERE tu.agent_definition_id IS NULL
      AND sa.session_id = tu.session_id
      AND sa.valid_from <= tu.created_at
      AND (sa.valid_to IS NULL OR tu.created_at < sa.valid_to);
