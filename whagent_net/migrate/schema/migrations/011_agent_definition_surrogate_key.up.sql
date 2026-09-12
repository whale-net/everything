-- agent_definition gains a surrogate `id` (issue: agent_id is a stable,
-- human-authored business key -- see agents.yaml's doc comment and
-- LB5/NFR6 -- and stays exactly that; it is not being replaced). A
-- composite (agent_id, version) primary key is awkward for any code or
-- log line that needs to name one row (session_agent, future FKs, error
-- messages) without repeating both columns every time. `id` gives every
-- row a single stable handle; (agent_id, version) moves from PRIMARY KEY
-- to an explicit UNIQUE constraint, so the one invariant that actually
-- matters -- at most one row per (agent_id, version), which is what
-- stops migrate/seed's seedOne from ever writing a duplicate version even
-- if two `migrate` runs race in dev -- still holds at the database layer,
-- not just in application code.
ALTER TABLE agent_definition
    ADD COLUMN id UUID NOT NULL DEFAULT gen_random_uuid();

ALTER TABLE agent_definition
    DROP CONSTRAINT agent_definition_pkey,
    ADD CONSTRAINT agent_definition_pkey PRIMARY KEY (id),
    ADD CONSTRAINT agent_definition_agent_id_version_key UNIQUE (agent_id, version);
