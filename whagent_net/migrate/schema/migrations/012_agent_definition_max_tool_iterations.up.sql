-- Adds agent_definition.max_tool_iterations (the inner tool-call loop's own
-- guardrail, alongside max_turns/max_cost_usd): the maximum number of model
-- calls worker/workflow.go's processTurn may make within a single external
-- turn while the model keeps requesting tool calls, before ending the
-- session capped rather than looping without bound. DEFAULT 10 backfills
-- every existing row (worker/caps.go's defaultMaxToolIterations mirrors this
-- same default for a zero-valued field, the same convention max_turns/
-- max_cost_usd already follow).
ALTER TABLE agent_definition
    ADD COLUMN max_tool_iterations INT NOT NULL DEFAULT 10;

-- sessions.cap_kind gains 'tool_iterations' as a third possible value
-- (session.CapKindToolIterations) alongside 'turns'/'cost' -- tripping the
-- new cap ends a session capped exactly like the other two.
ALTER TABLE sessions
    DROP CONSTRAINT sessions_cap_kind_check,
    ADD CONSTRAINT sessions_cap_kind_check CHECK (cap_kind IN ('turns', 'cost', 'tool_iterations'));
