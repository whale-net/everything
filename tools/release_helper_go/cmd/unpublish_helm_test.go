package cmd

import (
	"fmt"
	"strings"
	"testing"
)

func sampleIndexYAML(chartName string, versions []string) []byte {
	versionEntries := ""
	for _, v := range versions {
		versionEntries += fmt.Sprintf("  - version: %q\n    name: %q\n", v, chartName)
	}
	return []byte(fmt.Sprintf("apiVersion: v1\nentries:\n  %s:\n%sgenerated: \"2024-01-01T00:00:00Z\"\n", chartName, versionEntries))
}

func TestUnpublishHelmMissingIndexFile(t *testing.T) {
	withFS(newFakeFS(), func() {
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

func TestRemoveHelmChartVersions(t *testing.T) {
	indexData := sampleIndexYAML("test-chart", []string{"v1.0.0", "v1.1.0", "v1.2.0"})

	removed, updated, err := removeHelmChartVersions(indexData, "test-chart", []string{"v1.0.0", "v1.1.0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}
	if updated == nil {
		t.Fatal("expected updated content")
	}
	if strings.Contains(string(updated), "v1.0.0") {
		t.Error("v1.0.0 should be removed from output")
	}
	if !strings.Contains(string(updated), "v1.2.0") {
		t.Error("v1.2.0 should remain in output")
	}
}

func TestRemoveHelmChartVersionsNotFound(t *testing.T) {
	indexData := sampleIndexYAML("test-chart", []string{"v1.0.0"})
	_, _, err := removeHelmChartVersions(indexData, "other-chart", []string{"v1.0.0"})
	if err == nil {
		t.Fatal("expected error for chart not found in index")
	}
}

func TestRemoveHelmChartVersionsNoMatch(t *testing.T) {
	indexData := sampleIndexYAML("test-chart", []string{"v1.0.0"})
	removed, _, err := removeHelmChartVersions(indexData, "test-chart", []string{"v9.9.9"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}
}

func TestRemoveHelmChartVersionsAllRemoved(t *testing.T) {
	indexData := sampleIndexYAML("test-chart", []string{"v1.0.0"})
	removed, updated, err := removeHelmChartVersions(indexData, "test-chart", []string{"v1.0.0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}
	// Chart entry should be deleted entirely
	if strings.Contains(string(updated), "test-chart") {
		t.Error("chart entry should be removed entirely when all versions deleted")
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV("v1.0.0, v1.1.0,v1.2.0")
	if len(got) != 3 {
		t.Errorf("expected 3 parts, got %d: %v", len(got), got)
	}
}
