-- Adds agent_definition.system_prompt: an optional system-role instruction
-- text for a session using this definition. NULL (the default for every
-- existing row) means no system prompt is set -- the historical behavior,
-- unchanged. Nothing reads this column into a model call yet; storage only,
-- to be wired into worker's message construction in a follow-up.
ALTER TABLE agent_definition
    ADD COLUMN system_prompt TEXT;
