-- Reverses 011_agent_definition_surrogate_key.up.sql.
ALTER TABLE agent_definition
    DROP CONSTRAINT agent_definition_pkey,
    DROP CONSTRAINT agent_definition_agent_id_version_key,
    ADD CONSTRAINT agent_definition_pkey PRIMARY KEY (agent_id, version);

ALTER TABLE agent_definition
    DROP COLUMN id;
