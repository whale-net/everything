-- Reverse migration 035: drop the reported-manifest snapshot (FR49).

DROP INDEX IF EXISTS idx_board_manifest_report_entry_hw_key;
DROP INDEX IF EXISTS idx_board_manifest_report_entry_board_id;
DROP TABLE IF EXISTS board_manifest_report_entry;
DROP TABLE IF EXISTS board_manifest_report;
