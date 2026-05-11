package cmd

import (
	"strings"
	"testing"
)

func TestCleanupRequiresGithubToken(t *testing.T) {
	withEnv(map[string]string{}, func() {
		_, stderr, err := runTest([]string{"cleanup-releases", "--dry-run"})
		if err == nil {
			t.Fatal("expected error when GITHUB_TOKEN is missing")
		}
		if !strings.Contains(stderr, "GITHUB_TOKEN environment variable not set") {
			t.Errorf("expected GITHUB_TOKEN error in stderr, got: %q", stderr)
		}
	})
}

func TestCleanupWithToken(t *testing.T) {
	withEnv(map[string]string{"GITHUB_TOKEN": "fake-token"}, func() {
		_, _, err := runTest([]string{"cleanup-releases", "--dry-run"})
		// Should fail because not fully implemented, but NOT because of missing token.
		if err == nil {
			t.Fatal("expected error (not yet implemented)")
		}
	})
}
