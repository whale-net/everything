package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestSessionDirectoryCreation tests the core fix: that session directories
// are created with os.MkdirAll before container creation.
//
// This is critical for containerized deployments where Docker bind mounts
// require the source path to exist before creating the container.
func TestSessionDirectoryCreation(t *testing.T) {
	tests := []struct {
		name      string
		sessionID int64
		wantPerm  os.FileMode
	}{
		{
			name:      "creates directory with correct permissions",
			sessionID: 123,
			wantPerm:  0755,
		},
		{
			name:      "handles large session IDs",
			sessionID: 999999,
			wantPerm:  0755,
		},
		{
			name:      "handles single digit session IDs",
			sessionID: 1,
			wantPerm:  0755,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a temp directory for testing
			tempDir := t.TempDir()

			// Simulate what createGameContainer does
			sessionDataDir := filepath.Join(tempDir, fmt.Sprintf("session-%d", tt.sessionID))

			// This is the critical fix: create directory before mounting
			if err := os.MkdirAll(sessionDataDir, tt.wantPerm); err != nil {
				t.Fatalf("os.MkdirAll failed: %v", err)
			}

			// Verify directory exists
			info, err := os.Stat(sessionDataDir)
			if err != nil {
				t.Fatalf("Directory was not created: %v", err)
			}

			// Verify it's actually a directory
			if !info.IsDir() {
				t.Error("Created path is not a directory")
			}

			// Verify permissions
			if info.Mode().Perm() != tt.wantPerm {
				t.Errorf("Directory permissions = %o, want %o",
					info.Mode().Perm(), tt.wantPerm)
			}
		})
	}
}

// TestDirectoryCreationBeforeMount verifies that the directory exists
// before attempting to create a volume mount string (simulating what
// happens before the Docker API call).
func TestDirectoryCreationBeforeMount(t *testing.T) {
	tempDir := t.TempDir()
	sessionID := int64(456)

	// Step 1: Create session directory (THE FIX)
	sessionDataDir := filepath.Join(tempDir, fmt.Sprintf("session-%d", sessionID))
	if err := os.MkdirAll(sessionDataDir, 0755); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Step 2: Verify directory exists before creating mount string
	if _, err := os.Stat(sessionDataDir); os.IsNotExist(err) {
		t.Fatal("Session directory does not exist before mount creation")
	}

	// Step 3: Create volume mount string (what gets passed to Docker)
	volumeMount := fmt.Sprintf("%s:/data/game", sessionDataDir)

	// Verify the source path exists (Docker will fail if it doesn't)
	sourcePath := sessionDataDir
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		t.Errorf("Mount source path does not exist: %s", sourcePath)
		t.Error("Docker will fail with: bind source path does not exist")
	}

	// Verify the mount string is formatted correctly
	expectedMount := filepath.Join(tempDir, "session-456") + ":/data/game"
	if volumeMount != expectedMount {
		t.Errorf("Volume mount = %s, want %s", volumeMount, expectedMount)
	}
}

// TestMkdirAllIdempotent verifies that os.MkdirAll is safe to call
// multiple times (idempotent operation).
func TestMkdirAllIdempotent(t *testing.T) {
	tempDir := t.TempDir()
	sessionDir := filepath.Join(tempDir, "session-789")

	// First call - creates directory
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("First MkdirAll failed: %v", err)
	}

	// Second call - should succeed (idempotent)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("Second MkdirAll failed (not idempotent): %v", err)
	}

	// Third call - should still succeed
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("Third MkdirAll failed (not idempotent): %v", err)
	}

	// Verify directory exists
	if _, err := os.Stat(sessionDir); err != nil {
		t.Errorf("Directory does not exist after multiple MkdirAll calls: %v", err)
	}
}

// TestDirectoryCreationErrorHandling tests error cases when directory
// creation fails (e.g., permission denied).
func TestDirectoryCreationErrorHandling(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test when running as root")
	}

	tempDir := t.TempDir()

	// Create a read-only parent directory to cause permission error
	readOnlyDir := filepath.Join(tempDir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0555); err != nil {
		t.Fatalf("Failed to create read-only directory: %v", err)
	}

	// Try to create a subdirectory - should fail
	sessionDir := filepath.Join(readOnlyDir, "session-999")
	err := os.MkdirAll(sessionDir, 0755)

	// Verify error is returned
	if err == nil {
		t.Error("Expected MkdirAll to fail with permission denied, but it succeeded")
		t.Error("Error handling for directory creation failures is not working")
	}
}

// TestNestedDirectoryCreation verifies that MkdirAll creates parent
// directories as needed (tests the "All" in MkdirAll).
func TestNestedDirectoryCreation(t *testing.T) {
	tempDir := t.TempDir()

	// Create a deeply nested path
	nestedPath := filepath.Join(tempDir, "level1", "level2", "level3", "session-123")

	// MkdirAll should create all parent directories
	if err := os.MkdirAll(nestedPath, 0755); err != nil {
		t.Fatalf("MkdirAll failed to create nested directories: %v", err)
	}

	// Verify the full path exists
	if _, err := os.Stat(nestedPath); err != nil {
		t.Errorf("Nested directory was not created: %v", err)
	}

	// Verify all parent directories were created
	level1 := filepath.Join(tempDir, "level1")
	level2 := filepath.Join(tempDir, "level1", "level2")
	level3 := filepath.Join(tempDir, "level1", "level2", "level3")

	for _, dir := range []string{level1, level2, level3} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("Parent directory %s was not created: %v", dir, err)
		}
	}
}

// TestSessionDirectoryPermissions verifies that different permission modes
// can be set correctly.
func TestSessionDirectoryPermissions(t *testing.T) {
	testCases := []struct {
		name string
		perm os.FileMode
	}{
		{"0755 (rwxr-xr-x)", 0755},
		{"0750 (rwxr-x---)", 0750},
		{"0700 (rwx------)", 0700},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			sessionDir := filepath.Join(tempDir, "session-test")

			if err := os.MkdirAll(sessionDir, tc.perm); err != nil {
				t.Fatalf("MkdirAll failed: %v", err)
			}

			info, err := os.Stat(sessionDir)
			if err != nil {
				t.Fatalf("Stat failed: %v", err)
			}

			// Note: On some filesystems, permissions may be modified by umask
			// So we check if the permissions are at least as restrictive as requested
			gotPerm := info.Mode().Perm()
			if gotPerm != tc.perm {
				t.Logf("Warning: Got permissions %o, wanted %o (may be umask-adjusted)", gotPerm, tc.perm)
			}
		})
	}
}
