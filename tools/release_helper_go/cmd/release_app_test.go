package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
)

func setupReleaseAppFixtures() (BazelRunner, GitRunner, DockerRunner, FileSystem, string, ImageTagger) {
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

	return bazelRunner, gitRunner, dockerRunner, fs, fakeWorkspaceRoot, newFakeTagger()
}

func TestExecuteReleaseApp_Success(t *testing.T) {
	bazel, git, docker, fs, ws, tagger := setupReleaseAppFixtures()
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
		Tagger:               tagger,
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

	// Publish should retag the build-scoped (git SHA) image with the
	// release version -- see ImageReleaser.Publish's doc comment.
	fakeTagger, ok := tagger.(*fakeImageTagger)
	if !ok {
		t.Fatalf("expected tagger to be *fakeImageTagger, got %T", tagger)
	}
	if len(fakeTagger.calls) != 1 {
		t.Fatalf("expected 1 registry retag call, got %d: %+v", len(fakeTagger.calls), fakeTagger.calls)
	}
	if fakeTagger.calls[0].newTag != "v1.0.0" {
		t.Errorf("expected retag to v1.0.0, got %q", fakeTagger.calls[0].newTag)
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
		Tagger:               newFakeTagger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.DigestUnchanged {
		t.Errorf("expected DigestUnchanged=false for major bump with shared digest, got true")
	}
	if !res.Published {
		t.Errorf("expected Published=true for major/minor shared digest release, got false")
	}
	if res.EffectiveVersion != "v1.0.0" {
		t.Errorf("expected EffectiveVersion='v1.0.0', got %q", res.EffectiveVersion)
	}
	if res.EffectiveTag != "demo-hello-go.v1.0.0" {
		t.Errorf("expected EffectiveTag='demo-hello-go.v1.0.0', got %q", res.EffectiveTag)
	}
	if res.PreviousTag != "demo-hello-go.v0.9.0" {
		t.Errorf("expected PreviousTag='demo-hello-go.v0.9.0', got %q", res.PreviousTag)
	}

	// RecordArtifact SHOULD be called for the new major/minor version with the shared digest
	if len(fakeArtifactClient.RecordArtifactCalls) != 1 {
		t.Fatalf("expected 1 RecordArtifact call for shared digest release, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
	recReq := fakeArtifactClient.RecordArtifactCalls[0]
	if recReq.Version != "v1.0.0" || recReq.Digest != sharedDigest {
		t.Errorf("expected RecordArtifact for v1.0.0 and sharedDigest, got version=%q digest=%q", recReq.Version, recReq.Digest)
	}
}

func TestExecuteReleaseApp_PatchBumpSameDigestNoOp(t *testing.T) {
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
		fakeGitCall{argsContain: []string{"tag", "--list", "demo-hello-go.v*"}, output: "demo-hello-go.v0.9.0"},
	)

	// Docker inspect returns the identical digest for v0.9.1 (same major.minor as v0.9.0)
	const sharedDigest = "sha256:9999888877776666555544443333222211110000aaaa"
	dockerRunner := newFakeDocker(
		fakeDockerCall{
			argsContain: []string{"buildx", "imagetools", "inspect"},
			output:      fmt.Sprintf("Digest: %s", sharedDigest),
		},
	)

	fakeArtifactClient := NewFakeArtifactRegistryClient()
	tagger := newFakeTagger()

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "demo",
		App:                  "hello-go",
		Version:              "v0.9.1",
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
		Tagger:               tagger,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.DigestUnchanged {
		t.Errorf("expected DigestUnchanged=true, got false")
	}
	if res.Published {
		t.Errorf("expected Published=false on patch no-op rebuild, got true")
	}
	if res.EffectiveVersion != "v0.9.0" {
		t.Errorf("expected EffectiveVersion='v0.9.0', got %q", res.EffectiveVersion)
	}
	if res.EffectiveTag != "demo-hello-go.v0.9.0" {
		t.Errorf("expected EffectiveTag='demo-hello-go.v0.9.0', got %q", res.EffectiveTag)
	}

	// Regression guard: a no-op rebuild must never create the v0.9.1 tag in
	// the registry -- Publish (and its retag) must not run when the release
	// is a no-op, or GHCR ends up with a version tag App Registry considers
	// unpublished (this test's whole reason for existing).
	if len(tagger.calls) != 0 {
		t.Errorf("expected 0 registry retag calls on no-op rebuild, got %d: %+v", len(tagger.calls), tagger.calls)
	}

	// FailPublish should be called to record no-op status
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call for patch no-op rebuild, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
	// RecordArtifact should NOT be called on patch no-op
	if len(fakeArtifactClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 RecordArtifact calls on patch no-op rebuild, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
}

func TestExecuteReleaseApp_GRPCErrorsHandledGracefully(t *testing.T) {
	bazel, git, docker, fs, ws, tagger := setupReleaseAppFixtures()

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
		Tagger:               tagger,
	})
	if err != nil {
		t.Fatalf("expected release-app to succeed despite non-fatal registry errors, got: %v", err)
	}
	if !res.Published {
		t.Errorf("expected Published=true, got false")
	}
}

func TestExecuteReleaseApp_SkipRegistry(t *testing.T) {
	bazel, git, docker, fs, ws, tagger := setupReleaseAppFixtures()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:         "demo",
		App:            "hello-go",
		Version:        "v1.0.0",
		BuildID:        "build-101",
		SkipRegistry:   true,
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		WorkspaceRoot:  ws,
		ArtifactClient: fakeArtifactClient,
		Tagger:         tagger,
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
	bazel, git, docker, fs, ws, _ := setupReleaseAppFixtures()
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
	bazel, _, _, fs, ws, _ := setupReleaseAppFixtures()

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
	bazel, git, docker, fs, _, _ := setupReleaseAppFixtures()

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
	if res.Digest == "" {
		t.Errorf("expected non-empty Digest for non-image app, got empty")
	}
	if len(fakeArtifactClient.BeginPublishCalls) != 1 {
		t.Errorf("expected 1 BeginPublishCall for non-image app, got %d", len(fakeArtifactClient.BeginPublishCalls))
	} else if fakeArtifactClient.BeginPublishCalls[0].Kind != pb.ArtifactKind_ARTIFACT_KIND_BINARY {
		t.Errorf("expected ARTIFACT_KIND_BINARY in BeginPublish, got %v", fakeArtifactClient.BeginPublishCalls[0].Kind)
	}
	if len(fakeArtifactClient.RecordArtifactCalls) != 1 {
		t.Errorf("expected 1 RecordArtifactCall for non-image app, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	} else if fakeArtifactClient.RecordArtifactCalls[0].Kind != pb.ArtifactKind_ARTIFACT_KIND_BINARY {
		t.Errorf("expected ARTIFACT_KIND_BINARY in RecordArtifact, got %v", fakeArtifactClient.RecordArtifactCalls[0].Kind)
	}
	if len(dockerRunner.recorded) != 0 {
		t.Errorf("expected 0 docker calls for non-image app, got %d", len(dockerRunner.recorded))
	}
}

// TestExecuteReleaseApp_NonImageNoOpDigestDetection reproduces the class of
// bug behind tools-app-registry v0.2.3 getting stuck "publishing" with no
// digest in prod: a CLI/binary patch bump whose binary content is
// byte-identical to the previous version in the same minor series used to
// go straight to RecordArtifact, which collides with
// artifact_digest_major_minor_idx (owner_id, kind, version_major,
// version_minor, digest) and fails with ErrAlreadyExists -- leaving the row
// BeginPublish created stranded forever. This asserts the no-op is now
// detected up front (via GetArtifact, since binaries have no container
// registry to query the way the image branch queries docker) and the
// "publishing" row is cleaned up via FailPublish instead of ever calling
// RecordArtifact.
func TestExecuteReleaseApp_NonImageNoOpDigestDetection(t *testing.T) {
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

	allBazelCalls := append(bazel.calls,
		fakeBazelCall{
			argsContain: []string{"build"},
			output:      "Built binary target",
		},
	)
	bazelRunner := newFakeBazel(allBazelCalls...)

	// Previous tag exists: tools-app-registry.v0.2.2, same major.minor (0.2)
	// as the v0.2.3 release under test.
	gitRunner := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "sha123456789"},
		fakeGitCall{argsContain: []string{"tag", "--list", "tools-app-registry.v*"}, output: "tools-app-registry.v0.2.2"},
	)

	dockerRunner := newFakeDocker()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	// No built binary exists on disk in this fake filesystem, so
	// binaryDigest falls back to "sha256:" + gitSHA (see computeFileSHA256
	// candidate-path fallback in the app-registry-cli branch of
	// ExecuteReleaseApp). Make GetArtifact report that exact digest as
	// already recorded for the previous version, simulating a no-op rebuild.
	const noOpDigest = "sha256:sha123456789"
	fakeArtifactClient.GetArtifactFn = func(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
		if in.Version != "v0.2.2" {
			t.Errorf("expected GetArtifact lookup for previous version v0.2.2, got %q", in.Version)
		}
		return &pb.GetArtifactResponse{Artifact: &pb.Artifact{Digest: noOpDigest, Version: "v0.2.2"}}, nil
	}

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "tools",
		App:                  "app-registry",
		Version:              "v0.2.3",
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
	if res.EffectiveVersion != "v0.2.2" {
		t.Errorf("expected EffectiveVersion='v0.2.2', got %q", res.EffectiveVersion)
	}
	if res.EffectiveTag != "tools-app-registry.v0.2.2" {
		t.Errorf("expected EffectiveTag='tools-app-registry.v0.2.2', got %q", res.EffectiveTag)
	}

	// BeginPublish still runs as the heartbeat/allocation for the new version...
	if len(fakeArtifactClient.BeginPublishCalls) != 1 {
		t.Fatalf("expected 1 BeginPublish call, got %d", len(fakeArtifactClient.BeginPublishCalls))
	}
	// ...but RecordArtifact must NOT be attempted once the no-op is detected --
	// attempting it is exactly what produced the stuck-in-"publishing" row.
	if len(fakeArtifactClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 RecordArtifact calls on no-op rebuild, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
	// FailPublish must be called to resolve the "publishing" row BeginPublish created.
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call for no-op rebuild, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
	failReq := fakeArtifactClient.FailPublishCalls[0]
	if failReq.Kind != pb.ArtifactKind_ARTIFACT_KIND_BINARY {
		t.Errorf("expected ARTIFACT_KIND_BINARY in FailPublish, got %v", failReq.Kind)
	}
	if failReq.Version != "v0.2.3" {
		t.Errorf("expected FailPublish version 'v0.2.3', got %q", failReq.Version)
	}
	if !strings.Contains(failReq.Reason, "no-op rebuild") {
		t.Errorf("expected FailPublish reason to mention no-op rebuild, got %q", failReq.Reason)
	}
}

// TestExecuteReleaseApp_NonImageRecordArtifactFailureCleansUpPublishing is
// the direct regression test for the prod incident: RecordArtifact fails
// for the binary/CLI path (e.g. a genuine unique-constraint conflict the
// no-op check above didn't catch -- no previous tag/digest info available)
// and, pre-fix, nothing ever called FailPublish to resolve the "publishing"
// row BeginPublish created, leaving it stuck forever with no digest. This
// asserts FailPublish is now always called when RecordArtifact fails.
func TestExecuteReleaseApp_NonImageRecordArtifactFailureCleansUpPublishing(t *testing.T) {
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

	allBazelCalls := append(bazel.calls,
		fakeBazelCall{
			argsContain: []string{"build"},
			output:      "Built binary target",
		},
	)
	bazelRunner := newFakeBazel(allBazelCalls...)

	// No previous tag at all, so the up-front no-op check never fires and
	// RecordArtifact is attempted directly.
	gitRunner := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "sha123456789"},
		fakeGitCall{argsContain: []string{"tag", "--list", "tools-app-registry.v*"}, output: ""},
		fakeGitCall{argsContain: []string{"rev-parse", "tools-app-registry.v0.2.3"}, err: fmt.Errorf("not found")},
		fakeGitCall{argsContain: []string{"tag", "-a"}, output: ""},
	)

	dockerRunner := newFakeDocker()
	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.RecordArtifactFn = func(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error) {
		return nil, fmt.Errorf("already exists: artifact tools-app-registry v0.2.3 already published with digest sha256:...")
	}

	res, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "tools",
		App:                  "app-registry",
		Version:              "v0.2.3",
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
		t.Fatalf("unexpected error (RecordArtifact failure must be non-fatal): %v", err)
	}
	if !res.Published {
		t.Errorf("expected Published=true (build itself succeeded), got false")
	}

	if len(fakeArtifactClient.RecordArtifactCalls) != 1 {
		t.Fatalf("expected 1 RecordArtifact call, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
	// This is the core regression assertion: before the fix, a
	// RecordArtifact failure here was only ever logged as a warning, and
	// FailPublish was never called -- leaving the row BeginPublish created
	// stuck in "publishing" with no digest forever.
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call to clean up the stuck 'publishing' row after RecordArtifact failure, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
	failReq := fakeArtifactClient.FailPublishCalls[0]
	if failReq.Kind != pb.ArtifactKind_ARTIFACT_KIND_BINARY {
		t.Errorf("expected ARTIFACT_KIND_BINARY in FailPublish, got %v", failReq.Kind)
	}
	if failReq.OwnerFullName != "tools-app-registry" {
		t.Errorf("expected owner 'tools-app-registry', got %q", failReq.OwnerFullName)
	}
	if failReq.Version != "v0.2.3" {
		t.Errorf("expected FailPublish version 'v0.2.3', got %q", failReq.Version)
	}
	if !strings.Contains(failReq.Reason, "RecordArtifact failed") {
		t.Errorf("expected FailPublish reason to mention RecordArtifact failure, got %q", failReq.Reason)
	}
}

// TestExecuteReleaseApp_AllocateStage_NonImageRecordArtifactFailureIsFatal proves
// that for a domain at adoption stage "allocate" (issue #834), RecordArtifact
// failures are fatal: FailPublish is called to clean up the publishing row, and
// ExecuteReleaseApp returns a fatal error failing the release.
func TestExecuteReleaseApp_AllocateStage_NonImageRecordArtifactFailureIsFatal(t *testing.T) {
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

	allBazelCalls := append(bazel.calls,
		fakeBazelCall{
			argsContain: []string{"build"},
			output:      "Built binary target",
		},
	)
	bazelRunner := newFakeBazel(allBazelCalls...)

	gitRunner := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "sha123456789"},
		fakeGitCall{argsContain: []string{"tag", "--list", "tools-app-registry.v*"}, output: ""},
		fakeGitCall{argsContain: []string{"rev-parse", "tools-app-registry.v0.2.3"}, err: fmt.Errorf("not found")},
		fakeGitCall{argsContain: []string{"tag", "-a"}, output: ""},
	)

	dockerRunner := newFakeDocker()
	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeArtifactClient.RecordArtifactFn = func(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error) {
		return nil, fmt.Errorf("already exists: artifact tools-app-registry v0.2.3 already published")
	}

	_, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "tools",
		App:                  "app-registry",
		Version:              "v0.2.3",
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
	if err == nil {
		t.Fatal("expected fatal error for RecordArtifact failure on allocate-stage domain, got nil")
	}
	if !strings.Contains(err.Error(), "RecordArtifact failed") {
		t.Errorf("expected error message to mention RecordArtifact failed, got: %v", err)
	}

	if len(fakeArtifactClient.RecordArtifactCalls) != 1 {
		t.Fatalf("expected 1 RecordArtifact call, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call after RecordArtifact failure, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
}

// TestExecuteReleaseApp_AllocateStage_NonImageBeginPublishFailureIsFatal proves
// that for a domain at adoption stage "allocate" (issue #834), BeginPublish
// failure is fatal and aborts the release before building.
func TestExecuteReleaseApp_AllocateStage_NonImageBeginPublishFailureIsFatal(t *testing.T) {
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
	bazelRunner := newFakeBazel(bazel.calls...)
	gitRunner := newFakeGit(fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "sha123456789"})
	dockerRunner := newFakeDocker()

	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeArtifactClient.BeginPublishFn = func(ctx context.Context, in *pb.BeginPublishRequest, opts ...grpc.CallOption) (*pb.BeginPublishResponse, error) {
		return nil, fmt.Errorf("connection refused")
	}

	_, err := ExecuteReleaseApp(ReleaseAppParams{
		Domain:               "tools",
		App:                  "app-registry",
		Version:              "v0.2.3",
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
		t.Fatal("expected fatal error for BeginPublish failure on allocate-stage domain, got nil")
	}
	if !strings.Contains(err.Error(), "BeginPublish failed") {
		t.Errorf("expected error message to mention BeginPublish failed, got: %v", err)
	}
}

// TestExecuteReleaseApp_AllocateStage_ImageRecordArtifactFailureIsFatal proves
// that for an image app in an allocate-stage domain, RecordArtifact failure
// cleans up publishing row and returns a fatal error.
func TestExecuteReleaseApp_AllocateStage_ImageRecordArtifactFailureIsFatal(t *testing.T) {
	bazel, git, docker, fs, ws, tagger := setupReleaseAppFixtures()

	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeArtifactClient.RecordArtifactFn = func(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error) {
		return nil, fmt.Errorf("database constraint violation")
	}

	_, err := ExecuteReleaseApp(ReleaseAppParams{
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
		Tagger:               tagger,
	})
	if err == nil {
		t.Fatal("expected fatal error on image RecordArtifact failure for allocate domain, got nil")
	}
	if !strings.Contains(err.Error(), "RecordArtifact failed") {
		t.Errorf("expected error message to mention RecordArtifact failed, got: %v", err)
	}
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
}

// TestExecuteReleaseApp_AllocateStage_ImageBeginPublishFailureIsFatal proves
// that for an image app in an allocate-stage domain, BeginPublish failure is fatal.
func TestExecuteReleaseApp_AllocateStage_ImageBeginPublishFailureIsFatal(t *testing.T) {
	bazel, git, docker, fs, ws, _ := setupReleaseAppFixtures()

	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeArtifactClient.BeginPublishFn = func(ctx context.Context, in *pb.BeginPublishRequest, opts ...grpc.CallOption) (*pb.BeginPublishResponse, error) {
		return nil, fmt.Errorf("registry network timeout")
	}

	_, err := ExecuteReleaseApp(ReleaseAppParams{
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
	if err == nil {
		t.Fatal("expected fatal error on image BeginPublish failure for allocate domain, got nil")
	}
	if !strings.Contains(err.Error(), "BeginPublish failed") {
		t.Errorf("expected error message to mention BeginPublish failed, got: %v", err)
	}
}
