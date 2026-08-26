package handlers

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// publishImage takes demo-image-app (created by setupAllocate) all the way
// to ARTIFACT_STATE_PUBLISHED at the given version, via the same
// allocate -> begin-publish -> record path TestArtifactLifecycle_
// AllocateBeginPublishRecord exercises. CheckChartHermeticity only treats a
// pin as satisfied once it reaches this state -- see ArtifactState's doc
// comment in repository/models.go.
func publishImage(t *testing.T, artifactSrv *ArtifactServer, version, idemPrefix string) {
	t.Helper()
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, idemPrefix+"-run")
	if _, err := artifactSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: version, BuildId: build.BuildId, IdempotencyKey: idemPrefix + "-begin",
	}); err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}
	if _, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:" + idemPrefix, Version: version,
		IdentityDigest: "sha256:" + idemPrefix,
		IdempotencyKey: idemPrefix + "-record",
	}); err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}
}

// TestCheckChartHermeticity_NotEnforcedAtObserve proves the per-domain gate:
// at the default stage ("observe", where every domain sits today per
// ARCHITECTURE.md), the RPC reports enforced=false and never inspects
// artifact state -- an unpublished pin must not surface as a violation.
func TestCheckChartHermeticity_NotEnforcedAtObserve(t *testing.T) {
	_, artifactSrv := setupAllocate(t)
	resp, err := artifactSrv.CheckChartHermeticity(authedCtx(), &pb.CheckChartHermeticityRequest{
		ChartDomain: "demo",
		Pins:        []*pb.ChartPin{{AppFullName: "demo-image-app", Version: "v9.9.9"}},
	})
	if err != nil {
		t.Fatalf("CheckChartHermeticity: %v", err)
	}
	if resp.Enforced {
		t.Fatalf("expected enforced=false at adoption stage observe, got true")
	}
	if len(resp.Violations) != 0 {
		t.Fatalf("expected no violations when not enforced, got %v", resp.Violations)
	}
}

// TestCheckChartHermeticity_NotEnforcedAtPromote is the same proof at
// "promote", the other stage recording is mandatory at but compose-time
// enforcement is not (ARCHITECTURE.md "Availability, restated per adoption
// stage").
func TestCheckChartHermeticity_NotEnforcedAtPromote(t *testing.T) {
	repo, artifactSrv := setupAllocate(t)
	repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStagePromote)
	resp, err := artifactSrv.CheckChartHermeticity(authedCtx(), &pb.CheckChartHermeticityRequest{
		ChartDomain: "demo",
		Pins:        []*pb.ChartPin{{AppFullName: "demo-image-app", Version: "v9.9.9"}},
	})
	if err != nil {
		t.Fatalf("CheckChartHermeticity: %v", err)
	}
	if resp.Enforced {
		t.Fatalf("expected enforced=false at adoption stage promote, got true")
	}
}

// TestCheckChartHermeticity_AllocateRejectsUnpublishedPin is AR-7f's core
// exit criterion: an allocate-stage domain whose member app was never
// published fails the check, naming the app.
func TestCheckChartHermeticity_AllocateRejectsUnpublishedPin(t *testing.T) {
	repo, artifactSrv := setupAllocate(t)
	repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStageAllocate)

	resp, err := artifactSrv.CheckChartHermeticity(authedCtx(), &pb.CheckChartHermeticityRequest{
		ChartDomain: "demo",
		Pins:        []*pb.ChartPin{{AppFullName: "demo-image-app", Version: "v1.0.0"}},
	})
	if err != nil {
		t.Fatalf("CheckChartHermeticity: %v", err)
	}
	if !resp.Enforced {
		t.Fatalf("expected enforced=true at adoption stage allocate, got false")
	}
	if len(resp.Violations) != 1 {
		t.Fatalf("expected exactly 1 violation, got %v", resp.Violations)
	}
	v := resp.Violations[0]
	if v.AppFullName != "demo-image-app" || v.Version != "v1.0.0" {
		t.Fatalf("violation does not name the offending app/version: %+v", v)
	}
}

// TestCheckChartHermeticity_AllocateAcceptsPublishedPin proves the
// mirror-image case: once the pinned version is actually published, the
// same allocate-stage domain reports no violations.
func TestCheckChartHermeticity_AllocateAcceptsPublishedPin(t *testing.T) {
	repo, artifactSrv := setupAllocate(t)
	publishImage(t, artifactSrv, "v1.0.0", "accept")
	repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStageAllocate)

	resp, err := artifactSrv.CheckChartHermeticity(authedCtx(), &pb.CheckChartHermeticityRequest{
		ChartDomain: "demo",
		Pins:        []*pb.ChartPin{{AppFullName: "demo-image-app", Version: "v1.0.0"}},
	})
	if err != nil {
		t.Fatalf("CheckChartHermeticity: %v", err)
	}
	if !resp.Enforced {
		t.Fatalf("expected enforced=true at adoption stage allocate, got false")
	}
	if len(resp.Violations) != 0 {
		t.Fatalf("expected no violations for a published pin, got %v", resp.Violations)
	}
}

// TestCheckChartHermeticity_AllocateRejectsPublishingNotYetPublished proves
// a pin sitting in "publishing" (BeginPublish called, RecordArtifact not
// yet) is still a violation -- only ARTIFACT_STATE_PUBLISHED satisfies the
// check, matching ArtifactState's doc comment on what published actually
// guarantees.
func TestCheckChartHermeticity_AllocateRejectsPublishingNotYetPublished(t *testing.T) {
	repo, artifactSrv := setupAllocate(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "inflight-run")
	if _, err := artifactSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", BuildId: build.BuildId, IdempotencyKey: "inflight-begin",
	}); err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}
	repo.SetDomainAdoptionStage("demo", repository.DomainAdoptionStageAllocate)

	resp, err := artifactSrv.CheckChartHermeticity(ctx, &pb.CheckChartHermeticityRequest{
		ChartDomain: "demo",
		Pins:        []*pb.ChartPin{{AppFullName: "demo-image-app", Version: "v1.0.0"}},
	})
	if err != nil {
		t.Fatalf("CheckChartHermeticity: %v", err)
	}
	if len(resp.Violations) != 1 {
		t.Fatalf("expected 1 violation for a still-publishing pin, got %v", resp.Violations)
	}
}

// TestCheckChartHermeticity_RequiresChartDomain guards the request
// validation.
func TestCheckChartHermeticity_RequiresChartDomain(t *testing.T) {
	_, artifactSrv := setupAllocate(t)
	_, err := artifactSrv.CheckChartHermeticity(authedCtx(), &pb.CheckChartHermeticityRequest{
		Pins: []*pb.ChartPin{{AppFullName: "demo-image-app", Version: "v1.0.0"}},
	})
	if err == nil {
		t.Fatal("expected error for missing chart_domain")
	}
}
