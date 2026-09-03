-- Reverse migration 002: drop views, then tables in FK-safe order
-- (children before parents). No data-preservation attempted, matching
-- migration 001's down convention.

DROP VIEW IF EXISTS v_prediction_vs_outcome;
DROP VIEW IF EXISTS v_current_verdict;

DROP TABLE IF EXISTS mcp_idempotency;
DROP TABLE IF EXISTS video_schedule_match;
DROP TABLE IF EXISTS video_metrics;
DROP TABLE IF EXISTS synced_video;
DROP TABLE IF EXISTS schedule_entry;
DROP TABLE IF EXISTS pacing_policy;
DROP TABLE IF EXISTS verdict_citation;
DROP TABLE IF EXISTS viability_verdict;
DROP TABLE IF EXISTS research_note;
DROP TABLE IF EXISTS idea;
