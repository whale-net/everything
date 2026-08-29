//go:build integration

// Real-Postgres integration coverage for artifactRepo.AdoptArtifact
// (artifact.go) -- reconciling an externally-observed artifact (e.g. a
// digest discovered by a scanner) against repository state. See
// postgres_integration_helpers_test.go's doc comment for why this package
// builds these files under the "integration" tag, and TESTING.md for how
// to run them.
package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/tools/app_registry/server/handlers"
	"github.com/whale-net/everything/tools/app_registry/server/repository"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// ============================================================================
// AdoptArtifact (AR-7e, issue #558)
// ============================================================================

// adoptArtifactTx runs Artifacts().AdoptArtifact inside a real WithTx
// transaction, exactly as handlers.ArtifactServer.AdoptArtifact does in
// production.
func adoptArtifactTx(t *testing.T, reg *Registry, a repository.Artifact, contains []repository.ContainedImageInput, reason, actor string) (*repository.Artifact, bool, error) {
	t.Helper()
	var out *repository.Artifact
	var already bool
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, already, ferr = r.Artifacts().AdoptArtifact(ctx, a, contains, reason, actor)
		return ferr
	})
	return out, already, err
}

// TestAdoptArtifact_NewRow_CreatesSyntheticBuild_Postgres is AdoptArtifact's
// primary case against real Postgres: no row exists for (owner, kind,
// version) yet, so a synthetic `build` row is created to satisfy
// artifact.build_id's real foreign key and migration 007's
// artifact_state_shape CHECK (build_id NOT NULL once "published"). Proves
// the synthetic row's shape: a non-numeric workflow_run_id that can never
// collide with a real GitHub Actions run id, and git_ref "adopted" as the
// same at-a-glance marker.
func TestAdoptArtifact_NewRow_CreatesSyntheticBuild_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-new", "image")

	adopted, alreadyRecorded, err := adoptArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-new", Version: "v0.9.0", Digest: "sha256:adopt-new-pg",
	}, nil, "pre-dates the registry", "admin@example.com")
	if err != nil {
		t.Fatalf("AdoptArtifact (∅ -> published): %v", err)
	}
	if alreadyRecorded {
		t.Fatal("expected alreadyRecorded=false for a brand-new adoption")
	}
	if adopted.State != repository.ArtifactStatePublished {
		t.Fatalf("expected state published, got %q", adopted.State)
	}
	if adopted.Provenance != repository.ArtifactProvenanceAdopted {
		t.Fatalf("expected provenance adopted, got %q", adopted.Provenance)
	}
	if adopted.BuildID == "" {
		t.Fatal("expected a build_id to be stamped")
	}

	var gitRef, workflowRunID, actor string
	if err := pool.QueryRow(context.Background(),
		`SELECT git_ref, workflow_run_id, actor FROM build WHERE build_id = $1`, adopted.BuildID,
	).Scan(&gitRef, &workflowRunID, &actor); err != nil {
		t.Fatalf("read synthetic build row: %v", err)
	}
	if gitRef != "adopted" {
		t.Fatalf("expected synthetic build's git_ref = %q, got %q", "adopted", gitRef)
	}
	if !strings.HasPrefix(workflowRunID, "adopted:") {
		t.Fatalf("expected synthetic build's workflow_run_id to start with %q, got %q", "adopted:", workflowRunID)
	}
	if actor != "admin@example.com" {
		t.Fatalf("expected synthetic build's actor to be the calling admin, got %q", actor)
	}
}

// TestAdoptArtifact_UnblocksChartPin_Postgres is PLAN.md's AR-7e exit
// criterion against real Postgres, exercised through the handler layer end
// to end: a chart record fails on an unrecorded pin (simulating an image
// published before the registry existed), and adopting the image unblocks
// the SAME chart record -- one documented, audited command.
func TestAdoptArtifact_UnblocksChartPin_Postgres(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := authedCtx()
	appSrv := handlers.NewAppServer(reg)
	artSrv := handlers.NewArtifactServer(reg)

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-adopt-unblock", 100, 100,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "adopt-unblock-app")},
			[]*appmetapb.ChartManifest{{Domain: "acme", Name: "adopt-unblock-chart", Apps: []string{"adopt-unblock-app"}}}),
		IdempotencyKey: "adopt-unblock-reconcile",
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	build, err := artSrv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha-adopt-unblock", WorkflowRunId: "run-adopt-unblock-pg", IdempotencyKey: "adopt-unblock-build",
	})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	if _, err := artSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "acme-adopt-unblock-chart", Version: "v1.0.0",
		Repository:     "https://charts.example.com/acme-adopt-unblock-chart",
		IdempotencyKey: "adopt-unblock-chart-1-begin",
	}); err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}

	// Fails first: the pinned image was never recorded.
	_, err = artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "acme-adopt-unblock-chart", Digest: "sha256:adopt-unblock-chart-pg", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "acme-adopt-unblock-app", Repository: "ghcr.io/acme/adopt-unblock-app", Version: "v0.9.0", Digest: "sha256:adopt-unblock-image-pg"},
		},
		IdempotencyKey: "adopt-unblock-chart-1",
	})
	if err == nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected the chart record to reject the unrecorded pin first, got %v", err)
	}

	// Adopt the pre-registry image.
	adoptResp, err := artSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "acme-adopt-unblock-app",
		Repository: "ghcr.io/acme/adopt-unblock-app", Version: "v0.9.0", Digest: "sha256:adopt-unblock-image-pg",
		Reason: "pre-dates the registry", IdempotencyKey: "adopt-unblock-adopt-1",
	})
	if err != nil {
		t.Fatalf("AdoptArtifact: %v", err)
	}
	if adoptResp.Artifact.Provenance != pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED {
		t.Fatalf("expected ADOPTED provenance, got %v", adoptResp.Artifact.Provenance)
	}

	// The SAME chart record now succeeds.
	chartResp, err := artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "acme-adopt-unblock-chart", Digest: "sha256:adopt-unblock-chart-pg", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "acme-adopt-unblock-app", Repository: "ghcr.io/acme/adopt-unblock-app", Version: "v0.9.0", Digest: "sha256:adopt-unblock-image-pg"},
		},
		IdempotencyKey: "adopt-unblock-chart-2",
	})
	if err != nil {
		t.Fatalf("RecordArtifact after adoption should succeed: %v", err)
	}
	if len(chartResp.Artifact.Contains) != 1 || chartResp.Artifact.Contains[0].Digest != "sha256:adopt-unblock-image-pg" {
		t.Fatalf("expected the chart to pin the adopted image, got %+v", chartResp.Artifact.Contains)
	}

	// "distinguishable in one query": the image is ADOPTED, the chart is
	// OBSERVED, in one ListArtifacts(Provenance=ADOPTED) call.
	adoptedOnly, _, err := reg.Artifacts().ListArtifacts(context.Background(), repository.ArtifactListFilter{Provenance: repository.ArtifactProvenanceAdopted}, 0, "")
	if err != nil {
		t.Fatalf("ListArtifacts(Provenance=adopted): %v", err)
	}
	if len(adoptedOnly) != 1 || adoptedOnly[0].Digest != "sha256:adopt-unblock-image-pg" {
		t.Fatalf("expected exactly the adopted image, got %+v", adoptedOnly)
	}
}

// TestAdoptArtifact_NeverDowngradesObservedProvenance_Postgres is the
// critical invariant against real Postgres: adopting a digest that turns
// out to already be an "observed" row must be an idempotent no-op, never a
// rewrite.
func TestAdoptArtifact_NeverDowngradesObservedProvenance_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-no-downgrade", "image")
	buildID := seedBuild(t, pool, "run-adopt-no-downgrade")

	recorded, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-no-downgrade", Version: "v1.0.0",
		Digest: "sha256:observed-not-downgraded-pg", BuildID: buildID,
	}, nil)
	if err != nil {
		t.Fatalf("seed observed artifact: %v", err)
	}
	if recorded.Provenance != repository.ArtifactProvenanceObserved {
		t.Fatalf("expected provenance observed, got %q", recorded.Provenance)
	}

	adopted, alreadyRecorded, err := adoptArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-no-downgrade", Version: "v1.0.0",
		Digest: "sha256:observed-not-downgraded-pg",
	}, nil, "operator mistake -- already observed", "admin@example.com")
	if err != nil {
		t.Fatalf("AdoptArtifact against an already-observed digest should be an idempotent no-op, got %v", err)
	}
	if !alreadyRecorded {
		t.Fatal("expected alreadyRecorded=true")
	}
	if adopted.Provenance != repository.ArtifactProvenanceObserved {
		t.Fatalf("expected provenance to STAY observed (never downgraded to adopted), got %q", adopted.Provenance)
	}
	if adopted.BuildID != buildID {
		t.Fatalf("expected the original build_id to be untouched, got %q vs %q", adopted.BuildID, buildID)
	}
}

// TestAdoptArtifact_DifferentDigestConflict_Postgres proves adoption cannot
// silently overwrite an already-published version with a different digest.
func TestAdoptArtifact_DifferentDigestConflict_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-conflict", "image")
	buildID := seedBuild(t, pool, "run-adopt-conflict")

	if _, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-conflict", Version: "v1.0.0",
		Digest: "sha256:adopt-conflict-original", BuildID: buildID,
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err := adoptArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-conflict", Version: "v1.0.0",
		Digest: "sha256:adopt-conflict-different",
	}, nil, "trying to overwrite", "admin@example.com")
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

// TestAdoptArtifact_RejectsAllocated_AdoptsPublishing_Postgres proves adoption
// rejects an "allocated" row (a live version reservation without push) while
// successfully adopting an in-flight/orphaned "publishing" row (reusing its build_id).
func TestAdoptArtifact_RejectsAllocated_AdoptsPublishing_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-live", "image")
	buildID := seedBuild(t, pool, "run-adopt-live")

	t.Run("allocated", func(t *testing.T) {
		seedRawArtifact(t, pool, appID, repository.ArtifactStateAllocated, "v1.0.0", "")
		_, _, err := adoptArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/adopt-live", Version: "v1.0.0",
			Digest: "sha256:adopt-over-allocated-pg",
		}, nil, "should be rejected", "admin@example.com")
		if !errors.Is(err, repository.ErrFailedPrecondition) {
			t.Fatalf("expected ErrFailedPrecondition adopting over an allocated row, got %v", err)
		}
	})

	t.Run("publishing", func(t *testing.T) {
		seedRawArtifact(t, pool, appID, repository.ArtifactStatePublishing, "v2.0.0", buildID)
		out, already, err := adoptArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/adopt-live", Version: "v2.0.0",
			Digest: "sha256:adopt-over-publishing-pg",
		}, nil, "recover orphaned publish", "admin@example.com")
		if err != nil {
			t.Fatalf("unexpected error adopting over a publishing row: %v", err)
		}
		if already {
			t.Fatalf("expected alreadyRecorded false")
		}
		if out.State != repository.ArtifactStatePublished {
			t.Fatalf("expected state PUBLISHED, got %v", out.State)
		}
		if out.Provenance != repository.ArtifactProvenanceAdopted {
			t.Fatalf("expected provenance ADOPTED, got %v", out.Provenance)
		}
		if out.BuildID != buildID {
			t.Fatalf("expected build_id %s preserved, got %s", buildID, out.BuildID)
		}
	})
}

// TestAdoptArtifact_FailedRow_Postgres is the disaster-recovery case against
// real Postgres: a run already tried and failed (or was reaped), but the
// artifact demonstrably exists. Covers BOTH sub-cases of "does the failed
// row already carry a build_id": a row reaped from "publishing" does (that
// REAL build_id is reused); a row reaped from "allocated" does not (a
// synthetic one is minted, same as the ∅ -> published branch).
func TestAdoptArtifact_FailedRow_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-failed", "image")
	buildID := seedBuild(t, pool, "run-adopt-failed")

	t.Run("reuses existing build_id when the failed row has one", func(t *testing.T) {
		seedRawArtifact(t, pool, appID, repository.ArtifactStateFailed, "v1.0.0", buildID)
		adopted, alreadyRecorded, err := adoptArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/adopt-failed", Version: "v1.0.0",
			Digest: "sha256:adopt-failed-reuse-pg",
		}, nil, "confirmed pushed despite recording failure", "admin@example.com")
		if err != nil {
			t.Fatalf("AdoptArtifact over a failed row with a build_id: %v", err)
		}
		if alreadyRecorded {
			t.Fatal("expected alreadyRecorded=false")
		}
		if adopted.State != repository.ArtifactStatePublished {
			t.Fatalf("expected state published, got %q", adopted.State)
		}
		if adopted.Provenance != repository.ArtifactProvenanceAdopted {
			t.Fatalf("expected provenance adopted, got %q", adopted.Provenance)
		}
		if adopted.BuildID != buildID {
			t.Fatalf("expected the REAL CI build_id %q to be reused (not a synthetic one), got %q", buildID, adopted.BuildID)
		}
	})

	t.Run("mints a synthetic build_id when the failed row has none", func(t *testing.T) {
		// A "failed" row reaped from "allocated" (not "publishing") never
		// had a build_id -- see ArtifactRepository.AdoptArtifact's doc
		// comment.
		seedRawArtifact(t, pool, appID, repository.ArtifactStateFailed, "v2.0.0", "")
		adopted, _, err := adoptArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/adopt-failed", Version: "v2.0.0",
			Digest: "sha256:adopt-failed-synthetic-pg",
		}, nil, "confirmed pushed despite recording failure", "admin@example.com")
		if err != nil {
			t.Fatalf("AdoptArtifact over a failed row with no build_id: %v", err)
		}
		if adopted.BuildID == "" {
			t.Fatal("expected a synthetic build_id to be stamped")
		}
		if adopted.BuildID == buildID {
			t.Fatal("expected a DIFFERENT (synthetic) build_id, not the other subtest's real one")
		}
		var gitRef string
		if err := pool.QueryRow(context.Background(), `SELECT git_ref FROM build WHERE build_id = $1`, adopted.BuildID).Scan(&gitRef); err != nil {
			t.Fatalf("read synthetic build row: %v", err)
		}
		if gitRef != "adopted" {
			t.Fatalf("expected synthetic build's git_ref = %q, got %q", "adopted", gitRef)
		}
	})
}
