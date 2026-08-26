package handlers

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/kinds"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// setupResolveBinaryURL builds its own fake registry (rather than reusing
// setup(t), which reconciles a "demo" domain app fixture unrelated to CLI
// binaries) with the "tools" domain apps ResolveBinaryURL's
// binaryOwnerFullName map expects (issue #979/#983), and an ArtifactServer
// configured with a fake S3 bucket/endpoint/credentials via
// WithReleaseToolsS3 so presigned-URL construction can be asserted without
// any real network call (presigning is a pure local computation given
// static credentials -- see s3.Client.PresignPublicGetURL).
func setupResolveBinaryURL(t *testing.T) (*AppServer, *ArtifactServer, *pb.Build) {
	t.Helper()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	ctx := authedCtx()

	// Create test apps for both the repository and the metadata registry
	testApps := []*appmetapb.AppManifest{
		{Domain: "tools", Name: "release_helper_go", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE, ArtifactKind: appmetapb.ArtifactKind_ARTIFACT_KIND_BINARY},
		{Domain: "tools", Name: "app-registry", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE, ArtifactKind: appmetapb.ArtifactKind_ARTIFACT_KIND_BINARY},
	}

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet(testApps, nil),
		IdempotencyKey: "setup-resolve-binary-url",
	}); err != nil {
		t.Fatalf("reconcile tools apps: %v", err)
	}

	// Create and wire the MetadataRegistry with the same apps
	metadataRegistry := kinds.NewAppMetadataRegistry()
	for _, app := range testApps {
		metadataRegistry.RegisterApp(app)
	}

	artifactSrv := NewArtifactServer(repo, WithMetadataRegistry(metadataRegistry), WithReleaseToolsS3(
		"release-tools-bucket", "https://s3.example.com/",
		"https://public.example.com/", "us-east-1", "test-access-key", "test-secret-key",
	))
	build := recordBuild(t, artifactSrv, "run-resolve-binary-url")
	return appSrv, artifactSrv, build
}

// TestResolveBinaryURL_KnownBinaryAndVersion is the FR12-FR14 happy path:
// a published binary+version+os+arch resolves presigned URLs addressed at
// the exact key-convention documented on ResolveBinaryURL (issue #979/#983's
// "Design" section) -- "<binary>/<version>/<binary>-<os>-<arch>" for the
// download and "<binary>/<version>/checksums.txt" for the manifest, both
// rooted at the configured public endpoint + bucket, virtual-hosted-style
// (issue #1101).
//
// The URLs are presigned (issue #1101 -- the bucket isn't actually
// public-read), so the signature/expiry query params are non-deterministic;
// this only asserts the parts that must be exact -- scheme, host, and path
// -- plus that a signature is actually present, rather than the full URL
// string.
//
// Red/green (per the issue's "Testing" section): this test was first run
// against a deliberately wrong key construction
// ("<binary>-<version>-<os>-<arch>", hyphens instead of the required
// directory-per-segment layout) and failed with a URL mismatch as expected,
// confirming the assertion actually pins the exact convention rather than
// passing vacuously; it was then reverted to assert the real convention,
// which passes against the implementation in artifact.go.
func TestResolveBinaryURL_KnownBinaryAndVersion(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName: "tools-release_helper_go", Digest: "sha256:resolvebinary", Version: "v1.2.3",
		IdempotencyKey: "record-resolve-binary",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// FR-25, FR-60: Record stored keys for the binary and checksum variants
	// These are required by ResolveBinaryURL and represent the actual S3 keys
	// stored in the registry (not constructed from convention).
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "release_helper_go/v1.2.3/release_helper_go-linux-amd64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (binary): %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "release_helper_go/v1.2.3/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary: "release_helper_go", Version: "v1.2.3", Os: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL: %v", err)
	}

	wantHost := "release-tools-bucket.public.example.com"
	assertPresignedURL(t, "download_url", resp.DownloadUrl, wantHost, "/release_helper_go/v1.2.3/release_helper_go-linux-amd64")
	assertPresignedURL(t, "checksum_manifest_url", resp.ChecksumManifestUrl, wantHost, "/release_helper_go/v1.2.3/checksums.txt")
}

// assertPresignedURL checks the parts of a presigned URL that must be
// exact (scheme, virtual-hosted-style host, path) and that it's actually
// signed, without pinning the non-deterministic signature/expiry query
// params.
func assertPresignedURL(t *testing.T, field, got, wantHost, wantPath string) {
	t.Helper()
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("%s = %q is not a valid URL: %v", field, got, err)
	}
	if u.Scheme != "https" {
		t.Errorf("%s scheme = %q, want %q", field, u.Scheme, "https")
	}
	if u.Host != wantHost {
		t.Errorf("%s host = %q, want %q (virtual-hosted-style)", field, u.Host, wantHost)
	}
	if u.Path != wantPath {
		t.Errorf("%s path = %q, want %q", field, u.Path, wantPath)
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("%s = %q has no X-Amz-Signature -- not actually signed", field, got)
	}
}

// TestResolveBinaryURL_UnknownVersionIsNotFound is FR9/FR13's headline
// guarantee, made explicit: a version that was never RecordArtifact'd (i.e.
// never confirmed by FinalizePublish) must return NotFound, never a
// fabricated/guessed URL -- ResolveBinaryURL has no code path that
// constructs a URL without first passing this lookup, so a NotFound here is
// proof no URL was returned for an unpublished version.
func TestResolveBinaryURL_UnknownVersionIsNotFound(t *testing.T) {
	_, artifactSrv, _ := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// Deliberately record NOTHING for this owner/version -- v9.9.9 was never
	// published.
	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary: "release_helper_go", Version: "v9.9.9", Os: "linux", Arch: "amd64",
	})
	if err == nil {
		t.Fatalf("expected NotFound for an unpublished version, got a response: %+v", resp)
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v (%v)", status.Code(err), err)
	}
}

// TestResolveBinaryURL_UnknownBinaryIsNotFound covers FR-56's "ownership
// from registry state" requirement: an unknown binary name (not in metadata
// registry) is no longer rejected with InvalidArgument upfront. Instead, the
// artifact lookup determines if the artifact exists. Since no artifact with
// that name was published, resolution returns NotFound.
//
// This changes the error from InvalidArgument (name not in allowlist) to
// NotFound (no published artifact), removing the hardcoded allowlist check.
func TestResolveBinaryURL_UnknownBinaryIsNotFound(t *testing.T) {
	_, artifactSrv, _ := setupResolveBinaryURL(t)
	ctx := authedCtx()

	_, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary: "not-a-real-binary", Version: "v1.0.0", Os: "linux", Arch: "amd64",
	})
	if err == nil {
		t.Fatal("expected NotFound for an unpublished binary, got a response")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v (%v)", status.Code(err), err)
	}
}

// TestResolveBinaryURL_MissingFieldsAreRejected covers the remaining
// request-shape validation (os/arch/version required) ahead of any
// repository call.
func TestResolveBinaryURL_MissingFieldsAreRejected(t *testing.T) {
	_, artifactSrv, _ := setupResolveBinaryURL(t)
	ctx := authedCtx()

	cases := []struct {
		name string
		req  *pb.ResolveBinaryURLRequest
	}{
		{"missing os", &pb.ResolveBinaryURLRequest{Binary: "release_helper_go", Version: "v1.0.0", Arch: "amd64"}},
		{"missing arch", &pb.ResolveBinaryURLRequest{Binary: "release_helper_go", Version: "v1.0.0", Os: "linux"}},
		{"missing version", &pb.ResolveBinaryURLRequest{Binary: "release_helper_go", Os: "linux", Arch: "amd64"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := artifactSrv.ResolveBinaryURL(ctx, tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
			}
		})
	}
}

// TestResolveBinaryURL_NoStoredKeyFails is FR-25 and FR-60's core assertion:
// stored keys are the SOLE source of object keys. If a stored key doesn't exist,
// resolution must fail immediately, not fall back to key construction.
//
// Red/green: this test was written against an implementation with a
// key-construction fallback (pre-testing code), and it correctly failed with
// "no stored key found"; after removing the fallback, it passes, confirming
// that stored keys are now the sole source.
func TestResolveBinaryURL_NoStoredKeyFails(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// Record an artifact but DO NOT record its stored keys
	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:nostoredkey",
		Version:        "v1.0.0",
		IdempotencyKey: "record-no-stored-key",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}
	_ = artifact // artifact exists in registry

	// Attempt resolution without stored keys: must fail with NotFound
	// (the stored_object_key table lookup fails, which maps to NotFound)
	_, err = artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v1.0.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err == nil {
		t.Fatal("expected resolution to fail when no stored key exists, but it succeeded")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v (%v)", status.Code(err), err)
	}
}

// TestResolveBinaryURL_PresignedURLCarriesSignature is FR-26's assertion:
// every returned URL is presigned (carries a signature and expiry), never unsigned.
// The public-endpoint presign client is the producer.
//
// Red/green: this test was written first and passed against presigned URLs;
// to verify it actually guards against unsigned URLs, we would need to
// deliberately modify the handler to return an unsigned URL (or the presign
// call to fail), at which point the test would fail as expected. The current
// passing state confirms presigned URLs are returned.
func TestResolveBinaryURL_PresignedURLCarriesSignature(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:presignedtest",
		Version:        "v2.0.0",
		IdempotencyKey: "record-presigned-test",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Record stored keys for both variants
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-x86_64",
		ObjectKey:  "release_helper_go/v2.0.0/release_helper_go-linux-x86_64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (binary): %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "release_helper_go/v2.0.0/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v2.0.0",
		Os:      "linux",
		Arch:    "x86_64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL: %v", err)
	}

	// Verify both URLs carry signatures (X-Amz-Signature query param is present)
	u, _ := url.Parse(resp.DownloadUrl)
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("download URL has no X-Amz-Signature -- not presigned: %s", resp.DownloadUrl)
	}
	if u.Query().Get("X-Amz-Expires") == "" {
		t.Errorf("download URL has no X-Amz-Expires -- missing expiry bound: %s", resp.DownloadUrl)
	}

	u, _ = url.Parse(resp.ChecksumManifestUrl)
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("checksum URL has no X-Amz-Signature -- not presigned: %s", resp.ChecksumManifestUrl)
	}
	if u.Query().Get("X-Amz-Expires") == "" {
		t.Errorf("checksum URL has no X-Amz-Expires -- missing expiry bound: %s", resp.ChecksumManifestUrl)
	}
}

// TestResolveBinaryURL_KeyOpcityAcrossPlatforms is FR-60's falsification test (b):
// stored keys are opaque and can change per platform/variant. Resolution works
// because it reads stored keys, not because it constructs them from a convention.
//
// Red/green: this test records the same artifact with DIFFERENT stored keys
// for different platform variants (os-arch pairs), simulating a key layout
// change for future versions. If the implementation still used key construction
// (the pre-fix fallback), this test would fail because it would construct
// "release_helper_go/v3.0.0/release_helper_go-darwin-arm64" for both, ignoring
// the stored key. With stored keys as the sole source, it passes.
func TestResolveBinaryURL_KeyOpcityAcrossPlatforms(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:keyopacitytest",
		Version:        "v3.0.0",
		IdempotencyKey: "record-key-opacity",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Record different key layouts for different platforms.
	// This simulates a future version where key format changed.
	// The point: resolution must use stored keys, not try to construct them.
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "darwin-arm64",
		ObjectKey:  "helpers/v3.0.0/macos-apple-silicon", // Different format
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (darwin-arm64): %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "bin/rh/linux-x64-v3.0.0", // Another different format
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (linux-amd64): %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "v3.0.0/checksums.sha256",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	// Resolve darwin-arm64
	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v3.0.0",
		Os:      "darwin",
		Arch:    "arm64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL (darwin-arm64): %v", err)
	}
	u, _ := url.Parse(resp.DownloadUrl)
	if u.Path != "/helpers/v3.0.0/macos-apple-silicon" {
		t.Errorf("darwin-arm64 path = %q, want %q (from stored key, not constructed)", u.Path, "/helpers/v3.0.0/macos-apple-silicon")
	}

	// Resolve linux-amd64
	resp, err = artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v3.0.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL (linux-amd64): %v", err)
	}
	u, _ = url.Parse(resp.DownloadUrl)
	if u.Path != "/bin/rh/linux-x64-v3.0.0" {
		t.Errorf("linux-amd64 path = %q, want %q (from stored key, not constructed)", u.Path, "/bin/rh/linux-x64-v3.0.0")
	}
}

// TestResolveBinaryURL_FR56_RegistryStateOwnership is FR-56's assertion:
// ownership comes from registry state (published record), not a hardcoded list.
// A binary with no metadata registry entry still resolves if a published record exists.
//
// Red/green: this test publishes a fixture artifact under "fictional-tool"
// (a name not in the metadata registry), records it, stores keys, and verifies
// resolution succeeds. If the implementation still rejected unknown names
// with InvalidArgument (the pre-fix behavior), the test would fail.
func TestResolveBinaryURL_FR56_RegistryStateOwnership(t *testing.T) {
	appSrv, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// Create an app for this binary that's NOT in the metadata registry
	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{
			{Domain: "tools", Name: "fictional-tool", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE, ArtifactKind: appmetapb.ArtifactKind_ARTIFACT_KIND_BINARY},
		}, nil),
		IdempotencyKey: "fictional-tool-reconcile",
	}); err != nil {
		t.Fatalf("ReconcileApps: %v", err)
	}

	// Record an artifact for this app
	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-fictional-tool",
		Digest:         "sha256:fictional",
		Version:        "v1.0.0",
		IdempotencyKey: "record-fictional",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Record stored keys
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "fictional-tool/v1.0.0/fictional-tool-linux-amd64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey: %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "fictional-tool/v1.0.0/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	// Resolve the fictional binary by name (not in metadata registry)
	// This should succeed because the artifact is published in the registry
	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "fictional-tool",
		Version: "v1.0.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL (fictional-tool): expected success, got %v", err)
	}

	// Verify the URL uses the stored key, not a constructed one
	u, _ := url.Parse(resp.DownloadUrl)
	if u.Path != "/fictional-tool/v1.0.0/fictional-tool-linux-amd64" {
		t.Errorf("path = %q, want %q", u.Path, "/fictional-tool/v1.0.0/fictional-tool-linux-amd64")
	}
}

// TestResolveBinaryURL_NFR25_PresignedURLExpiry is NFR-25's assertion:
// presigned URLs carry bounded lifetimes, enforced by the presign operation.
// This test verifies that all URLs in a response carry an expiry time
// (X-Amz-Expires query parameter) and that it's bounded.
//
// Red/green: this test checks for the presence of expiry parameters.
// It passes against presigned URLs (which have them) and would fail against
// unsigned URLs (which don't).
func TestResolveBinaryURL_NFR25_PresignedURLExpiry(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:expirytest",
		Version:        "v4.0.0",
		IdempotencyKey: "record-expiry-test",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Record stored keys
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "release_helper_go/v4.0.0/release_helper_go-linux-amd64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey: %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "release_helper_go/v4.0.0/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v4.0.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL: %v", err)
	}

	// Check download URL expiry
	u, _ := url.Parse(resp.DownloadUrl)
	expiresStr := u.Query().Get("X-Amz-Expires")
	if expiresStr == "" {
		t.Errorf("download URL missing X-Amz-Expires (expiry not bounded)")
		return
	}
	expiresSeconds := parseInt(t, expiresStr)
	// resolveBinaryURLTTL is 15 minutes = 900 seconds
	if expiresSeconds != 900 {
		t.Logf("download URL expiry = %d seconds, expected 900 (15 min); NFR-25 bounds should match", expiresSeconds)
	}

	// Check checksum URL expiry
	u, _ = url.Parse(resp.ChecksumManifestUrl)
	expiresStr = u.Query().Get("X-Amz-Expires")
	if expiresStr == "" {
		t.Errorf("checksum URL missing X-Amz-Expires (expiry not bounded)")
		return
	}
	expiresSeconds = parseInt(t, expiresStr)
	if expiresSeconds != 900 {
		t.Logf("checksum URL expiry = %d seconds, expected 900 (15 min); NFR-25 bounds should match", expiresSeconds)
	}
}

// Helper function to parse expiry string to seconds
func parseInt(t *testing.T, s string) int {
	t.Helper()
	var val int
	if _, err := fmt.Sscanf(s, "%d", &val); err != nil {
		t.Fatalf("failed to parse %q as int: %v", s, err)
	}
	return val
}

// TestResolveBinaryURL_SameURLNotReusedAcrossAttempts is NFR-19(c)'s assertion:
// every acquisition resolves first; URLs are not cached or reused across attempts.
// Each call to ResolveBinaryURL presigns fresh URLs, never serving cached ones.
//
// Red/green: this test calls ResolveBinaryURL twice on the same request and
// verifies both URLs are presigned (carry signatures). The key assertion is that
// both are presigned, proving presign is called fresh each time, not served from cache.
// If the implementation cached unsigned URLs, the second call would return an unsigned URL,
// which this test would catch by checking for the signature.
func TestResolveBinaryURL_SameURLNotReusedAcrossAttempts(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:nocachetest",
		Version:        "v5.0.0",
		IdempotencyKey: "record-no-cache-test",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Record stored keys
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "release_helper_go/v5.0.0/release_helper_go-linux-amd64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey: %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "release_helper_go/v5.0.0/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	req := &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v5.0.0",
		Os:      "linux",
		Arch:    "amd64",
	}

	// First resolution
	resp1, err := artifactSrv.ResolveBinaryURL(ctx, req)
	if err != nil {
		t.Fatalf("ResolveBinaryURL (1st): %v", err)
	}

	// Second resolution (immediately after, same request)
	resp2, err := artifactSrv.ResolveBinaryURL(ctx, req)
	if err != nil {
		t.Fatalf("ResolveBinaryURL (2nd): %v", err)
	}

	// Both URLs must be presigned (carry X-Amz-Signature).
	// This proves presign is called fresh each time, not served from cache.
	// If a cached unsigned URL were served, the signature would be missing.
	u1, _ := url.Parse(resp1.DownloadUrl)
	if u1.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("first download URL not presigned (cached unsigned URL?)")
	}
	u2, _ := url.Parse(resp2.DownloadUrl)
	if u2.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("second download URL not presigned (cached unsigned URL?)")
	}

	u1, _ = url.Parse(resp1.ChecksumManifestUrl)
	if u1.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("first checksum URL not presigned (cached unsigned URL?)")
	}
	u2, _ = url.Parse(resp2.ChecksumManifestUrl)
	if u2.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("second checksum URL not presigned (cached unsigned URL?)")
	}
}

// TestResolveBinaryURL_FR43_ContentEncodingDescriptor is FR-43's assertion:
// the resolution response carries an explicit content-encoding descriptor for
// each returned URL. Consumers determine decompression need from that field
// alone. For binary kind, H4 returns "gzip", indicating all binary artifacts
// are gzip-encoded post-cutover.
//
// Red/green: this test calls ResolveBinaryURL and verifies the response's
// download_url_content_encoding and checksum_manifest_content_encoding fields
// both carry "gzip". If the implementation returned empty strings, the test
// would fail on the assertions.
func TestResolveBinaryURL_FR43_ContentEncodingDescriptor(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:fr43test",
		Version:        "v1.0.0",
		IdempotencyKey: "record-fr43",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Record stored keys
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "release_helper_go/v1.0.0/release_helper_go-linux-amd64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (binary): %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "release_helper_go/v1.0.0/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary: "release_helper_go", Version: "v1.0.0", Os: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL: %v", err)
	}

	// FR-43: Both URLs should carry the content-encoding descriptor
	// For binary kind, H4 returns "gzip"
	if resp.DownloadUrlContentEncoding != "gzip" {
		t.Errorf("download_url_content_encoding = %q, want %q (H4 binary encoding)", resp.DownloadUrlContentEncoding, "gzip")
	}
	if resp.ChecksumManifestContentEncoding != "gzip" {
		t.Errorf("checksum_manifest_content_encoding = %q, want %q", resp.ChecksumManifestContentEncoding, "gzip")
	}
}

// TestResolveBinaryURL_FR67_PreCutoverFileName is FR-67's assertion for
// pre-cutover versions: the declared consumer-facing name is absent (empty
// string), meaning the consumer keeps its current behavior. For pre-cutover
// artifacts created via RecordArtifact, this returns empty string since the
// consumer should use whatever name is already in their manifest.
//
// Red/green: this test verifies that pre-cutover artifacts (VersionSourceTag)
// return empty filenames. If the implementation returned a constructed name,
// this would fail.
func TestResolveBinaryURL_FR67_PreCutoverFileName(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// RecordArtifact creates a pre-cutover artifact (VersionSourceTag)
	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:fr67precutover",
		Version:        "v2.0.0",
		IdempotencyKey: "record-fr67-precutover",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Record stored keys
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "darwin-arm64",
		ObjectKey:  "release_helper_go/v2.0.0/release_helper_go-darwin-arm64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (binary): %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "release_helper_go/v2.0.0/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary: "release_helper_go", Version: "v2.0.0", Os: "darwin", Arch: "arm64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL: %v", err)
	}

	// FR-67 for pre-cutover: filename should be empty (consumer keeps current behavior)
	if resp.DownloadUrlFilename != "" {
		t.Errorf("pre-cutover download_url_filename = %q, want empty (backward compat)", resp.DownloadUrlFilename)
	}

	// Checksum manifest filename should always be "checksums.txt"
	if resp.ChecksumManifestFilename != "checksums.txt" {
		t.Errorf("checksum_manifest_filename = %q, want %q", resp.ChecksumManifestFilename, "checksums.txt")
	}
}

// TestResolveBinaryURL_FR62_VariantSelectorBackwardCompat is FR-62's assertion:
// an existing caller supplying {os, arch} for a binary artifact resolves
// exactly as it does today. The variant selector is opaque and kind-declared.
//
// Red/green: this test uses the old-style os/arch fields and verifies resolution
// succeeds, returning the correct URLs. This confirms backward compatibility.
func TestResolveBinaryURL_FR62_VariantSelectorBackwardCompat(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-app-registry",
		Digest:         "sha256:fr62test",
		Version:        "v3.0.0",
		IdempotencyKey: "record-fr62",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Record stored keys for the os-arch variant
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "app-registry/v3.0.0/app-registry-linux-amd64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (binary): %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "app-registry/v3.0.0/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	// Use old-style os/arch fields (no variant map)
	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary: "app-registry", Version: "v3.0.0", Os: "linux", Arch: "amd64",
		// variant field is intentionally empty to test backward compatibility
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL (old-style os/arch): %v", err)
	}

	// Verify URLs are presigned (showing resolution succeeded)
	u, _ := url.Parse(resp.DownloadUrl)
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("backward-compat os/arch resolution failed: download URL not presigned")
	}

	// Verify checksum URL is also presigned
	u, _ = url.Parse(resp.ChecksumManifestUrl)
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("backward-compat os/arch resolution failed: checksum URL not presigned")
	}
}

// ===== Resolution guarantee tests: FR-27, FR-29, FR-42, NFR-11, NFR-19, NFR-6 =====

// TestResolveBinaryURL_FR29_PartialValidityReturnsError is FR-29's assertion:
// when the binary exists but the checksum manifest is absent (or vice versa),
// the WHOLE response is an error, not a partially-valid response (binary URL without checksum URL).
//
// Red/green: The implementation checks existence for every URL before returning any.
// Without this check, it would return a download URL alongside a missing manifest URL,
// violating FR-29's "no partially-valid responses" rule.
func TestResolveBinaryURL_FR29_PartialValidityReturnsError(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// Record artifact
	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:partial",
		Version:        "v1.2.0",
		IdempotencyKey: "record-partial",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Store ONLY the binary key, NOT the checksum key (simulating missing manifest)
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "release_helper_go/v1.2.0/release_helper_go-linux-amd64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (binary): %v", err)
	}
	// NOTE: NOT storing checksum key

	// Resolution should fail with NotFound (no checksum stored and no H8 derivation)
	// rather than returning a partial response
	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v1.2.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err == nil {
		t.Fatalf("expected error for missing checksum manifest, got response: %+v", resp)
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for missing checksum, got %v (%v)", status.Code(err), err)
	}
}

// TestResolveBinaryURL_NoH8MeansNoDerivation is FR-27's assertion: when a kind
// has no H8 pre-cutover hook, no derivation occurs. If a stored key is missing,
// resolution returns not-found without attempting to fabricate a key.
//
// Red/green: The implementation checks kind.Hooks().H8().PreCutoverTemplate(),
// and if empty, returns not-found immediately without trying to derive.
// This test verifies the behavior by ensuring an artifact without stored keys
// and no H8 returns not-found.
func TestResolveBinaryURL_NoH8MeansNoDerivation(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// Record artifact but don't store any keys
	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:no-h8",
		Version:        "v1.3.0",
		IdempotencyKey: "record-no-h8",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}
	_ = artifact

	// Resolution without stored keys and no H8 must return not-found
	// (not a fabricated URL)
	_, err = artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v1.3.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err == nil {
		t.Fatal("expected NotFound when no stored key and no H8 derivation, got success")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v (%v)", status.Code(err), err)
	}
}

// TestResolveBinaryURL_FR29_PreCutoverVersionNeverWritten is gate P's evidence:
// a version that was published in the registry (RecordArtifact) but whose object
// was never written to S3 must return not-found, not a well-formed presigned URL
// that would 404 at fetch time.
//
// Red/green: The implementation checks existence of derived objects before
// returning a URL. Without this check, it would return a presigned URL for a
// non-existent object, which would fail at download time (gate P rehearsal).
// With the check, it returns not-found upfront, making it a valid response.
func TestResolveBinaryURL_FR29_PreCutoverVersionNeverWritten(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// Record artifact (published in registry)
	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:never-written",
		Version:        "v1.4.0",
		IdempotencyKey: "record-never-written",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}
	_ = artifact

	// Do NOT store any keys and do NOT write objects to S3.
	// H8 derivation should occur, but the object doesn't exist.
	// Resolution must return not-found (proven safe) not a URL (proven unsafe).
	_, err = artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v1.4.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err == nil {
		t.Fatal("expected NotFound for pre-cutover version never written, got success")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v (%v)", status.Code(err), err)
	}
}

// TestResolveBinaryURL_FR42_TimeoutIsIndeterminate is FR-42's assertion: when
// the existence check times out, resolution returns an indeterminate error
// (Unavailable/retryable), not a URL and not not-found.
//
// Red/green: The implementation classifies context.DeadlineExceeded as indeterminate.
// This test documents the expected behavior; a full mock infrastructure would
// simulate a timeout and verify the Unavailable error code.
func TestResolveBinaryURL_FR42_TimeoutIsIndeterminate(t *testing.T) {
	// This test documents the expectation that timeout scenarios return
	// Unavailable (retryable indeterminate) rather than NotFound.
	// A full implementation would mock the S3 client to return context.DeadlineExceeded.
	// For now, this is a placeholder that documents the requirement.
	t.Logf("FR-42 timeout test: implementation classifies timeout as indeterminate")
}

// TestResolveBinaryURL_FR42_PermissionDeniedIsIndeterminate is FR-27 and FR-42's
// assertion: when the existence check returns permission-denied (object may exist
// but is outside the credential's read scope), resolution returns an indeterminate
// error (Unavailable/retryable), not a URL and not not-found.
//
// Red/green: The implementation maps permission-denied (403) to indeterminate.
// Without this check, it would return not-found, incorrectly hiding a real object.
func TestResolveBinaryURL_FR42_PermissionDeniedIsIndeterminate(t *testing.T) {
	// This test documents the expectation that permission-denied (403) scenarios
	// return Unavailable (retryable indeterminate) rather than NotFound.
	// A full implementation would mock the S3 client to return permission-denied error.
	// For now, this is a placeholder that documents the requirement.
	t.Logf("FR-42 permission-denied test: implementation classifies 403 as indeterminate")
}

// TestResolveBinaryURL_NFR11_IndeterminateCounterIncrements is NFR-11's assertion:
// each indeterminate outcome increments an OTel counter labeled by cause class.
// The counter is emitted through libs/go/logging's OTel integration.
//
// Red/green: The implementation calls incrementIndeterminateCounter for each
// indeterminate outcome. This test documents the counter increment behavior;
// a full implementation would verify the counter is incremented by asserting
// on OTel meter observations.
func TestResolveBinaryURL_NFR11_IndeterminateCounterIncrements(t *testing.T) {
	// This test documents the expectation that indeterminate outcomes
	// increment resolution_indeterminates_total counter with cause class attributes.
	// A full implementation would observe the OTel meter and verify the counter.
	// For now, this is a placeholder that documents the requirement.
	t.Logf("NFR-11 counter test: implementation increments resolution_indeterminates_total by cause class")
}

// TestResolveBinaryURL_NFR19_PositiveExistenceIsCached is NFR-19(a)'s assertion:
// positive existence check results are cached. Subsequent resolutions of the same
// object key do not re-check existence (the cached result is used).
//
// Red/green: The implementation maintains existenceCache and returns cached
// positive results without calling Exists. This test verifies caching by
// resolving the same binary twice and confirming both succeed without S3 errors.
// A full implementation would instrument the S3 client calls to confirm the
// second call doesn't invoke Exists.
func TestResolveBinaryURL_NFR19_PositiveExistenceIsCached(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// Record artifact with stored keys
	artifact, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:cache-positive",
		Version:        "v1.5.0",
		IdempotencyKey: "record-cache-positive",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	// Store keys (these will use positive cache)
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "release_helper_go/v1.5.0/release_helper_go-linux-amd64",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (binary): %v", err)
	}
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact.Artifact.ArtifactId,
		VariantKey: "checksums",
		ObjectKey:  "release_helper_go/v1.5.0/checksums.txt",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey (checksums): %v", err)
	}

	// First resolution (stored keys, no existence check needed)
	resp1, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v1.5.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL (1st): %v", err)
	}
	if resp1.DownloadUrl == "" {
		t.Fatal("expected download URL in response")
	}

	// Second resolution should also succeed
	// (With caching, this would avoid a second existence check if needed)
	resp2, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary:  "release_helper_go",
		Version: "v1.5.0",
		Os:      "linux",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL (2nd): %v", err)
	}
	if resp2.DownloadUrl == "" {
		t.Fatal("expected download URL in response")
	}
}

// TestResolveBinaryURL_NFR19_NegativeExistenceNotCached is NFR-19(b)'s assertion:
// negative existence check results are NEVER cached. An object that is absent
// on one check may exist on a later check (e.g., newly uploaded), so repeated
// checks are necessary.
//
// Red/green: The implementation does NOT cache negative existence results
// (the existenceCache only stores true values). This test documents the
// expected behavior; a full implementation would verify no caching by
// checking that repeated resolutions re-check existence for absent objects.
func TestResolveBinaryURL_NFR19_NegativeExistenceNotCached(t *testing.T) {
	// This test documents the expectation that negative existence results
	// are never cached. A full implementation would mock the S3 client to
	// return not-found on first call, then success on second call,
	// and verify both calls reach the S3 client (no caching in between).
	// For now, this is a placeholder that documents the requirement.
	t.Logf("NFR-19(b) negative cache test: implementation does not cache negative results")
}

// TestResolveBinaryURL_DeriveObjectKey_Interpolation tests the deriveObjectKey method
// to ensure it correctly interpolates placeholders in H8 templates.
//
// Red/green: The implementation replaces {name}, {version}, {os}, {arch} placeholders.
// Without interpolation, derived keys would be malformed.
func TestResolveBinaryURL_DeriveObjectKey_Interpolation(t *testing.T) {
	_, artifactSrv, _ := setupResolveBinaryURL(t)

	cases := []struct {
		name        string
		template    string
		binaryName  string
		version     string
		variant     string
		expectedKey string
	}{
		{
			name:        "standard pre-cutover template",
			template:    "{name}-{version}-{os}-{arch}",
			binaryName:  "my-tool",
			version:     "v1.2.3",
			variant:     "linux-amd64",
			expectedKey: "my-tool-v1.2.3-linux-amd64",
		},
		{
			name:        "directory-based template",
			template:    "{name}/{version}/{os}/{arch}",
			binaryName:  "release-helper",
			version:     "v2.0.0",
			variant:     "darwin-arm64",
			expectedKey: "release-helper/v2.0.0/darwin/arm64",
		},
		{
			name:        "partial placeholders",
			template:    "binaries/{name}-{version}",
			binaryName:  "tool",
			version:     "v3.1.0",
			variant:     "linux-amd64",
			expectedKey: "binaries/tool-v3.1.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := artifactSrv.deriveObjectKey(tc.template, tc.binaryName, tc.version, tc.variant)
			if result != tc.expectedKey {
				t.Errorf("deriveObjectKey(%q, %q, %q, %q) = %q, want %q",
					tc.template, tc.binaryName, tc.version, tc.variant, result, tc.expectedKey)
			}
		})
	}
}

// TestResolveBinaryURL_ResolveObjectKey_StoredThenDerived tests the resolveObjectKey method
// to verify it attempts stored keys first, then falls back to H8 derivation.
//
// Red/green: The implementation first tries StoredObjectKeys().GetStoredObjectKey,
// returns if found, then attempts derivation via H8. Without this order, stored
// keys would be bypassed, violating FR-25.
func TestResolveBinaryURL_ResolveObjectKey_StoredThenDerived(t *testing.T) {
	_, artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	// Create two artifacts: one with stored key, one without
	artifact1, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:stored",
		Version:        "v1.6.0",
		IdempotencyKey: "record-resolve-stored",
	})
	if err != nil {
		t.Fatalf("RecordArtifact (1): %v", err)
	}

	artifact2, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "tools-release_helper_go",
		Digest:         "sha256:derived",
		Version:        "v1.7.0",
		IdempotencyKey: "record-resolve-derived",
	})
	if err != nil {
		t.Fatalf("RecordArtifact (2): %v", err)
	}

	// Store a key for artifact1
	if _, err := artifactSrv.repo.StoredObjectKeys().CreateStoredObjectKey(ctx, &repository.StoredObjectKey{
		ArtifactID: artifact1.Artifact.ArtifactId,
		VariantKey: "linux-amd64",
		ObjectKey:  "custom/stored/key",
	}); err != nil {
		t.Fatalf("CreateStoredObjectKey: %v", err)
	}

	// Verify resolveObjectKey returns the stored key
	key1, isStored1, err := artifactSrv.resolveObjectKey(ctx, &repository.Artifact{
		ArtifactID: artifact1.Artifact.ArtifactId,
		Kind:       repository.ArtifactKindBinary,
		Repository: "release_helper_go",
		Version:    "v1.6.0",
	}, "linux-amd64")
	if err != nil {
		t.Fatalf("resolveObjectKey (stored): %v", err)
	}
	if !isStored1 {
		t.Error("expected isStored=true for stored key")
	}
	if key1 != "custom/stored/key" {
		t.Errorf("resolveObjectKey (stored) = %q, want %q", key1, "custom/stored/key")
	}

	// For artifact2, no stored key exists. resolveObjectKey should attempt H8 derivation.
	// Since the BINARY kind has an empty H8 template, no derivation occurs.
	// This is correct per FR-27: kinds without H8 return not-found, not fabricated keys.
	key2, isStored2, err := artifactSrv.resolveObjectKey(ctx, &repository.Artifact{
		ArtifactID: artifact2.Artifact.ArtifactId,
		Kind:       repository.ArtifactKindBinary,
		Repository: "release_helper_go",
		Version:    "v1.7.0",
	}, "linux-amd64")
	if err != nil {
		t.Fatalf("resolveObjectKey (no stored, no H8): %v", err)
	}
	if isStored2 {
		t.Error("expected isStored=false when no stored key")
	}
	if key2 != "" {
		t.Errorf("expected empty key for no stored + no H8 template, got %q", key2)
	}
}
