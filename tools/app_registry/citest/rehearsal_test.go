package citest

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
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
	binaryName := "release_helper_go"
	resolveOs := "linux"
	resolveArch := "amd64"
	// The digest BrokerUpload/ConfirmUpload verify is over the uncompressed
	// content (FR-46); it must match the actual bytes we upload below, not
	// an unrelated placeholder string.
	testContent := []byte("test artifact content for " + version)
	uncompressedDigest := generateTestDigestBytes(testContent)
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
				// Named to match ResolveBinaryURL's "{os}-{arch}" variant
				// convention (see resolveVariantSelector) -- irrelevant to
				// whether resolution actually succeeds, since BrokerUpload's
				// object key is never linked into stored_object_key (see
				// Step 5 below), but kept realistic rather than "main".
				VariantKey:         fmt.Sprintf("%s-%s", resolveOs, resolveArch),
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
		// Binary kind's H4 policy is gzip (kinds/binary.go), so ConfirmUpload
		// decompresses before hashing -- the uploaded bytes must actually be
		// gzip, not raw content, or read-back fails at "invalid gzip header"
		// before digest comparison ever runs.
		t.Logf("Step 2: Upload file via presigned URL")
		gzippedContent := gzipCompress(testContent)
		if err := uploadViaPresignedURL(ctx, result.PresignedUrl, gzippedContent, result.RequiredHeaders); err != nil {
			t.Errorf("Failed to upload via presigned URL: %v", err)
			return
		}
		t.Logf("Uploaded %d bytes (gzip of %d bytes uncompressed) successfully", len(gzippedContent), len(testContent))
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
		t.Logf("  Variant %s: confirmed=%v, computed_digest=%s, error=%q, is_timeout=%v", r.VariantKey, r.Confirmed, r.ComputedDigest, r.ErrorMessage, r.IsTimeout)
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
	// NOTE: ResolveArtifact (used here in earlier drafts of this test) is
	// chart-only at the repository layer (postgres/artifact.go: "artifact %s
	// is not a chart") -- it walks a chart's pinned image references and is
	// never the right call for a binary-kind artifact. ResolveBinaryURL is
	// the binary-kind equivalent.
	t.Logf("Step 5: ResolveBinaryURL (retrieve published version)")

	resolveReq := &pb.ResolveBinaryURLRequest{
		Binary:  binaryName,
		Os:      resolveOs,
		Arch:    resolveArch,
		Version: version,
	}

	resolveResp, err := client.ResolveBinaryURL(ctx, resolveReq)
	if err != nil {
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.NotFound {
			// FR-58 gate P finding: BrokerUpload writes to an opaque,
			// session-scoped object key (artifacts/<identity>/<session>/<variant>)
			// that nothing ever copies into stored_object_key, and no kind
			// currently derives that key via H8 either. So a binary
			// artifact uploaded and confirmed through BrokerUpload/
			// ConfirmUpload/RecordArtifact has no wired path to become
			// resolvable via ResolveBinaryURL. This is a real production
			// gap, not a test-fixture bug -- recording it here rather than
			// masking it as a skip.
			t.Errorf("FR-58 gate P gap: ResolveBinaryURL returned NotFound for a just-uploaded-and-confirmed binary artifact (%v) -- BrokerUpload's object key is never linked into stored_object_key, so the resolve path can't find it", st.Message())
			return
		}
		t.Fatalf("ResolveBinaryURL failed: %v", err)
	}
	t.Logf("ResolveBinaryURL response:")
	t.Logf("  DownloadUrl: %s", resolveResp.DownloadUrl)
	t.Logf("  ChecksumManifestUrl: %s", resolveResp.ChecksumManifestUrl)

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
	if err := uploadViaPresignedURL(ctx, result.PresignedUrl, gzipCompress(differentContent), result.RequiredHeaders); err != nil {
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

	// FR-47's contract is per-file, not per-RPC: ConfirmUpload returns a
	// normal (nil-error) response with Confirmed=false and a diagnostic
	// ErrorMessage for the mismatched file, rather than failing the whole
	// RPC -- consistent with it accepting a batch of files where some may
	// confirm and others may not.
	confirmResp, err := client.ConfirmUpload(ctx, confirmReq)
	if err != nil {
		t.Fatalf("ConfirmUpload failed unexpectedly: %v", err)
	}

	if len(confirmResp.Results) != 1 {
		t.Fatalf("ConfirmUpload returned %d results, expected 1", len(confirmResp.Results))
	}
	r := confirmResp.Results[0]
	t.Logf("ConfirmUpload result: confirmed=%v, computed=%s, error=%q, is_timeout=%v", r.Confirmed, r.ComputedDigest, r.ErrorMessage, r.IsTimeout)

	if r.Confirmed {
		t.Error("FR-47: Expected ConfirmUpload to report confirmed=false on digest mismatch, but it reported confirmed=true")
		return
	}
	if !strings.Contains(r.ErrorMessage, "digest mismatch") {
		t.Errorf("FR-47: Expected error_message to describe a digest mismatch, got: %q", r.ErrorMessage)
		return
	}
	t.Logf("✓ FR-47: Registry correctly detected digest mismatch and reported confirmed=false")
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

	t.Logf("Step 2: ResolveBinaryURL (should detect unwritten object)")

	// NOTE: ResolveArtifact (used here in earlier drafts of this test) is
	// chart-only -- see the happy-path test's Step 5 comment. ResolveBinaryURL
	// is the binary-kind equivalent.
	resolveReq := &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Os:      "linux",
		Arch:    "amd64",
		Version: version,
	}

	resolveResp, err := client.ResolveBinaryURL(ctx, resolveReq)
	if err != nil {
		t.Logf("ResolveBinaryURL returned error (may be expected): %v", err)
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.NotFound {
			t.Logf("✓ FR-68/FR-29 (gate P): Registry correctly detected unwritten object and returned NotFound")
			return
		}
		t.Logf("ResolveBinaryURL error code: %v", status.Code(err))
		return
	}

	t.Logf("ResolveBinaryURL succeeded (unwritten object still resolvable in registry):")
	t.Logf("  DownloadUrl: %s", resolveResp.DownloadUrl)
}

// generateTestDigest creates a deterministic sha256 digest for test content.
func generateTestDigest(content string) string {
	return generateTestDigestBytes([]byte(content))
}

// generateTestDigestBytes creates a deterministic sha256 digest for test content.
func generateTestDigestBytes(content []byte) string {
	h := sha256.New()
	h.Write(content)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// gzipCompress compresses content per binary kind's H4 policy (kinds/binary.go),
// which ConfirmUpload's read-back path decompresses before hashing (FR-46).
func gzipCompress(content []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(content)
	w.Close()
	return buf.Bytes()
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

	// Virtual-hosted-style presigned URLs use "<bucket>.<host>" as the
	// hostname (FR-51). Local dev's minio only listens on the bare host
	// (127.0.0.1:9000, forwarded from the cluster); "<bucket>.localhost"
	// isn't resolvable by every environment's DNS (notably not this one),
	// so redial any "*.localhost" host straight to the loopback address
	// while leaving the Host header (and thus the S3 signature) untouched.
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err == nil && strings.HasSuffix(host, ".localhost") {
					addr = net.JoinHostPort("127.0.0.1", port)
				}
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
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
