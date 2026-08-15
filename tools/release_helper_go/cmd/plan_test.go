package cmd

import (
	"errors"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// ── input validation ──────────────────────────────────────────────────────────

func TestPlanInvalidEventType(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "invalid-event"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "event-type must be one of") {
		t.Errorf("want 'event-type must be one of' in stderr, got: %q", stderr)
	}
}

func TestPlanInvalidFormat(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--apps", "all", "--version", "v1.0.0", "--format", "invalid"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "format must be one of: json, github") {
		t.Errorf("want format error in stderr, got: %q", stderr)
	}
}

func TestPlanMutuallyExclusiveVersionAndMinor(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--version", "v1.0.0", "--increment-minor"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("want 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

func TestPlanMutuallyExclusiveVersionAndPatch(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--version", "v1.0.0", "--increment-patch"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("want 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

func TestPlanMutuallyExclusiveMinorAndPatch(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--increment-minor", "--increment-patch"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("want 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

func TestPlanValidEventTypes(t *testing.T) {
	for _, et := range validEventTypes {
		// use invalid format to trigger early exit after event-type passes
		_, _, err := runTest([]string{"plan", "--event-type", et, "--format", "invalid"})
		if err == nil {
			t.Fatalf("expected format error for event-type=%q", et)
		}
	}
}

// ── planRelease unit tests (no cobra, direct function call) ──────────────────

func makeTestApps() ([]fakeApp, *fakeFS, *fakeBazelRunner) {
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
		{pkg: "demo/hello_python", targetSuffix: "hello-python_metadata", name: "hello-python", domain: "demo"},
		{pkg: "manmanv2/api", targetSuffix: "control-api_metadata", name: "control-api", domain: "manmanv2"},
		{pkg: "manmanv2/processor", targetSuffix: "event-processor_metadata", name: "event-processor", domain: "manmanv2"},
	}
	fs, bazel := buildFakeInfra(apps)
	return apps, fs, bazel
}

func TestPlanReleaseWorkflowDispatchAll(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType:     "workflow_dispatch",
		requestedApps: "all",
		version:       "v1.0.0",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// demo excluded by default
	if len(result.Apps) != 2 {
		t.Errorf("expected 2 non-demo apps, got %d: %v", len(result.Apps), result.Apps)
	}
	for _, name := range result.Apps {
		if strings.HasPrefix(name, "demo-") {
			t.Errorf("demo app should be excluded: %q", name)
		}
	}
}

func TestPlanReleaseWorkflowDispatchAllIncludeDemo(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType:     "workflow_dispatch",
		requestedApps: "all",
		version:       "v2.0.0",
		includeDemo:   true,
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Apps) != 4 {
		t.Errorf("expected 4 apps (including demo), got %d: %v", len(result.Apps), result.Apps)
	}
}

func TestPlanReleaseWorkflowDispatchSpecificApps(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType:     "workflow_dispatch",
		requestedApps: "demo-hello-go,control-api",
		version:       "v1.5.0",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Apps) != 2 {
		t.Errorf("expected 2 apps, got %d: %v", len(result.Apps), result.Apps)
	}
}

func TestPlanReleaseWorkflowDispatchByDomain(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType:     "workflow_dispatch",
		requestedApps: "manmanv2",
		version:       "v1.0.0",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Apps) != 2 {
		t.Errorf("expected 2 manmanv2 apps, got %d: %v", len(result.Apps), result.Apps)
	}
}

func TestPlanReleaseWorkflowDispatchMissingApps(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	_, err := planRelease(planParams{
		eventType:     "workflow_dispatch",
		requestedApps: "",
		version:       "v1.0.0",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err == nil {
		t.Fatal("expected error when --apps not specified")
	}
}

func TestPlanReleaseWorkflowDispatchInvalidApp(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	_, err := planRelease(planParams{
		eventType:     "workflow_dispatch",
		requestedApps: "nonexistent-app",
		version:       "v1.0.0",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err == nil {
		t.Fatal("expected error for invalid app name")
	}
}

func TestPlanReleaseTagPushNoVersion(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	_, err := planRelease(planParams{
		eventType:     "tag_push",
		version:       "",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err == nil {
		t.Fatal("expected error when version missing for tag_push")
	}
}

func TestPlanReleaseTagPushWithChanges(t *testing.T) {
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
		{pkg: "manmanv2/api", targetSuffix: "control-api_metadata", name: "control-api", domain: "manmanv2"},
	}
	fs, baseBazel := buildFakeInfra(apps)
	target := "//demo/hello_go:hello-go_metadata"
	allCalls := append(baseBazel.calls,
		fakeBazelCall{
			argsContain:    []string{"//demo/hello_go:main.go"},
			argsNotContain: []string{"rdeps"},
			output:         "//demo/hello_go:main.go",
		},
		fakeBazelCall{argsContain: []string{"rdeps(//...,"},  output: target},
		fakeBazelCall{
			argsContain:    []string{"rdeps(", target},
			argsNotContain: []string{"rdeps(//...,"},
			output:         target,
		},
	)
	bazel := newFakeBazel(allCalls...)
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"describe"}, output: "prev-tag.v1.0.0"},
		fakeGitCall{argsContain: []string{"diff", "--name-only"}, output: "demo/hello_go/main.go"},
	)

	result, err := planRelease(planParams{
		eventType:     "tag_push",
		version:       "v2.0.0",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Apps) != 1 || result.Apps[0] != "demo-hello-go" {
		t.Errorf("expected [demo-hello-go], got %v", result.Apps)
	}
}

func TestPlanReleaseFallback(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType:     "fallback",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Apps) != 4 {
		t.Errorf("expected all 4 apps for fallback, got %d", len(result.Apps))
	}
}

func TestPlanReleasePullRequestNoBaseCommit(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType:     "pull_request",
		baseCommit:    "",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Apps) != 4 {
		t.Errorf("expected all 4 apps when no base commit, got %d", len(result.Apps))
	}
}

// ── helper tests ──────────────────────────────────────────────────────────────

func TestIncrementVersion(t *testing.T) {
	tests := []struct {
		input         string
		incrementType string
		want          string
	}{
		{"v1.2.3", "minor", "v1.3.0"},
		{"v1.2.3", "patch", "v1.2.4"},
		{"v0.0.0", "minor", "v0.1.0"},
		{"v2.5.1", "patch", "v2.5.2"},
		{"v1.2.3-beta1", "patch", "v1.2.4"},
	}
	for _, tt := range tests {
		got, err := incrementVersion(tt.input, tt.incrementType)
		if err != nil {
			t.Errorf("incrementVersion(%q, %q): %v", tt.input, tt.incrementType, err)
			continue
		}
		if got != tt.want {
			t.Errorf("incrementVersion(%q, %q) = %q, want %q", tt.input, tt.incrementType, got, tt.want)
		}
	}
}

func TestAutoIncrementVersionNoTags(t *testing.T) {
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: ""},
	)
	ver, err := autoIncrementVersion("demo", "hello-go", "minor", git)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "v0.1.0" {
		t.Errorf("got %q, want %q", ver, "v0.1.0")
	}
}

func TestAutoIncrementVersionWithTags(t *testing.T) {
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: "demo-hello-go.v1.3.0\ndemo-hello-go.v1.2.0"},
	)
	ver, err := autoIncrementVersion("demo", "hello-go", "patch", git)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "v1.3.1" {
		t.Errorf("got %q, want %q", ver, "v1.3.1")
	}
}

func TestResolveApps(t *testing.T) {
	allApps := []AppMetadata{
		{AppManifest: &appmetapb.AppManifest{Name: "hello-go", Domain: "demo"}, BazelTarget: "//demo/hello_go:hello-go_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "hello-python", Domain: "demo"}, BazelTarget: "//demo/hello_python:hello-python_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "control-api", Domain: "manmanv2"}, BazelTarget: "//manmanv2/api:control-api_metadata"},
	}

	tests := []struct {
		requested []string
		wantCount int
		wantErr   bool
	}{
		{[]string{"demo-hello-go"}, 1, false},           // full name
		{[]string{"demo"}, 2, false},                    // domain
		{[]string{"control-api"}, 1, false},             // short (unambiguous)
		{[]string{"nonexistent"}, 0, true},              // invalid
		{[]string{"demo-hello-go", "control-api"}, 2, false},
	}
	for _, tt := range tests {
		got, err := resolveApps(tt.requested, allApps)
		if tt.wantErr {
			if err == nil {
				t.Errorf("resolveApps(%v): expected error", tt.requested)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveApps(%v): unexpected error: %v", tt.requested, err)
			continue
		}
		if len(got) != tt.wantCount {
			t.Errorf("resolveApps(%v): got %d apps, want %d", tt.requested, len(got), tt.wantCount)
		}
	}
}

func TestJoinStrings(t *testing.T) {
	got := joinStrings([]string{"a", "b", "c"})
	if got != "a, b, c" {
		t.Errorf("joinStrings = %q, want %q", got, "a, b, c")
	}
}

func TestIsValidEventType(t *testing.T) {
	for _, et := range validEventTypes {
		if !isValidEventType(et) {
			t.Errorf("%q should be valid", et)
		}
	}
	if isValidEventType("invalid") {
		t.Error("'invalid' should not be valid")
	}
}

// ── typed validation errors ──────────────────────────────────────────────────

func TestPlanTypedValidationErrors(t *testing.T) {
	_, _, err := runTest([]string{"plan", "--event-type", "invalid-type"})
	if err == nil {
		t.Fatal("expected error")
	}
	var valErr *PlanValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("expected PlanValidationError, got %T: %v", err, err)
	}
	if valErr.Field != "event-type" {
		t.Errorf("expected field 'event-type', got %q", valErr.Field)
	}

	_, _, err = runTest([]string{"plan", "--event-type", "workflow_dispatch", "--version", "v1.0.0", "--increment-minor"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.As(err, &valErr) || valErr.Field != "version_options" {
		t.Errorf("expected version_options validation error, got %v", err)
	}
}

// ── OpenAPI Planning ─────────────────────────────────────────────────────────

func TestPlanOpenAPISpecsPlanning(t *testing.T) {
	apps := []fakeApp{
		{
			pkg:          "demo/hello_py",
			targetSuffix: "hello-py_metadata",
			name:         "hello-py",
			domain:       "demo",
			customJSON: []byte(
				`{"name":"hello-py","domain":"demo","language":"python","registry":"ghcr.io","organization":"whale-net","repo_name":"demo-hello-py","image_target":"@@//demo/hello_py:image","binary_target":"@@//demo/hello_py:bin","version":"latest","openapi_spec_target":"@@//demo/hello_py:spec"}`,
			),
		},
		{
			pkg:          "manmanv2/api",
			targetSuffix: "control-api_metadata",
			name:         "control-api",
			domain:       "manmanv2",
		},
	}
	fs, bazel := buildFakeInfra(apps)
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType:     "workflow_dispatch",
		requestedApps: "demo-hello-py,control-api",
		version:       "v1.0.0",
		bazel:         bazel,
		git:           git,
		fs:            fs,
		workspaceRoot: fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.HasSpecs {
		t.Fatal("expected HasSpecs to be true")
	}
	if result.OpenAPIMatrix == nil {
		t.Fatal("expected OpenAPIMatrix to be non-nil")
	}
	include, ok := result.OpenAPIMatrix["include"].([]map[string]string)
	if !ok || len(include) != 1 {
		t.Fatalf("expected 1 openapi spec in include, got %+v", result.OpenAPIMatrix)
	}
	if include[0]["app"] != "hello-py" || include[0]["openapi_target"] != "//demo/hello_py:spec" {
		t.Errorf("unexpected openapi spec entry: %+v", include[0])
	}
}

// ── Helm Charts Planning ────────────────────────────────────────────────────

func TestPlanReleaseWithCharts(t *testing.T) {
	fs := newFakeFS()

	appQueryLines := []string{"//manmanv2/api:control-api_metadata"}
	appCqueryLines := []string{"@@//manmanv2/api:control-api_metadata\t" + string(sampleMetaJSON("control-api", "manmanv2"))}
	chartQueryLines := []string{"//manmanv2:control_chart_metadata"}
	chartCqueryLines := []string{"@@//manmanv2:control_chart_metadata\t" + string(sampleHelmMetaJSON("helm-control", "manmanv2", []string{"control-api"}))}

	bazelCalls := []fakeBazelCall{
		{argsContain: []string{"query", "kind(app_metadata"}, argsNotContain: []string{"cquery"}, output: strings.Join(appQueryLines, "\n")},
		{argsContain: []string{"cquery", "control-api_metadata"}, output: strings.Join(appCqueryLines, "\n")},
		{argsContain: []string{"query", "kind(helm_chart_metadata"}, argsNotContain: []string{"cquery"}, output: strings.Join(chartQueryLines, "\n")},
		{argsContain: []string{"cquery", "control_chart_metadata"}, output: strings.Join(chartCqueryLines, "\n")},
	}
	bazel := newFakeBazel(bazelCalls...)
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort", "helm-control.*"}, output: "helm-control.v1.0.0"},
	)

	result, err := planRelease(planParams{
		eventType:       "workflow_dispatch",
		requestedApps:   "control-api",
		requestedCharts: "all",
		incrementMinor:  true,
		bazel:           bazel,
		git:             git,
		fs:              fs,
		workspaceRoot:   fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Charts) != 1 || result.Charts[0] != "helm-control" {
		t.Fatalf("expected charts [helm-control], got %v", result.Charts)
	}
	chartMatrixInclude, ok := result.ChartMatrix["include"].([]map[string]string)
	if !ok || len(chartMatrixInclude) != 1 {
		t.Fatalf("expected 1 chart in ChartMatrix, got %+v", result.ChartMatrix)
	}
	if chartMatrixInclude[0]["version"] != "v1.1.0" {
		t.Errorf("expected chart version v1.1.0, got %s", chartMatrixInclude[0]["version"])
	}
}

// ── App Registry Upfront Calls ──────────────────────────────────────────────

func TestPlanAppRegistryUpfrontCalls(t *testing.T) {
	apps, fs, baseBazel := makeTestApps()
	bazelCalls := append(baseBazel.calls,
		fakeBazelCall{argsContain: []string{"query", "kind(helm_chart_metadata"}, output: ""},
	)
	bazel := newFakeBazel(bazelCalls...)

	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "abc123commit"},
		fakeGitCall{argsContain: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "main"},
		fakeGitCall{argsContain: []string{"log", "-1"}, output: "1670000000"},
	)

	fakeAppClient := NewFakeAppRegistryClient()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	result, err := planRelease(planParams{
		eventType:              "workflow_dispatch",
		requestedApps:          "control-api",
		version:                "v1.0.0",
		gitSHA:                 "test-sha",
		gitRef:                 "refs/heads/main",
		workflowRunID:          "run-999",
		workflowAttempt:        2,
		actor:                  "ci-bot",
		idempotencyKeyPrefix:   "run-999-2",
		bazel:                  bazel,
		git:                    git,
		fs:                     fs,
		workspaceRoot:          fakeWorkspaceRoot,
		appRegistryClient:      fakeAppClient,
		artifactRegistryClient: fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.BuildID != "test-build-id" {
		t.Errorf("expected BuildID 'test-build-id', got %q", result.BuildID)
	}

	// Verify AssertApps was called
	if len(fakeAppClient.AssertAppsCalls) != 1 {
		t.Fatalf("expected 1 AssertApps call, got %d", len(fakeAppClient.AssertAppsCalls))
	}
	assertReq := fakeAppClient.AssertAppsCalls[0]
	if assertReq.IdempotencyKey != "run-999-2-assert" {
		t.Errorf("expected idempotency key 'run-999-2-assert', got %q", assertReq.IdempotencyKey)
	}
	if assertReq.Manifests == nil || len(assertReq.Manifests.Apps) != len(apps) {
		t.Errorf("expected all %d apps in assert manifests, got %+v", len(apps), assertReq.Manifests)
	}

	// Verify RecordBuild was called
	if len(fakeArtifactClient.RecordBuildCalls) != 1 {
		t.Fatalf("expected 1 RecordBuild call, got %d", len(fakeArtifactClient.RecordBuildCalls))
	}
	buildReq := fakeArtifactClient.RecordBuildCalls[0]
	if buildReq.GitSha != "test-sha" || buildReq.WorkflowRunId != "run-999" || buildReq.WorkflowAttempt != 2 || buildReq.Actor != "ci-bot" {
		t.Errorf("unexpected RecordBuild request: %+v", buildReq)
	}

	// Verify BeginPublishBatch was called
	if len(fakeArtifactClient.BeginPublishBatchCalls) != 1 {
		t.Fatalf("expected 1 BeginPublishBatch call, got %d", len(fakeArtifactClient.BeginPublishBatchCalls))
	}
	batchReq := fakeArtifactClient.BeginPublishBatchCalls[0]
	if batchReq.BuildId != "test-build-id" {
		t.Errorf("expected batch BuildId 'test-build-id', got %q", batchReq.BuildId)
	}
	if len(batchReq.Targets) != 1 {
		t.Fatalf("expected 1 target in batch, got %d", len(batchReq.Targets))
	}
	if batchReq.Targets[0].OwnerFullName != "manmanv2-control-api" || batchReq.Targets[0].Version != "v1.0.0" || batchReq.Targets[0].Kind != pb.ArtifactKind_ARTIFACT_KIND_IMAGE {
		t.Errorf("unexpected batch target: %+v", batchReq.Targets[0])
	}
}

func TestPlanDryRunAndSkipRegistry(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	fakeAppClient := NewFakeAppRegistryClient()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	// Dry run: no registry calls
	_, err := planRelease(planParams{
		eventType:              "workflow_dispatch",
		requestedApps:          "control-api",
		version:                "v1.0.0",
		dryRun:                 true,
		bazel:                  bazel,
		git:                    git,
		fs:                     fs,
		workspaceRoot:          fakeWorkspaceRoot,
		appRegistryClient:      fakeAppClient,
		artifactRegistryClient: fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fakeAppClient.AssertAppsCalls) != 0 || len(fakeArtifactClient.RecordBuildCalls) != 0 {
		t.Errorf("expected 0 registry calls during dry run")
	}

	// Skip registry: no registry calls
	_, err = planRelease(planParams{
		eventType:              "workflow_dispatch",
		requestedApps:          "control-api",
		version:                "v1.0.0",
		skipRegistry:           true,
		bazel:                  bazel,
		git:                    git,
		fs:                     fs,
		workspaceRoot:          fakeWorkspaceRoot,
		appRegistryClient:      fakeAppClient,
		artifactRegistryClient: fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fakeAppClient.AssertAppsCalls) != 0 || len(fakeArtifactClient.RecordBuildCalls) != 0 {
		t.Errorf("expected 0 registry calls during skip registry")
	}
}

