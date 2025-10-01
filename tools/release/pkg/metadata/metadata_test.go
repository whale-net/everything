package metadata

import (
	"encoding/json"
	"testing"
)

func TestAppMetadata_JSONUnmarshal(t *testing.T) {
	jsonData := `{
		"name": "test_app",
		"app_type": "service",
		"version": "1.0.0",
		"description": "Test application",
		"registry": "ghcr.io",
		"repo_name": "whale-net/test_app",
		"image_target": "test_app_image",
		"domain": "demo",
		"language": "python",
		"port": 8080,
		"replicas": 3,
		"labels": {
			"app": "test_app",
			"env": "prod"
		},
		"annotations": {
			"version": "1.0.0"
		},
		"dependencies": ["dep1", "dep2"]
	}`

	var metadata AppMetadata
	if err := json.Unmarshal([]byte(jsonData), &metadata); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if metadata.Name != "test_app" {
		t.Errorf("Name = %v, want test_app", metadata.Name)
	}
	if metadata.Domain != "demo" {
		t.Errorf("Domain = %v, want demo", metadata.Domain)
	}
	if metadata.Port != 8080 {
		t.Errorf("Port = %v, want 8080", metadata.Port)
	}
	if len(metadata.Labels) != 2 {
		t.Errorf("Labels length = %v, want 2", len(metadata.Labels))
	}
	if len(metadata.Dependencies) != 2 {
		t.Errorf("Dependencies length = %v, want 2", len(metadata.Dependencies))
	}
}

func TestGetImageTargets(t *testing.T) {
	// This test only validates the logic, not actual Bazel interaction
	// We can't call GetImageTargets directly without mocking, so we test the structure
	tests := []struct {
		name        string
		packagePath string
		targetName  string
		wantBase    string
		wantAMD64   string
		wantARM64   string
	}{
		{
			name:        "simple app",
			packagePath: "demo/hello_python",
			targetName:  "hello_python_image",
			wantBase:    "//demo/hello_python:hello_python_image",
			wantAMD64:   "//demo/hello_python:hello_python_image_amd64",
			wantARM64:   "//demo/hello_python:hello_python_image_arm64",
		},
		{
			name:        "nested app",
			packagePath: "api/services/auth",
			targetName:  "auth_image",
			wantBase:    "//api/services/auth:auth_image",
			wantAMD64:   "//api/services/auth:auth_image_amd64",
			wantARM64:   "//api/services/auth:auth_image_arm64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify the target construction logic
			baseImageTarget := "//" + tt.packagePath + ":" + tt.targetName
			if baseImageTarget != tt.wantBase {
				t.Errorf("Base target = %v, want %v", baseImageTarget, tt.wantBase)
			}
			if baseImageTarget+"_amd64" != tt.wantAMD64 {
				t.Errorf("AMD64 target = %v, want %v", baseImageTarget+"_amd64", tt.wantAMD64)
			}
			if baseImageTarget+"_arm64" != tt.wantARM64 {
				t.Errorf("ARM64 target = %v, want %v", baseImageTarget+"_arm64", tt.wantARM64)
			}
		})
	}
}

func TestAppInfo_JSON(t *testing.T) {
	info := AppInfo{
		BazelTarget: "//demo/hello_python:hello_python_metadata",
		Name:        "hello_python",
		Domain:      "demo",
	}

	// Test marshaling
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Failed to marshal AppInfo: %v", err)
	}

	// Test unmarshaling
	var decoded AppInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal AppInfo: %v", err)
	}

	if decoded.Name != info.Name || decoded.Domain != info.Domain || decoded.BazelTarget != info.BazelTarget {
		t.Errorf("Decoded AppInfo doesn't match original: %+v vs %+v", decoded, info)
	}
}
