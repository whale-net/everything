-- Migration 019: Clean up remaining parameter system artifacts
-- Removes leftover views and JSONB parameter columns from core tables

-- Drop parameter-related views (if they still exist)
DROP VIEW IF EXISTS parameter_usage_stats;
DROP VIEW IF EXISTS configs_with_missing_required_params;
DROP VIEW IF EXISTS parameter_value_distribution;

-- Drop JSONB parameter columns from original tables
-- These were superseded by the configuration_patches system
ALTER TABLE game_configs DROP COLUMN IF EXISTS parameters;
ALTER TABLE server_game_configs DROP COLUMN IF EXISTS parameters;
ALTER TABLE sessions DROP COLUMN IF EXISTS parameters;
