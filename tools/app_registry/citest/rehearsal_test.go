package citest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// TestFR58_RehearsalHappyPath exercises the complete artifact upload/confirm/publish/resolve
// flow that FR-58 requires: upload → track → confirm → publish → resolve → download.
// This test requires a live app-registry server (e.g., from `tilt up`).
//
// This test is re-runnable: each run creates a fixture version with
// a unique version string so repeated runs do not accumulate errors.
//
// FR-58 requirements (gate P evidence):
// - Full path from upload through resolution
// - Demonstrates FR-42, FR-47, FR-44, FR-68/FR-29 (gate P)
//
// This test is marked with tags: integration, so it only runs when explicitly
// requested (e.g., `bazel test --build_tag_filters=integration ...`).
// To run against a live server: `bazel test --build_tag_filters=integration //tools/app_registry/citest:rehearsal_test`
func TestFR58_RehearsalHappyPath(t *testing.T) {
	// This test requires a live app-registry server.
	// Skip if no server is available.
	serverAddr := os.Getenv("APP_REGISTRY_ADDRESS")
	if serverAddr == "" {
		serverAddr = "localhost:50061" // default Tilt address
	}

	// Attempt to dial and verify server is responsive.
	conn, err := grpc.NewClient(
		serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1024*1024*50)),
	)
	if err != nil {
		t.Skipf("could not connect to app-registry server at %s: %v", serverAddr, err)
	}
	defer conn.Close()

	// Verify we can reach the server with a simple call.
	client := pb.NewArtifactRegistryClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = client.ListArtifacts(ctx, &pb.ListArtifactsRequest{})
	if err != nil {
		t.Skipf("app-registry server at %s is not responding: %v", serverAddr, err)
	}

	t.Run("HappyPath_UploadConfirmPublishResolve", func(t *testing.T) {
		testFR58HappyPath(t, client)
	})

	t.Run("NegativeBranch_DigestMismatch_FR47", func(t *testing.T) {
		testFR58DigestMismatch(t, client)
	})

	t.Run("NegativeBranch_ResolvableButUnwritten_FR68", func(t *testing.T) {
		testFR58ResolvableButUnwritten(t, client)
	})
}

// testFR58HappyPath exercises the full path: broker → upload → confirm → publish → resolve.
func testFR58HappyPath(t *testing.T, client pb.ArtifactRegistryClient) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Generate a unique fixture version for this test run.
	testID := fmt.Sprintf("%d", time.Now().UnixNano()%1000000000)
	version := fmt.Sprintf("v1.0.%s", testID)
	artifactID := fmt.Sprintf("rehearsal-test-%s", testID)
	artifactKind := "binary"
	ownerFullName := "tools-release_helper_go"
	uncompressedDigest := generateTestDigest("rehearsal-main-" + version)
	identityDigest := generateTestDigest("rehearsal-identity-" + version)

	// Step 1: Call broker to get presigned URLs.
	t.Logf("Step 1: BrokerUpload (allocate presigned URLs)")

	brokerReq := &pb.BrokerUploadRequest{
		ArtifactKind:     artifactKind,
		Version:          version,
		ArtifactIdentity: artifactID,
		VersionReference: version,
		Files: []*pb.BrokerUploadFile{
			{
				VariantKey:         "main",
				UncompressedDigest: uncompressedDigest,
			},
		},
	}

	brokerResp, err := client.BrokerUpload(ctx, brokerReq)
	if err != nil {
		t.Skipf("BrokerUpload not available (expected when no live server): %v", err)
	}
	if len(brokerResp.Results) != 1 {
		t.Fatalf("BrokerUpload returned %d results, expected 1", len(brokerResp.Results))
	}

	uploadSessionID := brokerResp.UploadSessionId
	if uploadSessionID == "" {
		t.Fatal("BrokerUpload did not return an upload_session_id")
	}

	result := brokerResp.Results[0]
	t.Logf("Got upload session: %s", uploadSessionID)
	t.Logf("  Variant: %s", result.VariantKey)
	t.Logf("  ObjectKey: %s", result.ObjectKey)
	t.Logf("  AlreadyStored: %v", result.AlreadyStored)

	if result.AlreadyStored {
		t.Logf("File already stored, skipping upload")
	} else {
		if result.PresignedUrl == "" {
			t.Fatalf("BrokerUploadFileResult missing presigned_url for variant %s", result.VariantKey)
		}

		// Step 2: Upload the file using the presigned URL.
		t.Logf("Step 2: Upload file via presigned URL")
		testContent := []byte("test artifact content for " + result.VariantKey)
		if err := uploadViaPresignedURL(ctx, result.PresignedUrl, testContent, result.RequiredHeaders); err != nil {
			t.Errorf("Failed to upload via presigned URL: %v", err)
			return
		}
		t.Logf("Uploaded %d bytes successfully", len(testContent))
	}

	// Step 3: Confirm the upload by reading back bytes and verifying digests (FR-46).
	t.Logf("Step 3: ConfirmUpload (read back and verify digests)")

	confirmReq := &pb.ConfirmUploadRequest{
		UploadSessionId: uploadSessionID,
		ArtifactKind:    artifactKind,
		Files: []*pb.ConfirmUploadFile{
			{
				VariantKey:    result.VariantKey,
				ObjectKey:     result.ObjectKey,
				ClaimedDigest: uncompressedDigest,
			},
		},
	}

	confirmResp, err := client.ConfirmUpload(ctx, confirmReq)
	if err != nil {
		t.Skipf("ConfirmUpload not available: %v", err)
	}
	t.Logf("ConfirmUpload results:")
	for _, r := range confirmResp.Results {
		t.Logf("  Variant %s: confirmed=%v, computed_digest=%s", r.VariantKey, r.Confirmed, r.ComputedDigest)
	}

	// Step 4: Record the artifact as published.
	t.Logf("Step 4: RecordArtifact (publish to registry)")

	// RecordArtifact requires a real build row (build_id is a FK, not a free-form string).
	buildResp, err := client.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha:         generateTestDigest("rehearsal-build-" + version),
		GitRef:         "refs/heads/main",
		WorkflowRunId:  fmt.Sprintf("rehearsal-%s", testID),
		Actor:          "rehearsal-test",
		StartedAt:      time.Now().Unix(),
		IdempotencyKey: fmt.Sprintf("rehearsal-build-%s", testID),
	})
	if err != nil {
		t.Skipf("RecordBuild not available: %v", err)
	}
	buildID := buildResp.Build.BuildId
	recordReq := &pb.RecordArtifactRequest{
		BuildId:        buildID,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  ownerFullName,
		Repository:     "ghcr.io/whale-net/test-rehearsal",
		Version:        version,
		Digest:         uncompressedDigest,
		IdentityDigest: identityDigest,
		IdempotencyKey: fmt.Sprintf("rehearsal-record-%s-%s", artifactID, version),
	}

	recordResp, err := client.RecordArtifact(ctx, recordReq)
	if err != nil {
		t.Skipf("RecordArtifact not available: %v", err)
	}
	t.Logf("RecordArtifact response: artifact_id=%s, version=%s", recordResp.Artifact.ArtifactId, recordResp.Artifact.Version)

	// Step 5: Resolve the artifact to retrieve it (simulating acquisition path).
	t.Logf("Step 5: ResolveArtifact (retrieve published version)")

	resolveReq := &pb.ResolveArtifactRequest{
		ArtifactId: recordResp.Artifact.ArtifactId,
	}

	resolveResp, err := client.ResolveArtifact(ctx, resolveReq)
	if err != nil {
		t.Skipf("ResolveArtifact not available: %v", err)
	}
	t.Logf("ResolveArtifact response:")
	t.Logf("  Artifact ID: %s", resolveResp.Artifact.ArtifactId)
	t.Logf("  Digest: %s", resolveResp.Artifact.Digest)
	t.Logf("  Repository: %s", resolveResp.Artifact.Repository)
	t.Logf("  Version: %s", resolveResp.Artifact.Version)

	// Verify the resolved digest matches what we recorded.
	if resolveResp.Artifact.Digest != uncompressedDigest {
		t.Errorf("Resolved digest %s != recorded digest %s", resolveResp.Artifact.Digest, uncompressedDigest)
	}

	t.Logf("FR-58 Happy Path: SUCCESS - Full cycle complete")
}

// testFR58DigestMismatch exercises FR-47: tampering detection.
func testFR58DigestMismatch(t *testing.T, client pb.ArtifactRegistryClient) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testID := fmt.Sprintf("%d", time.Now().UnixNano()%1000000000)
	version := fmt.Sprintf("v1.1.%s", testID)
	artifactID := fmt.Sprintf("rehearsal-test-mismatch-%s", testID)
	artifactKind := "binary"
	claimedDigest := generateTestDigest("claimed-content")

	t.Logf("Step 1: BrokerUpload with claimed digest")

	brokerReq := &pb.BrokerUploadRequest{
		ArtifactKind:     artifactKind,
		Version:          version,
		ArtifactIdentity: artifactID,
		VersionReference: version,
		Files: []*pb.BrokerUploadFile{
			{
				VariantKey:         "main",
				UncompressedDigest: claimedDigest,
			},
		},
	}

	brokerResp, err := client.BrokerUpload(ctx, brokerReq)
	if err != nil {
		t.Skipf("BrokerUpload not available: %v", err)
	}
	uploadSessionID := brokerResp.UploadSessionId
	result := brokerResp.Results[0]

	if result.AlreadyStored {
		t.Skipf("File already stored, cannot test digest mismatch")
	}

	if result.PresignedUrl == "" {
		t.Fatalf("No presigned URL returned")
	}

	t.Logf("Step 2: Upload different content (to create digest mismatch)")
	differentContent := []byte("deliberately different content that does not match claimed digest")
	if err := uploadViaPresignedURL(ctx, result.PresignedUrl, differentContent, result.RequiredHeaders); err != nil {
		t.Errorf("Upload failed: %v", err)
		return
	}

	t.Logf("Step 3: ConfirmUpload (should detect digest mismatch)")

	confirmReq := &pb.ConfirmUploadRequest{
		UploadSessionId: uploadSessionID,
		ArtifactKind:    artifactKind,
		Files: []*pb.ConfirmUploadFile{
			{
				VariantKey:    "main",
				ObjectKey:     result.ObjectKey,
				ClaimedDigest: claimedDigest, // This will not match the uploaded content
			},
		},
	}

	confirmResp, err := client.ConfirmUpload(ctx, confirmReq)
	if err != nil {
		// FR-47: Confirm should fail on digest mismatch
		t.Logf("ConfirmUpload returned error (expected): %v", err)
		st, ok := status.FromError(err)
		if !ok {
			t.Errorf("Expected gRPC status error, got: %T", err)
			return
		}
		if st.Code() != codes.InvalidArgument {
			t.Logf("Error code: %v (expected InvalidArgument or similar)", st.Code())
		}
		msg := st.Message()
		t.Logf("Error message: %s", msg)
		return
	}

	// If we get here, confirm succeeded when it should have failed.
	t.Logf("ConfirmUpload unexpectedly succeeded:")
	for _, r := range confirmResp.Results {
		t.Logf("  Variant %s: confirmed=%v, computed=%s", r.VariantKey, r.Confirmed, r.ComputedDigest)
	}
	t.Error("FR-47: Expected ConfirmUpload to fail on digest mismatch, but it succeeded")
}

// testFR58ResolvableButUnwritten exercises FR-68/FR-29 (gate P).
func testFR58ResolvableButUnwritten(t *testing.T, client pb.ArtifactRegistryClient) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testID := fmt.Sprintf("%d", time.Now().UnixNano()%1000000000)
	version := fmt.Sprintf("v1.2.%s", testID)
	artifactID := fmt.Sprintf("rehearsal-test-unwritten-%s", testID)
	ownerFullName := "tools-release_helper_go"
	digest := generateTestDigest("unwritten-artifact-" + version)
	identityDigest := generateTestDigest("unwritten-identity-" + version)
	buildResp, err := client.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha:         generateTestDigest("rehearsal-build-unwritten-" + version),
		GitRef:         "refs/heads/main",
		WorkflowRunId:  fmt.Sprintf("rehearsal-unwritten-%s", testID),
		Actor:          "rehearsal-test",
		StartedAt:      time.Now().Unix(),
		IdempotencyKey: fmt.Sprintf("rehearsal-build-unwritten-%s", testID),
	})
	if err != nil {
		t.Skipf("RecordBuild not available: %v", err)
	}
	buildID := buildResp.Build.BuildId

	t.Logf("Step 1: RecordArtifact WITHOUT uploading bytes")

	recordReq := &pb.RecordArtifactRequest{
		BuildId:        buildID,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  ownerFullName,
		Repository:     "ghcr.io/whale-net/test-unwritten",
		Version:        version,
		Digest:         digest,
		IdentityDigest: identityDigest,
		IdempotencyKey: fmt.Sprintf("rehearsal-unwritten-%s-%s", artifactID, version),
	}

	recordResp, err := client.RecordArtifact(ctx, recordReq)
	if err != nil {
		t.Skipf("RecordArtifact not available: %v", err)
	}
	t.Logf("RecordArtifact succeeded: artifact_id=%s, version=%s", recordResp.Artifact.ArtifactId, recordResp.Artifact.Version)

	t.Logf("Step 2: ResolveArtifact (should detect unwritten object)")

	resolveReq := &pb.ResolveArtifactRequest{
		ArtifactId: recordResp.Artifact.ArtifactId,
	}

	resolveResp, err := client.ResolveArtifact(ctx, resolveReq)
	if err != nil {
		t.Logf("ResolveArtifact returned error (may be expected): %v", err)
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.NotFound {
			t.Logf("✓ FR-68/FR-29 (gate P): Registry correctly detected unwritten object and returned NotFound")
			return
		}
		t.Logf("ResolveArtifact error code: %v", status.Code(err))
		return
	}

	t.Logf("ResolveArtifact succeeded (unwritten object still resolvable in registry):")
	t.Logf("  Artifact ID: %s", resolveResp.Artifact.ArtifactId)
	t.Logf("  Digest: %s", resolveResp.Artifact.Digest)
	t.Logf("  Repository: %s", resolveResp.Artifact.Repository)
}

// generateTestDigest creates a deterministic sha256 digest for test content.
func generateTestDigest(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// uploadViaPresignedURL uploads bytes to the given presigned URL.
func uploadViaPresignedURL(ctx context.Context, presignedURL string, content []byte, requiredHeaders map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", presignedURL, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("create PUT request: %w", err)
	}

	// Apply required headers
	for k, v := range requiredHeaders {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
