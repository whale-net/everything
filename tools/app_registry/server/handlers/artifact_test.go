package handlers

import (
	"context"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
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
	ctx := context.Background()

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
	resp, err := srv.RecordBuild(context.Background(), &pb.RecordBuildRequest{
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
	ctx := context.Background()
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
	ctx := context.Background()
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
	ctx := context.Background()
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
}

// TestResolveArtifact_ReturnsImagesForChart is required coverage: resolving
// a chart artifact returns the correct pinned image artifacts and builds.
func TestResolveArtifact_ReturnsImagesForChart(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := context.Background()
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
	ctx := context.Background()
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
	ctx := context.Background()

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
	ctx := context.Background()
	build := recordBuild(t, artifactSrv, "run-noidem")

	_, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:noidem", Version: "v1.0.0",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing idempotency_key, got %v", err)
	}
}

func TestArtifactServer_AllocateVersionStillUnimplemented(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	_, err := artifactSrv.AllocateVersion(context.Background(), &pb.AllocateVersionRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %v", err)
	}
}
