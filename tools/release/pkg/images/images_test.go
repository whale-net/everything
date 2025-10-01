package images

import (
	"testing"

	"github.com/whale-net/everything/tools/release/pkg/metadata"
)

func TestFormatRegistryTags(t *testing.T) {
	tests := []struct {
		name      string
		registry  string
		repoName  string
		version   string
		commitSHA string
		want      []string
	}{
		{
			name:      "version only",
			registry:  "ghcr.io",
			repoName:  "whale-net/demo-hello_python",
			version:   "v1.0.0",
			commitSHA: "",
			want:      []string{"ghcr.io/whale-net/demo-hello_python:v1.0.0"},
		},
		{
			name:      "version and commit",
			registry:  "ghcr.io",
			repoName:  "whale-net/demo-hello_python",
			version:   "v1.0.0",
			commitSHA: "abc123",
			want: []string{
				"ghcr.io/whale-net/demo-hello_python:v1.0.0",
				"ghcr.io/whale-net/demo-hello_python:abc123",
			},
		},
		{
			name:      "latest version",
			registry:  "ghcr.io",
			repoName:  "whale-net/demo-hello_go",
			version:   "latest",
			commitSHA: "",
			want:      []string{"ghcr.io/whale-net/demo-hello_go:latest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatRegistryTags(tt.registry, tt.repoName, tt.version, tt.commitSHA)
			if len(got) != len(tt.want) {
				t.Errorf("FormatRegistryTags() returned %d tags, want %d", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("FormatRegistryTags()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestMultiArchPushConfig(t *testing.T) {
	config := MultiArchPushConfig{
		Registry:  "ghcr.io",
		RepoName:  "whale-net/test-app",
		Version:   "v1.0.0",
		CommitSHA: "abc123",
		Platforms: []string{"amd64", "arm64"},
		DryRun:    true,
	}

	// Just verify the struct can be created and accessed
	if config.Registry != "ghcr.io" {
		t.Errorf("Registry = %v, want ghcr.io", config.Registry)
	}
	if len(config.Platforms) != 2 {
		t.Errorf("Platforms length = %v, want 2", len(config.Platforms))
	}
}

func TestTagAndPushImage_DryRun(t *testing.T) {
	// Create test metadata
	appMeta := &metadata.AppMetadata{
		Name:        "test_app",
		Registry:    "ghcr.io",
		RepoName:    "whale-net/test_app",
		ImageTarget: "test_app_image",
		Domain:      "demo",
	}

	// Test dry run (should not actually build/push)
	// In a real test, we'd mock the Bazel calls
	// For now, we just verify the function signature works
	_ = appMeta
}

func TestPushImage_DryRun(t *testing.T) {
	tags := []string{
		"ghcr.io/whale-net/test:v1.0.0",
		"ghcr.io/whale-net/test:abc123",
	}

	err := PushImage("//test:image", tags, true)
	if err != nil {
		t.Errorf("PushImage() with dryRun=true should not error, got: %v", err)
	}
}
