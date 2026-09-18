ALTER TABLE agent_definition
    DROP CONSTRAINT agent_definition_tool_loading_mode_check,
    DROP COLUMN tool_loading_mode;
