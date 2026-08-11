package handlers

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file covers AR-7e's (issue #558) AdoptArtifact business logic --
// authorization itself is TestAdoptArtifact_Authorization in authz_test.go.
// setup/recordBuild are defined in artifact_test.go: "demo-image-app" is
// DEPLOY_UNIT_IMAGE, "demo-chart-app" is DEPLOY_UNIT_CHART (composed into
// chart "demo-achart"), "demo-none-app" is DEPLOY_UNIT_NONE.

// TestAdoptArtifact_NewRow_CreatesPublishedWithAdoptedProvenance is the
// primary case: no row exists at all for (owner, kind, version) yet.
func TestAdoptArtifact_NewRow_CreatesPublishedWithAdoptedProvenance(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()

	resp, err := artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Repository: "ghcr.io/demo/image-app", Version: "v0.9.0", Digest: "sha256:adopt-new",
		Reason: "pre-dates the registry", IdempotencyKey: "adopt-new-1",
	})
	if err != nil {
		t.Fatalf("AdoptArtifact: %v", err)
	}
	if resp.AlreadyRecorded {
		t.Fatal("expected AlreadyRecorded=false for a brand-new adoption")
	}
	if resp.Artifact.State != pb.ArtifactState_ARTIFACT_STATE_PUBLISHED {
		t.Fatalf("expected PUBLISHED, got %v", resp.Artifact.State)
	}
	if resp.Artifact.Provenance != pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED {
		t.Fatalf("expected ADOPTED provenance, got %v", resp.Artifact.Provenance)
	}
	if resp.Artifact.BuildId == "" {
		t.Fatal("expected a (synthetic) build_id to be stamped")
	}
}

// TestAdoptArtifact_UnblocksChartPinningPreRegistryImage is PLAN.md's AR-7e
// exit criterion, exercised end to end: a chart record fails on an
// unrecorded pin, adopting the pinned image unblocks the SAME chart record.
func TestAdoptArtifact_UnblocksChartPinningPreRegistryImage(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-adopt-unblock")

	// Fails first: "demo-chart-app"'s v0.9.0 was never recorded -- as if it
	// were published before the registry existed.
	_, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "demo-achart", Digest: "sha256:chart-preadopt", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "demo-chart-app", Repository: "ghcr.io/demo/chart-app", Version: "v0.9.0", Digest: "sha256:pre-registry-image"},
		},
		IdempotencyKey: "chart-preadopt-1",
	})
	if err == nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected the chart record to reject the unrecorded pin first, got %v", err)
	}

	adoptResp, err := artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-chart-app",
		Repository: "ghcr.io/demo/chart-app", Version: "v0.9.0", Digest: "sha256:pre-registry-image",
		Reason: "pre-dates the registry", IdempotencyKey: "adopt-image-1",
	})
	if err != nil {
		t.Fatalf("AdoptArtifact: %v", err)
	}
	if adoptResp.Artifact.Provenance != pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED {
		t.Fatalf("expected ADOPTED provenance, got %v", adoptResp.Artifact.Provenance)
	}

	// The SAME chart record now succeeds -- one documented, audited command
	// unblocked it.
	chartResp, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "demo-achart", Digest: "sha256:chart-preadopt", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "demo-chart-app", Repository: "ghcr.io/demo/chart-app", Version: "v0.9.0", Digest: "sha256:pre-registry-image"},
		},
		IdempotencyKey: "chart-preadopt-2",
	})
	if err != nil {
		t.Fatalf("RecordArtifact after adoption should succeed: %v", err)
	}
	if len(chartResp.Artifact.Contains) != 1 || chartResp.Artifact.Contains[0].Digest != "sha256:pre-registry-image" {
		t.Fatalf("expected the chart to pin the adopted image, got %+v", chartResp.Artifact.Contains)
	}
}

// TestAdoptArtifact_NeverDowngradesObservedProvenance is the critical
// invariant: adopting a digest that turns out to already be an "observed"
// row must be an idempotent no-op, never a rewrite -- see
// repository.ArtifactRepository.AdoptArtifact's doc comment.
func TestAdoptArtifact_NeverDowngradesObservedProvenance(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-adopt-no-downgrade")

	recorded, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:observed-not-downgraded", Version: "v1.0.0",
		IdempotencyKey: "record-observed-1",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}
	if recorded.Artifact.Provenance != pb.ArtifactProvenance_ARTIFACT_PROVENANCE_OBSERVED {
		t.Fatalf("expected OBSERVED provenance from RecordArtifact, got %v", recorded.Artifact.Provenance)
	}

	adopted, err := artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", Digest: "sha256:observed-not-downgraded",
		Reason: "operator mistake -- already observed", IdempotencyKey: "adopt-observed-1",
	})
	if err != nil {
		t.Fatalf("AdoptArtifact against an already-observed digest should be an idempotent no-op, got %v", err)
	}
	if !adopted.AlreadyRecorded {
		t.Fatal("expected AlreadyRecorded=true")
	}
	if adopted.Artifact.Provenance != pb.ArtifactProvenance_ARTIFACT_PROVENANCE_OBSERVED {
		t.Fatalf("expected provenance to STAY OBSERVED (never downgraded to ADOPTED), got %v", adopted.Artifact.Provenance)
	}
}

// TestAdoptArtifact_DifferentDigestConflict proves adoption cannot silently
// overwrite an already-published version with a different digest.
func TestAdoptArtifact_DifferentDigestConflict(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-adopt-conflict")

	if _, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:conflict-original", Version: "v1.0.0",
		IdempotencyKey: "record-conflict-1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", Digest: "sha256:conflict-different",
		Reason: "trying to overwrite", IdempotencyKey: "adopt-conflict-1",
	})
	if err == nil {
		t.Fatal("expected a conflict adopting a different digest onto an already-published version")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v (%v)", status.Code(err), err)
	}
}

// TestAdoptArtifact_RejectsAllocatedRow proves adoption does not silently
// discard a live version reservation.
func TestAdoptArtifact_RejectsAllocatedRow(t *testing.T) {
	repo := fake.New()
	appSrv := NewAppServer(repo)
	artifactSrv := NewArtifactServer(repo)
	ctx := authedCtx()

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{{Domain: "demo", Name: "allocsvc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE}}, nil),
		IdempotencyKey: "adopt-alloc-reconcile",
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStageAllocate)

	alloc, err := artifactSrv.AllocateVersion(ctx, &pb.AllocateVersionRequest{
		OwnerFullName: "demo-allocsvc", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		Increment: "patch", IdempotencyKey: "adopt-alloc-allocate",
	})
	if err != nil {
		t.Fatalf("AllocateVersion: %v", err)
	}

	_, err = artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-allocsvc",
		Version: alloc.Version, Digest: "sha256:adopt-over-allocated",
		Reason: "should be rejected", IdempotencyKey: "adopt-alloc-adopt",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition adopting over an allocated row, got %v (%v)", status.Code(err), err)
	}
}

// TestAdoptArtifact_RejectsPublishingRow proves adoption does not race a
// live in-flight publish.
func TestAdoptArtifact_RejectsPublishingRow(t *testing.T) {
	// setupAllocate, not setup: BeginPublish's ∅ -> publishing branch needs
	// a real ImageRepository (Registry/Organization/RepoName) to stamp onto
	// the fresh row -- setup()'s "demo-image-app" doesn't set those.
	_, artifactSrv := setupAllocate(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-adopt-publishing")

	if _, err := artifactSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", BuildId: build.BuildId, IdempotencyKey: "adopt-publishing-begin",
	}); err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}

	_, err := artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", Digest: "sha256:adopt-over-publishing",
		Reason: "should be rejected", IdempotencyKey: "adopt-publishing-adopt",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition adopting over a publishing row, got %v (%v)", status.Code(err), err)
	}
}

// TestAdoptArtifact_FailedRowBecomesPublished_ReusesBuildID is the
// disaster-recovery case: a run already tried and failed (or was reaped),
// but the artifact demonstrably exists. The existing row's REAL build_id
// (from the run that actually attempted the push) is reused rather than a
// synthetic one being minted.
func TestAdoptArtifact_FailedRowBecomesPublished_ReusesBuildID(t *testing.T) {
	// setupAllocate, not setup -- see TestAdoptArtifact_RejectsPublishingRow.
	_, artifactSrv := setupAllocate(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-adopt-failed")

	if _, err := artifactSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", BuildId: build.BuildId, IdempotencyKey: "adopt-failed-begin",
	}); err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}
	if _, err := artifactSrv.FailPublish(ctx, &pb.FailPublishRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", Reason: "push failed", IdempotencyKey: "adopt-failed-fail",
	}); err != nil {
		t.Fatalf("FailPublish: %v", err)
	}

	adopted, err := artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", Digest: "sha256:adopted-after-failure",
		Reason: "confirmed pushed despite recording failure", IdempotencyKey: "adopt-failed-adopt",
	})
	if err != nil {
		t.Fatalf("AdoptArtifact over a failed row: %v", err)
	}
	if adopted.Artifact.State != pb.ArtifactState_ARTIFACT_STATE_PUBLISHED {
		t.Fatalf("expected PUBLISHED, got %v", adopted.Artifact.State)
	}
	if adopted.Artifact.Provenance != pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED {
		t.Fatalf("expected ADOPTED provenance, got %v", adopted.Artifact.Provenance)
	}
	if adopted.Artifact.BuildId != build.BuildId {
		t.Fatalf("expected the REAL CI build_id %q to be reused (not a synthetic one), got %q", build.BuildId, adopted.Artifact.BuildId)
	}
}

// TestAdoptArtifact_ChartWithContains proves AdoptArtifact supports charts
// too, with the same contains-linking RecordArtifact provides.
func TestAdoptArtifact_ChartWithContains(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-adopt-chart-contains")

	imgResp, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-chart-app", Digest: "sha256:chart-contains-image", Version: "v1.0.0",
		IdempotencyKey: "chart-contains-image-1",
	})
	if err != nil {
		t.Fatalf("record image: %v", err)
	}

	adopted, err := artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART, OwnerFullName: "demo-achart",
		Version: "v1.0.0", Digest: "sha256:chart-contains-chart",
		Contains: []*pb.ContainedImage{
			{AppFullName: "demo-chart-app", Repository: "repo", Version: "v1.0.0", Digest: imgResp.Artifact.Digest},
		},
		Reason: "pre-registry chart", IdempotencyKey: "chart-contains-adopt-1",
	})
	if err != nil {
		t.Fatalf("AdoptArtifact chart: %v", err)
	}
	if len(adopted.Artifact.Contains) != 1 || adopted.Artifact.Contains[0].Digest != imgResp.Artifact.Digest {
		t.Fatalf("expected 1 contains link to the recorded image, got %+v", adopted.Artifact.Contains)
	}
}

// TestListArtifacts_ProvenanceFilter is AR-7e's "distinguishable in one
// query" exit criterion half.
func TestListArtifacts_ProvenanceFilter(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-adopt-provenance")

	if _, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:prov-observed", Version: "v1.0.0",
		IdempotencyKey: "prov-observed-1",
	}); err != nil {
		t.Fatalf("record observed: %v", err)
	}
	if _, err := artifactSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v2.0.0", Digest: "sha256:prov-adopted",
		Reason: "pre-registry", IdempotencyKey: "prov-adopted-1",
	}); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	resp, err := artifactSrv.ListArtifacts(ctx, &pb.ListArtifactsRequest{
		OwnerFullName: "demo-image-app", Provenance: pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED,
	})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Digest != "sha256:prov-adopted" {
		t.Fatalf("expected exactly the adopted artifact, got %+v", resp.Artifacts)
	}
}

// TestAdoptArtifact_ValidatesRequiredFields covers the handler's own
// validation, independent of authorization (authz_test.go) or repository
// state-machine rejections (the tests above).
func TestAdoptArtifact_ValidatesRequiredFields(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()

	base := func() *pb.AdoptArtifactRequest {
		return &pb.AdoptArtifactRequest{
			Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
			Version: "v1.0.0", Digest: "sha256:validate", Reason: "test", IdempotencyKey: "validate-1",
		}
	}

	t.Run("missing reason", func(t *testing.T) {
		req := base()
		req.Reason = ""
		_, err := artifactSrv.AdoptArtifact(ctx, req)
		requireCode(t, err, codes.InvalidArgument, "AdoptArtifact missing reason")
	})

	t.Run("missing digest", func(t *testing.T) {
		req := base()
		req.Digest = ""
		_, err := artifactSrv.AdoptArtifact(ctx, req)
		requireCode(t, err, codes.InvalidArgument, "AdoptArtifact missing digest")
	})

	t.Run("bad version", func(t *testing.T) {
		req := base()
		req.Version = "not-semver"
		_, err := artifactSrv.AdoptArtifact(ctx, req)
		requireCode(t, err, codes.InvalidArgument, "AdoptArtifact bad version")
	})

	t.Run("missing idempotency key", func(t *testing.T) {
		req := base()
		req.IdempotencyKey = ""
		_, err := artifactSrv.AdoptArtifact(ctx, req)
		requireCode(t, err, codes.InvalidArgument, "AdoptArtifact missing idempotency key")
	})

	t.Run("contains on an image", func(t *testing.T) {
		req := base()
		req.Contains = []*pb.ContainedImage{{AppFullName: "demo-image-app", Digest: "sha256:x"}}
		_, err := artifactSrv.AdoptArtifact(ctx, req)
		requireCode(t, err, codes.InvalidArgument, "AdoptArtifact contains on an image")
	})
}
