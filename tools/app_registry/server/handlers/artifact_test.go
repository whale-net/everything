package handlers

import (
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// setup builds an AppServer and ArtifactServer sharing one fake registry,
// with one app per DeployUnit so promotability derivation can be exercised
// for all three cases in one place.
func setup(t *testing.T) (*AppServer, *ArtifactServer, map[string]*pb.App) {
	t.Helper()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	artifactSrv := NewArtifactServer(repo)
	ctx := authedCtx()

	reconcile, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{
			{Domain: "demo", Name: "chart-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART},
			{Domain: "demo", Name: "image-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
			{Domain: "demo", Name: "none-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE},
		}, []*appmetapb.ChartManifest{
			{Domain: "demo", Name: "achart", Apps: []string{"chart-app"}},
		}),
		IdempotencyKey: "setup-1",
	})
	if err != nil {
		t.Fatalf("setup reconcile: %v", err)
	}

	apps := map[string]*pb.App{}
	for _, a := range reconcile.CreatedApps {
		apps[a.Name] = a
	}
	apps["achart"] = nil // placeholder; charts tracked separately below
	for _, c := range reconcile.CreatedCharts {
		_ = c
	}
	return appSrv, artifactSrv, apps
}

func recordBuild(t *testing.T, srv *ArtifactServer, runID string) *pb.Build {
	t.Helper()
	resp, err := srv.RecordBuild(authedCtx(), &pb.RecordBuildRequest{
		GitSha: "abc123", WorkflowRunId: runID, IdempotencyKey: runID + "-build",
	})
	if err != nil {
		t.Fatalf("RecordBuild: %v", err)
	}
	return resp.Build
}

// TestRecordArtifact_PromotabilityDerivation covers all three DeployUnit
// rows of ARCHITECTURE.md's promotability table for image artifacts, plus
// charts always being PROMOTABLE.
func TestRecordArtifact_PromotabilityDerivation(t *testing.T) {
	_, artifactSrv, apps := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-promo")

	cases := []struct {
		appName string
		want    pb.Promotability
	}{
		{"chart-app", pb.Promotability_PROMOTABILITY_VIA_CHART},
		{"image-app", pb.Promotability_PROMOTABILITY_PROMOTABLE},
		{"none-app", pb.Promotability_PROMOTABILITY_NOT_PROMOTABLE},
	}
	for _, tc := range cases {
		t.Run(tc.appName, func(t *testing.T) {
			resp, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
				BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
				OwnerFullName: "demo-" + tc.appName, Digest: "sha256:" + tc.appName, Version: "v1.0.0",
				IdempotencyKey: "record-" + tc.appName,
			})
			if err != nil {
				t.Fatalf("RecordArtifact(%s): %v", tc.appName, err)
			}
			if resp.Artifact.Promotability != tc.want {
				t.Fatalf("%s: expected promotability %v, got %v", tc.appName, tc.want, resp.Artifact.Promotability)
			}
		})
	}
	_ = apps
}

func TestRecordArtifact_ChartIsAlwaysPromotable(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-chart-promo")

	imgResp, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-chart-app", Digest: "sha256:imageforchart", Version: "v1.0.0",
		IdempotencyKey: "record-image-for-chart",
	})
	if err != nil {
		t.Fatalf("record image: %v", err)
	}

	chartResp, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "demo-achart", Digest: "sha256:chart1", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "demo-chart-app", Repository: "repo", Version: "v1.0.0", Digest: imgResp.Artifact.Digest},
		},
		IdempotencyKey: "record-chart1",
	})
	if err != nil {
		t.Fatalf("record chart: %v", err)
	}
	if chartResp.Artifact.Promotability != pb.Promotability_PROMOTABILITY_PROMOTABLE {
		t.Fatalf("expected chart PROMOTABLE, got %v", chartResp.Artifact.Promotability)
	}
}

// TestRecordArtifact_RejectsChartPinningUnrecordedImage is required coverage:
// a chart artifact may not reference an image digest that was never recorded.
func TestRecordArtifact_RejectsChartPinningUnrecordedImage(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-reject")

	_, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "demo-achart", Digest: "sha256:chartreject", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "demo-chart-app", Repository: "repo", Version: "v1.0.0", Digest: "sha256:neverrecorded"},
		},
		IdempotencyKey: "record-reject",
	})
	if err == nil {
		t.Fatal("expected an error for a chart pinning an unrecorded image digest")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "sha256:neverrecorded") {
		t.Fatalf("expected error to name the unrecorded digest, got %q", err.Error())
	}
}

// TestRecordArtifact_UnknownOwnerNamesTheOwnerAndHintsAtReconcile covers
// issue #548: resolveOwner used to return the bare repository.ErrInvalidArgument
// sentinel when the owner_full_name wasn't found, so a caller saw only
// "invalid argument" with no indication that the app/chart simply hadn't
// been reconciled yet. The fix must keep the status code InvalidArgument
// (mapRepoErr's errors.Is mapping must not change) while making the message
// name the owner and point at reconciliation as the likely cause.
func TestRecordArtifact_UnknownOwnerNamesTheOwnerAndHintsAtReconcile(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-unknown-owner")

	cases := []struct {
		name          string
		kind          pb.ArtifactKind
		ownerFullName string
	}{
		{"unknown app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "demo-never-reconciled-app"},
		{"unknown chart", pb.ArtifactKind_ARTIFACT_KIND_CHART, "demo-never-reconciled-chart"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
				BuildId: build.BuildId, Kind: tc.kind,
				OwnerFullName: tc.ownerFullName, Digest: "sha256:" + tc.name, Version: "v1.0.0",
				IdempotencyKey: "record-" + tc.name,
			})
			if err == nil {
				t.Fatalf("expected an error for unknown owner %q", tc.ownerFullName)
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
			}
			msg := status.Convert(err).Message()
			if msg == "invalid argument" {
				t.Fatalf("expected a message naming the owner, got the bare sentinel message %q", msg)
			}
			if !strings.Contains(msg, tc.ownerFullName) {
				t.Fatalf("expected message to contain owner %q, got %q", tc.ownerFullName, msg)
			}
			if !strings.Contains(msg, "reconciled") {
				t.Fatalf("expected message to hint at reconciliation, got %q", msg)
			}
		})
	}
}

// TestRecordArtifact_DuplicateOwnerKindVersionIsAlreadyExists covers the
// unique_violation mapping (SQLSTATE 23505 -> codes.AlreadyExists) that Fix 1
// adds centrally to the postgres layer: recording a second, differently-
// digested artifact for an (owner, kind, version) already claimed is a
// conflict a caller (e.g. AR-5's AllocateVersion) should retry with a
// different version, not codes.Internal — which would look like a server bug
// and invite a pointless retry of the exact same request. The fake enforces
// the same (owner, kind, version) uniqueness postgres's artifact_version_idx
// does, so this test predicts real behaviour.
func TestRecordArtifact_DuplicateOwnerKindVersionIsAlreadyExists(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-dup")

	first := &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:first", Version: "v1.2.3",
		IdempotencyKey: "record-dup-1",
	}
	if _, err := artifactSrv.RecordArtifact(ctx, first); err != nil {
		t.Fatalf("first RecordArtifact: %v", err)
	}

	// Same owner, kind, and version; a different digest and a fresh
	// idempotency key, so this is not an idempotent replay — it is a
	// genuine conflict the unique index rejects.
	second := &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:different", Version: "v1.2.3",
		IdempotencyKey: "record-dup-2",
	}
	_, err := artifactSrv.RecordArtifact(ctx, second)
	if err == nil {
		t.Fatal("expected an error recording a duplicate (owner, kind, version) with a different digest")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v (%v)", status.Code(err), err)
	}
}

// TestResolveArtifact_ReturnsImagesForChart is required coverage: resolving
// a chart artifact returns the correct pinned image artifacts and builds.
func TestResolveArtifact_ReturnsImagesForChart(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-resolve")

	img, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-chart-app", Digest: "sha256:resolveimg", Version: "v1.2.3",
		IdempotencyKey: "record-resolve-img",
	})
	if err != nil {
		t.Fatalf("record image: %v", err)
	}

	chart, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "demo-achart", Digest: "sha256:resolvechart", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "demo-chart-app", Repository: "repo", Version: "v1.2.3", Digest: img.Artifact.Digest},
		},
		IdempotencyKey: "record-resolve-chart",
	})
	if err != nil {
		t.Fatalf("record chart: %v", err)
	}

	resolved, err := artifactSrv.ResolveArtifact(ctx, &pb.ResolveArtifactRequest{ArtifactId: chart.Artifact.ArtifactId})
	if err != nil {
		t.Fatalf("ResolveArtifact: %v", err)
	}
	if len(resolved.Images) != 1 || resolved.Images[0].Digest != "sha256:resolveimg" {
		t.Fatalf("expected 1 resolved image with digest sha256:resolveimg, got %+v", resolved.Images)
	}
	if len(resolved.Builds) != 1 || resolved.Builds[0].BuildId != build.BuildId {
		t.Fatalf("expected the image's originating build, got %+v", resolved.Builds)
	}
}

// TestRecordArtifact_IdempotencyReplaysWithoutDoubleWrite proves a repeated
// RecordArtifact call with the same idempotency_key does not create a second
// row, asserted by ListArtifacts count.
func TestRecordArtifact_IdempotencyReplaysWithoutDoubleWrite(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-idem")

	req := &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:idem", Version: "v1.0.0",
		IdempotencyKey: "idem-key",
	}
	if _, err := artifactSrv.RecordArtifact(ctx, req); err != nil {
		t.Fatalf("first RecordArtifact: %v", err)
	}
	if _, err := artifactSrv.RecordArtifact(ctx, req); err != nil {
		t.Fatalf("replayed RecordArtifact: %v", err)
	}

	list, err := artifactSrv.ListArtifacts(ctx, &pb.ListArtifactsRequest{})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(list.Artifacts) != 1 {
		t.Fatalf("expected exactly 1 artifact row after replay, got %d", len(list.Artifacts))
	}
}

func TestRecordBuild_IdempotencyReplaysWithoutDoubleWrite(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()

	req := &pb.RecordBuildRequest{GitSha: "abc", WorkflowRunId: "dup-run", IdempotencyKey: "dup-key"}
	first, err := artifactSrv.RecordBuild(ctx, req)
	if err != nil {
		t.Fatalf("first RecordBuild: %v", err)
	}
	second, err := artifactSrv.RecordBuild(ctx, req)
	if err != nil {
		t.Fatalf("replayed RecordBuild: %v", err)
	}
	if second.Build.BuildId != first.Build.BuildId {
		t.Fatalf("expected replay to return the same build, got %s vs %s", second.Build.BuildId, first.Build.BuildId)
	}
}

func TestRecordArtifact_RejectsMissingIdempotencyKey(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-noidem")

	_, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:noidem", Version: "v1.0.0",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing idempotency_key, got %v", err)
	}
}

// setupAllocate builds its own fake registry (rather than reusing setup(t))
// because AllocateVersion tests need direct access to the fake so they can
// set a domain's adoption stage via fake.Registry.SetDomainAdoptionStage —
// there is no RPC to do that in this change (see PLAN.md's AR-5 status).
func setupAllocate(t *testing.T) (*fake.Registry, *ArtifactServer) {
	t.Helper()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	artifactSrv := NewArtifactServer(repo)
	ctx := authedCtx()

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{
			{Domain: "demo", Name: "image-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
		}, nil),
		IdempotencyKey: "setup-allocate-1",
	}); err != nil {
		t.Fatalf("setupAllocate reconcile: %v", err)
	}
	return repo, artifactSrv
}

// TestAllocateVersion_ValidationErrors covers the request-shape checks that
// run before the adoption-stage gate or any repository call.
func TestAllocateVersion_ValidationErrors(t *testing.T) {
	_, artifactSrv := setupAllocate(t)
	ctx := authedCtx()

	cases := []struct {
		name string
		req  *pb.AllocateVersionRequest
	}{
		{"missing kind", &pb.AllocateVersionRequest{OwnerFullName: "demo-image-app", Increment: "patch", IdempotencyKey: "k"}},
		{"missing owner", &pb.AllocateVersionRequest{Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, Increment: "patch", IdempotencyKey: "k"}},
		{"missing idempotency key", &pb.AllocateVersionRequest{Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app", Increment: "patch"}},
		{"bad increment", &pb.AllocateVersionRequest{Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app", Increment: "bogus", IdempotencyKey: "k"}},
		{"prerelease explicit_version", &pb.AllocateVersionRequest{Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app", ExplicitVersion: "v1.2.3-alpha", IdempotencyKey: "k"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := artifactSrv.AllocateVersion(ctx, tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v", err)
			}
		})
	}
}

// TestAllocateVersion_UnknownOwnerNamesTheOwnerAndHintsAtReconcile covers
// resolveOwnerAndDomain (artifact.go), the AllocateVersion sibling of
// resolveOwner fixed for issue #548: an unknown owner must still map to
// InvalidArgument, but the message must name the owner and hint that it may
// simply not have been reconciled yet, not just say "unknown app ...".
func TestAllocateVersion_UnknownOwnerNamesTheOwnerAndHintsAtReconcile(t *testing.T) {
	_, artifactSrv := setupAllocate(t)
	ctx := authedCtx()

	_, err := artifactSrv.AllocateVersion(ctx, &pb.AllocateVersionRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-never-reconciled-app",
		Increment: "patch", IdempotencyKey: "allocate-unknown-owner",
	})
	if err == nil {
		t.Fatal("expected an error for an unknown owner")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
	msg := status.Convert(err).Message()
	if !strings.Contains(msg, "demo-never-reconciled-app") {
		t.Fatalf("expected message to contain owner name, got %q", msg)
	}
	if !strings.Contains(msg, "reconciled") {
		t.Fatalf("expected message to hint at reconciliation, got %q", msg)
	}
}

// TestAllocateVersion_AdoptionStageGate proves the per-domain cutover gate:
// a domain that has never been set to "allocate" (every domain, as of
// AR-5a — see PLAN.md's AR-5 status) is rejected with FailedPrecondition,
// and only after the fixture explicitly cuts "demo" over does the same
// request succeed. This is the mechanism a future phase would use to cut a
// real domain over — see fake.Registry.SetDomainAdoptionStage's doc comment
// and postgres_integration_test.go's version of this same proof against a
// real domain_adoption row.
func TestAllocateVersion_AdoptionStageGate(t *testing.T) {
	repo, artifactSrv := setupAllocate(t)
	ctx := authedCtx()

	req := &pb.AllocateVersionRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Increment: "patch", IdempotencyKey: "gate-1",
	}
	_, err := artifactSrv.AllocateVersion(ctx, req)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for a domain not at stage 'allocate', got %v", err)
	}

	repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStageAllocate)
	resp, err := artifactSrv.AllocateVersion(ctx, req)
	if err != nil {
		t.Fatalf("expected success once 'demo' is cut over to 'allocate', got %v", err)
	}
	if resp.Version != "v0.0.1" {
		t.Fatalf("expected first patch allocation to be v0.0.1, got %q", resp.Version)
	}
}

// TestAllocateVersion_IncrementsFromLatestRecordedArtifact proves
// AllocateVersion accounts for artifacts already recorded via
// RecordArtifact, not just prior allocations, and that major/minor/patch
// all behave as PLAN.md's AR-5 addendum item 1 specifies.
func TestAllocateVersion_IncrementsFromLatestRecordedArtifact(t *testing.T) {
	cases := []struct {
		increment string
		want      string
	}{
		{"patch", "v1.2.4"},
		{"minor", "v1.3.0"},
		{"major", "v2.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.increment, func(t *testing.T) {
			// A fresh registry per case: each must increment from the SAME
			// seeded v1.2.3, not from a previous subtest's allocation.
			repo, artifactSrv := setupAllocate(t)
			ctx := authedCtx()
			repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStageAllocate)

			build := recordBuild(t, artifactSrv, "run-allocate-seed-"+tc.increment)
			if _, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
				BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
				OwnerFullName: "demo-image-app", Digest: "sha256:seed-" + tc.increment, Version: "v1.2.3",
				IdempotencyKey: "seed-artifact-" + tc.increment,
			}); err != nil {
				t.Fatalf("seed RecordArtifact: %v", err)
			}

			resp, err := artifactSrv.AllocateVersion(ctx, &pb.AllocateVersionRequest{
				Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
				Increment: tc.increment, IdempotencyKey: "alloc-" + tc.increment,
			})
			if err != nil {
				t.Fatalf("AllocateVersion(%s): %v", tc.increment, err)
			}
			if resp.Version != tc.want {
				t.Fatalf("AllocateVersion(%s) = %q, want %q", tc.increment, resp.Version, tc.want)
			}
			if resp.PreviousVersion != "v1.2.3" {
				t.Fatalf("AllocateVersion(%s): expected previous_version v1.2.3, got %q", tc.increment, resp.PreviousVersion)
			}
		})
	}
}

// TestAllocateVersion_IdempotencyKeyReplay proves a repeated call with the
// same idempotency_key returns the SAME version with already_allocated set,
// rather than allocating a second version — the AllocateVersion analogue of
// TestRecordBuild_IdempotencyKeyReplay_DoesNotDoubleWrite.
func TestAllocateVersion_IdempotencyKeyReplay(t *testing.T) {
	repo, artifactSrv := setupAllocate(t)
	ctx := authedCtx()
	repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStageAllocate)

	req := &pb.AllocateVersionRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Increment: "patch", IdempotencyKey: "replay-key",
	}
	first, err := artifactSrv.AllocateVersion(ctx, req)
	if err != nil {
		t.Fatalf("first AllocateVersion: %v", err)
	}
	if first.AlreadyAllocated {
		t.Fatalf("expected already_allocated=false on the first call")
	}

	second, err := artifactSrv.AllocateVersion(ctx, req)
	if err != nil {
		t.Fatalf("replayed AllocateVersion: %v", err)
	}
	if second.Version != first.Version {
		t.Fatalf("replay allocated a different version: first=%q second=%q", first.Version, second.Version)
	}
	if !second.AlreadyAllocated {
		t.Fatalf("expected already_allocated=true on the replayed call")
	}

	// A DIFFERENT idempotency key must allocate a genuinely new version.
	third, err := artifactSrv.AllocateVersion(ctx, &pb.AllocateVersionRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Increment: "patch", IdempotencyKey: "different-key",
	})
	if err != nil {
		t.Fatalf("third AllocateVersion: %v", err)
	}
	if third.Version == first.Version {
		t.Fatalf("expected a fresh idempotency key to allocate a NEW version, got the same one: %q", third.Version)
	}
}

// TestAllocateVersion_ExplicitVersionCollisionFails proves the "fails if
// taken" contract for explicit_version from
// api_messages_artifact.proto's AllocateVersionRequest doc comment.
func TestAllocateVersion_ExplicitVersionCollisionFails(t *testing.T) {
	repo, artifactSrv := setupAllocate(t)
	ctx := authedCtx()
	repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStageAllocate)

	req := &pb.AllocateVersionRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		ExplicitVersion: "v5.0.0", IdempotencyKey: "explicit-1",
	}
	if _, err := artifactSrv.AllocateVersion(ctx, req); err != nil {
		t.Fatalf("first explicit allocation: %v", err)
	}

	_, err := artifactSrv.AllocateVersion(ctx, &pb.AllocateVersionRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		ExplicitVersion: "v5.0.0", IdempotencyKey: "explicit-2",
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists for a taken explicit_version, got %v", err)
	}
}
