package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockArtifactReleaser struct {
	kind                pb.ArtifactKind
	buildDigest         string
	buildErr            error
	buildCalls          int
	builtVersions       []string
	fetchExists         bool
	fetchDigest         string
	fetchContentMatches bool
	fetchErr            error
	fetchCalls          int
	fetchedVersions     []string
	publishErr          error
	publishCalls        int
	publishedVersions   []string
	containedImages     []*pb.ContainedImage
}

func (m *mockArtifactReleaser) Kind() pb.ArtifactKind {
	return m.kind
}

func (m *mockArtifactReleaser) Build(ctx context.Context, version string) (string, error) {
	m.buildCalls++
	m.builtVersions = append(m.builtVersions, version)
	if m.buildErr != nil {
		return "", m.buildErr
	}
	return m.buildDigest, nil
}

func (m *mockArtifactReleaser) FetchPublished(ctx context.Context, version string) (bool, string, bool, error) {
	m.fetchCalls++
	m.fetchedVersions = append(m.fetchedVersions, version)
	if m.fetchErr != nil {
		return false, "", false, m.fetchErr
	}
	return m.fetchExists, m.fetchDigest, m.fetchContentMatches, nil
}

func (m *mockArtifactReleaser) Publish(ctx context.Context, version string) error {
	m.publishCalls++
	m.publishedVersions = append(m.publishedVersions, version)
	return m.publishErr
}

func (m *mockArtifactReleaser) ContainedImages() []*pb.ContainedImage {
	return m.containedImages
}

func TestExecuteRelease_StrategySuccess(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
		fakeGitCall{argsContain: []string{"rev-parse", "test-app.v1.0.0"}, err: fmt.Errorf("not found")},
		fakeGitCall{argsContain: []string{"tag", "--list"}, output: ""},
		fakeGitCall{argsContain: []string{"tag", "-a"}, output: ""},
	)

	releaser := &mockArtifactReleaser{
		kind:        pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		buildDigest: "sha256:112233445566",
	}

	res, err := ExecuteRelease(ReleaseParams{
		Domain:               "testdomain",
		OwnerFullName:        "test-app",
		Version:              "v1.0.0",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		GitSHA:               "gitsha12345",
		CreateGitTag:         true,
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.Published {
		t.Errorf("expected Published=true, got false")
	}
	if res.Digest != "sha256:112233445566" {
		t.Errorf("unexpected digest: %q", res.Digest)
	}
	if releaser.buildCalls != 1 || releaser.publishCalls != 1 {
		t.Errorf("expected 1 build and 1 publish call, got build=%d, pub=%d", releaser.buildCalls, releaser.publishCalls)
	}
	if len(fakeClient.BeginPublishCalls) != 1 || len(fakeClient.RecordArtifactCalls) != 1 {
		t.Errorf("expected 1 BeginPublish and 1 RecordArtifact call")
	}
}

func TestExecuteRelease_NoOpRebuild(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
		fakeGitCall{argsContain: []string{"tag", "--list"}, output: "test-app.v1.0.0"},
	)

	releaser := &mockArtifactReleaser{
		kind:                pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		buildDigest:         "sha256:aabbcc",
		fetchExists:         true,
		fetchDigest:         "sha256:aabbcc",
		fetchContentMatches: true,
	}

	res, err := ExecuteRelease(ReleaseParams{
		Domain:               "testdomain",
		OwnerFullName:        "test-app",
		Version:              "v1.0.1",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Published {
		t.Errorf("expected Published=false for no-op rebuild, got true")
	}
	if !res.DigestUnchanged {
		t.Errorf("expected DigestUnchanged=true, got false")
	}
	if res.EffectiveVersion != "v1.0.0" {
		t.Errorf("expected EffectiveVersion='v1.0.0', got %q", res.EffectiveVersion)
	}
	if releaser.publishCalls != 0 {
		t.Errorf("expected 0 publish calls on no-op, got %d", releaser.publishCalls)
	}
	if len(fakeClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call on no-op, got %d", len(fakeClient.FailPublishCalls))
	}
	if !strings.Contains(fakeClient.FailPublishCalls[0].Reason, "no-op rebuild") {
		t.Errorf("expected no-op reason in FailPublish, got %q", fakeClient.FailPublishCalls[0].Reason)
	}
}

func TestExecuteRelease_NewBaselineMajorBump(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
		fakeGitCall{argsContain: []string{"tag", "--list"}, output: "test-app.v0.9.0"},
		fakeGitCall{argsContain: []string{"rev-parse", "test-app.v1.0.0"}, err: fmt.Errorf("not found")},
		fakeGitCall{argsContain: []string{"tag", "-a"}, output: ""},
	)

	releaser := &mockArtifactReleaser{
		kind:                pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		buildDigest:         "sha256:same-content",
		fetchExists:         true,
		fetchDigest:         "sha256:same-content",
		fetchContentMatches: true,
	}

	res, err := ExecuteRelease(ReleaseParams{
		Domain:               "testdomain",
		OwnerFullName:        "test-app",
		Version:              "v1.0.0",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		CreateGitTag:         true,
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.Published {
		t.Errorf("expected Published=true for major bump new baseline, got false")
	}
	if res.EffectiveVersion != "v1.0.0" {
		t.Errorf("expected EffectiveVersion='v1.0.0', got %q", res.EffectiveVersion)
	}
	if len(fakeClient.RecordArtifactCalls) != 1 {
		t.Errorf("expected 1 RecordArtifact call for new baseline")
	}
}

func TestExecuteRelease_BuildFailure_CleansUpPublishing(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	releaser := &mockArtifactReleaser{
		kind:     pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		buildErr: fmt.Errorf("compilation failed"),
	}

	_, err := ExecuteRelease(ReleaseParams{
		Domain:               "testdomain",
		OwnerFullName:        "test-app",
		Version:              "v1.0.0",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		ArtifactClient:       fakeClient,
	})
	if err == nil {
		t.Fatalf("expected error on build failure, got nil")
	}

	if len(fakeClient.BeginPublishCalls) != 1 {
		t.Fatalf("expected 1 BeginPublish call")
	}
	if len(fakeClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call after build error")
	}
	if !strings.Contains(fakeClient.FailPublishCalls[0].Reason, "build failed") {
		t.Errorf("expected FailPublish reason to mention build failed, got %q", fakeClient.FailPublishCalls[0].Reason)
	}
}

func TestExecuteRelease_PublishFailure_CleansUpPublishing(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
		fakeGitCall{argsContain: []string{"tag", "--list"}, output: ""},
	)

	releaser := &mockArtifactReleaser{
		kind:        pb.ArtifactKind_ARTIFACT_KIND_CHART,
		buildDigest: "sha256:chart123",
		publishErr:  fmt.Errorf("ChartMuseum connection refused"),
	}

	_, err := ExecuteRelease(ReleaseParams{
		Domain:               "testdomain",
		OwnerFullName:        "demo-chart",
		Version:              "v0.1.0",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err == nil {
		t.Fatalf("expected error on publish failure, got nil")
	}

	if len(fakeClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call after publish error")
	}
	if !strings.Contains(fakeClient.FailPublishCalls[0].Reason, "connection refused") {
		t.Errorf("expected FailPublish reason to contain connection error, got %q", fakeClient.FailPublishCalls[0].Reason)
	}
}

func TestExecuteRelease_RecordArtifactFailure_CleansUpPublishing(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	fakeClient.RecordArtifactFn = func(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error) {
		return nil, fmt.Errorf("database connection lost")
	}
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
		fakeGitCall{argsContain: []string{"tag", "--list"}, output: ""},
	)

	releaser := &mockArtifactReleaser{
		kind:        pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		buildDigest: "sha256:binary123",
	}

	res, err := ExecuteRelease(ReleaseParams{
		Domain:               "testdomain",
		OwnerFullName:        "test-cli",
		Version:              "v0.1.0",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err != nil {
		t.Fatalf("expected non-fatal return on record failure when not allocate, got: %v", err)
	}
	if !res.Published {
		t.Errorf("expected Published=true because packaging succeeded")
	}

	if len(fakeClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call to clean up publishing row")
	}
	if !strings.Contains(fakeClient.FailPublishCalls[0].Reason, "RecordArtifact failed") {
		t.Errorf("expected FailPublish reason to mention RecordArtifact failed, got %q", fakeClient.FailPublishCalls[0].Reason)
	}
}

func TestExecuteRelease_ChartContainedImagesPassedToRecord(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
		fakeGitCall{argsContain: []string{"tag", "--list"}, output: ""},
	)

	contained := []*pb.ContainedImage{
		{
			AppFullName: "demo-api",
			Repository:  "ghcr.io/whale-net/demo-api",
			Version:     "v1.0.0",
			Digest:      "sha256:image123",
		},
	}

	releaser := &mockArtifactReleaser{
		kind:            pb.ArtifactKind_ARTIFACT_KIND_CHART,
		buildDigest:     "sha256:chart123",
		containedImages: contained,
	}

	res, err := ExecuteRelease(ReleaseParams{
		Domain:               "demo",
		OwnerFullName:        "demo-chart",
		Version:              "v0.1.0",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.ContainedImages) != 1 {
		t.Fatalf("expected 1 contained image in result, got %d", len(res.ContainedImages))
	}
	if len(fakeClient.RecordArtifactCalls) != 1 {
		t.Fatalf("expected 1 RecordArtifact call")
	}
	recReq := fakeClient.RecordArtifactCalls[0]
	if len(recReq.Contains) != 1 || recReq.Contains[0].AppFullName != "demo-api" {
		t.Errorf("expected contained images in RecordArtifactRequest, got %+v", recReq.Contains)
	}
}

func TestExecuteRelease_AllocateStage_NoOpRebuild(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	fakeClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		if in.ChartDomain == "tools" {
			return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
		}
		return &pb.CheckChartHermeticityResponse{Enforced: false}, nil
	}
	fakeClient.GetArtifactFn = func(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
		if in.OwnerFullName == "tools-release_helper_go" && in.LatestPublished {
			return &pb.GetArtifactResponse{
				Artifact: &pb.Artifact{
					Version: "v0.3.0",
					Digest:  "sha256:identical-digest",
					State:   pb.ArtifactState_ARTIFACT_STATE_PUBLISHED,
				},
			}, nil
		}
		return nil, status.Error(codes.NotFound, "not found")
	}

	// Git has NO tags listed -- proving git tag scanning is bypassed for allocate-stage domains
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
	)

	releaser := &mockArtifactReleaser{
		kind:        pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		buildDigest: "sha256:identical-digest",
	}

	res, err := ExecuteRelease(ReleaseParams{
		Domain:               "tools",
		OwnerFullName:        "tools-release_helper_go",
		Version:              "v0.3.1",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Published {
		t.Errorf("expected Published=false on no-op rebuild, got true")
	}
	if !res.DigestUnchanged {
		t.Errorf("expected DigestUnchanged=true, got false")
	}
	if res.EffectiveVersion != "v0.3.0" {
		t.Errorf("expected EffectiveVersion='v0.3.0', got %q", res.EffectiveVersion)
	}
	if releaser.publishCalls != 0 {
		t.Errorf("expected 0 publish calls on no-op, got %d", releaser.publishCalls)
	}
	if len(fakeClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call on no-op, got %d", len(fakeClient.FailPublishCalls))
	}
	if !strings.Contains(fakeClient.FailPublishCalls[0].Reason, "no-op rebuild") {
		t.Errorf("expected FailPublish reason to mention no-op rebuild, got %q", fakeClient.FailPublishCalls[0].Reason)
	}
}

func TestExecuteRelease_AllocateStage_ProceedDifferentDigest(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	fakeClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeClient.GetArtifactFn = func(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
		if in.OwnerFullName == "tools-release_helper_go" && in.LatestPublished {
			return &pb.GetArtifactResponse{
				Artifact: &pb.Artifact{
					Version: "v0.3.0",
					Digest:  "sha256:old-digest",
					State:   pb.ArtifactState_ARTIFACT_STATE_PUBLISHED,
				},
			}, nil
		}
		return nil, status.Error(codes.NotFound, "not found")
	}

	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
	)

	releaser := &mockArtifactReleaser{
		kind:        pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		buildDigest: "sha256:new-digest",
	}

	res, err := ExecuteRelease(ReleaseParams{
		Domain:               "tools",
		OwnerFullName:        "tools-release_helper_go",
		Version:              "v0.3.1",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.Published {
		t.Errorf("expected Published=true, got false")
	}
	if res.EffectiveVersion != "v0.3.1" {
		t.Errorf("expected EffectiveVersion='v0.3.1', got %q", res.EffectiveVersion)
	}
	if len(fakeClient.RecordArtifactCalls) != 1 {
		t.Fatalf("expected 1 RecordArtifact call")
	}
}

func TestExecuteRelease_AllocateStage_FirstReleaseNotFound(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	fakeClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeClient.GetArtifactFn = func(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}

	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
	)

	releaser := &mockArtifactReleaser{
		kind:        pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		buildDigest: "sha256:first-digest",
	}

	res, err := ExecuteRelease(ReleaseParams{
		Domain:               "tools",
		OwnerFullName:        "tools-release_helper_go",
		Version:              "v0.1.0",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.Published {
		t.Errorf("expected Published=true for first release, got false")
	}
	if res.EffectiveVersion != "v0.1.0" {
		t.Errorf("expected EffectiveVersion='v0.1.0', got %q", res.EffectiveVersion)
	}
}

func TestExecuteRelease_AllocateStage_RegistryErrorFailsLoudly(t *testing.T) {
	fakeClient := NewFakeArtifactRegistryClient()
	fakeClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeClient.GetArtifactFn = func(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
		return nil, status.Error(codes.Unavailable, "database connection error")
	}

	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "gitsha12345"},
	)

	releaser := &mockArtifactReleaser{
		kind:        pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		buildDigest: "sha256:some-digest",
	}

	_, err := ExecuteRelease(ReleaseParams{
		Domain:               "tools",
		OwnerFullName:        "tools-release_helper_go",
		Version:              "v0.3.1",
		BuildID:              "build-1",
		IdempotencyKeyPrefix: "run-1",
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       fakeClient,
	})
	if err == nil {
		t.Fatalf("expected error when App Registry GetArtifact fails for allocate domain, got nil")
	}
	if !strings.Contains(err.Error(), "App Registry GetArtifact (previous version) failed") {
		t.Errorf("expected error message to mention GetArtifact failure, got %v", err)
	}
}
