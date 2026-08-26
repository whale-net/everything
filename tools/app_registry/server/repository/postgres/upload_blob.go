package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// ============================================================================
// UploadRecordRepository
// ============================================================================

type uploadRecordRepo struct{ ex dbtx }

const uploadRecordColumns = `upload_id, object_key, artifact_kind, artifact_identity, version_reference, requesting_principal, issued_at, state, state_changed_at, attribution_principal, workflow_run_id, attribution_timestamp`

func scanUploadRecord(row pgx.Row) (repository.UploadRecord, error) {
	var u repository.UploadRecord
	if err := row.Scan(&u.UploadID, &u.ObjectKey, &u.ArtifactKind, &u.ArtifactIdentity, &u.VersionReference, &u.RequestingPrincipal, &u.IssuedAt, &u.State, &u.StateChangedAt, &u.AttributionPrincipal, &u.WorkflowRunID, &u.AttributionTimestamp); err != nil {
		return repository.UploadRecord{}, err
	}
	return u, nil
}

func (r *uploadRecordRepo) CreateUploadRecord(ctx context.Context, record *repository.UploadRecord) (*repository.UploadRecord, error) {
	if record.UploadID == "" {
		record.UploadID = uuid.NewString()
	}
	if record.IssuedAt.IsZero() {
		record.IssuedAt = time.Now().UTC()
	}
	if record.StateChangedAt.IsZero() {
		record.StateChangedAt = time.Now().UTC()
	}
	if record.AttributionTimestamp.IsZero() {
		record.AttributionTimestamp = time.Now().UTC()
	}
	if record.State == "" {
		record.State = repository.UploadStateAllocated
	}

	if _, err := r.ex.Exec(ctx, `
		INSERT INTO upload_record (upload_id, object_key, artifact_kind, artifact_identity, version_reference, requesting_principal, issued_at, state, state_changed_at, attribution_principal, workflow_run_id, attribution_timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		record.UploadID, record.ObjectKey, record.ArtifactKind, record.ArtifactIdentity, record.VersionReference, record.RequestingPrincipal, record.IssuedAt, record.State, record.StateChangedAt, record.AttributionPrincipal, record.WorkflowRunID, record.AttributionTimestamp); err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("upload record %s already exists", record.UploadID)); ok {
			return nil, de
		}
		return nil, fmt.Errorf("create upload record %s: %w", record.UploadID, err)
	}
	return record, nil
}

func (r *uploadRecordRepo) GetUploadRecord(ctx context.Context, uploadID string) (*repository.UploadRecord, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+uploadRecordColumns+` FROM upload_record WHERE upload_id = $1`, uploadID)
	u, err := scanUploadRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (r *uploadRecordRepo) ListUnconfirmedUploads(ctx context.Context, artifactIdentity, versionReference string) ([]repository.UploadRecord, error) {
	query := `SELECT ` + uploadRecordColumns + ` FROM upload_record WHERE state IN ('allocated', 'uploading')`
	args := []interface{}{}
	argIndex := 1

	if artifactIdentity != "" {
		query += fmt.Sprintf(` AND artifact_identity = $%d`, argIndex)
		args = append(args, artifactIdentity)
		argIndex++
	}
	if versionReference != "" {
		query += fmt.Sprintf(` AND version_reference = $%d`, argIndex)
		args = append(args, versionReference)
		argIndex++
	}

	query += ` ORDER BY issued_at ASC`

	rows, err := r.ex.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var uploads []repository.UploadRecord
	for rows.Next() {
		u, err := scanUploadRecord(rows)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return uploads, nil
}

func (r *uploadRecordRepo) UpdateUploadState(ctx context.Context, uploadID string, newState repository.UploadState) (*repository.UploadRecord, error) {
	now := time.Now().UTC()
	result, err := r.ex.Exec(ctx, `
		UPDATE upload_record SET state = $1, state_changed_at = $2 WHERE upload_id = $3`,
		newState, now, uploadID)
	if err != nil {
		return nil, fmt.Errorf("update upload state %s to %s: %w", uploadID, newState, err)
	}
	if result.RowsAffected() == 0 {
		return nil, repository.ErrNotFound
	}
	return r.GetUploadRecord(ctx, uploadID)
}

// ============================================================================
// BlobRecordRepository
// ============================================================================

type blobRecordRepo struct{ ex dbtx }

const blobRecordColumns = `blob_id, uncompressed_content_digest, stored_encoding, content_type, confirmation_state, created_at, confirmation_changed_at`

func scanBlobRecord(row pgx.Row) (repository.BlobRecord, error) {
	var b repository.BlobRecord
	if err := row.Scan(&b.BlobID, &b.UncompressedContentDigest, &b.StoredEncoding, &b.ContentType, &b.ConfirmationState, &b.CreatedAt, &b.ConfirmationChangedAt); err != nil {
		return repository.BlobRecord{}, err
	}
	return b, nil
}

func (r *blobRecordRepo) CreateBlobRecord(ctx context.Context, record *repository.BlobRecord) (*repository.BlobRecord, error) {
	if record.BlobID == "" {
		record.BlobID = uuid.NewString()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.ConfirmationChangedAt.IsZero() {
		record.ConfirmationChangedAt = time.Now().UTC()
	}
	if record.ConfirmationState == "" {
		record.ConfirmationState = repository.BlobConfirmationStateUnconfirmed
	}

	if _, err := r.ex.Exec(ctx, `
		INSERT INTO blob_record (blob_id, uncompressed_content_digest, stored_encoding, content_type, confirmation_state, created_at, confirmation_changed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		record.BlobID, record.UncompressedContentDigest, record.StoredEncoding, record.ContentType, record.ConfirmationState, record.CreatedAt, record.ConfirmationChangedAt); err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("blob already exists: %s/%s/%s", record.UncompressedContentDigest, record.StoredEncoding, record.ContentType)); ok {
			return nil, de
		}
		return nil, fmt.Errorf("create blob record: %w", err)
	}
	return record, nil
}

func (r *blobRecordRepo) GetBlobRecordByDigest(ctx context.Context, digest, encoding, contentType string) (*repository.BlobRecord, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+blobRecordColumns+` FROM blob_record WHERE uncompressed_content_digest = $1 AND stored_encoding = $2 AND content_type = $3`, digest, encoding, contentType)
	b, err := scanBlobRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &b, nil
}

func (r *blobRecordRepo) GetBlobRecord(ctx context.Context, blobID string) (*repository.BlobRecord, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+blobRecordColumns+` FROM blob_record WHERE blob_id = $1`, blobID)
	b, err := scanBlobRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &b, nil
}

func (r *blobRecordRepo) UpdateBlobConfirmation(ctx context.Context, blobID string, newState repository.BlobConfirmationState) (*repository.BlobRecord, error) {
	now := time.Now().UTC()
	result, err := r.ex.Exec(ctx, `
		UPDATE blob_record SET confirmation_state = $1, confirmation_changed_at = $2 WHERE blob_id = $3`,
		newState, now, blobID)
	if err != nil {
		return nil, fmt.Errorf("update blob confirmation %s to %s: %w", blobID, newState, err)
	}
	if result.RowsAffected() == 0 {
		return nil, repository.ErrNotFound
	}
	return r.GetBlobRecord(ctx, blobID)
}

// ============================================================================
// BlobVersionRepository
// ============================================================================

type blobVersionRepo struct{ ex dbtx }

func (r *blobVersionRepo) CreateBlobVersion(ctx context.Context, blobVersion *repository.BlobVersion) error {
	if _, err := r.ex.Exec(ctx, `
		INSERT INTO blob_version (blob_id, artifact_id) VALUES ($1, $2)`,
		blobVersion.BlobID, blobVersion.ArtifactID); err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("blob version already exists: blob %s artifact %s", blobVersion.BlobID, blobVersion.ArtifactID)); ok {
			return de
		}
		return fmt.Errorf("create blob version: %w", err)
	}
	return nil
}

func (r *blobVersionRepo) CountBlobVersionReferences(ctx context.Context, blobID string) (int32, error) {
	var count int32
	row := r.ex.QueryRow(ctx, `SELECT COUNT(*) FROM blob_version WHERE blob_id = $1`, blobID)
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count blob version references for %s: %w", blobID, err)
	}
	return count, nil
}

func (r *blobVersionRepo) ListVersionsForBlob(ctx context.Context, blobID string) ([]string, error) {
	rows, err := r.ex.Query(ctx, `SELECT artifact_id FROM blob_version WHERE blob_id = $1 ORDER BY artifact_id ASC`, blobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifactIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		artifactIDs = append(artifactIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return artifactIDs, nil
}

// ============================================================================
// StoredObjectKeyRepository
// ============================================================================

type storedObjectKeyRepo struct{ ex dbtx }

const storedObjectKeyColumns = `stored_object_key_id, artifact_id, variant_key, object_key, recorded_at`

func scanStoredObjectKey(row pgx.Row) (repository.StoredObjectKey, error) {
	var k repository.StoredObjectKey
	if err := row.Scan(&k.StoredObjectKeyID, &k.ArtifactID, &k.VariantKey, &k.ObjectKey, &k.RecordedAt); err != nil {
		return repository.StoredObjectKey{}, err
	}
	return k, nil
}

func (r *storedObjectKeyRepo) CreateStoredObjectKey(ctx context.Context, key *repository.StoredObjectKey) (*repository.StoredObjectKey, error) {
	if key.StoredObjectKeyID == "" {
		key.StoredObjectKeyID = uuid.NewString()
	}
	if key.RecordedAt.IsZero() {
		key.RecordedAt = time.Now().UTC()
	}

	if _, err := r.ex.Exec(ctx, `
		INSERT INTO stored_object_key (stored_object_key_id, artifact_id, variant_key, object_key, recorded_at)
		VALUES ($1, $2, $3, $4, $5)`,
		key.StoredObjectKeyID, key.ArtifactID, key.VariantKey, key.ObjectKey, key.RecordedAt); err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("stored object key already exists for artifact %s variant %s", key.ArtifactID, key.VariantKey)); ok {
			return nil, de
		}
		return nil, fmt.Errorf("create stored object key: %w", err)
	}
	return key, nil
}

func (r *storedObjectKeyRepo) GetStoredObjectKey(ctx context.Context, artifactID, variantKey string) (*repository.StoredObjectKey, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+storedObjectKeyColumns+` FROM stored_object_key WHERE artifact_id = $1 AND variant_key = $2`, artifactID, variantKey)
	k, err := scanStoredObjectKey(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &k, nil
}

func (r *storedObjectKeyRepo) ListStoredObjectKeysForArtifact(ctx context.Context, artifactID string) ([]repository.StoredObjectKey, error) {
	rows, err := r.ex.Query(ctx, `SELECT `+storedObjectKeyColumns+` FROM stored_object_key WHERE artifact_id = $1 ORDER BY recorded_at ASC`, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []repository.StoredObjectKey
	for rows.Next() {
		k, err := scanStoredObjectKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}
