ALTER TABLE backups ADD COLUMN deleted_at TIMESTAMP;
ALTER TABLE backup_configs ADD COLUMN deleted_at TIMESTAMP;
