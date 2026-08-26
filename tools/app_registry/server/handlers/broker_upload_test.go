package handlers

import (
	"context"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// setupBroker creates a test ArtifactServer.
func setupBroker(t *testing.T) *ArtifactServer {
	t.Helper()
	repo := fake.New()
	// Configure S3 for presigned URLs in tests
	return NewArtifactServer(repo,
		WithArtifactsS3(
			"test-artifacts-bucket",
			"https://s3.test.example.com",
			"us-east-1",
			"test-access-key",
			"test-secret-key",
		),
	)
}

// TestBrokerUpload_Authorization tests FR-4: authorization requirement.
func TestBrokerUpload_Authorization_Unauthenticated(t *testing.T) {
	srv := setupBroker(t)
	ctx := context.Background()

	resp, err := srv.BrokerUpload(ctx, &pb.BrokerUploadRequest{
		ArtifactKind:     "binary",
		Version:          "v1.0.0",
		ArtifactIdentity: "test-identity",
		Files: []*pb.BrokerUploadFile{
			{VariantKey: "variant1", UncompressedDigest: "sha256:abc123"},
		},
	})

	if err == nil {
		t.Fatal("expected authorization error for unauthenticated caller")
	}
	if resp != nil {
		t.Fatal("expected nil response for unauthenticated caller")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated code, got %v", st.Code())
	}
}

// TestBrokerUpload_ValidationErrors tests request validation.
func TestBrokerUpload_ValidationErrors(t *testing.T) {
	srv := setupBroker(t)
	ctx := authedCtx()

	cases := []struct {
		name    string
		request *pb.BrokerUploadRequest
		wantErr bool
	}{
		{
			name: "missing_artifact_kind",
			request: &pb.BrokerUploadRequest{
				ArtifactKind:     "",
				Version:          "v1.0.0",
				ArtifactIdentity: "test",
				Files: []*pb.BrokerUploadFile{
					{VariantKey: "v1", UncompressedDigest: "sha256:test"},
				},
			},
			wantErr: true,
		},
		{
			name: "missing_version",
			request: &pb.BrokerUploadRequest{
				ArtifactKind:     "binary",
				Version:          "",
				ArtifactIdentity: "test",
				Files: []*pb.BrokerUploadFile{
					{VariantKey: "v1", UncompressedDigest: "sha256:test"},
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.BrokerUpload(ctx, tc.request)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if resp != nil {
					t.Fatal("expected nil response on error")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp == nil {
					t.Fatal("expected non-nil response")
				}
			}
		})
	}
}


// TestBrokerUpload_FR7_RecordBeforeURL tests FR-7: upload record has non-empty ObjectKey in response (proxy for durability).
func TestBrokerUpload_FR7_RecordBeforeURL(t *testing.T) {
	srv := setupBroker(t)
	ctx := authedCtx()

	req := &pb.BrokerUploadRequest{
		ArtifactKind:      "binary",
		Version:           "v1.0.0",
		ArtifactIdentity:  "test-app",
		VersionReference:  "v1.0.0",
		Files: []*pb.BrokerUploadFile{
			{VariantKey: "amd64", UncompressedDigest: "sha256:abc123"},
		},
	}

	resp, err := srv.BrokerUpload(ctx, req)
	if err != nil {
		t.Fatalf("BrokerUpload failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// FR-7 requirement: response must include object_key and presigned URL together
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one file result")
	}

	result := resp.Results[0]
	if result.AlreadyStored {
		t.Fatal("expected new file to not be already stored")
	}

	// Verify presigned URL is present (FR-7: URL not returned without record)
	if result.PresignedUrl == "" {
		t.Fatal("expected non-empty presigned URL")
	}

	// Verify object key is present and includes the upload session ID
	if result.ObjectKey == "" {
		t.Fatal("expected non-empty ObjectKey in response")
	}

	if !Contains(result.ObjectKey, resp.UploadSessionId) {
		t.Fatalf("expected ObjectKey to contain UploadSessionId %q, got %q", resp.UploadSessionId, result.ObjectKey)
	}

	// Verify object key includes the variant
	if !Contains(result.ObjectKey, "amd64") {
		t.Fatalf("expected ObjectKey to contain variant 'amd64', got %q", result.ObjectKey)
	}
}

// TestBrokerUpload_FR2_DistinctKeysForConcurrentRequests tests FR-2: concurrent requests for same identity get distinct keys.
func TestBrokerUpload_FR2_DistinctKeysForConcurrentRequests(t *testing.T) {
	srv := setupBroker(t)
	ctx := authedCtx()

	req := &pb.BrokerUploadRequest{
		ArtifactKind:      "binary",
		Version:           "v1.0.0",
		ArtifactIdentity:  "test-app",
		VersionReference:  "v1.0.0",
		Files: []*pb.BrokerUploadFile{
			{VariantKey: "amd64", UncompressedDigest: "sha256:abc123"},
		},
	}

	// Make two brokering calls for the same artifact identity
	resp1, err1 := srv.BrokerUpload(ctx, req)
	if err1 != nil {
		t.Fatalf("First BrokerUpload failed: %v", err1)
	}

	resp2, err2 := srv.BrokerUpload(ctx, req)
	if err2 != nil {
		t.Fatalf("Second BrokerUpload failed: %v", err2)
	}

	// Verify both uploads have different session IDs
	if resp1.UploadSessionId == resp2.UploadSessionId {
		t.Fatal("expected distinct upload session IDs for concurrent requests")
	}

	// Verify both uploads have different object keys
	key1 := resp1.Results[0].ObjectKey
	key2 := resp2.Results[0].ObjectKey
	if key1 == key2 {
		t.Fatalf("expected distinct object keys, got %q for both", key1)
	}

	// Verify the object keys follow the expected format with their respective session IDs
	if !Contains(key1, resp1.UploadSessionId) {
		t.Fatalf("expected key1 to contain session ID %q, got %q", resp1.UploadSessionId, key1)
	}
	if !Contains(key2, resp2.UploadSessionId) {
		t.Fatalf("expected key2 to contain session ID %q, got %q", resp2.UploadSessionId, key2)
	}
}

// TestBrokerUpload_FR1_AlreadyStoredDedup tests FR-1: already-stored blobs return no URL.
func TestBrokerUpload_FR1_AlreadyStoredDedup(t *testing.T) {
	srv := setupBroker(t)
	ctx := authedCtx()

	// First, create and confirm a blob (outside the BrokerUpload call)
	digest := "sha256:abc123"
	encodingVal := "gzip"
	contentTypeVal := "application/octet-stream"

	if err := srv.repo.WithTx(ctx, func(ctx context.Context, r repository.Registry) error {
		_, err := r.BlobRecords().CreateBlobRecord(ctx, &repository.BlobRecord{
			UncompressedContentDigest: digest,
			StoredEncoding:            encodingVal,
			ContentType:               contentTypeVal,
			ConfirmationState:         repository.BlobConfirmationStateConfirmed,
		})
		return err
	}); err != nil {
		t.Fatalf("CreateBlobRecord failed: %v", err)
	}

	// Now broker an upload for the same blob
	req := &pb.BrokerUploadRequest{
		ArtifactKind:      "binary",
		Version:           "v1.0.0",
		ArtifactIdentity:  "test-app",
		VersionReference:  "v1.0.0",
		Files: []*pb.BrokerUploadFile{
			{VariantKey: "amd64", UncompressedDigest: digest},
		},
	}

	resp, err := srv.BrokerUpload(ctx, req)
	if err != nil {
		t.Fatalf("BrokerUpload failed: %v", err)
	}

	// Verify the result is already stored
	result := resp.Results[0]
	if !result.AlreadyStored {
		t.Fatal("expected already_stored=true for confirmed blob")
	}

	// Verify no presigned URL is returned
	if result.PresignedUrl != "" {
		t.Fatalf("expected empty presigned URL for already stored blob, got %q", result.PresignedUrl)
	}

	// Verify no object key is returned
	if result.ObjectKey != "" {
		t.Fatalf("expected empty object key for already stored blob, got %q", result.ObjectKey)
	}
}

// TestBrokerUpload_FR1_MixedAlreadyStoredAndNew tests FR-1: batch with both already-stored and new files.
func TestBrokerUpload_FR1_MixedAlreadyStoredAndNew(t *testing.T) {
	srv := setupBroker(t)
	ctx := authedCtx()

	// Create and confirm a blob for the first file
	digest1 := "sha256:abc123"
	if err := srv.repo.WithTx(ctx, func(ctx context.Context, r repository.Registry) error {
		_, err := r.BlobRecords().CreateBlobRecord(ctx, &repository.BlobRecord{
			UncompressedContentDigest: digest1,
			StoredEncoding:            "gzip",
			ContentType:               "application/octet-stream",
			ConfirmationState:         repository.BlobConfirmationStateConfirmed,
		})
		return err
	}); err != nil {
		t.Fatalf("CreateBlobRecord failed: %v", err)
	}

	// Broker a request with 3 files: 1 already stored, 2 new
	req := &pb.BrokerUploadRequest{
		ArtifactKind:      "binary",
		Version:           "v1.0.0",
		ArtifactIdentity:  "test-app",
		VersionReference:  "v1.0.0",
		Files: []*pb.BrokerUploadFile{
			{VariantKey: "amd64", UncompressedDigest: digest1},            // already stored
			{VariantKey: "arm64", UncompressedDigest: "sha256:def456"},   // new
			{VariantKey: "ppc64le", UncompressedDigest: "sha256:ghi789"}, // new
		},
	}

	resp, err := srv.BrokerUpload(ctx, req)
	if err != nil {
		t.Fatalf("BrokerUpload failed: %v", err)
	}

	if len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resp.Results))
	}

	// First result: already stored
	if !resp.Results[0].AlreadyStored {
		t.Fatal("expected first result to be already_stored=true")
	}
	if resp.Results[0].PresignedUrl != "" {
		t.Fatal("expected empty presigned URL for already stored blob")
	}
	if resp.Results[0].ObjectKey != "" {
		t.Fatal("expected empty object key for already stored blob")
	}

	// Second and third results: new uploads
	for i := 1; i < 3; i++ {
		if resp.Results[i].AlreadyStored {
			t.Fatalf("expected result %d to be already_stored=false", i)
		}
		if resp.Results[i].PresignedUrl == "" {
			t.Fatalf("expected non-empty presigned URL for result %d", i)
		}
		if resp.Results[i].ObjectKey == "" {
			t.Fatalf("expected non-empty object key for result %d", i)
		}
	}

	// Verify all new results have the same batch key but different variant suffixes
	batchKey := resp.UploadSessionId
	for i := 1; i < 3; i++ {
		key := resp.Results[i].ObjectKey
		if !Contains(key, batchKey) {
			t.Fatalf("expected result %d key to contain batch key %q, got %q", i, batchKey, key)
		}
	}

	// Verify arm64 and ppc64le keys are different
	if resp.Results[1].ObjectKey == resp.Results[2].ObjectKey {
		t.Fatalf("expected distinct keys for arm64 and ppc64le, got same key %q", resp.Results[1].ObjectKey)
	}
}

// Helper function to check if a string contains a substring
func Contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
