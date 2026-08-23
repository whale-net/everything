package handlers

import (
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
// configured with a fake public S3 bucket/endpoint via WithReleaseToolsS3 so
// PublicURL construction can be asserted without any real network call.
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

	artifactSrv := NewArtifactServer(repo, WithReleaseToolsS3("release-tools-bucket", "https://s3.example.com/"))
	build := recordBuild(t, artifactSrv, "run-resolve-binary-url")
	return artifactSrv, build
}

// TestResolveBinaryURL_KnownBinaryAndVersion is the FR12-FR14 happy path:
// a published binary+version+os+arch resolves the exact key-convention URLs
// documented on ResolveBinaryURL (issue #979/#983's "Design" section) --
// "<binary>/<version>/<binary>-<os>-<arch>" for the download and
// "<binary>/<version>/checksums.txt" for the manifest, both rooted at the
// configured public endpoint + bucket.
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

	if _, err := artifactSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName: "tools-release_helper_go", Version: "v1.2.3",
		Repository:     "github.com/whale-net/everything",
		IdempotencyKey: "record-resolve-binary-begin",
	}); err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}

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

	wantDownload := "https://s3.example.com/release-tools-bucket/release_helper_go/v1.2.3/release_helper_go-linux-amd64"
	wantChecksum := "https://s3.example.com/release-tools-bucket/release_helper_go/v1.2.3/checksums.txt"
	if resp.DownloadUrl != wantDownload {
		t.Errorf("download_url = %q, want %q", resp.DownloadUrl, wantDownload)
	}
	if resp.ChecksumManifestUrl != wantChecksum {
		t.Errorf("checksum_manifest_url = %q, want %q", resp.ChecksumManifestUrl, wantChecksum)
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

// TestResolveBinaryURL_InvalidBinaryNameIsRejected covers the binaryOwnerFullName
// allowlist: any binary name outside {release_helper_go, app-registry} is
// rejected with InvalidArgument before any repository lookup happens.
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
