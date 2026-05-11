package cmd

import (
	"testing"
)

func TestFilterBuildFiles(t *testing.T) {
	tests := []struct {
		input []string
		want  []string
	}{
		{
			input: []string{".github/workflows/ci.yml", "docs/RELEASE.md", "manmanv2/api/main.go"},
			want:  []string{"manmanv2/api/main.go"},
		},
		{
			input: []string{"libs/go/rmq/consumer.go", "README.md"},
			want:  []string{"libs/go/rmq/consumer.go"},
		},
		{
			input: []string{".github/actions/setup/action.yml"},
			want:  nil,
		},
		{
			input: []string{"BUILD.bazel", "manmanv2/api/BUILD.bazel"},
			want:  []string{"BUILD.bazel", "manmanv2/api/BUILD.bazel"},
		},
	}
	for _, tt := range tests {
		got := filterBuildFiles(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("filterBuildFiles(%v) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("filterBuildFiles(%v)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestFilesToBazelLabels(t *testing.T) {
	files := []string{
		"manmanv2/api/main.go",
		"libs/go/rmq/consumer.go",
		"demo/hello_go/BUILD.bazel",
		"script.bzl", // should be skipped
	}
	labels, pkgs := filesToBazelLabels(files)

	if len(labels) != 2 {
		t.Errorf("expected 2 labels, got %d: %v", len(labels), labels)
	}
	if len(pkgs) != 1 {
		t.Errorf("expected 1 package, got %d: %v", len(pkgs), pkgs)
	}
	if _, ok := pkgs["//demo/hello_go"]; !ok {
		t.Errorf("expected //demo/hello_go in packages, got %v", pkgs)
	}
}

func TestDetectChangedAppsNoBaseCommit(t *testing.T) {
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
		{pkg: "demo/hello_python", targetSuffix: "hello-python_metadata", name: "hello-python", domain: "demo"},
	}
	fs, bazel := buildFakeInfra(apps)
	git := newFakeGit()

	result, err := DetectChangedApps("", bazel, git, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No base commit → all apps
	if len(result) != 2 {
		t.Errorf("expected 2 apps (all), got %d", len(result))
	}
}

func TestDetectChangedAppsNoChangedFiles(t *testing.T) {
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
	}
	fs, bazel := buildFakeInfra(apps)
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"diff", "--name-only"}, output: ""},
	)

	result, err := DetectChangedApps("abc123", bazel, git, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 apps, got %d", len(result))
	}
}

func TestDetectChangedAppsOnlyNonBuildFiles(t *testing.T) {
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
	}
	fs, bazel := buildFakeInfra(apps)
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"diff", "--name-only"}, output: "docs/RELEASE.md\n.github/workflows/ci.yml"},
	)

	result, err := DetectChangedApps("abc123", bazel, git, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 apps when only non-build files changed, got %d", len(result))
	}
}

func TestDetectChangedAppsWithAffectedApp(t *testing.T) {
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
		{pkg: "libs/go/rmq", targetSuffix: "rmq_metadata", name: "rmq", domain: "libs"},
	}
	fs, baseBazel := buildFakeInfra(apps)

	helloGoTarget := "//demo/hello_go:hello-go_metadata"
	rmqTarget := "//libs/go/rmq:rmq_metadata"

	// Add rdeps and meta-rdeps responses
	additionalCalls := []fakeBazelCall{
		// validate labels — must not match rdeps calls
		{
			argsContain:    []string{"//libs/go/rmq:consumer.go"},
			argsNotContain: []string{"rdeps"},
			output:         "//libs/go/rmq:consumer.go",
		},
		// rdeps(//..., ...) — broad reverse-dep check
		{
			argsContain: []string{"rdeps(//...,"},
			output:      helloGoTarget + "\n" + rmqTarget,
		},
		// rdeps(metaTargets, ...) — narrow to affected metadata targets
		// meta targets appear anywhere in the expression, not at the start of "rdeps("
		{
			argsContain:    []string{"rdeps(", helloGoTarget},
			argsNotContain: []string{"rdeps(//...,"},
			output:         helloGoTarget,
		},
	}
	allCalls := append(baseBazel.calls, additionalCalls...)
	bazel := newFakeBazel(allCalls...)

	git := newFakeGit(
		fakeGitCall{argsContain: []string{"diff", "--name-only"}, output: "libs/go/rmq/consumer.go"},
	)

	result, err := DetectChangedApps("abc123", bazel, git, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0].Name != "hello-go" {
		t.Errorf("expected hello-go to be affected, got %v", result)
	}
}

func TestGetPreviousTag(t *testing.T) {
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"describe"}, output: "demo-hello-go.v1.2.3"},
	)
	tag, err := getPreviousTag(git)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "demo-hello-go.v1.2.3" {
		t.Errorf("got %q, want %q", tag, "demo-hello-go.v1.2.3")
	}
}

func TestGetPreviousTagError(t *testing.T) {
	git := newFakeGit() // no match
	_, err := getPreviousTag(git)
	if err == nil {
		t.Fatal("expected error")
	}
}
