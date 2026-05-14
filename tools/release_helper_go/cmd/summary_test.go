package cmd

import (
	"strings"
	"testing"
)

func TestSummaryInvalidEventType(t *testing.T) {
	_, stderr, err := runTest([]string{"summary", "--matrix", "{}", "--version", "v1.0.0", "--event-type", "invalid-event"})
	if err == nil {
		t.Fatal("expected error for invalid event-type")
	}
	if !strings.Contains(stderr, "event-type must be one of: workflow_dispatch, tag_push") {
		t.Errorf("expected error message in stderr, got: %q", stderr)
	}
}

func TestSummaryEmptyMatrixExitsZero(t *testing.T) {
	stdout, _, err := runTest([]string{"summary", "--matrix", "{}", "--version", "v1.0.0", "--event-type", "workflow_dispatch"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(stdout, "No apps detected for release") {
		t.Errorf("expected 'No apps detected' in stdout, got: %q", stdout)
	}
}

func TestSummaryEmptyMatrixTagPush(t *testing.T) {
	_, _, err := runTest([]string{"summary", "--matrix", "{}", "--version", "v1.0.0", "--event-type", "tag_push"})
	if err != nil {
		t.Fatalf("expected no error for tag_push, got: %v", err)
	}
}

func TestSummaryNonEmptyMatrixDryRun(t *testing.T) {
	matrix := `{"include":[{"app":"hello_python","version":"v1.0.0"}]}`
	stdout, _, err := runTest([]string{
		"summary",
		"--matrix", matrix,
		"--version", "v1.0.0",
		"--event-type", "workflow_dispatch",
		"--dry-run",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(stdout, "Release completed") {
		t.Errorf("expected 'Release completed' in stdout, got: %q", stdout)
	}
	if !strings.Contains(stdout, "hello_python") {
		t.Errorf("expected app name in stdout, got: %q", stdout)
	}
	if !strings.Contains(stdout, "Dry run mode - no images were published") {
		t.Errorf("expected dry-run message in stdout, got: %q", stdout)
	}
}

func TestSummaryRepositoryOwner(t *testing.T) {
	matrix := `{"include":[{"app":"hello_python","domain":"demo","version":"v1.0.0"}]}`
	stdout, _, err := runTest([]string{
		"summary",
		"--matrix", matrix,
		"--version", "v1.0.0",
		"--event-type", "workflow_dispatch",
		"--repository-owner", "whale-net",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(stdout, "whale-net") {
		t.Errorf("expected repository owner in stdout, got: %q", stdout)
	}
	if !strings.Contains(stdout, "demo-hello_python") {
		t.Errorf("expected domain-app image name in stdout, got: %q", stdout)
	}
}

func TestGenerateSummaryEmptyMatrix(t *testing.T) {
	out := generateSummary("{}", "v1.0.0", "workflow_dispatch", false, "")
	if !strings.Contains(out, "No apps detected for release") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestGenerateSummaryDryRun(t *testing.T) {
	matrix := `{"include":[{"app":"myapp","domain":"demo","version":"v2.0.0"}]}`
	out := generateSummary(matrix, "v2.0.0", "workflow_dispatch", true, "myorg")
	if !strings.Contains(out, "Release completed") {
		t.Errorf("expected 'Release completed': %q", out)
	}
	if !strings.Contains(out, "Dry run mode - no images were published") {
		t.Errorf("expected dry-run message: %q", out)
	}
}

func TestGenerateSummaryTagPush(t *testing.T) {
	matrix := `{"include":[{"app":"myapp","domain":"demo","version":"v2.0.0"}]}`
	out := generateSummary(matrix, "v2.0.0", "tag_push", false, "myorg")
	if !strings.Contains(out, "Git tag push") {
		t.Errorf("expected 'Git tag push': %q", out)
	}
	if !strings.Contains(out, "ghcr.io/myorg/demo-myapp:v2.0.0") {
		t.Errorf("expected image reference: %q", out)
	}
}
