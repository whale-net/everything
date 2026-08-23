package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestPlanMutuallyExclusiveVersionAndMajor(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--version", "v1.0.0", "--increment-major"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("want 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

func TestPlanMutuallyExclusiveMajorAndMinor(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--increment-major", "--increment-minor"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("want 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

func TestPlanMutuallyExclusiveMajorAndPatch(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--increment-major", "--increment-patch"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("want 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

func TestPlanMissingVersionOptionWorkflowDispatch(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--apps", "all"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "manual releases require --version, --increment-major, --increment-minor, --increment-patch, or --version-selections") {
		t.Errorf("want missing version option error, got: %q", stderr)
	}
}

func TestPlanInvalidSemver(t *testing.T) {
	_, stderr, err := runTest([]string{"plan", "--event-type", "workflow_dispatch", "--apps", "all", "--version", "1.0.0"})
	if err == nil {
		t.Fatal("expected error for version missing 'v' prefix")
	}
	if !strings.Contains(stderr, "does not follow semantic versioning") {
		t.Errorf("want semver error in stderr, got: %q", stderr)
	}

	_, stderr, err = runTest([]string{"plan", "--event-type", "workflow_dispatch", "--apps", "all", "--version", "invalid"})
	if err == nil {
		t.Fatal("expected error for non-semver version")
	}
	if !strings.Contains(stderr, "does not follow semantic versioning") {
		t.Errorf("want semver error in stderr, got: %q", stderr)
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
		requestedApps: "demo-hello-go,manmanv2-control-api",
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

// TestPlanReleaseWorkflowDispatch_AppsMetadata_NoBazelQuery proves the
// --apps-metadata path (issue #889 follow-up -- see
// tools/app_registry/worker/release/plan.go's package doc comment) never
// calls bazel: newFakeBazel() with zero registered calls errors on any
// Run() invocation, so a successful plan with zero recorded calls is
// direct proof ListAllApps' bazel query was never reached.
func TestPlanReleaseWorkflowDispatch_AppsMetadata_NoBazelQuery(t *testing.T) {
	bazel := newFakeBazel()
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType: "workflow_dispatch",
		appsMetadata: []AppMetadataInput{
			{Domain: "demo", Name: "hello-go", AppType: "external-api"},
			{Domain: "manmanv2", Name: "control-api", AppType: "internal-api"},
		},
		version: "v1.5.0",
		bazel:   bazel,
		git:     git,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Apps) != 2 {
		t.Errorf("expected 2 apps, got %d: %v", len(result.Apps), result.Apps)
	}
	if len(bazel.recorded) != 0 {
		t.Errorf("expected zero bazel calls for the metadata-input path, got %v", bazel.recorded)
	}
}

// TestPlanReleaseWorkflowDispatch_ChartsMetadata_NoBazelQuery mirrors
// TestPlanReleaseWorkflowDispatch_AppsMetadata_NoBazelQuery for
// --charts-metadata.
func TestPlanReleaseWorkflowDispatch_ChartsMetadata_NoBazelQuery(t *testing.T) {
	bazel := newFakeBazel()
	git := newFakeGit()

	result, err := planRelease(planParams{
		eventType: "workflow_dispatch",
		chartsMetadata: []HelmChartMetadataInput{
			{Domain: "manmanv2", Name: "control-services"},
		},
		version: "v1.5.0",
		bazel:   bazel,
		git:     git,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Charts) != 1 {
		t.Errorf("expected 1 chart, got %d: %v", len(result.Charts), result.Charts)
	}
	if len(bazel.recorded) != 0 {
		t.Errorf("expected zero bazel calls for the metadata-input path, got %v", bazel.recorded)
	}
}

// TestPlanMutuallyExclusiveAppsAndAppsMetadata exercises the CLI-layer
// validation (newPlanCmd's RunE) via runTest, since planRelease itself has
// no --apps vs --apps-metadata guard -- that mutual exclusion is enforced
// before planRelease is ever called.
func TestPlanMutuallyExclusiveAppsAndAppsMetadata(t *testing.T) {
	_, stderr, err := runTest([]string{
		"plan", "--event-type", "workflow_dispatch",
		"--apps", "demo-hello-go",
		"--apps-metadata", `[{"domain":"demo","name":"hello-go","app_type":"external-api"}]`,
		"--version", "v1.0.0",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("want 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

// TestPlanMutuallyExclusiveChartsAndChartsMetadata mirrors
// TestPlanMutuallyExclusiveAppsAndAppsMetadata for charts.
func TestPlanMutuallyExclusiveChartsAndChartsMetadata(t *testing.T) {
	_, stderr, err := runTest([]string{
		"plan", "--event-type", "workflow_dispatch",
		"--charts", "manmanv2-control-services",
		"--charts-metadata", `[{"domain":"manmanv2","name":"control-services"}]`,
		"--version", "v1.0.0",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("want 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

// TestPlanCmd_AppsMetadata_InvalidJSON proves a malformed --apps-metadata
// value is rejected with a clear error rather than an obscure downstream
// panic.
func TestPlanCmd_AppsMetadata_InvalidJSON(t *testing.T) {
	_, stderr, err := runTest([]string{
		"plan", "--event-type", "workflow_dispatch",
		"--apps-metadata", `not-json`,
		"--version", "v1.0.0",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "--apps-metadata is not valid JSON") {
		t.Errorf("want '--apps-metadata is not valid JSON' in stderr, got: %q", stderr)
	}
}

// ── --version-selections (issue #889 follow-up: per-target version picker) ────

// TestPlanReleaseWorkflowDispatch_VersionSelections_MixedPerTarget is the
// direct regression test for the release-trigger UI's per-target Draft-page
// picker: one app gets an explicit hardcoded version, one gets its own bump
// type overriding the (absent) batch default, and one has no per-target
// entry at all and falls back to the batch-wide --increment-minor flag.
func TestPlanReleaseWorkflowDispatch_VersionSelections_MixedPerTarget(t *testing.T) {
	git := newFakeGit(
		// Every `git tag --list` call succeeds but matches zero tags --
		// autoIncrementVersion's "no tags" default applies to every
		// bump-type resolution below. Stubbed explicitly (rather than
		// left unregistered) so a real git failure and "ran fine, found
		// nothing" stay distinguishable -- see autoIncrementVersion's doc
		// comment.
		fakeGitCall{argsContain: []string{"tag", "--list"}, output: "", err: nil},
	)
	bazel := newFakeBazel()

	result, err := planRelease(planParams{
		eventType: "workflow_dispatch",
		appsMetadata: []AppMetadataInput{
			{Domain: "demo", Name: "hello-go", AppType: "external-api"},
			{Domain: "demo", Name: "worker", AppType: "worker"},
			{Domain: "demo", Name: "job", AppType: "job"},
		},
		versionSelections: map[string]string{
			"demo-hello-go": "v9.9.9",
			"demo-worker":   "major",
		},
		incrementMinor: true, // batch-wide default, applies only to demo-job (no per-target entry)
		bazel:          bazel,
		git:            git,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.Versions["demo-hello-go"]; got != "v9.9.9" {
		t.Errorf("demo-hello-go: want explicit v9.9.9, got %q", got)
	}
	if got := result.Versions["demo-worker"]; got != "v0.0.1" {
		t.Errorf("demo-worker: want major-bump-type resolution with no existing tags -> autoIncrementVersion's no-tags default v0.0.1 (only \"minor\" defaults to v0.1.0 with no tags), got %q", got)
	}
	if got := result.Versions["demo-job"]; got != "v0.1.0" {
		t.Errorf("demo-job: want batch-wide --increment-minor default v0.1.0 (no per-target entry), got %q", got)
	}
}

// TestPlanCmd_VersionSelections_InvalidEntry proves a --version-selections
// value that is neither a bump keyword nor a valid semver is rejected
// clearly, the same way --version's own malformed-value check is.
func TestPlanCmd_VersionSelections_InvalidEntry(t *testing.T) {
	_, stderr, err := runTest([]string{
		"plan", "--event-type", "workflow_dispatch",
		"--apps-metadata", `[{"domain":"demo","name":"hello-go","app_type":"external-api"}]`,
		"--version-selections", `{"demo-hello-go":"not-a-version"}`,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "version-selections") {
		t.Errorf("want 'version-selections' in stderr, got: %q", stderr)
	}
}

// TestPlanCmd_VersionSelections_InvalidJSON proves malformed
// --version-selections JSON is rejected rather than panicking downstream.
func TestPlanCmd_VersionSelections_InvalidJSON(t *testing.T) {
	_, stderr, err := runTest([]string{
		"plan", "--event-type", "workflow_dispatch",
		"--apps-metadata", `[{"domain":"demo","name":"hello-go","app_type":"external-api"}]`,
		"--version-selections", `not-json`,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "--version-selections is not valid JSON") {
		t.Errorf("want '--version-selections is not valid JSON' in stderr, got: %q", stderr)
	}
}

// TestPlanCmd_VersionSelections_SatisfiesMissingVersionOptionCheck proves a
// --version-selections that covers every requested target satisfies
// workflow_dispatch's "must have some version source" validation on its
// own, with none of --version/--increment-* supplied.
func TestPlanCmd_VersionSelections_SatisfiesMissingVersionOptionCheck(t *testing.T) {
	stdout, stderr, err := runTest([]string{
		"plan", "--event-type", "workflow_dispatch",
		"--apps-metadata", `[{"domain":"demo","name":"hello-go","app_type":"external-api"}]`,
		"--version-selections", `{"demo-hello-go":"v3.4.5"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, `"v3.4.5"`) {
		t.Errorf("want resolved version v3.4.5 in stdout, got: %q", stdout)
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
		fakeBazelCall{argsContain: []string{"rdeps(//...,"}, output: target},
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
		{"v1.2.3", "major", "v2.0.0"},
		{"v0.1.0", "major", "v1.0.0"},
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

func TestAutoIncrementVersionMajor(t *testing.T) {
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: "demo-hello-go.v1.3.0\ndemo-hello-go.v1.2.0"},
	)
	ver, err := autoIncrementVersion("demo", "hello-go", "major", git)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "v2.0.0" {
		t.Errorf("got %q, want %q", ver, "v2.0.0")
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

func TestPlanReleaseWorkflowDispatchIncrementMajor(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: "manmanv2-control-api.v1.2.3"},
	)

	result, err := planRelease(planParams{
		eventType:      "workflow_dispatch",
		requestedApps:  "manmanv2-control-api",
		incrementMajor: true,
		bazel:          bazel,
		git:            git,
		fs:             fs,
		workspaceRoot:  fakeWorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Apps) != 1 || result.Apps[0] != "manmanv2-control-api" {
		t.Fatalf("expected [manmanv2-control-api], got %v", result.Apps)
	}
	if result.Versions["manmanv2-control-api"] != "v2.0.0" {
		t.Errorf("expected version v2.0.0, got %s", result.Versions["manmanv2-control-api"])
	}
}

// TestPlanReleaseWorkflowDispatch_AllocateDomainUsesRegistry is issue #829's
// fix for the real production app call site: with the domain opted into App
// Registry and AllocateVersion succeeding, the allocated version must be
// used verbatim -- ignoring the git-tag fixture entirely (which would have
// produced v2.0.0 via a major bump from manmanv2-control-api.v1.2.3).
func TestPlanReleaseWorkflowDispatch_AllocateDomainUsesRegistry(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: "manmanv2-control-api.v1.2.3"},
	)
	artClient := NewFakeArtifactRegistryClient()
	artClient.AllocateVersionFn = func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
		if in.OwnerFullName != "manmanv2-control-api" || in.Increment != "major" {
			t.Errorf("unexpected AllocateVersionRequest: %+v", in)
		}
		return &pb.AllocateVersionResponse{Version: "v9.0.0"}, nil
	}

	var result *PlanResult
	var err error
	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		result, err = planRelease(planParams{
			eventType:              "workflow_dispatch",
			requestedApps:          "manmanv2-control-api",
			incrementMajor:         true,
			bazel:                  bazel,
			git:                    git,
			fs:                     fs,
			workspaceRoot:          fakeWorkspaceRoot,
			artifactRegistryClient: artClient,
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(artClient.AllocateVersionCalls) != 1 {
		t.Fatalf("expected 1 AllocateVersion call, got %d", len(artClient.AllocateVersionCalls))
	}
	if result.Versions["manmanv2-control-api"] != "v9.0.0" {
		t.Errorf("expected registry-allocated version v9.0.0, got %s", result.Versions["manmanv2-control-api"])
	}
}

// TestPlanReleaseWorkflowDispatch_FailedPreconditionIsFatal proves a
// FailedPrecondition from AllocateVersion is fatal exactly like any other
// registry error, not a signal to silently revert to tags.
func TestPlanReleaseWorkflowDispatch_FailedPreconditionIsFatal(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: "manmanv2-control-api.v1.2.3"},
	)
	artClient := NewFakeArtifactRegistryClient()
	artClient.AllocateVersionFn = func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, `domain "manmanv2" allocation failed`)
	}

	var err error
	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		_, err = planRelease(planParams{
			eventType:              "workflow_dispatch",
			requestedApps:          "manmanv2-control-api",
			incrementMajor:         true,
			bazel:                  bazel,
			git:                    git,
			fs:                     fs,
			workspaceRoot:          fakeWorkspaceRoot,
			artifactRegistryClient: artClient,
		})
	})
	if err == nil {
		t.Fatal("expected a fatal error, got nil")
	}
}

// TestPlanReleaseWorkflowDispatch_RegistryErrorIsFatal proves the safety
// property issue #829 actually asks for: once opted in, any AllocateVersion
// error must fail the release loudly rather than silently reverting to
// tag-scanning -- the exact bug this issue reports.
func TestPlanReleaseWorkflowDispatch_RegistryErrorIsFatal(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: "manmanv2-control-api.v1.2.3"},
	)
	artClient := NewFakeArtifactRegistryClient()
	artClient.AllocateVersionFn = func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
		return nil, status.Error(codes.Unavailable, "registry down")
	}

	var err error
	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		_, err = planRelease(planParams{
			eventType:              "workflow_dispatch",
			requestedApps:          "manmanv2-control-api",
			incrementMajor:         true,
			bazel:                  bazel,
			git:                    git,
			fs:                     fs,
			workspaceRoot:          fakeWorkspaceRoot,
			artifactRegistryClient: artClient,
		})
	})
	if err == nil {
		t.Fatal("expected planRelease to fail when AllocateVersion errors")
	}
}

// TestPlanReleaseWorkflowDispatch_ChartAllocateVersionUsesFullOwnerName is
// the direct regression test for a real prod failure: the App Registry
// domain's own release ("app-registry" domain, chart also named
// "app-registry") failed with `AllocateVersion: rpc error: code =
// InvalidArgument desc = chart "app-registry" not found`. assignChartVersions
// (plan.go) was sending AllocateVersion the bare chart Name
// ("app-registry") instead of the server's actual lookup key, the composed
// "domain-name" full name ("app-registry-app-registry") -- this affected
// every chart on the auto-increment path, not just this one; it only read
// as a domain==name coincidence because that happens to be true for the App
// Registry's own chart. Mirrors
// TestPlanReleaseWorkflowDispatch_AllocateDomainUsesRegistry (the equivalent
// app-side regression test, issue #829) for charts. This variant covers the
// App-Registry-DB-backed --charts-metadata path (HelmChartMetadataFromInputs);
// see TestPlanReleaseWorkflowDispatch_ChartAllocateVersion_BazelDiscoveredName
// below for the bazel-query-discovered --charts name path, which needed a
// second, distinct fix (HelmChartMetadata.FullName, plan_helm.go).
func TestPlanReleaseWorkflowDispatch_ChartAllocateVersionUsesFullOwnerName(t *testing.T) {
	bazel := newFakeBazel()
	git := newFakeGit(
		// Must never be consulted: AllocateVersion succeeds below, so the
		// git-tag fallback (which would silently return the wrong version)
		// must not run at all.
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: "app-registry-app-registry.v1.0.0"},
	)
	artClient := NewFakeArtifactRegistryClient()
	artClient.AllocateVersionFn = func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
		if in.OwnerFullName != "app-registry-app-registry" || in.Kind != pb.ArtifactKind_ARTIFACT_KIND_CHART || in.Increment != "minor" {
			t.Errorf("unexpected AllocateVersionRequest: %+v", in)
		}
		return &pb.AllocateVersionResponse{Version: "v9.0.0"}, nil
	}

	var result *PlanResult
	var err error
	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		result, err = planRelease(planParams{
			eventType: "workflow_dispatch",
			chartsMetadata: []HelmChartMetadataInput{
				{Domain: "app-registry", Name: "app-registry"},
			},
			incrementMinor:         true,
			bazel:                  bazel,
			git:                    git,
			artifactRegistryClient: artClient,
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(artClient.AllocateVersionCalls) != 1 {
		t.Fatalf("expected 1 AllocateVersion call, got %d", len(artClient.AllocateVersionCalls))
	}
	if result.Versions["app-registry-app-registry"] != "v9.0.0" {
		t.Errorf("expected registry-allocated version v9.0.0, got %s", result.Versions["app-registry-app-registry"])
	}
}

// TestPlanReleaseWorkflowDispatch_ChartAllocateVersion_BazelDiscoveredName
// is the direct regression test for a real prod failure hit via v1
// (release.yml, GHA run 32654699873's sibling): AllocateVersion rejected
// "app-registry-helm-app-registry-app-registry" as not found. This is a
// second variant of TestPlanReleaseWorkflowDispatch_ChartAllocateVersionUsesFullOwnerName
// above (which covers the App-Registry-DB-backed --charts-metadata path)
// -- this one goes through the bazel-query-discovered --charts name path
// instead (ListAllHelmCharts), where HelmChartMetadata.Name carries the raw
// Bazel-declared "helm-{domain}-{chart_name}" name (release.bzl's
// release_helm_chart macro), not the bare chart_name -- see
// HelmChartMetadata.FullName's doc comment (plan_helm.go) for why naive
// Domain+"-"+Name concatenation double-counted the domain for exactly this
// discovery path.
func TestPlanReleaseWorkflowDispatch_ChartAllocateVersion_BazelDiscoveredName(t *testing.T) {
	fs, bazel := buildFakeHelmInfra([]fakeHelmChart{
		{pkg: "tools/app_registry", targetName: "app_registry_chart_chart_metadata", name: "helm-app-registry-app-registry", domain: "app-registry", apps: []string{}},
	})
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--sort"}, output: "app-registry-app-registry.v1.0.0"},
	)
	artClient := NewFakeArtifactRegistryClient()
	artClient.AllocateVersionFn = func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
		if in.OwnerFullName != "app-registry-app-registry" || in.Kind != pb.ArtifactKind_ARTIFACT_KIND_CHART || in.Increment != "patch" {
			t.Errorf("unexpected AllocateVersionRequest: %+v", in)
		}
		return &pb.AllocateVersionResponse{Version: "v9.0.0"}, nil
	}

	var result *PlanResult
	var err error
	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		result, err = planRelease(planParams{
			eventType:              "workflow_dispatch",
			requestedCharts:        "app-registry",
			incrementPatch:         true,
			bazel:                  bazel,
			git:                    git,
			fs:                     fs,
			workspaceRoot:          fakeWorkspaceRoot,
			artifactRegistryClient: artClient,
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(artClient.AllocateVersionCalls) != 1 {
		t.Fatalf("expected 1 AllocateVersion call, got %d", len(artClient.AllocateVersionCalls))
	}
	if result.Versions["app-registry-app-registry"] != "v9.0.0" {
		t.Errorf("expected registry-allocated version v9.0.0, got %s", result.Versions["app-registry-app-registry"])
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
		{[]string{"demo-hello-go"}, 1, false},      // full name
		{[]string{"demo"}, 2, false},                // domain sweep
		{[]string{"control-api"}, 0, true},          // bare short name -- no longer matched, must be full "manmanv2-control-api"
		{[]string{"nonexistent"}, 0, true},          // invalid
		{[]string{"demo-hello-go", "manmanv2-control-api"}, 2, false},
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

// TestResolveApps_NameDomainCollision covers the real-world app-registry
// case: domain "app-registry" (server/migrate/worker/ui) collides with app
// name "app-registry" (the tools-domain CLI, full name
// "tools-app-registry"). resolveApps no longer matches bare app names at
// all (see resolveApps' doc comment) precisely so this kind of collision
// can't happen: the CLI app is reachable only via its full name
// "tools-app-registry", and the bare "app-registry" always means the
// domain sweep -- see release-v2.yml's "Package CLI binaries" step, which
// now calls `package-assets tools-app-registry`.
func TestResolveApps_NameDomainCollision(t *testing.T) {
	allApps := []AppMetadata{
		{AppManifest: &appmetapb.AppManifest{Name: "app-registry", Domain: "tools", AppType: "cli"}, BazelTarget: "//tools/app_registry/cli:app-registry_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "api", Domain: "app-registry", AppType: "external-api"}, BazelTarget: "//tools/app_registry/server:api_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "migrate", Domain: "app-registry"}, BazelTarget: "//tools/app_registry/migrate:migrate_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "worker", Domain: "app-registry"}, BazelTarget: "//tools/app_registry/worker:worker_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "ui", Domain: "app-registry"}, BazelTarget: "//tools/app_registry/ui:ui_metadata"},
	}

	// The bare app name is no longer matched -- it resolves as the domain
	// sweep instead, unambiguously.
	got, err := resolveApps([]string{"app-registry"}, allApps)
	if err != nil {
		t.Fatalf("resolveApps([app-registry]): unexpected error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("resolveApps([app-registry]): got %d apps, want 4 (the domain sweep): %v", len(got), got)
	}

	// The CLI app is reachable only through its full name.
	cliApp, err := resolveApps([]string{"tools-app-registry"}, allApps)
	if err != nil || len(cliApp) != 1 || cliApp[0].FullName() != "tools-app-registry" {
		t.Errorf("resolveApps([tools-app-registry]) = %v, %v; want single tools-app-registry", cliApp, err)
	}

	// Requesting both is not a duplicate: the domain sweep and the CLI app
	// are disjoint sets now that the CLI app is only reachable by full name.
	both, err := resolveApps([]string{"app-registry", "tools-app-registry"}, allApps)
	if err != nil {
		t.Fatalf("resolveApps([app-registry, tools-app-registry]): unexpected error: %v", err)
	}
	if len(both) != 5 {
		t.Fatalf("resolveApps([app-registry, tools-app-registry]): got %d apps, want 5: %v", len(both), both)
	}
}

// TestResolveApps_ToolsAndAppRegistryDomainsDoNotCollide is the regression
// test for a real "plan --apps 'tools, app-registry'" run
// (https://github.com/whale-net/everything/actions/runs/32621937589): a
// prior fix made resolveApps prefer an unambiguous app-name match over a
// same-named domain sweep, which made the app-registry *domain*
// (server/migrate/worker/ui) unreachable via a bare "app-registry" and
// caused the "tools" domain sweep (which contains the same-named
// tools-app-registry CLI) plus a bare "app-registry" to collide into a
// false duplicate. Removing bare app-name matching entirely (this change)
// fixes it at the root: "tools" and "app-registry" are two disjoint domain
// sweeps, full stop.
func TestResolveApps_ToolsAndAppRegistryDomainsDoNotCollide(t *testing.T) {
	allApps := []AppMetadata{
		{AppManifest: &appmetapb.AppManifest{Name: "app-registry", Domain: "tools", AppType: "cli"}, BazelTarget: "//tools/app_registry/cli:app-registry_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "release_helper_go", Domain: "tools", AppType: "cli"}, BazelTarget: "//tools/release_helper_go:release_helper_go_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "api", Domain: "app-registry", AppType: "external-api"}, BazelTarget: "//tools/app_registry/server:api_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "migrate", Domain: "app-registry"}, BazelTarget: "//tools/app_registry/migrate:migrate_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "worker", Domain: "app-registry"}, BazelTarget: "//tools/app_registry/worker:worker_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "ui", Domain: "app-registry"}, BazelTarget: "//tools/app_registry/ui:ui_metadata"},
	}

	union, err := resolveApps([]string{"tools", "app-registry"}, allApps)
	if err != nil {
		t.Fatalf("resolveApps([tools, app-registry]): unexpected error: %v", err)
	}
	if len(union) != 6 {
		t.Fatalf("resolveApps([tools, app-registry]): got %d apps, want 6: %v", len(union), union)
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

	_, _, err = runTest([]string{"plan", "--event-type", "workflow_dispatch", "--apps", "all", "--format", "yaml"})
	if err == nil {
		t.Fatal("expected error for format")
	}
	if !errors.As(err, &valErr) || valErr.Field != "format" {
		t.Errorf("expected format validation error, got %v", err)
	}

	_, _, err = runTest([]string{"plan", "--event-type", "workflow_dispatch", "--apps", "all"})
	if err == nil {
		t.Fatal("expected error for missing version option")
	}
	if !errors.As(err, &valErr) || valErr.Field != "version_options" {
		t.Errorf("expected version_options validation error, got %v", err)
	}

	_, _, err = runTest([]string{"plan", "--event-type", "workflow_dispatch", "--apps", "all", "--version", "bad-ver"})
	if err == nil {
		t.Fatal("expected error for invalid version")
	}
	if !errors.As(err, &valErr) || valErr.Field != "version" {
		t.Errorf("expected version validation error, got %v", err)
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
		requestedApps: "demo-hello-py,manmanv2-control-api",
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
		// control-api's own auto-increment (requestedApps includes it
		// alongside the chart) -- no tags found, no-tags default applies.
		fakeGitCall{argsContain: []string{"tag", "--sort", "manmanv2-control-api.v*"}, output: ""},
	)

	result, err := planRelease(planParams{
		eventType:       "workflow_dispatch",
		requestedApps:   "manmanv2-control-api",
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

// TestPlanReleaseWithCharts_VersionsMapCoversComposedMemberApp is issue
// #901's actual multi-target-batch scenario (#878's exact repro shape): a
// single `plan` invocation whose requested apps AND requested charts
// overlap -- here, chart "helm-control" (domain manmanv2) composes app
// "control-api" (domain manmanv2), and both are explicit targets of the
// same batch (requestedApps: "manmanv2-control-api", requestedCharts: "all"). This
// is the exact map (PlanResult.Versions) that flows, unchanged in shape,
// through worker/release/plan.go's ResolvePlan re-keying, github.go's
// appVersionsJSON, release.yml's --app-versions plumbing, and finally
// build_helm.go's resolveChartAppVersions precedence check -- so a bug in
// this map (missing entry, or a chart/app key collision silently
// overwriting the app's entry) would defeat #901's fix even though every
// downstream layer's own unit tests pass in isolation.
func TestPlanReleaseWithCharts_VersionsMapCoversComposedMemberApp(t *testing.T) {
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
		fakeGitCall{argsContain: []string{"tag", "--sort", "manmanv2-control-api.v*"}, output: "manmanv2-control-api.v2.3.0"},
	)

	result, err := planRelease(planParams{
		eventType:       "workflow_dispatch",
		requestedApps:   "manmanv2-control-api",
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

	// The chart's own version entry and the composed member app's own
	// version entry must both be present, correctly resolved, and keyed
	// distinctly (no silent overwrite of one by the other).
	appVersion, ok := result.Versions["manmanv2-control-api"]
	if !ok {
		t.Fatalf("expected result.Versions to include the composed member app's own resolved version (key %q), got: %+v", "manmanv2-control-api", result.Versions)
	}
	if appVersion != "v2.4.0" {
		t.Errorf("expected manmanv2-control-api resolved to v2.4.0 (minor-incremented from v2.3.0), got %q", appVersion)
	}

	chartVersion, ok := result.Versions["manmanv2-helm-control"]
	if !ok {
		t.Fatalf("expected result.Versions to include the chart's own resolved version (key %q), got: %+v", "manmanv2-helm-control", result.Versions)
	}
	if chartVersion != "v1.1.0" {
		t.Errorf("expected manmanv2-helm-control resolved to v1.1.0 (minor-incremented from v1.0.0), got %q", chartVersion)
	}
	if chartVersion == appVersion {
		t.Fatalf("chart's own version entry collided with the composed member app's version entry (both %q) -- resolveChartAppVersions would resolve the wrong value for #878's scenario", chartVersion)
	}

	// This is the exact map worker/release/plan.go's ResolvePlan re-keys
	// into ResolvedPlan.Versions (FullName -> "kind:owner"), and
	// github.go's appVersionsJSON then filters to image-kind entries only
	// -- confirm the app entry alone (not the chart's) is what would reach
	// resolveChartAppVersions as a plan pin for the composed app.
	if len(result.Versions) != 2 {
		t.Fatalf("expected exactly 2 entries in result.Versions (one app, one chart), got %d: %+v", len(result.Versions), result.Versions)
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
		requestedApps:          "manmanv2-control-api",
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

func TestPlanAppRegistryIntegration_IncludesBinaryKindInBatch(t *testing.T) {
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
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "test-sha"},
		fakeGitCall{argsContain: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "main"},
	)

	fakeAppClient := NewFakeAppRegistryClient()
	fakeArtifactClient := NewFakeArtifactRegistryClient()

	_, err := planRelease(planParams{
		eventType:              "workflow_dispatch",
		requestedApps:          "tools-app-registry",
		version:                "v1.0.0",
		gitSHA:                 "test-sha",
		workflowRunID:          "run-100",
		actor:                  "ci-bot",
		bazel:                  bazel,
		git:                    git,
		fs:                     fs,
		workspaceRoot:          fakeWorkspaceRoot,
		appRegistryClient:      fakeAppClient,
		artifactRegistryClient: fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected planRelease error: %v", err)
	}

	if len(fakeArtifactClient.RecordBuildCalls) != 1 {
		t.Fatalf("expected 1 RecordBuild call, got %d", len(fakeArtifactClient.RecordBuildCalls))
	}
	if len(fakeArtifactClient.BeginPublishBatchCalls) != 1 {
		t.Fatalf("expected 1 BeginPublishBatch call for non-image CLI app, got %d", len(fakeArtifactClient.BeginPublishBatchCalls))
	}
	batchReq := fakeArtifactClient.BeginPublishBatchCalls[0]
	if len(batchReq.Targets) != 1 {
		t.Fatalf("expected 1 target in batch, got %d", len(batchReq.Targets))
	}
	if batchReq.Targets[0].Kind != pb.ArtifactKind_ARTIFACT_KIND_BINARY {
		t.Errorf("expected ARTIFACT_KIND_BINARY, got %v", batchReq.Targets[0].Kind)
	}
	if batchReq.Targets[0].OwnerFullName != "tools-app-registry" {
		t.Errorf("expected tools-app-registry, got %s", batchReq.Targets[0].OwnerFullName)
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
		requestedApps:          "manmanv2-control-api",
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
		requestedApps:          "manmanv2-control-api",
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

// ── CLI Matrix Generation Tests ──────────────────────────────────────────────

func TestPlanCmd_MatrixJSONOutput(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	withFS(fs, func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withWorkspace(fakeWorkspaceRoot, func() {
					stdout, stderr, err := runTest([]string{
						"plan",
						"--event-type", "workflow_dispatch",
						"--apps", "manmanv2-control-api",
						"--version", "v1.0.0",
						"--format", "json",
						"--dry-run",
					})
					if err != nil {
						t.Fatalf("unexpected error: %v (stderr: %s)", err, stderr)
					}

					var res PlanResult
					if err := json.Unmarshal([]byte(stdout), &res); err != nil {
						t.Fatalf("failed to unmarshal plan JSON output %q: %v", stdout, err)
					}

					if len(res.Apps) != 1 || res.Apps[0] != "manmanv2-control-api" {
						t.Errorf("expected Apps [manmanv2-control-api], got %v", res.Apps)
					}
					if res.Version == nil || *res.Version != "v1.0.0" {
						t.Errorf("expected Version 'v1.0.0', got %v", res.Version)
					}
					if res.EventType != "workflow_dispatch" {
						t.Errorf("expected EventType 'workflow_dispatch', got %q", res.EventType)
					}
					if res.Matrix == nil {
						t.Fatal("expected matrix in JSON output")
					}
					include, ok := res.Matrix["include"].([]interface{})
					if !ok || len(include) != 1 {
						t.Fatalf("expected 1 item in matrix include, got %+v", res.Matrix["include"])
					}
					item := include[0].(map[string]interface{})
					if item["app"] != "control-api" || item["domain"] != "manmanv2" || item["version"] != "v1.0.0" {
						t.Errorf("unexpected matrix item: %+v", item)
					}
				})
			})
		})
	})
}

func TestPlanCmd_MatrixGitHubOutput(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	withFS(fs, func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withWorkspace(fakeWorkspaceRoot, func() {
					stdout, stderr, err := runTest([]string{
						"plan",
						"--event-type", "workflow_dispatch",
						"--apps", "manmanv2-control-api",
						"--version", "v1.0.0",
						"--format", "github",
						"--dry-run",
					})
					if err != nil {
						t.Fatalf("unexpected error: %v (stderr: %s)", err, stderr)
					}

					lines := strings.Split(strings.TrimSpace(stdout), "\n")
					outputMap := make(map[string]string)
					for _, line := range lines {
						parts := strings.SplitN(line, "=", 2)
						if len(parts) == 2 {
							outputMap[parts[0]] = parts[1]
						}
					}

					if outputMap["apps"] != "manmanv2-control-api" {
						t.Errorf("expected apps=manmanv2-control-api, got %q", outputMap["apps"])
					}
					if outputMap["version"] != "v1.0.0" {
						t.Errorf("expected version=v1.0.0, got %q", outputMap["version"])
					}
					if outputMap["matrix"] == "" {
						t.Errorf("expected non-empty matrix output, got %q", outputMap["matrix"])
					}

					var matrixObj map[string]interface{}
					if err := json.Unmarshal([]byte(outputMap["matrix"]), &matrixObj); err != nil {
						t.Fatalf("failed to parse matrix JSON string: %v", err)
					}

					// issue #901: --format github must also emit a `versions=`
					// line carrying PlanResult.Versions as JSON, so
					// release.yml's plan-release job can forward it (via a new
					// `versions` output) to release-helm-charts' --app-versions
					// flag for the manual/legacy dispatch fallback path.
					if outputMap["versions"] == "" {
						t.Fatalf("expected non-empty versions output, got outputs: %+v", outputMap)
					}
					var versionsObj map[string]string
					if err := json.Unmarshal([]byte(outputMap["versions"]), &versionsObj); err != nil {
						t.Fatalf("failed to parse versions JSON string %q: %v", outputMap["versions"], err)
					}
					if versionsObj["manmanv2-control-api"] != "v1.0.0" {
						t.Errorf("expected versions[manmanv2-control-api]=v1.0.0, got %+v", versionsObj)
					}
				})
			})
		})
	})
}

// ── --from-resolved-plan (issue #929) ───────────────────────────────────────

// TestPlanCmd_FromResolvedPlan_MatchesFreshPlanGithubOutput covers the core
// claim of issue #929's fix: `plan --format github --from-resolved-plan
// <json>` (the CLI-owned parse path release-v2.yml's plan-release job now
// delegates to, replacing a hand-written jq mirror of the same field
// mapping) produces identical --format github output to a fresh
// planRelease() + --format github emission for an equivalent plan. Both
// invocations share the same underlying PlanResult (obtained via a real
// --format json plan call), so this asserts the --from-resolved-plan path
// reaches the exact same emission code as the normal path, not merely a
// similar one.
func TestPlanCmd_FromResolvedPlan_MatchesFreshPlanGithubOutput(t *testing.T) {
	_, fs, bazel := makeTestApps()
	git := newFakeGit()

	var freshJSON, freshGithub string
	withFS(fs, func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withWorkspace(fakeWorkspaceRoot, func() {
					var err error
					freshJSON, _, err = runTest([]string{
						"plan",
						"--event-type", "workflow_dispatch",
						"--apps", "manmanv2-control-api",
						"--version", "v1.0.0",
						"--format", "json",
						"--dry-run",
					})
					if err != nil {
						t.Fatalf("unexpected error planning (json): %v", err)
					}

					freshGithub, _, err = runTest([]string{
						"plan",
						"--event-type", "workflow_dispatch",
						"--apps", "manmanv2-control-api",
						"--version", "v1.0.0",
						"--format", "github",
						"--dry-run",
					})
					if err != nil {
						t.Fatalf("unexpected error planning (github): %v", err)
					}
				})
			})
		})
	})

	// --from-resolved-plan must not touch fs/bazel/git/workspace at all --
	// deliberately invoked outside every with* fake wrapper above, so a
	// stray dependency on any of them would panic (nil FileSystem/
	// BazelRunner/GitRunner) or fail workspace-root discovery rather than
	// silently pass.
	resolvedGithub, stderr, err := runTest([]string{
		"plan",
		"--format", "github",
		"--from-resolved-plan", freshJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, stderr)
	}

	if resolvedGithub != freshGithub {
		t.Errorf("--from-resolved-plan output diverged from fresh plan's --format github output:\n--from-resolved-plan:\n%s\nfresh:\n%s", resolvedGithub, freshGithub)
	}
}

// TestPlanCmd_FromResolvedPlanMalformedJSON covers issue #929's other half:
// malformed --from-resolved-plan input must fail clearly and typed, not
// panic or silently emit an empty/garbage plan.
func TestPlanCmd_FromResolvedPlanMalformedJSON(t *testing.T) {
	_, stderr, err := runTest([]string{
		"plan",
		"--format", "github",
		"--from-resolved-plan", "{not valid json",
	})
	if err == nil {
		t.Fatal("expected error for malformed --from-resolved-plan JSON")
	}
	var valErr *PlanValidationError
	if !errors.As(err, &valErr) || valErr.Field != "from-resolved-plan" {
		t.Errorf("expected from-resolved-plan validation error, got %v", err)
	}
	if !strings.Contains(stderr, "not valid JSON") {
		t.Errorf("want 'not valid JSON' in stderr, got: %q", stderr)
	}
}
