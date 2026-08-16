package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
)

func setupReleaseAppFixtures() (BazelRunner, GitRunner, DockerRunner, FileSystem, string) {
	apps := []fakeApp{
		{
			pkg:          "demo/hello_go",
			targetSuffix: "hello-go_metadata",
			name:         "hello-go",
			domain:       "demo",
		},
		{
			pkg:          "manmanv2/api",
			targetSuffix: "control-api_metadata",
			name:         "control-api",
			domain:       "manmanv2",
		},
	}
	fs, bazel := buildFakeInfra(apps)

	// Add push run target handling to fakeBazel
	allBazelCalls := append(bazel.calls,
		fakeBazelCall{
			argsContain: []string{"run"},
			output:      "Successfully pushed image",
		},
	)
	bazelRunner := newFakeBazel(allBazelCalls...)

	gitRunner := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "sha123456789"},
		fakeGitCall{argsContain: []string{"rev-parse", "demo-hello-go.v1.0.0"}, err: fmt.Errorf("not found")},
		fakeGitCall{argsContain: []string{"tag", "--list", "demo-hello-go.v*"}, output: ""},
		fakeGitCall{argsContain: []string{"tag", "-a"}, output: ""},
	)

	dockerRunner := newFakeDocker(
		fakeDockerCall{
			argsContain: []string{"buildx", "imagetools", "inspect"},
			output:      "Digest: sha256:aabbccdd11223344556677889900aabbccdd11223344556677889900aabbccdd",
		},
	)

	return bazelRunner, gitRunner, dockerRunner, fs, fakeWorkspaceRoot
}

func TestExecuteReleaseApp_Success(t *testing.T) {
	bazel, git, docker, fs, ws := setupReleaseAppFixtures()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "demo",
		App:                  "hello-go",
		Version:              "v1.0.0",
		BuildID:              "build-101",
		IdempotencyKeyPrefix: "run-1-1",
		GitSHA:               "sha123456789",
		Registry:             "ghcr.io",
		CreateGitTag:         true,
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		WorkspaceRoot:        ws,
		ArtifactClient:       fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Domain != "demo" || res.App != "hello-go" || res.Version != "v1.0.0" {
		t.Errorf("unexpected metadata in result: %+v", res)
	}
	if !res.Published {
		t.Errorf("expected Published=true, got false")
	}
	if res.DigestUnchanged {
		t.Errorf("expected DigestUnchanged=false, got true")
	}
	if res.EffectiveVersion != "v1.0.0" {
		t.Errorf("expected EffectiveVersion='v1.0.0', got %q", res.EffectiveVersion)
	}
	if res.EffectiveTag != "demo-hello-go.v1.0.0" {
		t.Errorf("expected EffectiveTag='demo-hello-go.v1.0.0', got %q", res.EffectiveTag)
	}
	if res.Digest != "sha256:aabbccdd11223344556677889900aabbccdd11223344556677889900aabbccdd" {
		t.Errorf("unexpected digest: %q", res.Digest)
	}

	// Verify BeginPublish call
	if len(fakeArtifactClient.BeginPublishCalls) != 1 {
		t.Fatalf("expected 1 BeginPublish call, got %d", len(fakeArtifactClient.BeginPublishCalls))
	}
	beginReq := fakeArtifactClient.BeginPublishCalls[0]
	if beginReq.Kind != pb.ArtifactKind_ARTIFACT_KIND_IMAGE {
		t.Errorf("expected ARTIFACT_KIND_IMAGE, got %v", beginReq.Kind)
	}
	if beginReq.OwnerFullName != "demo-hello-go" {
		t.Errorf("expected owner 'demo-hello-go', got %q", beginReq.OwnerFullName)
	}
	if beginReq.Version != "v1.0.0" {
		t.Errorf("expected version 'v1.0.0', got %q", beginReq.Version)
	}
	if beginReq.BuildId != "build-101" {
		t.Errorf("expected buildId 'build-101', got %q", beginReq.BuildId)
	}
	if beginReq.Repository != "ghcr.io/whale-net/demo-hello-go" {
		t.Errorf("expected repository 'ghcr.io/whale-net/demo-hello-go', got %q", beginReq.Repository)
	}

	// Verify RecordArtifact call
	if len(fakeArtifactClient.RecordArtifactCalls) != 1 {
		t.Fatalf("expected 1 RecordArtifact call, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
	recordReq := fakeArtifactClient.RecordArtifactCalls[0]
	if recordReq.Kind != pb.ArtifactKind_ARTIFACT_KIND_IMAGE {
		t.Errorf("expected ARTIFACT_KIND_IMAGE, got %v", recordReq.Kind)
	}
	if recordReq.OwnerFullName != "demo-hello-go" {
		t.Errorf("expected owner 'demo-hello-go', got %q", recordReq.OwnerFullName)
	}
	if recordReq.Version != "v1.0.0" {
		t.Errorf("expected version 'v1.0.0', got %q", recordReq.Version)
	}
	if recordReq.Digest != res.Digest {
		t.Errorf("expected digest %q, got %q", res.Digest, recordReq.Digest)
	}
	if recordReq.Repository != "ghcr.io/whale-net/demo-hello-go" {
		t.Errorf("expected repository 'ghcr.io/whale-net/demo-hello-go', got %q", recordReq.Repository)
	}
}

func TestExecuteReleaseApp_BazelPushFailure(t *testing.T) {
	apps := []fakeApp{
		{
			pkg:          "demo/hello_go",
			targetSuffix: "hello-go_metadata",
			name:         "hello-go",
			domain:       "demo",
		},
	}
	fs, bazel := buildFakeInfra(apps)

	// Make bazel run push fail
	allBazelCalls := append(bazel.calls,
		fakeBazelCall{
			argsContain: []string{"run"},
			err:         fmt.Errorf("docker authentication failed"),
		},
	)
	bazelRunner := newFakeBazel(allBazelCalls...)

	gitRunner := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "sha123456789"},
	)
	dockerRunner := newFakeDocker()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	_, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "demo",
		App:                  "hello-go",
		Version:              "v1.0.0",
		BuildID:              "build-101",
		IdempotencyKeyPrefix: "run-1-1",
		GitSHA:               "sha123456789",
		Bazel:                bazelRunner,
		Git:                  gitRunner,
		Docker:               dockerRunner,
		FS:                   fs,
		WorkspaceRoot:        fakeWorkspaceRoot,
		ArtifactClient:       fakeArtifactClient,
	})
	if err == nil {
		t.Fatal("expected error on bazel push failure, got nil")
	}
	if !strings.Contains(err.Error(), "docker authentication failed") {
		t.Errorf("expected error to contain push error details, got: %v", err)
	}

	// BeginPublish should have been called
	if len(fakeArtifactClient.BeginPublishCalls) != 1 {
		t.Fatalf("expected 1 BeginPublish call, got %d", len(fakeArtifactClient.BeginPublishCalls))
	}

	// FailPublish should have been called after push failure
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call after failure, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
	failReq := fakeArtifactClient.FailPublishCalls[0]
	if failReq.Kind != pb.ArtifactKind_ARTIFACT_KIND_IMAGE {
		t.Errorf("expected ARTIFACT_KIND_IMAGE, got %v", failReq.Kind)
	}
	if failReq.OwnerFullName != "demo-hello-go" {
		t.Errorf("expected owner 'demo-hello-go', got %q", failReq.OwnerFullName)
	}
	if failReq.Version != "v1.0.0" {
		t.Errorf("expected version 'v1.0.0', got %q", failReq.Version)
	}
	if !strings.Contains(failReq.Reason, "docker authentication failed") {
		t.Errorf("expected reason to contain failure details, got %q", failReq.Reason)
	}

	// RecordArtifact should not have been called
	if len(fakeArtifactClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 RecordArtifact calls on failure, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
}

func TestExecuteReleaseApp_NoOpDigestDetection(t *testing.T) {
	apps := []fakeApp{
		{
			pkg:          "demo/hello_go",
			targetSuffix: "hello-go_metadata",
			name:         "hello-go",
			domain:       "demo",
		},
	}
	fs, bazel := buildFakeInfra(apps)
	allBazelCalls := append(bazel.calls,
		fakeBazelCall{
			argsContain: []string{"run"},
			output:      "Successfully pushed",
		},
	)
	bazelRunner := newFakeBazel(allBazelCalls...)

	// Previous tag exists: demo-hello-go.v0.9.0
	gitRunner := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "sha123456789"},
		fakeGitCall{argsContain: []string{"tag", "--list", "demo-hello-go.v*"}, output: "demo-hello-go.v0.9.0\ndemo-hello-go.v0.8.0"},
	)

	// Docker inspect returns the identical digest for both v1.0.0 and v0.9.0
	const sharedDigest = "sha256:9999888877776666555544443333222211110000aaaa"
	dockerRunner := newFakeDocker(
		fakeDockerCall{
			argsContain: []string{"buildx", "imagetools", "inspect"},
			output:      fmt.Sprintf("Digest: %s", sharedDigest),
		},
	)

	fakeArtifactClient := NewFakeArtifactRegistryClient()

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "demo",
		App:                  "hello-go",
		Version:              "v1.0.0",
		BuildID:              "build-101",
		IdempotencyKeyPrefix: "run-1-1",
		GitSHA:               "sha123456789",
		CreateGitTag:         true,
		Bazel:                bazelRunner,
		Git:                  gitRunner,
		Docker:               dockerRunner,
		FS:                   fs,
		WorkspaceRoot:        fakeWorkspaceRoot,
		ArtifactClient:       fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.DigestUnchanged {
		t.Errorf("expected DigestUnchanged=true, got false")
	}
	if res.Published {
		t.Errorf("expected Published=false on no-op rebuild, got true")
	}
	if res.EffectiveVersion != "v0.9.0" {
		t.Errorf("expected EffectiveVersion='v0.9.0', got %q", res.EffectiveVersion)
	}
	if res.EffectiveTag != "demo-hello-go.v0.9.0" {
		t.Errorf("expected EffectiveTag='demo-hello-go.v0.9.0', got %q", res.EffectiveTag)
	}
	if res.PreviousTag != "demo-hello-go.v0.9.0" {
		t.Errorf("expected PreviousTag='demo-hello-go.v0.9.0', got %q", res.PreviousTag)
	}

	// FailPublish should be called to record no-op rebuild status in the registry
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call for no-op rebuild, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
	failReq := fakeArtifactClient.FailPublishCalls[0]
	if !strings.Contains(failReq.Reason, "digest unchanged") {
		t.Errorf("expected reason to mention digest unchanged, got %q", failReq.Reason)
	}

	// RecordArtifact should NOT be called on no-op
	if len(fakeArtifactClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 RecordArtifact calls on no-op rebuild, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
}

func TestExecuteReleaseApp_GRPCErrorsHandledGracefully(t *testing.T) {
	bazel, git, docker, fs, ws := setupReleaseAppFixtures()

	// Create fake ArtifactClient where BeginPublish and RecordArtifact return errors
	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.BeginPublishFn = func(ctx context.Context, in *pb.BeginPublishRequest, opts ...grpc.CallOption) (*pb.BeginPublishResponse, error) {
		return nil, fmt.Errorf("registry unavailable")
	}
	fakeArtifactClient.RecordArtifactFn = func(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error) {
		return nil, fmt.Errorf("registry write failure")
	}

	// The push should still succeed and return result even if registry warnings occur
	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "demo",
		App:                  "hello-go",
		Version:              "v1.0.0",
		BuildID:              "build-101",
		IdempotencyKeyPrefix: "run-1-1",
		GitSHA:               "sha123456789",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		WorkspaceRoot:        ws,
		ArtifactClient:       fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("expected release-app to succeed despite non-fatal registry errors, got: %v", err)
	}
	if !res.Published {
		t.Errorf("expected Published=true, got false")
	}
}

func TestExecuteReleaseApp_SkipRegistry(t *testing.T) {
	bazel, git, docker, fs, ws := setupReleaseAppFixtures()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "demo",
		App:                  "hello-go",
		Version:              "v1.0.0",
		BuildID:              "build-101",
		SkipRegistry:         true,
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		WorkspaceRoot:        ws,
		ArtifactClient:       fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Published {
		t.Errorf("expected Published=true, got false")
	}
	if len(fakeArtifactClient.BeginPublishCalls) != 0 || len(fakeArtifactClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 registry calls when SkipRegistry is true")
	}
}

func TestExecuteReleaseApp_DryRun(t *testing.T) {
	bazel, git, docker, fs, ws := setupReleaseAppFixtures()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:         "demo",
		App:            "hello-go",
		Version:        "v1.0.0",
		DryRun:         true,
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		WorkspaceRoot:  ws,
		ArtifactClient: fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Published {
		t.Errorf("expected Published=false in dry run, got true")
	}
	if len(fakeArtifactClient.BeginPublishCalls) != 0 || len(fakeArtifactClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 registry calls in dry run")
	}
}

func TestExecuteReleaseApp_InputValidation(t *testing.T) {
	bazel, _, _, fs, ws := setupReleaseAppFixtures()

	// Missing Domain
	_, err := ExecuteReleaseApp(ReleaseAppParams{
		App:           "hello-go",
		Version:       "v1.0.0",
		Bazel:         bazel,
		FS:            fs,
		WorkspaceRoot: ws,
	})
	if err == nil || !strings.Contains(err.Error(), "domain is required") {
		t.Errorf("expected domain error, got: %v", err)
	}

	// Missing App
	_, err = ExecuteReleaseApp(ReleaseAppParams{
		Domain:        "demo",
		Version:       "v1.0.0",
		Bazel:         bazel,
		FS:            fs,
		WorkspaceRoot: ws,
	})
	if err == nil || !strings.Contains(err.Error(), "app is required") {
		t.Errorf("expected app error, got: %v", err)
	}

	// Missing Version
	_, err = ExecuteReleaseApp(ReleaseAppParams{
		Domain:        "demo",
		App:           "hello-go",
		Bazel:         bazel,
		FS:            fs,
		WorkspaceRoot: ws,
	})
	if err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Errorf("expected version error, got: %v", err)
	}

	// Nonexistent App in domain
	_, err = ExecuteReleaseApp(ReleaseAppParams{
		Domain:        "demo",
		App:           "nonexistent-app",
		Version:       "v1.0.0",
		Bazel:         bazel,
		FS:            fs,
		WorkspaceRoot: ws,
	})
	if err == nil || !strings.Contains(err.Error(), "not found in domain") {
		t.Errorf("expected app not found error, got: %v", err)
	}
}

func TestReleaseAppCmd_CLI(t *testing.T) {
	bazel, git, docker, fs, _ := setupReleaseAppFixtures()

	withFS(fs, func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withDocker(docker, func() {
					withWorkspace(fakeWorkspaceRoot, func() {
						// Missing flags
						_, _, err := runTest([]string{"release-app"})
						if err == nil {
							t.Fatal("expected error for missing flags")
						}

						_, _, err = runTest([]string{"release-app", "--domain", "demo"})
						if err == nil {
							t.Fatal("expected error for missing --app flag")
						}

						_, _, err = runTest([]string{"release-app", "--domain", "demo", "--app", "hello-go"})
						if err == nil {
							t.Fatal("expected error for missing --version flag")
						}

						// Dry-run execution
						stdout, stderr, err := runTest([]string{
							"release-app",
							"--domain", "demo",
							"--app", "hello-go",
							"--version", "v1.0.0",
							"--dry-run",
						})
						if err != nil {
							t.Fatalf("unexpected error in dry-run: %v (stderr: %s)", err, stderr)
						}
						_ = stdout
					})
				})
			})
		})
	})
}

func TestExecuteReleaseApp_NonImageCLIApp(t *testing.T) {
	cliJSON := []byte(`{"name":"app-registry","domain":"tools","app_type":"cli","language":"go","binary_target":"@@//tools/app_registry/cli:app-registry","version":"latest"}`)
	apps := []fakeApp{
		{
			pkg:          "tools/app_registry/cli",
			targetSuffix: "app_registry_cli_metadata",
			name:         "app-registry",
			domain:       "tools",
			customJSON:   cliJSON,
		},
	}
	fs, bazel := buildFakeInfra(apps)

	// Add build target handling to fakeBazel for non-image CLI binary
	allBazelCalls := append(bazel.calls,
		fakeBazelCall{
			argsContain: []string{"build"},
			output:      "Built binary target",
		},
	)
	bazelRunner := newFakeBazel(allBazelCalls...)

	gitRunner := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "sha123456789"},
		fakeGitCall{argsContain: []string{"rev-parse", "tools-app-registry.v0.1.0"}, err: fmt.Errorf("not found")},
		fakeGitCall{argsContain: []string{"tag", "-a"}, output: ""},
	)

	dockerRunner := newFakeDocker()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:         "tools",
		App:            "app-registry",
		Version:        "v0.1.0",
		BuildID:        "build-123",
		CreateGitTag:   true,
		Bazel:          bazelRunner,
		Git:            gitRunner,
		Docker:         dockerRunner,
		FS:             fs,
		WorkspaceRoot:  fakeWorkspaceRoot,
		ArtifactClient: fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected ExecuteReleaseApp error: %v", err)
	}

	if !res.Published {
		t.Errorf("expected Published true, got false")
	}
	if res.Digest != "" {
		t.Errorf("expected empty Digest for non-image app, got %s", res.Digest)
	}
	if len(fakeArtifactClient.BeginPublishCalls) != 0 {
		t.Errorf("expected 0 BeginPublishCalls for non-image app, got %d", len(fakeArtifactClient.BeginPublishCalls))
	}
	if len(fakeArtifactClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 RecordArtifactCalls for non-image app, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
	if len(dockerRunner.recorded) != 0 {
		t.Errorf("expected 0 docker calls for non-image app, got %d", len(dockerRunner.recorded))
	}
}

