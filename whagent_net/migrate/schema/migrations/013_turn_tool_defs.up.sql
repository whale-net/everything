-- turn_tool_defs: mirrors turn_context's shape (migration 001) for the tool
-- catalog ListToolDefinitions resolves each turn -- the full tool list
-- (JSON schemas included) is materialized here once per turn instead of
-- being forwarded verbatim on every one of that turn's CallModel activity
-- calls (the initial call plus every tool-loop iteration, up to
-- max_tool_iterations), which was duplicating it into Temporal workflow
-- history up to ~11 times per turn (ARCHITECTURE.md "Activity payload
-- discipline").
CREATE TABLE turn_tool_defs (
    session_id UUID NOT NULL,
    turn       INT NOT NULL,
    tools      JSONB NOT NULL,
    PRIMARY KEY (session_id, turn)
);
