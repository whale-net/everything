# Backup Restore System — Implementation Plan

**Status**: 🔲 Not Started
**Feature**: Restore game data from existing backups via SGC danger-zone UI

---

## Overview

Add the ability to restore an SGC's volume data from a previously completed backup. The system creates a safety backup before every restore, prevents restores while a session is active, and prevents session starts while a restore is in progress. Phase 2 adds user-uploaded backup support.

### Design Decisions

- **Restore initiated from SGC detail page only** — prevents accidental cross-SGC mistakes from a global backup list.
- **Safety backup before restore** — a new backup is created automatically, tagged `backup_type='pre-restore'` with `source_backup_id` linking to the backup being restored.
- **`restores` table** — dedicated table with a partial unique constraint enforcing at most one active restore per SGC. Does not conflate with sessions.
- **Session blocking** — restore is blocked if any session is running. During an active restore, new session starts are blocked unless `force=true` (which acts as an abort/override).
- **Fire-and-forget command** — restore command follows the same pattern as backup: publish to RabbitMQ, host reports status back asynchronously.
- **Phase 2: User uploads** — uploaded .tar.gz files go through the same backup backend, tracked with `source_type='user-provided'` vs `'scheduled'`.

---

## Task 1: Database Migration — Restore Tracking & Backup Types

**Goal**: Schema supports restore operations and backup type metadata.

Files:
- `migrate/migrations/031_restore_system.up.sql`
- `migrate/migrations/031_restore_system.down.sql`
- `models.go` — add `Restore` struct, add `BackupType` and `SourceBackupID` fields to `Backup`

Schema:
```sql
-- restores table
CREATE TABLE restores (
    restore_id        BIGSERIAL PRIMARY KEY,
    sgc_id            BIGINT NOT NULL REFERENCES server_game_configs(sgc_id),
    backup_id         BIGINT NOT NULL REFERENCES backups(backup_id),
    safety_backup_id  BIGINT REFERENCES backups(backup_id),
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    error_message     TEXT,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at      TIMESTAMP
);

-- Enforce at most one active restore per SGC
CREATE UNIQUE INDEX idx_restores_active_sgc
    ON restores (sgc_id) WHERE status IN ('pending', 'running');

-- backups table additions
ALTER TABLE backups
    ADD COLUMN backup_type      TEXT NOT NULL DEFAULT 'scheduled'
        CHECK (backup_type IN ('scheduled', 'manual', 'pre-restore')),
    ADD COLUMN source_backup_id BIGINT REFERENCES backups(backup_id);
```

Validation:
- Run migration up/down
- Verify unique constraint blocks second active restore for same SGC
- Verify constraint allows multiple completed/failed restores

---

## Task 2: RabbitMQ Restore Command & Host Handler

**Goal**: Host manager can receive restore commands, download from S3, and extract to volume path.

Files:
- `host/rmq/messages.go` — add `RestoreCommand`, `RestoreStatusUpdate`
- `host/rmq/consumer.go` — add routing key `command.host.{id}.restore`, register handler
- `host/restore.go` — implement `HandleRestore`
- `api/handlers/command_publisher.go` — add `PublishRestore`

`RestoreCommand` fields: `restore_id`, `sgc_id`, `s3_key`, `presigned_url` (GET), `volume_host_path`, `backup_path`, `created_at`

Host handler logic:
1. Download tar.gz from presigned URL to temp file
2. Extract to `{internalDataDir}/sgc[-{env}]-{sgcID}/{volumeHostSubpath}/{backupPath}`
3. Publish `RestoreStatusUpdate` (completed/failed)

Validation:
- Unit test with mock presigned URL

---

## Task 3: Restore Repository & Status Handler

**Goal**: CRUD for restore records, processor handles status updates from host.

Files:
- `api/repository/postgres/restore.go` — `RestoreRepository` (Create, Get, GetActiveBySGCID, UpdateStatus)
- `api/repository/repository.go` — add `RestoreRepository` interface, add to `Repository` struct
- `processor/handlers/restore_status.go` — handle `status.restore.*` messages
- `processor/main.go` — register handler, wire repository

Validation:
- Create restore, verify `GetActiveBySGCID` returns it
- Update status to completed, verify `GetActiveBySGCID` returns nil

---

## Task 4: RestoreBackup gRPC RPC

**Goal**: API endpoint orchestrates safety backup → restore command lifecycle.

Files:
- `protos/api.proto` — add `RestoreBackup` RPC, request/response messages
- `api/handlers/restore.go` — implement RPC:
  1. Validate backup exists with S3 URL
  2. Check no active session for SGC (`IsActive()`)
  3. Check no active restore for SGC
  4. Trigger safety backup (create backup record with `backup_type='pre-restore'`, `source_backup_id`, publish backup command, poll for completion with timeout)
  5. Create restore record (status=pending)
  6. Generate presigned GET URL for source backup
  7. Publish `RestoreCommand`
  8. Return restore_id + safety_backup_id
- `api/handlers/api.go` — wire handler

Validation:
- Call RPC with valid backup → verify safety backup created, restore record created, command published
- Call RPC with active session → verify error returned
- Call RPC with active restore → verify error returned

---

## Task 5: Session Start Validation

**Goal**: Block session creation during active restore (force start bypasses).

Files:
- `api/handlers/api.go` — update `CreateSession`:
  - After existing active-session check, call `restoreRepo.GetActiveBySGCID(sgcID)`
  - If active restore found and `force=false`: return error "Restore in progress for this SGC. Use force start to override."
  - If `force=true`: log warning, proceed

Validation:
- Create active restore → session start without force fails
- Create active restore → session start with force succeeds

---

## Task 6: SGC Danger-Zone UI — Restore from Backup

**Goal**: Users can select a completed backup and initiate restore from the SGC page.

Files:
- `ui/pages/sgc_detail.templ` — add restore section in danger zone:
  - List completed backups (timestamp, size, description, type badge)
  - "Restore" button per backup with Alpine.js confirmation dialog
  - Show active restore status if one exists
- `ui/handlers_restore.go` — `handleRestoreBackup`: parse form, call gRPC `RestoreBackup`, redirect with flash
- `ui/main.go` — register `POST /sgc/{sgc_id}/restore`
- `ui/grpc_client.go` — add `RestoreBackup` wrapper

Validation:
- Manual: SGC page shows backups in danger zone, confirmation works, restore initiates, error shown if session active

---

## Task 7 (Phase 2): User-Uploaded Backup Support — Schema

**Goal**: Track backup source type for user-provided vs system-generated backups.

Files:
- `migrate/migrations/032_backup_source_type.up.sql`:
  ```sql
  ALTER TABLE backups ADD COLUMN source_type TEXT NOT NULL DEFAULT 'scheduled'
      CHECK (source_type IN ('scheduled', 'manual', 'user-provided'));
  -- Backfill
  UPDATE backups SET source_type = 'manual' WHERE backup_config_id IS NULL;
  ```
- `migrate/migrations/032_backup_source_type.down.sql`
- `models.go` — add `SourceType` field to `Backup`

---

## Task 8 (Phase 2): Upload Backup Endpoint & UI

**Goal**: Users can upload a .tar.gz file that becomes a restorable backup.

Files:
- `ui/pages/sgc_detail.templ` — add upload form in backups section (file input, volume selector, description)
- `ui/handlers_restore.go` — `handleUploadBackup`:
  1. Parse multipart form
  2. Validate .tar.gz (check magic bytes `1f 8b`)
  3. TODO: Check if tar contains a folder matching the backup_path name vs folder contents — warn user if folder structure detected
  4. Upload to S3: `backups/{sgc_id}/user-uploads/{timestamp}.tar.gz`
  5. Create backup record: `source_type='user-provided'`, `backup_type='manual'`, `status='completed'`
  6. Redirect with success flash
- `ui/main.go` — register `POST /sgc/{sgc_id}/upload-backup`

Validation:
- Upload valid .tar.gz → appears in backup list, can be restored
- Upload non-.tar.gz → error shown
