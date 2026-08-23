package handlers

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
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
		IdempotencyKey: idemPrefix + "-record",
	}); err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}
}

// TestCheckChartHermeticity_RejectsUnpublishedPin is AR-7f's core exit
// criterion, unconditional for every domain: a member app that was never
// published fails the check, naming the app.
func TestCheckChartHermeticity_RejectsUnpublishedPin(t *testing.T) {
	_, artifactSrv := setupAllocate(t)

	resp, err := artifactSrv.CheckChartHermeticity(authedCtx(), &pb.CheckChartHermeticityRequest{
		ChartDomain: "demo",
		Pins:        []*pb.ChartPin{{AppFullName: "demo-image-app", Version: "v1.0.0"}},
	})
	if err != nil {
		t.Fatalf("CheckChartHermeticity: %v", err)
	}
	if !resp.Enforced {
		t.Fatalf("expected enforced=true, got false")
	}
	if len(resp.Violations) != 1 {
		t.Fatalf("expected exactly 1 violation, got %v", resp.Violations)
	}
	v := resp.Violations[0]
	if v.AppFullName != "demo-image-app" || v.Version != "v1.0.0" {
		t.Fatalf("violation does not name the offending app/version: %+v", v)
	}
}

// TestCheckChartHermeticity_AcceptsPublishedPin proves the mirror-image
// case: once the pinned version is actually published, the check reports no
// violations.
func TestCheckChartHermeticity_AcceptsPublishedPin(t *testing.T) {
	_, artifactSrv := setupAllocate(t)
	publishImage(t, artifactSrv, "v1.0.0", "accept")

	resp, err := artifactSrv.CheckChartHermeticity(authedCtx(), &pb.CheckChartHermeticityRequest{
		ChartDomain: "demo",
		Pins:        []*pb.ChartPin{{AppFullName: "demo-image-app", Version: "v1.0.0"}},
	})
	if err != nil {
		t.Fatalf("CheckChartHermeticity: %v", err)
	}
	if !resp.Enforced {
		t.Fatalf("expected enforced=true, got false")
	}
	if len(resp.Violations) != 0 {
		t.Fatalf("expected no violations for a published pin, got %v", resp.Violations)
	}
}

// TestCheckChartHermeticity_RejectsPublishingNotYetPublished proves a pin
// sitting in "publishing" (BeginPublish called, RecordArtifact not yet) is
// still a violation -- only ARTIFACT_STATE_PUBLISHED satisfies the check,
// matching ArtifactState's doc comment on what published actually
// guarantees.
func TestCheckChartHermeticity_RejectsPublishingNotYetPublished(t *testing.T) {
	_, artifactSrv := setupAllocate(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "inflight-run")
	if _, err := artifactSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "demo-image-app",
		Version: "v1.0.0", BuildId: build.BuildId, IdempotencyKey: "inflight-begin",
	}); err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}

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
