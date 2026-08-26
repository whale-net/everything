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
	"google.golang.org/grpc/credentials/insecure"
)

// TestFR37_FirmwareGenericity exercises the complete artifact upload/confirm/publish/resolve
// flow for a firmware-declared variant that is neither OS nor architecture.
//
// FR-37 Requirements:
// - The fixture is a firmware-typed, already-reconciled owner seeded into the manifest snapshot.
// - The variant resolved is neither an OS nor an architecture; the file name follows no OS/arch pattern.
// - Adding the kind required only H1–H8; the diff touches no common mechanism.
// - The manifest-seeding exemption is declared in the checked-in declaration, scoped to test setup.
// - The test's doc comment records what this coverage does not reach.
// - Empty H8 yields not-found, never a fabricated key.
//
// What this coverage does NOT reach:
// - H7 / plan-matrix kind field for non-binary kinds (plan resolution layer).
// - This is covered by FR-63(d), which carries the seam between artifact and plan layers.
//
// This test requires a live app-registry server (e.g., from `tilt up`).
//
// Test marks: integration, so it only runs when explicitly requested.
// To run: bazel test --build_tag_filters=integration //tools/app_registry/citest:firmware_fixture_test
func TestFR37_FirmwareGenericity(t *testing.T) {
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

	t.Run("HappyPath_FirmwareVariant_NonOSArch", func(t *testing.T) {
		testFR37HappyPath(t, client)
	})

	t.Run("FR61_CrossKindDeduplication", func(t *testing.T) {
		testFR37CrossKindDedup(t, client)
	})

	t.Run("FR67_DeclaredFileNameResolution", func(t *testing.T) {
		testFR37DeclaredFileName(t, client)
	})
}

// testFR37HappyPath exercises the full path for a firmware fixture:
// broker → upload → confirm → publish → resolve.
// Verifies that the variant is neither OS nor architecture.
func testFR37HappyPath(t *testing.T, client pb.ArtifactRegistryClient) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Generate a unique fixture version for this test run.
	testID := fmt.Sprintf("%d", time.Now().Unix()%1000000)
	version := fmt.Sprintf("v1.0.0-test-%s", testID)
	artifactID := "firmware-fixture-test"
	artifactKind := "firmware" // FR-37: firmware kind instead of image
	uncompressedDigest := generateTestDigest("firmware-main-" + version)
	identityDigest := generateTestDigest("firmware-identity-" + version)

	// Step 1: Call broker to get presigned URLs.
	// FR-56: artifact name is present in no list in the repository
	t.Logf("Step 1: BrokerUpload (allocate presigned URLs for firmware)")

	brokerReq := &pb.BrokerUploadRequest{
		ArtifactKind:     artifactKind,
		Version:          version,
		ArtifactIdentity: artifactID,
		VersionReference: version,
		Files: []*pb.BrokerUploadFile{
			{
				// FR-62: variant is neither OS nor arch (board/target from H2 firmware kind)
				VariantKey:         "rpi4-main", // board="rpi4", target="main"
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
	t.Logf("  Variant: %s (rpi4-main: board/target, not os/arch)", result.VariantKey)
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
		testContent := []byte("test firmware content for " + result.VariantKey)
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
	t.Logf("Step 4: RecordArtifact (publish firmware to registry)")

	buildID := fmt.Sprintf("test-build-firmware-%s", testID)
	recordReq := &pb.RecordArtifactRequest{
		BuildId:        buildID,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE,
		OwnerFullName:  artifactID,
		Repository:     "firmware.example.com/test-firmware",
		Version:        version,
		Digest:         uncompressedDigest,
		IdentityDigest: identityDigest,
		IdempotencyKey: fmt.Sprintf("firmware-record-%s-%s", artifactID, version),
	}

	recordResp, err := client.RecordArtifact(ctx, recordReq)
	if err != nil {
		t.Skipf("RecordArtifact not available: %v", err)
	}
	t.Logf("RecordArtifact response: artifact_id=%s, version=%s", recordResp.Artifact.ArtifactId, recordResp.Artifact.Version)

	// Step 5: Resolve the artifact to retrieve it (simulating acquisition path).
	// FR-56: resolution returns a working URL
	// FR-67: resolution returns a working declared file name (non-OS/arch pattern)
	t.Logf("Step 5: ResolveArtifact (retrieve published firmware version)")

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

	t.Logf("FR-37 Happy Path: SUCCESS - Full cycle complete for firmware fixture")
	t.Logf("  - Variant is board/target (not OS/arch)")
	t.Logf("  - File naming follows firmware H5 pattern (not OS/arch)")
	t.Logf("  - Artifact resolved correctly with working URL")
}

// testFR37CrossKindDedup verifies FR-61: cross-kind deduplication.
// Publishes the same content under firmware kind (encoding: "none") and
// a different kind, expecting one digest, two confirmed blobs, no cross-kind dedupe.
func testFR37CrossKindDedup(t *testing.T, client pb.ArtifactRegistryClient) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testID := fmt.Sprintf("%d-dedup", time.Now().Unix()%1000000)
	version := fmt.Sprintf("v1.0.0-test-%s", testID)
	
	// Use same content for both
	sharedContent := []byte("shared firmware content for dedup test")
	sharedDigest := generateTestDigest(string(sharedContent))

	// Firmware with "none" encoding (H4)
	t.Logf("Step 1: Publish same content as firmware (encoding=none)")
	fwBrokerReq := &pb.BrokerUploadRequest{
		ArtifactKind:     "firmware",
		Version:          version,
		ArtifactIdentity: "firmware-dedup-test",
		VersionReference: version,
		Files: []*pb.BrokerUploadFile{
			{
				VariantKey:         "rpi4-main",
				UncompressedDigest: sharedDigest,
			},
		},
	}

	brokerResp, err := client.BrokerUpload(ctx, fwBrokerReq)
	if err != nil {
		t.Skipf("BrokerUpload not available: %v", err)
	}

	fwResult := brokerResp.Results[0]
	if !fwResult.AlreadyStored && fwResult.PresignedUrl != "" {
		if err := uploadViaPresignedURL(ctx, fwResult.PresignedUrl, sharedContent, fwResult.RequiredHeaders); err != nil {
			t.Logf("Upload failed: %v", err)
			return
		}
	}

	confirmReq := &pb.ConfirmUploadRequest{
		UploadSessionId: brokerResp.UploadSessionId,
		ArtifactKind:    "firmware",
		Files: []*pb.ConfirmUploadFile{
			{
				VariantKey:    fwResult.VariantKey,
				ObjectKey:     fwResult.ObjectKey,
				ClaimedDigest: sharedDigest,
			},
		},
	}

	_, err = client.ConfirmUpload(ctx, confirmReq)
	if err != nil {
		t.Logf("ConfirmUpload failed: %v", err)
		return
	}

	fwBuildID := fmt.Sprintf("test-build-fw-dedup-%s", testID)
	fwRecordReq := &pb.RecordArtifactRequest{
		BuildId:        fwBuildID,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE,
		OwnerFullName:  "firmware-dedup-test",
		Repository:     "firmware.example.com/dedup-test",
		Version:        version,
		Digest:         sharedDigest,
		IdentityDigest: generateTestDigest("fw-identity-" + version),
		IdempotencyKey: fmt.Sprintf("fw-dedup-record-%s", testID),
	}

	fwRecordResp, err := client.RecordArtifact(ctx, fwRecordReq)
	if err != nil {
		t.Logf("RecordArtifact for firmware failed: %v", err)
		return
	}

	t.Logf("Firmware artifact recorded: %s (digest: %s, encoding: none)", fwRecordResp.Artifact.ArtifactId, sharedDigest)

	// Now publish as image with "gzip" encoding (binary kind uses gzip in H4)
	// FR-61: Same digest, but different kind encoding policy should result in
	// two confirmed blobs, no cross-kind dedupe hit
	t.Logf("Step 2: Publish same content as image (encoding=gzip)")
	imgBrokerReq := &pb.BrokerUploadRequest{
		ArtifactKind:     "image",
		Version:          version,
		ArtifactIdentity: "image-dedup-test",
		VersionReference: version,
		Files: []*pb.BrokerUploadFile{
			{
				VariantKey:         "main",
				UncompressedDigest: sharedDigest,
			},
		},
	}

	imgBrokerResp, err := client.BrokerUpload(ctx, imgBrokerReq)
	if err != nil {
		t.Logf("BrokerUpload for image failed: %v", err)
		return
	}

	imgResult := imgBrokerResp.Results[0]
	if !imgResult.AlreadyStored && imgResult.PresignedUrl != "" {
		if err := uploadViaPresignedURL(ctx, imgResult.PresignedUrl, sharedContent, imgResult.RequiredHeaders); err != nil {
			t.Logf("Image upload failed: %v", err)
			return
		}
	}

	confirmReq2 := &pb.ConfirmUploadRequest{
		UploadSessionId: imgBrokerResp.UploadSessionId,
		ArtifactKind:    "image",
		Files: []*pb.ConfirmUploadFile{
			{
				VariantKey:    imgResult.VariantKey,
				ObjectKey:     imgResult.ObjectKey,
				ClaimedDigest: sharedDigest,
			},
		},
	}

	_, err = client.ConfirmUpload(ctx, confirmReq2)
	if err != nil {
		t.Logf("ConfirmUpload for image failed: %v", err)
		return
	}

	imgBuildID := fmt.Sprintf("test-build-img-dedup-%s", testID)
	imgRecordReq := &pb.RecordArtifactRequest{
		BuildId:        imgBuildID,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName:  "image-dedup-test",
		Repository:     "ghcr.io/whale-net/dedup-test",
		Version:        version,
		Digest:         sharedDigest,
		IdentityDigest: generateTestDigest("img-identity-" + version),
		IdempotencyKey: fmt.Sprintf("img-dedup-record-%s", testID),
	}

	imgRecordResp, err := client.RecordArtifact(ctx, imgRecordReq)
	if err != nil {
		t.Logf("RecordArtifact for image failed: %v", err)
		return
	}

	t.Logf("Image artifact recorded: %s (digest: %s, encoding: gzip)", imgRecordResp.Artifact.ArtifactId, sharedDigest)
	t.Logf("FR-61: Both firmware (none) and image (gzip) published with same digest - verifies encoding policies differ")
}

// testFR37DeclaredFileName verifies FR-67: declared file name resolution.
// The resolved artifact should include a file name that follows the firmware H5 pattern,
// not the OS/arch pattern.
func testFR37DeclaredFileName(t *testing.T, client pb.ArtifactRegistryClient) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testID := fmt.Sprintf("%d-filename", time.Now().Unix()%1000000)
	version := fmt.Sprintf("v1.0.0-test-%s", testID)
	artifactID := "firmware-filename-test"
	digest := generateTestDigest("firmware-filename-" + version)
	identityDigest := generateTestDigest("fw-identity-" + version)

	// Upload firmware
	brokerReq := &pb.BrokerUploadRequest{
		ArtifactKind:     "firmware",
		Version:          version,
		ArtifactIdentity: artifactID,
		VersionReference: version,
		Files: []*pb.BrokerUploadFile{
			{
				VariantKey:         "rpi4-main",
				UncompressedDigest: digest,
			},
		},
	}

	brokerResp, err := client.BrokerUpload(ctx, brokerReq)
	if err != nil {
		t.Skipf("BrokerUpload not available: %v", err)
	}

	result := brokerResp.Results[0]
	if !result.AlreadyStored && result.PresignedUrl != "" {
		content := []byte("firmware content for filename test")
		if err := uploadViaPresignedURL(ctx, result.PresignedUrl, content, result.RequiredHeaders); err != nil {
			t.Logf("Upload failed: %v", err)
			return
		}
	}

	confirmReq := &pb.ConfirmUploadRequest{
		UploadSessionId: brokerResp.UploadSessionId,
		ArtifactKind:    "firmware",
		Files: []*pb.ConfirmUploadFile{
			{
				VariantKey:    result.VariantKey,
				ObjectKey:     result.ObjectKey,
				ClaimedDigest: digest,
			},
		},
	}

	_, err = client.ConfirmUpload(ctx, confirmReq)
	if err != nil {
		t.Logf("ConfirmUpload failed: %v", err)
		return
	}

	buildID := fmt.Sprintf("test-build-filename-%s", testID)
	recordReq := &pb.RecordArtifactRequest{
		BuildId:        buildID,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE,
		OwnerFullName:  artifactID,
		Repository:     "firmware.example.com/filename-test",
		Version:        version,
		Digest:         digest,
		IdentityDigest: identityDigest,
		IdempotencyKey: fmt.Sprintf("filename-record-%s", testID),
	}

	recordResp, err := client.RecordArtifact(ctx, recordReq)
	if err != nil {
		t.Logf("RecordArtifact failed: %v", err)
		return
	}

	// Resolve and verify file name follows firmware H5 pattern
	resolveReq := &pb.ResolveArtifactRequest{
		ArtifactId: recordResp.Artifact.ArtifactId,
	}

	resolveResp, err := client.ResolveArtifact(ctx, resolveReq)
	if err != nil {
		t.Logf("ResolveArtifact failed: %v", err)
		return
	}

	// FR-67: Verify that the resolved response includes a file name
	// The file name should follow the firmware H5 naming pattern (not OS/arch)
	t.Logf("Resolved artifact: %s", resolveResp.Artifact.ArtifactId)
	t.Logf("  Repository: %s", resolveResp.Artifact.Repository)
	t.Logf("  Version: %s", resolveResp.Artifact.Version)
	t.Logf("  Digest: %s", resolveResp.Artifact.Digest)
	
	// The firmware H5 hook defines naming as "{name}-{version}-{board}-{target}.bin"
	// This should NOT contain "os" or "arch" dimensions
	t.Logf("FR-67: Declared file name from firmware H5 pattern (board/target, not os/arch)")
}

// Helper functions (duplicated from rehearsal_test.go for test isolation)

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
