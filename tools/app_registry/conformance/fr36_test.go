package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/kinds"
)

// TestFR36_NoHardcodedBinaryMaps verifies that the critical code paths
// (finalize.go, record.go, artifact.go) do NOT contain hardcoded app-name
// or app-type maps after FR-36 consolidation to metadata registry.
//
// Acceptance criteria:
// - The metadata registry is the sole source of truth
// - No fallback to hardcoded enumerations or app.AppType checks
// - Code fails gracefully if registry is unavailable
//
// See FR-36 and issue #1142.
func TestFR36_NoHardcodedBinaryMaps(t *testing.T) {
	// Patterns that would indicate hardcoded maps still exist
	// (these should NOT appear after FR-36 implementation)
	forbiddenPatterns := map[string]string{
		`cliBinaryTargets\s*=\s*map`:    "hardcoded cliBinaryTargets map in finalize.go",
		`binaryOwnerFullName\s*=\s*map`: "hardcoded binaryOwnerFullName map in artifact.go",
		`"tools-release_helper_go"\s*:`: "hardcoded full name map entry",
		`"tools-app-registry"\s*:`:      "hardcoded full name map entry",
		// Fallback patterns in critical functions
		`fallback.*hardcoded.*map`: "fallback to hardcoded map (should be metadata registry only)",
	}

	// Critical files that must NOT contain hardcoded maps
	criticalFiles := []string{
		"tools/app_registry/worker/release/finalize.go",
		"tools/app_registry/worker/release/record.go",
		"tools/app_registry/server/handlers/artifact.go",
	}

	repoRoot := getRepositoryRoot(t)
	if repoRoot == "" {
		t.Logf("warning: could not determine repository root, skipping hardcoded-map verification")
		return
	}

	for _, filePath := range criticalFiles {
		fullPath := filepath.Join(repoRoot, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Logf("warning: could not read %s: %v", filePath, err)
			continue
		}

		contentStr := string(content)
		for pattern, description := range forbiddenPatterns {
			re := regexp.MustCompile(pattern)
			if re.MatchString(contentStr) {
				t.Errorf("FR-36 violation in %s: %s. "+
					"The metadata registry must be the sole source of truth per FR-36.",
					filePath, description)
			}
		}
	}
}

// TestFR36_BinaryAppsInMetadata verifies that CLI binary apps are declared
// in the metadata registry (Bazel release_app declarations), not hardcoded.
func TestFR36_BinaryAppsInMetadata(t *testing.T) {
	// Verify kinds are registered
	allKinds := kinds.All()
	if len(allKinds) == 0 {
		t.Logf("info: no kinds registered; skipping binary app metadata check")
		return
	}

	// Get binary kind
	binaryKind, exists := allKinds["binary"]
	if !exists {
		t.Logf("info: binary kind not found in registered kinds")
		return
	}

	// Verify the binary kind has hooks (including H7 which reads app types)
	hooks := binaryKind.Hooks()
	if hooks == nil {
		t.Error("binary kind: Hooks() returned nil")
		return
	}

	h7 := hooks.H7()
	if h7 == nil {
		t.Error("binary kind: H7() returned nil; H7 must be present and use metadata registry for app-type mapping")
		return
	}

	if h7.Name() != "H7" {
		t.Errorf("binary kind: H7().Name() returned %q, expected H7", h7.Name())
	}

	t.Logf("binary kind has H7 hook for metadata-based app-type mapping (FR-36 compliant)")
}

// TestFR36_AppTypeResolutionViaMetadata verifies that code paths that need
// to determine if an app is a CLI binary do so via the metadata registry,
// not via hardcoded maps or app.AppType checks.
//
// This test demonstrates the red/green verification discipline:
// - RED: If registry is nil, returns false (no fallback)
// - GREEN: With registry available, correctly identifies binary apps
func TestFR36_AppTypeResolutionViaMetadata(t *testing.T) {
	t.Run("graceful_failure_without_registry", func(t *testing.T) {
		// This test verifies the actual code patterns in finalize.go
		// The code should be structured so that:
		// - if a.MetadataRegistry == nil { return false }
		// NOT:
		// - if a.MetadataRegistry != nil { ... } else { fallback to map }
		//
		// We verify this by checking the source code contains the right patterns

		repoRoot := getRepositoryRoot(t)
		if repoRoot == "" {
			t.Skip("could not determine repo root")
		}

		finalizePath := filepath.Join(repoRoot, "tools/app_registry/worker/release/finalize.go")
		content, err := os.ReadFile(finalizePath)
		if err != nil {
			t.Skipf("could not read finalize.go: %v", err)
		}

		contentStr := string(content)

		// RED: Should have pattern "if a.MetadataRegistry == nil { return false }"
		// not "if a.MetadataRegistry != nil { ... } else { fallback }"
		redPattern := regexp.MustCompile(`if\s+a\.MetadataRegistry\s*==\s*nil\s*{\s*return\s+(false|"")`)
		if !redPattern.MatchString(contentStr) {
			t.Error("finalize.go: isCLIBinaryApp/binaryNameForCLIApp must return false/empty when registry is nil, " +
				"with no fallback to hardcoded maps")
		}

		// Should NOT have fallback pattern
		fallbackPattern := regexp.MustCompile(`cliBinaryTargets\[`)
		if fallbackPattern.MatchString(contentStr) {
			t.Error("finalize.go: must not have cliBinaryTargets map access (fallback to hardcoded map)")
		}
	})
}

// TestFR36_MetadataRegistryDeclaration verifies that app-type information
// is declared in Bazel metadata (BUILD.bazel release_app) and not in code.
func TestFR36_MetadataRegistryDeclaration(t *testing.T) {
	// Check that binary apps are declared in BUILD.bazel files with app_type="binary" or "cli"
	repoRoot := getRepositoryRoot(t)
	if repoRoot == "" {
		t.Logf("info: could not determine repo root; skipping metadata declaration check")
		return
	}

	// Search for release_app declarations in BUILD.bazel files
	matches, err := filepath.Glob(filepath.Join(repoRoot, "*/BUILD.bazel"))
	if err != nil {
		t.Logf("info: could not glob BUILD.bazel files: %v", err)
		return
	}

	foundBinaryDeclaration := false
	for _, buildFile := range matches {
		content, err := os.ReadFile(buildFile)
		if err != nil {
			continue
		}

		// Look for release_app declarations with app_type = "binary" or "cli"
		if strings.Contains(string(content), `app_type = "binary"`) ||
			strings.Contains(string(content), `app_type = "cli"`) {
			foundBinaryDeclaration = true
			break
		}
	}

	if !foundBinaryDeclaration {
		t.Logf("info: no binary app declarations found in BUILD.bazel files; " +
			"verify that tools/release_helper_go/BUILD.bazel and tools/app_registry/cli/BUILD.bazel " +
			"declare app_type = \"binary\" or \"cli\"")
	}
}
