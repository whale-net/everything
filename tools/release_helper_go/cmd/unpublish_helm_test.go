package cmd

import (
	"strings"
	"testing"
)

func TestUnpublishHelmMissingIndexFile(t *testing.T) {
	withFS(&fakeFS{existing: map[string]bool{}}, func() {
		_, stderr, err := runTest([]string{
			"unpublish-helm-chart", "/nonexistent/index.yaml",
			"--chart", "test-chart",
			"--versions", "v1.0.0",
		})
		if err == nil {
			t.Fatal("expected error for missing index file")
		}
		if !strings.Contains(stderr, "Index file not found") {
			t.Errorf("expected 'Index file not found' in stderr, got: %q", stderr)
		}
	})
}

func TestUnpublishHelmExistingIndexFile(t *testing.T) {
	withFS(&fakeFS{existing: map[string]bool{"/existing/index.yaml": true}}, func() {
		_, _, err := runTest([]string{
			"unpublish-helm-chart", "/existing/index.yaml",
			"--chart", "test-chart",
			"--versions", "v1.0.0",
		})
		// Should fail because not fully implemented, but NOT because of missing file.
		if err == nil {
			t.Fatal("expected error (not yet implemented)")
		}
	})
}
