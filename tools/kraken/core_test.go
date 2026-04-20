package kraken

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindWorkspaceRootWithBuildWorkspaceDirectory(t *testing.T) {
	testPath := "/workspace/build/dir"
	os.Setenv("BUILD_WORKSPACE_DIRECTORY", testPath)
	defer os.Unsetenv("BUILD_WORKSPACE_DIRECTORY")

	result, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != testPath {
		t.Errorf("expected %s, got %s", testPath, result)
	}
}

func TestFindWorkspaceRootWithWorkspaceFile(t *testing.T) {
	os.Unsetenv("BUILD_WORKSPACE_DIRECTORY")

	// Create a temp directory with a WORKSPACE file
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(tmpDir, "WORKSPACE"), []byte(""), 0644)

	// Change to subdir
	origDir, _ := os.Getwd()
	os.Chdir(subDir)
	defer os.Chdir(origDir)

	result, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != tmpDir {
		t.Errorf("expected %s, got %s", tmpDir, result)
	}
}

func TestFindWorkspaceRootWithModuleBazelFile(t *testing.T) {
	os.Unsetenv("BUILD_WORKSPACE_DIRECTORY")

	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(tmpDir, "MODULE.bazel"), []byte(""), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(subDir)
	defer os.Chdir(origDir)

	result, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != tmpDir {
		t.Errorf("expected %s, got %s", tmpDir, result)
	}
}

func TestFindWorkspaceRootNoMarkersFound(t *testing.T) {
	os.Unsetenv("BUILD_WORKSPACE_DIRECTORY")

	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	result, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fall back to current directory
	if result != tmpDir {
		t.Errorf("expected %s, got %s", tmpDir, result)
	}
}

func TestFindWorkspaceRootCurrentDirectoryHasMarker(t *testing.T) {
	os.Unsetenv("BUILD_WORKSPACE_DIRECTORY")

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "MODULE.bazel"), []byte(""), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	result, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != tmpDir {
		t.Errorf("expected %s, got %s", tmpDir, result)
	}
}

func TestFindWorkspaceRootDeepNestedStructure(t *testing.T) {
	os.Unsetenv("BUILD_WORKSPACE_DIRECTORY")

	tmpDir := t.TempDir()
	deepDir := filepath.Join(tmpDir, "a", "b", "c", "d")
	os.MkdirAll(deepDir, 0755)
	os.WriteFile(filepath.Join(tmpDir, "WORKSPACE"), []byte(""), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(deepDir)
	defer os.Chdir(origDir)

	result, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != tmpDir {
		t.Errorf("expected %s, got %s", tmpDir, result)
	}
}
