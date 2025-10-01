package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindWorkspaceRoot_WithBuildWorkspaceDirectory(t *testing.T) {
	// Save and restore original env
	originalEnv := os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BUILD_WORKSPACE_DIRECTORY", originalEnv)
		} else {
			os.Unsetenv("BUILD_WORKSPACE_DIRECTORY")
		}
	}()

	testPath := "/test/workspace"
	os.Setenv("BUILD_WORKSPACE_DIRECTORY", testPath)

	root, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("FindWorkspaceRoot() error = %v", err)
	}
	if root != testPath {
		t.Errorf("FindWorkspaceRoot() = %v, want %v", root, testPath)
	}
}

func TestFindWorkspaceRoot_WithModuleBazel(t *testing.T) {
	// Clear env
	originalEnv := os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	os.Unsetenv("BUILD_WORKSPACE_DIRECTORY")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BUILD_WORKSPACE_DIRECTORY", originalEnv)
		}
	}()

	// Since we're in the actual workspace, this should find MODULE.bazel
	root, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("FindWorkspaceRoot() error = %v", err)
	}

	// Verify it found a valid workspace root
	if root == "" {
		t.Error("FindWorkspaceRoot() returned empty string")
	}

	// Check that MODULE.bazel exists at the root
	modulePath := filepath.Join(root, "MODULE.bazel")
	if _, err := os.Stat(modulePath); err != nil {
		t.Errorf("MODULE.bazel not found at %s", modulePath)
	}
}

func TestBazelResult_Lines(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   []string
	}{
		{
			name:   "empty output",
			stdout: "",
			want:   []string{},
		},
		{
			name:   "single line",
			stdout: "line1",
			want:   []string{"line1"},
		},
		{
			name:   "multiple lines",
			stdout: "line1\nline2\nline3",
			want:   []string{"line1", "line2", "line3"},
		},
		{
			name:   "lines with trailing newline",
			stdout: "line1\nline2\n",
			want:   []string{"line1", "line2"},
		},
		{
			name:   "lines with empty lines",
			stdout: "line1\n\nline2\n\n",
			want:   []string{"line1", "line2"},
		},
		{
			name:   "lines with whitespace",
			stdout: "  line1  \n  line2  \n",
			want:   []string{"line1", "line2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &BazelResult{
				Stdout: tt.stdout,
			}
			got := r.Lines()
			if len(got) != len(tt.want) {
				t.Errorf("Lines() returned %d lines, want %d", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Lines()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
