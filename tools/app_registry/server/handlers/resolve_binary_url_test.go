package handlers

import (
	"net/url"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
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
func setupResolveBinaryURL(t *testing.T) (*ArtifactServer, *pb.Build) {
	t.Helper()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	ctx := authedCtx()
	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{
			{Domain: "tools", Name: "release_helper_go", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE},
			{Domain: "tools", Name: "app-registry", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE},
		}, nil),
		IdempotencyKey: "setup-resolve-binary-url",
	}); err != nil {
		t.Fatalf("reconcile tools apps: %v", err)
	}

	artifactSrv := NewArtifactServer(repo, WithReleaseToolsS3(
		"release-tools-bucket", "https://s3.example.com/",
		"us-east-1", "test-access-key", "test-secret-key",
	))
	build := recordBuild(t, artifactSrv, "run-resolve-binary-url")
	return artifactSrv, build
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
	artifactSrv, build := setupResolveBinaryURL(t)
	ctx := authedCtx()

	if _, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName: "tools-release_helper_go", Digest: "sha256:resolvebinary", Version: "v1.2.3",
		IdempotencyKey: "record-resolve-binary",
	}); err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	resp, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary: "release_helper_go", Version: "v1.2.3", Os: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatalf("ResolveBinaryURL: %v", err)
	}

	wantHost := "release-tools-bucket.s3.example.com"
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
	artifactSrv, _ := setupResolveBinaryURL(t)
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

// TestResolveBinaryURL_InvalidBinaryNameIsRejected covers metadata-based
// binary resolution: any binary name not in the metadata registry
// (i.e., no app with ArtifactKindBinary has that Name) is
// rejected with InvalidArgument before any repository lookup happens.
// This replaces the prior hardcoded allowlist with FR-36's metadata lookups.
func TestResolveBinaryURL_InvalidBinaryNameIsRejected(t *testing.T) {
	artifactSrv, _ := setupResolveBinaryURL(t)
	ctx := authedCtx()

	_, err := artifactSrv.ResolveBinaryURL(ctx, &pb.ResolveBinaryURLRequest{
		Binary: "not-a-real-binary", Version: "v1.0.0", Os: "linux", Arch: "amd64",
	})
	if err == nil {
		t.Fatal("expected an error for an unknown binary name")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
}

// TestResolveBinaryURL_MissingFieldsAreRejected covers the remaining
// request-shape validation (os/arch/version required) ahead of any
// repository call.
func TestResolveBinaryURL_MissingFieldsAreRejected(t *testing.T) {
	artifactSrv, _ := setupResolveBinaryURL(t)
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
