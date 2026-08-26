package fake

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// ============================================================================
// Upload Record
// ============================================================================

type uploadRecordFake struct{ r *Registry }

// safeLock acquires the mutex only if we're not already inside a transaction.
func (f uploadRecordFake) safeLock() func() {
	if f.r.inTx {
		// Already holding the lock, return a no-op unlock
		return func() {}
	}
	f.r.mu.Lock()
	return f.r.mu.Unlock
}



func (f uploadRecordFake) CreateUploadRecord(ctx context.Context, record *repository.UploadRecord) (*repository.UploadRecord, error) {
	unlock := f.safeLock()
	defer unlock()

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

	if _, ok := f.r.state.UploadRecords[record.UploadID]; ok {
		return nil, fmt.Errorf("%w: upload record %s already exists", repository.ErrAlreadyExists, record.UploadID)
	}

	f.r.state.UploadRecords[record.UploadID] = *record
	return record, nil
}

func (f uploadRecordFake) GetUploadRecord(ctx context.Context, uploadID string) (*repository.UploadRecord, error) {
	unlock := f.safeLock()
	defer unlock()

	record, ok := f.r.state.UploadRecords[uploadID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &record, nil
}

func (f uploadRecordFake) ListUnconfirmedUploads(ctx context.Context, artifactIdentity, versionReference string) ([]repository.UploadRecord, error) {
	unlock := f.safeLock()
	defer unlock()

	var result []repository.UploadRecord
	for _, record := range f.r.state.UploadRecords {
		// Only include unconfirmed (allocated or uploading) records
		if record.State != repository.UploadStateAllocated && record.State != repository.UploadStateUploading {
			continue
		}
		// Apply optional filters
		if artifactIdentity != "" && record.ArtifactIdentity != artifactIdentity {
			continue
		}
		if versionReference != "" && record.VersionReference != versionReference {
			continue
		}
		result = append(result, record)
	}
	return result, nil
}

func (f uploadRecordFake) UpdateUploadState(ctx context.Context, uploadID string, newState repository.UploadState) (*repository.UploadRecord, error) {
	unlock := f.safeLock()
	defer unlock()

	record, ok := f.r.state.UploadRecords[uploadID]
	if !ok {
		return nil, repository.ErrNotFound
	}

	record.State = newState
	record.StateChangedAt = time.Now().UTC()
	f.r.state.UploadRecords[uploadID] = record
	return &record, nil
}

// ============================================================================
// Blob Record
// ============================================================================

type blobRecordFake struct{ r *Registry }

// safeLock acquires the mutex only if we're not already inside a transaction.
func (f blobRecordFake) safeLock() func() {
	if f.r.inTx {
		// Already holding the lock, return a no-op unlock
		return func() {}
	}
	f.r.mu.Lock()
	return f.r.mu.Unlock
}

func (f blobRecordFake) CreateBlobRecord(ctx context.Context, record *repository.BlobRecord) (*repository.BlobRecord, error) {
	unlock := f.safeLock()
	defer unlock()

	// Check for duplicate three-tuple key
	for _, existing := range f.r.state.BlobRecords {
		if existing.UncompressedContentDigest == record.UncompressedContentDigest &&
			existing.StoredEncoding == record.StoredEncoding &&
			existing.ContentType == record.ContentType {
			return nil, fmt.Errorf("%w: blob %s/%s/%s already exists", repository.ErrAlreadyExists, record.UncompressedContentDigest, record.StoredEncoding, record.ContentType)
		}
	}

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

	f.r.state.BlobRecords[record.BlobID] = *record
	return record, nil
}

func (f blobRecordFake) GetBlobRecordByDigest(ctx context.Context, digest, encoding, contentType string) (*repository.BlobRecord, error) {
	unlock := f.safeLock()
	defer unlock()

	for _, record := range f.r.state.BlobRecords {
		if record.UncompressedContentDigest == digest &&
			record.StoredEncoding == encoding &&
			record.ContentType == contentType {
			return &record, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f blobRecordFake) GetBlobRecord(ctx context.Context, blobID string) (*repository.BlobRecord, error) {
	unlock := f.safeLock()
	defer unlock()

	record, ok := f.r.state.BlobRecords[blobID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &record, nil
}

func (f blobRecordFake) UpdateBlobConfirmation(ctx context.Context, blobID string, newState repository.BlobConfirmationState) (*repository.BlobRecord, error) {
	unlock := f.safeLock()
	defer unlock()

	record, ok := f.r.state.BlobRecords[blobID]
	if !ok {
		return nil, repository.ErrNotFound
	}

	record.ConfirmationState = newState
	record.ConfirmationChangedAt = time.Now().UTC()
	f.r.state.BlobRecords[blobID] = record
	return &record, nil
}

// ============================================================================
// Blob Version
// ============================================================================

type blobVersionFake struct{ r *Registry }

// safeLock acquires the mutex only if we're not already inside a transaction.
func (f blobVersionFake) safeLock() func() {
	if f.r.inTx {
		// Already holding the lock, return a no-op unlock
		return func() {}
	}
	f.r.mu.Lock()
	return f.r.mu.Unlock
}

func blobVersionKey(blobID, artifactID string) string {
	return blobID + ":" + artifactID
}

func (f blobVersionFake) CreateBlobVersion(ctx context.Context, blobVersion *repository.BlobVersion) error {
	unlock := f.safeLock()
	defer unlock()

	key := blobVersionKey(blobVersion.BlobID, blobVersion.ArtifactID)
	if _, ok := f.r.state.BlobVersions[key]; ok {
		return fmt.Errorf("%w: blob version already exists: blob %s artifact %s", repository.ErrAlreadyExists, blobVersion.BlobID, blobVersion.ArtifactID)
	}

	f.r.state.BlobVersions[key] = *blobVersion
	return nil
}

func (f blobVersionFake) CountBlobVersionReferences(ctx context.Context, blobID string) (int32, error) {
	unlock := f.safeLock()
	defer unlock()

	count := 0
	for _, bv := range f.r.state.BlobVersions {
		if bv.BlobID == blobID {
			count++
		}
	}
	return int32(count), nil
}

func (f blobVersionFake) ListVersionsForBlob(ctx context.Context, blobID string) ([]string, error) {
	unlock := f.safeLock()
	defer unlock()

	var artifactIDs []string
	for _, bv := range f.r.state.BlobVersions {
		if bv.BlobID == blobID {
			artifactIDs = append(artifactIDs, bv.ArtifactID)
		}
	}
	return artifactIDs, nil
}

// ============================================================================
// Stored Object Key
// ============================================================================

type storedObjectKeyFake struct{ r *Registry }

// safeLock acquires the mutex only if we're not already inside a transaction.
func (f storedObjectKeyFake) safeLock() func() {
	if f.r.inTx {
		// Already holding the lock, return a no-op unlock
		return func() {}
	}
	f.r.mu.Lock()
	return f.r.mu.Unlock
}

func (f storedObjectKeyFake) CreateStoredObjectKey(ctx context.Context, key *repository.StoredObjectKey) (*repository.StoredObjectKey, error) {
	unlock := f.safeLock()
	defer unlock()

	// Check for duplicate (artifact_id, variant_key) pair
	for _, existing := range f.r.state.StoredObjectKeys {
		if existing.ArtifactID == key.ArtifactID && existing.VariantKey == key.VariantKey {
			return nil, fmt.Errorf("%w: stored object key already exists for artifact %s variant %s", repository.ErrAlreadyExists, key.ArtifactID, key.VariantKey)
		}
	}

	if key.StoredObjectKeyID == "" {
		key.StoredObjectKeyID = uuid.NewString()
	}
	if key.RecordedAt.IsZero() {
		key.RecordedAt = time.Now().UTC()
	}

	f.r.state.StoredObjectKeys[key.StoredObjectKeyID] = *key
	return key, nil
}

func (f storedObjectKeyFake) GetStoredObjectKey(ctx context.Context, artifactID, variantKey string) (*repository.StoredObjectKey, error) {
	unlock := f.safeLock()
	defer unlock()

	for _, key := range f.r.state.StoredObjectKeys {
		if key.ArtifactID == artifactID && key.VariantKey == variantKey {
			return &key, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f storedObjectKeyFake) ListStoredObjectKeysForArtifact(ctx context.Context, artifactID string) ([]repository.StoredObjectKey, error) {
	unlock := f.safeLock()
	defer unlock()

	var keys []repository.StoredObjectKey
	for _, key := range f.r.state.StoredObjectKeys {
		if key.ArtifactID == artifactID {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
