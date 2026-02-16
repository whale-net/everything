-- Revert log_references comment to local file path
COMMENT ON COLUMN log_references.file_path IS 'Local file path for the log file';
