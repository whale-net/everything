package cmd

import (
	"strings"
	"testing"
)

func TestReleaseNotesInvalidFormat(t *testing.T) {
	_, stderr, err := runTest([]string{"release-notes", "some-app", "--format", "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !strings.Contains(stderr, "format must be one of: markdown, plain, json") {
		t.Errorf("expected format error in stderr, got: %q", stderr)
	}
}

func TestReleaseNotesAllInvalidFormat(t *testing.T) {
	_, stderr, err := runTest([]string{"release-notes-all", "--format", "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !strings.Contains(stderr, "format must be one of: markdown, plain, json") {
		t.Errorf("expected format error in stderr, got: %q", stderr)
	}
}

func TestPlanHelmReleaseInvalidFormat(t *testing.T) {
	_, stderr, err := runTest([]string{"plan-helm-release", "--format", "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !strings.Contains(stderr, "format must be one of: json, github") {
		t.Errorf("expected format error in stderr, got: %q", stderr)
	}
}

func TestBuildHelmChartInvalidBump(t *testing.T) {
	_, stderr, err := runTest([]string{"build-helm-chart", "mychart", "--bump", "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid bump type")
	}
	if !strings.Contains(stderr, "--bump must be one of: major, minor, patch") {
		t.Errorf("expected bump error in stderr, got: %q", stderr)
	}
}

func TestBuildHelmChartValidBumps(t *testing.T) {
	// Valid bumps pass validation but fail later (chart not found). Confirm no "invalid bump" error.
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"kind(helm_chart_metadata"}, output: ""})
	withBazel(bazel, func() {
		withWorkspace(fakeWorkspaceRoot, func() {
			withFS(newFakeFS(), func() {
				for _, bump := range []string{"major", "minor", "patch"} {
					_, _, err := runTest([]string{"build-helm-chart", "mychart", "--bump", bump})
					if err == nil {
						t.Fatalf("expected error (chart not found) for bump=%q", bump)
					}
					if strings.Contains(err.Error(), "invalid bump") {
						t.Errorf("valid bump %q should not trigger validation error", bump)
					}
				}
			})
		})
	})
}

func TestIsValidNotesFormat(t *testing.T) {
	for _, f := range []string{"markdown", "plain", "json"} {
		if !isValidNotesFormat(f) {
			t.Errorf("expected %q to be valid", f)
		}
	}
	if isValidNotesFormat("xml") {
		t.Error("expected 'xml' to be invalid")
	}
}
