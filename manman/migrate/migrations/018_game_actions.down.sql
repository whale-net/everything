-- Migration 018 Rollback: Game Actions System

-- Drop views first (depend on tables)
DROP VIEW IF EXISTS action_counts_by_game;
DROP VIEW IF EXISTS action_with_inputs;
DROP VIEW IF EXISTS action_summary;

-- Drop tables in reverse dependency order
DROP TABLE IF EXISTS action_executions;
DROP TABLE IF EXISTS action_visibility_overrides;
DROP TABLE IF EXISTS action_input_options;
DROP TABLE IF EXISTS action_input_fields;
DROP TABLE IF EXISTS action_definitions;
