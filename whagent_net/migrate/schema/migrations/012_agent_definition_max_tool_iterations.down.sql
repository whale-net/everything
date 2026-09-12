ALTER TABLE sessions
    DROP CONSTRAINT sessions_cap_kind_check,
    ADD CONSTRAINT sessions_cap_kind_check CHECK (cap_kind IN ('turns', 'cost'));

ALTER TABLE agent_definition
    DROP COLUMN max_tool_iterations;
