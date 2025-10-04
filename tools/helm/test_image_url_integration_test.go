package helm

import (
"os"
"path/filepath"
"strings"
"testing"
)

// TestGeneratedValuesImageURL verifies that generated values.yaml contains correct image URLs with organization
func TestGeneratedValuesImageURL(t *testing.T) {
// Create a temporary directory for test output
tmpDir, err := os.MkdirTemp("", "helm-integration-test")
if err != nil {
t.Fatalf("Failed to create temp dir: %v", err)
}
defer os.RemoveAll(tmpDir)

// Use the sample metadata files
metadataDir := "testdata/sample_metadata"
metadataFiles := []string{
filepath.Join(metadataDir, "status_api.json"),
filepath.Join(metadataDir, "experience_api.json"),
}

// Create composer and load metadata
config := ChartConfig{
ChartName:   "test-chart",
Version:     "1.0.0",
Environment: "dev",
Namespace:   "test",
OutputDir:   tmpDir,
}
composer := NewComposer(config, "templates")

err = composer.LoadMetadata(metadataFiles)
if err != nil {
t.Fatalf("LoadMetadata failed: %v", err)
}

// Generate the chart
err = composer.GenerateChart()
if err != nil {
t.Fatalf("GenerateChart failed: %v", err)
}

// Read the generated values.yaml
valuesPath := filepath.Join(tmpDir, "test-chart", "values.yaml")
valuesContent, err := os.ReadFile(valuesPath)
if err != nil {
t.Fatalf("Failed to read values.yaml: %v", err)
}

valuesStr := string(valuesContent)

// Verify that image URLs include the organization
expectedImages := []string{
"image: ghcr.io/whale-net/demo-status-api",
"image: ghcr.io/whale-net/demo-experience-api",
}

for _, expected := range expectedImages {
if !strings.Contains(valuesStr, expected) {
t.Errorf("Expected image URL '%s' not found in values.yaml", expected)
t.Logf("values.yaml content:\n%s", valuesStr)
}
}

// Verify that the old incorrect format is NOT present
incorrectImages := []string{
"image: ghcr.io/demo-status-api",
"image: ghcr.io/demo-experience-api",
}

for _, incorrect := range incorrectImages {
if strings.Contains(valuesStr, incorrect) {
t.Errorf("Incorrect image URL '%s' found in values.yaml (should include whale-net)", incorrect)
t.Logf("values.yaml content:\n%s", valuesStr)
}
}
}
