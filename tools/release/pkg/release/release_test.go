package release

import (
	"encoding/json"
	"testing"
)

func TestReleaseApp_JSON(t *testing.T) {
	app := ReleaseApp{
		Name:        "hello_python",
		BazelTarget: "//demo/hello_python:hello_python_metadata",
		Version:     "v1.0.0",
	}

	data, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("Failed to marshal ReleaseApp: %v", err)
	}

	var decoded ReleaseApp
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal ReleaseApp: %v", err)
	}

	if decoded.Name != app.Name || decoded.Version != app.Version {
		t.Errorf("Decoded ReleaseApp doesn't match: %+v vs %+v", decoded, app)
	}
}

func TestReleasePlan_JSON(t *testing.T) {
	plan := &ReleasePlan{
		Matrix: ReleaseMatrix{
			Include: []ReleaseApp{
				{
					Name:        "hello_python",
					BazelTarget: "//demo/hello_python:hello_python_metadata",
					Version:     "v1.0.0",
				},
				{
					Name:        "hello_go",
					BazelTarget: "//demo/hello_go:hello_go_metadata",
					Version:     "v1.0.0",
				},
			},
		},
		Apps:    []string{"hello_python", "hello_go"},
		Version: "v1.0.0",
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Failed to marshal ReleasePlan: %v", err)
	}

	var decoded ReleasePlan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal ReleasePlan: %v", err)
	}

	if len(decoded.Matrix.Include) != 2 {
		t.Errorf("Expected 2 apps in matrix, got %d", len(decoded.Matrix.Include))
	}

	if len(decoded.Apps) != 2 {
		t.Errorf("Expected 2 app names, got %d", len(decoded.Apps))
	}
}

func TestFormatReleasePlan_JSON(t *testing.T) {
	plan := &ReleasePlan{
		Matrix: ReleaseMatrix{
			Include: []ReleaseApp{
				{
					Name:        "test_app",
					BazelTarget: "//test:app",
					Version:     "v1.0.0",
				},
			},
		},
		Apps:    []string{"test_app"},
		Version: "v1.0.0",
	}

	output, err := FormatReleasePlan(plan, "json")
	if err != nil {
		t.Fatalf("FormatReleasePlan() error = %v", err)
	}

	// Verify it's valid JSON
	var decoded ReleasePlan
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Errorf("Output is not valid JSON: %v", err)
	}
}

func TestFormatReleasePlan_GitHub(t *testing.T) {
	plan := &ReleasePlan{
		Matrix: ReleaseMatrix{
			Include: []ReleaseApp{
				{
					Name:        "test_app",
					BazelTarget: "//test:app",
					Version:     "v1.0.0",
				},
			},
		},
		Apps:    []string{"test_app"},
		Version: "v1.0.0",
	}

	output, err := FormatReleasePlan(plan, "github")
	if err != nil {
		t.Fatalf("FormatReleasePlan() error = %v", err)
	}

	// Verify it starts with "matrix="
	if len(output) < 7 || output[:7] != "matrix=" {
		t.Errorf("GitHub format should start with 'matrix=', got: %s", output[:min(20, len(output))])
	}
}

func TestFormatReleasePlan_InvalidFormat(t *testing.T) {
	plan := &ReleasePlan{}

	_, err := FormatReleasePlan(plan, "invalid")
	if err == nil {
		t.Error("Expected error for invalid format, got nil")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
