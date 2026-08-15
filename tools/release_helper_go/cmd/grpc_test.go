package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
)

func TestNewRegistryClients_MissingAddress(t *testing.T) {
	withEnv(map[string]string{}, func() {
		_, err := NewRegistryClients(context.Background())
		if err == nil {
			t.Fatal("expected error when APP_REGISTRY_ADDRESS is not set, got nil")
		}
		if !strings.Contains(err.Error(), "APP_REGISTRY_ADDRESS is not set") {
			t.Fatalf("expected error mentioning APP_REGISTRY_ADDRESS, got: %v", err)
		}
	})
}

func TestNewRegistryClients_AuthModeOIDC_MissingConfig(t *testing.T) {
	withEnv(map[string]string{
		"APP_REGISTRY_ADDRESS": "localhost:50051",
		"GRPC_AUTH_MODE":       "oidc",
	}, func() {
		_, err := NewRegistryClients(context.Background())
		if err == nil {
			t.Fatal("expected error when OIDC config is missing, got nil")
		}
		if !strings.Contains(err.Error(), "TokenURL is required") {
			t.Fatalf("expected error mentioning TokenURL, got: %v", err)
		}
	})
}

func TestFakeAppRegistryClient(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeAppRegistryClient()

	// Default AssertApps behavior
	assertResp, err := fake.AssertApps(ctx, &pb.AssertAppsRequest{IdempotencyKey: "key-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if assertResp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(fake.AssertAppsCalls) != 1 || fake.AssertAppsCalls[0].IdempotencyKey != "key-123" {
		t.Errorf("expected 1 recorded AssertApps call with idempotency_key 'key-123', got %+v", fake.AssertAppsCalls)
	}

	// Custom hook
	fake.AssertAppsFn = func(ctx context.Context, in *pb.AssertAppsRequest, opts ...grpc.CallOption) (*pb.AssertAppsResponse, error) {
		return nil, fmt.Errorf("simulated failure")
	}
	// Note: using the method through pb.AppRegistryClient interface
	var iface pb.AppRegistryClient = fake
	_, err = iface.AssertApps(ctx, &pb.AssertAppsRequest{IdempotencyKey: "key-456"})
	if err == nil || !strings.Contains(err.Error(), "simulated failure") {
		t.Fatalf("expected simulated failure, got: %v", err)
	}
	if len(fake.AssertAppsCalls) != 2 {
		t.Fatalf("expected 2 calls recorded, got %d", len(fake.AssertAppsCalls))
	}

	// ReconcileApps
	recResp, err := iface.ReconcileApps(ctx, &pb.ReconcileAppsRequest{})
	if err != nil || recResp == nil {
		t.Fatalf("expected default ReconcileApps response, got resp=%v, err=%v", recResp, err)
	}
	if len(fake.ReconcileAppsCalls) != 1 {
		t.Errorf("expected 1 ReconcileApps call recorded")
	}

	// ListApps & GetApp
	listResp, err := iface.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil || listResp == nil {
		t.Fatalf("expected default ListApps response, got resp=%v, err=%v", listResp, err)
	}
	getResp, err := iface.GetApp(ctx, &pb.GetAppRequest{AppId: "demo-app"})
	if err != nil || getResp == nil {
		t.Fatalf("expected default GetApp response, got resp=%v, err=%v", getResp, err)
	}

	// ListCharts, SetAppStatus, ListReconcileRuns
	chartsResp, err := iface.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil || chartsResp == nil {
		t.Fatalf("expected default ListCharts response, got resp=%v, err=%v", chartsResp, err)
	}
	statusResp, err := iface.SetAppStatus(ctx, &pb.SetAppStatusRequest{})
	if err != nil || statusResp == nil {
		t.Fatalf("expected default SetAppStatus response, got resp=%v, err=%v", statusResp, err)
	}
	runsResp, err := iface.ListReconcileRuns(ctx, &pb.ListReconcileRunsRequest{})
	if err != nil || runsResp == nil {
		t.Fatalf("expected default ListReconcileRuns response, got resp=%v, err=%v", runsResp, err)
	}
}

func TestFakeArtifactRegistryClient(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeArtifactRegistryClient()

	var iface pb.ArtifactRegistryClient = fake

	// RecordBuild
	rbResp, err := iface.RecordBuild(ctx, &pb.RecordBuildRequest{GitSha: "abc1234"})
	if err != nil || rbResp.Build == nil || rbResp.Build.GitSha != "abc1234" {
		t.Fatalf("expected git_sha 'abc1234', got resp=%v, err=%v", rbResp, err)
	}
	if len(fake.RecordBuildCalls) != 1 {
		t.Errorf("expected 1 RecordBuild call recorded")
	}

	// RecordArtifact
	raResp, err := iface.RecordArtifact(ctx, &pb.RecordArtifactRequest{BuildId: "build-1", Digest: "sha256:1234"})
	if err != nil || raResp.Artifact.State != pb.ArtifactState_ARTIFACT_STATE_PUBLISHED || raResp.Artifact.Digest != "sha256:1234" {
		t.Fatalf("expected PUBLISHED state, got resp=%v, err=%v", raResp, err)
	}
	if len(fake.RecordArtifactCalls) != 1 {
		t.Errorf("expected 1 RecordArtifact call recorded")
	}

	// BeginPublish
	bpResp, err := iface.BeginPublish(ctx, &pb.BeginPublishRequest{BuildId: "build-1"})
	if err != nil || bpResp.Artifact.State != pb.ArtifactState_ARTIFACT_STATE_PUBLISHING {
		t.Fatalf("expected PUBLISHING state, got resp=%v, err=%v", bpResp, err)
	}
	if len(fake.BeginPublishCalls) != 1 {
		t.Errorf("expected 1 BeginPublish call recorded")
	}

	// FailPublish
	fpResp, err := iface.FailPublish(ctx, &pb.FailPublishRequest{Reason: "build failed"})
	if err != nil || fpResp.Artifact.State != pb.ArtifactState_ARTIFACT_STATE_FAILED || fpResp.Artifact.FailReason != "build failed" {
		t.Fatalf("expected FAILED state with reason, got resp=%v, err=%v", fpResp, err)
	}
	if len(fake.FailPublishCalls) != 1 {
		t.Errorf("expected 1 FailPublish call recorded")
	}

	// BeginPublishBatch
	bpbResp, err := iface.BeginPublishBatch(ctx, &pb.BeginPublishBatchRequest{BuildId: "build-1"})
	if err != nil || bpbResp == nil {
		t.Fatalf("expected default BeginPublishBatch response, got resp=%v, err=%v", bpbResp, err)
	}
	if len(fake.BeginPublishBatchCalls) != 1 {
		t.Errorf("expected 1 BeginPublishBatch call recorded")
	}

	// CheckChartHermeticity
	cchResp, err := iface.CheckChartHermeticity(ctx, &pb.CheckChartHermeticityRequest{ChartDomain: "demo"})
	if err != nil || cchResp.Enforced {
		t.Fatalf("expected Enforced=false default, got resp=%v, err=%v", cchResp, err)
	}
	if len(fake.CheckChartHermeticityCalls) != 1 {
		t.Errorf("expected 1 CheckChartHermeticity call recorded")
	}

	// Read/lookup methods
	if resp, err := iface.ListArtifacts(ctx, &pb.ListArtifactsRequest{}); err != nil || resp == nil {
		t.Fatalf("ListArtifacts failed: %v", err)
	}
	if resp, err := iface.GetArtifact(ctx, &pb.GetArtifactRequest{}); err != nil || resp == nil {
		t.Fatalf("GetArtifact failed: %v", err)
	}
	if resp, err := iface.GetReleaseRun(ctx, &pb.GetReleaseRunRequest{}); err != nil || resp == nil {
		t.Fatalf("GetReleaseRun failed: %v", err)
	}
	if resp, err := iface.ResolveArtifact(ctx, &pb.ResolveArtifactRequest{}); err != nil || resp == nil {
		t.Fatalf("ResolveArtifact failed: %v", err)
	}
	if resp, err := iface.ListBuilds(ctx, &pb.ListBuildsRequest{}); err != nil || resp == nil {
		t.Fatalf("ListBuilds failed: %v", err)
	}
	if resp, err := iface.ListArtifactPins(ctx, &pb.ListArtifactPinsRequest{}); err != nil || resp == nil {
		t.Fatalf("ListArtifactPins failed: %v", err)
	}
	if resp, err := iface.AllocateVersion(ctx, &pb.AllocateVersionRequest{}); err != nil || resp == nil {
		t.Fatalf("AllocateVersion failed: %v", err)
	}
	if resp, err := iface.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{}); err != nil || resp == nil {
		t.Fatalf("AdoptArtifact failed: %v", err)
	}
}

func TestWithRegistryClients(t *testing.T) {
	ctx := context.Background()
	fakeApp := NewFakeAppRegistryClient()
	fakeArtifact := NewFakeArtifactRegistryClient()

	withRegistryClients(fakeApp, fakeArtifact, func() {
		appClient, closeApp, err := NewAppRegistryClient(ctx)
		if err != nil {
			t.Fatalf("unexpected error getting AppRegistryClient: %v", err)
		}
		defer closeApp() //nolint:errcheck

		if appClient != fakeApp {
			t.Errorf("expected injected fakeApp client, got %v", appClient)
		}

		artifactClient, closeArtifact, err := NewArtifactRegistryClient(ctx)
		if err != nil {
			t.Fatalf("unexpected error getting ArtifactRegistryClient: %v", err)
		}
		defer closeArtifact() //nolint:errcheck

		if artifactClient != fakeArtifact {
			t.Errorf("expected injected fakeArtifact client, got %v", artifactClient)
		}
	})
}
