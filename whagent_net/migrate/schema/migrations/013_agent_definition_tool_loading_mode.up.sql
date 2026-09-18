-- Adds agent_definition.tool_loading_mode (FR1): whether a session using
-- this definition loads its full tool_set up front ('bulk', today's only
-- behavior) or discovers tools by search at call time ('search', M4's
-- opt-in). DEFAULT 'bulk' backfills every existing row and is what makes
-- FR1's "every existing row defaults to bulk" true by construction -- no
-- caller reads this column yet (FR2 holds trivially until it does).
ALTER TABLE agent_definition
    ADD COLUMN tool_loading_mode TEXT NOT NULL DEFAULT 'bulk',
    ADD CONSTRAINT agent_definition_tool_loading_mode_check
        CHECK (tool_loading_mode IN ('bulk', 'search'));
