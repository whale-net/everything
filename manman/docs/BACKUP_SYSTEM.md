# Backup & Restore System

## Overview

The ManManV2 backup system provides S3-based backup and restore functionality for game save data. Backups are created from GSC data directories and stored as compressed tarballs in S3.

## Architecture

### Current Status (Phase 5)

The backup API infrastructure is complete:
- ✓ Database schema for backup metadata
- ✓ Protobuf definitions for backup RPCs
- ✓ Repository layer for backup persistence
- ✓ API handlers for backup management
- ✓ S3 integration for backup storage

### Pending (Phase 4 - Integration)

Actual backup creation/restoration requires host manager integration:
- ⏳ Backup creation: Host manager creates tarball and uploads to S3
- ⏳ Restore: Host manager downloads and extracts tarball to GSC directory
- ⏳ RabbitMQ command/response flow for backup operations

## Database Schema

```sql
CREATE TABLE backups (
    backup_id BIGSERIAL PRIMARY KEY,
    session_id BIGINT REFERENCES sessions(session_id),
    server_game_config_id BIGINT REFERENCES server_game_configs(sgc_id),
    s3_url TEXT NOT NULL,  -- S3 URL for backup tarball
    size_bytes BIGINT NOT NULL,
    description TEXT,
    created_at TIMESTAMP NOT NULL
);

-- Sessions can track which backup they were restored from
ALTER TABLE sessions
ADD COLUMN restored_from_backup_id BIGINT REFERENCES backups(backup_id);
```

## API

### gRPC Methods

```protobuf
service ManManAPI {
  rpc CreateBackup(CreateBackupRequest) returns (CreateBackupResponse);
  rpc ListBackups(ListBackupsRequest) returns (ListBackupsResponse);
  rpc GetBackup(GetBackupRequest) returns (GetBackupResponse);
  rpc DeleteBackup(DeleteBackupRequest) returns (DeleteBackupResponse);
}
```

### Example: Create Backup

```bash
grpcurl -plaintext \
  -d '{"session_id": 123, "description": "Before update"}' \
  localhost:50051 \
  manman.v1.ManManAPI/CreateBackup
```

**Status:** Returns `UNIMPLEMENTED` until Phase 4 integration is complete.

### Example: List Backups

```bash
grpcurl -plaintext \
  -d '{"server_game_config_id": 42}' \
  localhost:50051 \
  manman.v1.ManManAPI/ListBackups
```

### Example: Delete Backup

```bash
grpcurl -plaintext \
  -d '{"backup_id": 5}' \
  localhost:50051 \
  manman.v1.ManManAPI/DeleteBackup
```

Deletes both the database record and the S3 file.

## S3 Storage Structure

Backups are stored with the following key format:

```
backups/{sgc_id}/{backup_id}.tar.gz
```

### Example

```
s3://manman-logs/backups/42/1.tar.gz
s3://manman-logs/backups/42/2.tar.gz
s3://manman-logs/67/3.tar.gz
```

## Backup Contents

Each backup tarball contains:

```
game/              # Game server data directory
  world/           # Example: Minecraft world files
  config/          # Game-specific config files
  ...
```

The wrapper's state and logs are NOT included (those are ephemeral).

## Phase 4 Integration Plan

### Backup Creation Flow

1. **API**: Receives `CreateBackup(session_id)` request
2. **API**: Creates pending backup record in database
3. **API**: Sends backup command to host manager via RabbitMQ:
   ```json
   {
     "command": "backup_session",
     "session_id": 123,
     "backup_id": 5,
     "s3_key": "backups/42/5.tar.gz"
   }
   ```
4. **Host Manager**:
   - Creates tarball of `/data/gsc-{env}-{sgc_id}`
   - Uploads to S3 using provided key
   - Reports success/failure + size via RabbitMQ
5. **API**: Updates backup record with S3 URL and size

### Restore Flow

1. **API**: Receives `StartSession` with `restore_from_backup_id`
2. **API**: Validates backup exists and belongs to same SGC
3. **API**: Creates session with `restored_from_backup_id` set
4. **API**: Sends start command to host manager with restore info:
   ```json
   {
     "command": "start_session",
     "session_id": 456,
     "restore_from": {
       "backup_id": 5,
       "s3_url": "s3://bucket/backups/42/5.tar.gz"
     }
   }
   ```
5. **Host Manager**:
   - Downloads backup tarball from S3
   - Extracts to `/data/gsc-{env}-{sgc_id}`
   - Starts game server normally

## Error Handling

### Backup Failures

- **Tarball creation fails**: Host manager reports error, backup record marked as failed
- **S3 upload fails**: Retry with exponential backoff, eventually fail
- **Session not found**: API returns `NOT_FOUND`

### Restore Failures

- **Backup not found**: API returns `NOT_FOUND` before sending command
- **SGC mismatch**: API returns `INVALID_ARGUMENT` (can't restore Minecraft save to Valheim)
- **Download fails**: Host manager retries, eventually fails session startup
- **Extraction fails**: Host manager cleans up partial extraction, fails session

## Limitations

- Backups are immutable (no versioning beyond separate backup records)
- No automatic backup scheduling (must be triggered manually)
- No compression level configuration (uses default gzip)
- No incremental backups (always full backups)

## Future Enhancements

- **Automatic backups**: Trigger backup on session end or schedule
- **Backup retention policy**: Auto-delete old backups after N days
- **Compression options**: Allow choosing compression algorithm/level
- **Incremental backups**: Only backup changed files (rsync-style)
- **Backup verification**: Verify tarball integrity after upload
