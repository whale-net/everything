package cmd

import (
	"strings"
	"testing"
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
		{Name: "hello-go", Domain: "demo", BazelTarget: "//demo/hello_go:hello-go_metadata"},
		{Name: "hello-python", Domain: "demo", BazelTarget: "//demo/hello_python:hello-python_metadata"},
		{Name: "control-api", Domain: "manmanv2", BazelTarget: "//manmanv2/api:control-api_metadata"},
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
