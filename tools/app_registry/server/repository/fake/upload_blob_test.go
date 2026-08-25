package fake

import (
	"errors"
	"context"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// TestFake_UploadRecord_Create_And_Retrieve tests basic upload record operations on the fake registry.
func TestFake_UploadRecord_Create_And_Retrieve(t *testing.T) {
	reg := New()

	ur := &repository.UploadRecord{
		ObjectKey:            "s3://bucket/widget-v1.0.0.tar.gz",
		ArtifactKind:         repository.ArtifactKindImage,
		ArtifactIdentity:     "app-123",
		VersionReference:     "1.0.0",
		RequestingPrincipal:  "user:alice@example.com",
		State:                repository.UploadStateAllocated,
		AttributionPrincipal: "workflow:ci@example.com",
		WorkflowRunID:        "run-abc123",
	}

	created, err := reg.UploadRecords().CreateUploadRecord(context.Background(), ur)
	if err != nil {
		t.Fatalf("CreateUploadRecord: %v", err)
	}
	if created.UploadID == "" {
		t.Fatal("expected UploadID to be generated")
	}

	retrieved, err := reg.UploadRecords().GetUploadRecord(context.Background(), created.UploadID)
	if err != nil {
		t.Fatalf("GetUploadRecord: %v", err)
	}

	if retrieved.ObjectKey != ur.ObjectKey {
		t.Errorf("ObjectKey mismatch: expected %s, got %s", ur.ObjectKey, retrieved.ObjectKey)
	}
}

// TestFake_UploadRecord_StateTransition tests state transitions.
func TestFake_UploadRecord_StateTransition(t *testing.T) {
	reg := New()

	ur := &repository.UploadRecord{
		ObjectKey:            "s3://bucket/widget-v1.0.0.tar.gz",
		ArtifactKind:         repository.ArtifactKindImage,
		ArtifactIdentity:     "app-123",
		VersionReference:     "1.0.0",
		RequestingPrincipal:  "user:alice@example.com",
		State:                repository.UploadStateAllocated,
		AttributionPrincipal: "workflow:ci@example.com",
		WorkflowRunID:        "run-abc123",
	}

	created, err := reg.UploadRecords().CreateUploadRecord(context.Background(), ur)
	if err != nil {
		t.Fatalf("CreateUploadRecord: %v", err)
	}

	if err := reg.UploadRecords().UpdateUploadState(context.Background(), created.UploadID, repository.UploadStateUploading); err != nil {
		t.Fatalf("UpdateUploadState: %v", err)
	}

	retrieved, err := reg.UploadRecords().GetUploadRecord(context.Background(), created.UploadID)
	if err != nil {
		t.Fatalf("GetUploadRecord: %v", err)
	}

	if retrieved.State != repository.UploadStateUploading {
		t.Errorf("State mismatch: expected uploading, got %s", retrieved.State)
	}
}

// TestFake_UploadRecord_UnconfirmedQueryable tests that unconfirmed records are queryable.
func TestFake_UploadRecord_UnconfirmedQueryable(t *testing.T) {
	reg := New()

	appID := "app-123"

	// Create records with different states
	for i, state := range []repository.UploadState{
		repository.UploadStateAllocated,
		repository.UploadStateUploading,
		repository.UploadStateConfirmed,
	} {
		ur := &repository.UploadRecord{
			ObjectKey:            "s3://bucket/widget-" + string(rune(i)),
			ArtifactKind:         repository.ArtifactKindImage,
			ArtifactIdentity:     appID,
			VersionReference:     "1.0.0",
			RequestingPrincipal:  "user:test@example.com",
			State:                state,
			AttributionPrincipal: "workflow:ci@example.com",
			WorkflowRunID:        "run-" + string(rune(i)),
		}
		if _, err := reg.UploadRecords().CreateUploadRecord(context.Background(), ur); err != nil {
			t.Fatalf("CreateUploadRecord: %v", err)
		}
	}

	unconfirmed, err := reg.UploadRecords().ListUnconfirmedUploads(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListUnconfirmedUploads: %v", err)
	}

	if len(unconfirmed) != 2 {
		t.Errorf("Expected 2 unconfirmed records, got %d", len(unconfirmed))
	}

	for _, ur := range unconfirmed {
		if ur.State != repository.UploadStateAllocated && ur.State != repository.UploadStateUploading {
			t.Errorf("Expected unconfirmed states, got %s", ur.State)
		}
	}
}

// TestFake_UploadRecord_UnconfirmedNoBlock tests FR-10: unconfirmed records don't block retries.
func TestFake_UploadRecord_UnconfirmedNoBlock(t *testing.T) {
	reg := New()

	appID := "app-123"
	version := "1.0.0"

	// Create unconfirmed upload
	unconfirmed := &repository.UploadRecord{
		ObjectKey:            "s3://bucket/widget.tar.gz",
		ArtifactKind:         repository.ArtifactKindImage,
		ArtifactIdentity:     appID,
		VersionReference:     version,
		RequestingPrincipal:  "user:test@example.com",
		State:                repository.UploadStateAllocated,
		AttributionPrincipal: "workflow:ci@example.com",
		WorkflowRunID:        "run-1",
	}

	if _, err := reg.UploadRecords().CreateUploadRecord(context.Background(), unconfirmed); err != nil {
		t.Fatalf("Create unconfirmed: %v", err)
	}

	// Retry should succeed
	retry := &repository.UploadRecord{
		ObjectKey:            "s3://bucket/widget-v1.0.0.tar.gz",
		ArtifactKind:         repository.ArtifactKindImage,
		ArtifactIdentity:     appID,
		VersionReference:     version,
		RequestingPrincipal:  "user:test@example.com",
		State:                repository.UploadStateAllocated,
		AttributionPrincipal: "workflow:ci@example.com",
		WorkflowRunID:        "run-2",
	}

	retryRecord, err := reg.UploadRecords().CreateUploadRecord(context.Background(), retry)
	if err != nil {
		t.Fatalf("Retry after unconfirmed should not fail: %v", err)
	}

	if retryRecord.UploadID == "" {
		t.Fatal("Retry record should have been created")
	}

	unconfirmedList, err := reg.UploadRecords().ListUnconfirmedUploads(context.Background(), appID, version)
	if err != nil {
		t.Fatalf("ListUnconfirmedUploads: %v", err)
	}

	if len(unconfirmedList) != 2 {
		t.Errorf("Expected 2 unconfirmed records, got %d", len(unconfirmedList))
	}
}

// TestFake_BlobRecord_ThreeTupleUniqueness tests FR-61: blobs keyed on three-tuple.
func TestFake_BlobRecord_ThreeTupleUniqueness(t *testing.T) {
	reg := New()

	digest := "sha256:abc123"
	encoding1 := "gzip"
	encoding2 := "zstd"
	contentType := "application/octet-stream"

	blob1 := &repository.BlobRecord{
		UncompressedContentDigest: digest,
		StoredEncoding:            encoding1,
		ContentType:               contentType,
		ConfirmationState:         repository.BlobConfirmationStateConfirmed,
	}

	created1, err := reg.BlobRecords().CreateBlobRecord(context.Background(), blob1)
	if err != nil {
		t.Fatalf("CreateBlobRecord (blob1): %v", err)
	}

	// Different encoding should create a different blob
	blob2 := &repository.BlobRecord{
		UncompressedContentDigest: digest,
		StoredEncoding:            encoding2,
		ContentType:               contentType,
		ConfirmationState:         repository.BlobConfirmationStateConfirmed,
	}

	created2, err := reg.BlobRecords().CreateBlobRecord(context.Background(), blob2)
	if err != nil {
		t.Fatalf("CreateBlobRecord (blob2): %v", err)
	}

	if created1.BlobID == created2.BlobID {
		t.Fatal("Blobs with different encodings should have distinct IDs")
	}

	// Duplicate should fail
	duplicate := &repository.BlobRecord{
		UncompressedContentDigest: digest,
		StoredEncoding:            encoding1,
		ContentType:               contentType,
		ConfirmationState:         repository.BlobConfirmationStateConfirmed,
	}

	_, err = reg.BlobRecords().CreateBlobRecord(context.Background(), duplicate)
	if err == nil {
		t.Fatal("Expected ErrAlreadyExists for duplicate")
	}
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("Expected ErrAlreadyExists, got %v", err)
	}
}

// TestFake_BlobRecord_ContentTypeDifference tests FR-61: content_type is part of key.
func TestFake_BlobRecord_ContentTypeDifference(t *testing.T) {
	reg := New()

	digest := "sha256:xyz789"
	encoding := "gzip"
	contentType1 := "application/vnd.oci.image.config.v1+json"
	contentType2 := "application/vnd.oci.image.layer.v1.tar+gzip"

	blob1 := &repository.BlobRecord{
		UncompressedContentDigest: digest,
		StoredEncoding:            encoding,
		ContentType:               contentType1,
		ConfirmationState:         repository.BlobConfirmationStateConfirmed,
	}

	created1, err := reg.BlobRecords().CreateBlobRecord(context.Background(), blob1)
	if err != nil {
		t.Fatalf("CreateBlobRecord (contentType1): %v", err)
	}

	blob2 := &repository.BlobRecord{
		UncompressedContentDigest: digest,
		StoredEncoding:            encoding,
		ContentType:               contentType2,
		ConfirmationState:         repository.BlobConfirmationStateConfirmed,
	}

	created2, err := reg.BlobRecords().CreateBlobRecord(context.Background(), blob2)
	if err != nil {
		t.Fatalf("CreateBlobRecord (contentType2): %v", err)
	}

	if created1.BlobID == created2.BlobID {
		t.Fatal("Blobs with different content types should have distinct IDs")
	}
}

// TestFake_BlobRecord_ConfirmationState tests FR-46: confirmation state.
func TestFake_BlobRecord_ConfirmationState(t *testing.T) {
	reg := New()

	blob := &repository.BlobRecord{
		UncompressedContentDigest: "sha256:test",
		StoredEncoding:            "gzip",
		ContentType:               "application/octet-stream",
		ConfirmationState:         repository.BlobConfirmationStateUnconfirmed,
	}

	created, err := reg.BlobRecords().CreateBlobRecord(context.Background(), blob)
	if err != nil {
		t.Fatalf("CreateBlobRecord: %v", err)
	}

	if created.ConfirmationState != repository.BlobConfirmationStateUnconfirmed {
		t.Errorf("Expected unconfirmed, got %s", created.ConfirmationState)
	}

	if err := reg.BlobRecords().UpdateBlobConfirmation(context.Background(), created.BlobID, repository.BlobConfirmationStateConfirmed); err != nil {
		t.Fatalf("UpdateBlobConfirmation: %v", err)
	}

	retrieved, err := reg.BlobRecords().GetBlobRecord(context.Background(), created.BlobID)
	if err != nil {
		t.Fatalf("GetBlobRecord: %v", err)
	}

	if retrieved.ConfirmationState != repository.BlobConfirmationStateConfirmed {
		t.Errorf("Expected confirmed, got %s", retrieved.ConfirmationState)
	}
}

// TestFake_BlobVersion_ManyToOne tests FR-12: many versions can reference the same blob.
func TestFake_BlobVersion_ManyToOne(t *testing.T) {
	reg := New()

	blob := &repository.BlobRecord{
		UncompressedContentDigest: "sha256:shared",
		StoredEncoding:            "gzip",
		ContentType:               "application/octet-stream",
		ConfirmationState:         repository.BlobConfirmationStateConfirmed,
	}

	createdBlob, err := reg.BlobRecords().CreateBlobRecord(context.Background(), blob)
	if err != nil {
		t.Fatalf("CreateBlobRecord: %v", err)
	}

	// Create two blob versions pointing to the same blob
	bv1 := &repository.BlobVersion{
		BlobID:     createdBlob.BlobID,
		ArtifactID: "app-1",
	}

	bv2 := &repository.BlobVersion{
		BlobID:     createdBlob.BlobID,
		ArtifactID: "app-2",
	}

	if err := reg.BlobVersions().CreateBlobVersion(context.Background(), bv1); err != nil {
		t.Fatalf("CreateBlobVersion (bv1): %v", err)
	}

	if err := reg.BlobVersions().CreateBlobVersion(context.Background(), bv2); err != nil {
		t.Fatalf("CreateBlobVersion (bv2): %v", err)
	}

	count, err := reg.BlobVersions().CountBlobVersionReferences(context.Background(), createdBlob.BlobID)
	if err != nil {
		t.Fatalf("CountBlobVersionReferences: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected 2 version references, got %d (FR-12)", count)
	}

	versionIDs, err := reg.BlobVersions().ListVersionsForBlob(context.Background(), createdBlob.BlobID)
	if err != nil {
		t.Fatalf("ListVersionsForBlob: %v", err)
	}

	if len(versionIDs) != 2 {
		t.Errorf("Expected 2 versions, got %d", len(versionIDs))
	}
}

// TestFake_BlobVersion_Immutable tests FR-52: blob versions are immutable.
func TestFake_BlobVersion_Immutable(t *testing.T) {
	reg := New()

	blob := &repository.BlobRecord{
		UncompressedContentDigest: "sha256:immutable",
		StoredEncoding:            "gzip",
		ContentType:               "application/octet-stream",
		ConfirmationState:         repository.BlobConfirmationStateConfirmed,
	}

	createdBlob, err := reg.BlobRecords().CreateBlobRecord(context.Background(), blob)
	if err != nil {
		t.Fatalf("CreateBlobRecord: %v", err)
	}

	bv := &repository.BlobVersion{
		BlobID:     createdBlob.BlobID,
		ArtifactID: "app-1",
	}

	if err := reg.BlobVersions().CreateBlobVersion(context.Background(), bv); err != nil {
		t.Fatalf("CreateBlobVersion: %v", err)
	}

	// Duplicate should fail (immutability test)
	err2 := reg.BlobVersions().CreateBlobVersion(context.Background(), bv)
	if err2 == nil {
		t.Fatal("Expected ErrAlreadyExists for duplicate")
	}
	if !errors.Is(err2, repository.ErrAlreadyExists) {
		t.Errorf("Expected ErrAlreadyExists, got %v", err2)
	}
}

// TestFake_StoredObjectKey_PerVariant tests FR-25: object keys per variant.
func TestFake_StoredObjectKey_PerVariant(t *testing.T) {
	reg := New()

	appID := "app-123"

	key1 := &repository.StoredObjectKey{
		ArtifactID: appID,
		VariantKey: "amd64",
		ObjectKey:  "ghcr.io/example/widget:v1.0.0-amd64",
	}

	key2 := &repository.StoredObjectKey{
		ArtifactID: appID,
		VariantKey: "arm64",
		ObjectKey:  "ghcr.io/example/widget:v1.0.0-arm64",
	}

	created1, err := reg.StoredObjectKeys().CreateStoredObjectKey(context.Background(), key1)
	if err != nil {
		t.Fatalf("CreateStoredObjectKey (amd64): %v", err)
	}

	created2, err := reg.StoredObjectKeys().CreateStoredObjectKey(context.Background(), key2)
	if err != nil {
		t.Fatalf("CreateStoredObjectKey (arm64): %v", err)
	}

	if created1.StoredObjectKeyID == created2.StoredObjectKeyID {
		t.Fatal("Different variants should have distinct IDs")
	}

	retrieved1, err := reg.StoredObjectKeys().GetStoredObjectKey(context.Background(), appID, "amd64")
	if err != nil {
		t.Fatalf("GetStoredObjectKey (amd64): %v", err)
	}

	if retrieved1.ObjectKey != "ghcr.io/example/widget:v1.0.0-amd64" {
		t.Errorf("ObjectKey mismatch: got %s", retrieved1.ObjectKey)
	}

	allKeys, err := reg.StoredObjectKeys().ListStoredObjectKeysForArtifact(context.Background(), appID)
	if err != nil {
		t.Fatalf("ListStoredObjectKeysForArtifact: %v", err)
	}

	if len(allKeys) != 2 {
		t.Errorf("Expected 2 keys, got %d", len(allKeys))
	}
}

// TestFake_StoredObjectKey_UniqueConstraint tests (artifact_id, variant_key) uniqueness.
func TestFake_StoredObjectKey_UniqueConstraint(t *testing.T) {
	reg := New()

	appID := "app-123"

	key1 := &repository.StoredObjectKey{
		ArtifactID: appID,
		VariantKey: "amd64",
		ObjectKey:  "ghcr.io/example/widget:v1.0.0-amd64",
	}

	if _, err := reg.StoredObjectKeys().CreateStoredObjectKey(context.Background(), key1); err != nil {
		t.Fatalf("CreateStoredObjectKey: %v", err)
	}

	duplicate := &repository.StoredObjectKey{
		ArtifactID: appID,
		VariantKey: "amd64",
		ObjectKey:  "ghcr.io/example/widget:v1.0.0-amd64-different",
	}

	_, err := reg.StoredObjectKeys().CreateStoredObjectKey(context.Background(), duplicate)
	if err == nil {
		t.Fatal("Expected ErrAlreadyExists for duplicate")
	}
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("Expected ErrAlreadyExists, got %v", err)
	}
}

// TestFake_BlobVersion_ReferenceCount_FR12 tests the FR-12 reference count requirement.
func TestFake_BlobVersion_ReferenceCount_FR12(t *testing.T) {
	reg := New()

	blob := &repository.BlobRecord{
		UncompressedContentDigest: "sha256:contenthash",
		StoredEncoding:            "gzip",
		ContentType:               "application/vnd.oci.image.layer.v1.tar+gzip",
		ConfirmationState:         repository.BlobConfirmationStateConfirmed,
	}

	createdBlob, err := reg.BlobRecords().CreateBlobRecord(context.Background(), blob)
	if err != nil {
		t.Fatalf("CreateBlobRecord: %v", err)
	}

	// First version reference
	bv1 := &repository.BlobVersion{
		BlobID:     createdBlob.BlobID,
		ArtifactID: "app-v1",
	}

	if err := reg.BlobVersions().CreateBlobVersion(context.Background(), bv1); err != nil {
		t.Fatalf("CreateBlobVersion (v1): %v", err)
	}

	count, err := reg.BlobVersions().CountBlobVersionReferences(context.Background(), createdBlob.BlobID)
	if err != nil {
		t.Fatalf("CountBlobVersionReferences: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 reference after first version, got %d", count)
	}

	// Second version reference (same blob, different version)
	bv2 := &repository.BlobVersion{
		BlobID:     createdBlob.BlobID,
		ArtifactID: "app-v2",
	}

	if err := reg.BlobVersions().CreateBlobVersion(context.Background(), bv2); err != nil {
		t.Fatalf("CreateBlobVersion (v2): %v", err)
	}

	count, err = reg.BlobVersions().CountBlobVersionReferences(context.Background(), createdBlob.BlobID)
	if err != nil {
		t.Fatalf("CountBlobVersionReferences: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected 2 references after second version (FR-12), got %d", count)
	}
}
