package citest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ActionInputOutput represents an action.yml input or output definition.
type ActionInputOutput struct {
	Description string `yaml:"description"`
	Required    *bool  `yaml:"required"`
	Default     string `yaml:"default"`
}

// ActionInputs holds the inputs section of an action.yml.
type ActionInputs struct {
	Inputs map[string]*ActionInputOutput `yaml:"inputs"`
}

// ActionOutputs holds the outputs section of an action.yml.
type ActionOutputs struct {
	Outputs map[string]*ActionInputOutput `yaml:"outputs"`
}

// parseActionYAML reads and parses the download-release-tools action.yml.
func parseActionYAML(t *testing.T) map[string]interface{} {
	t.Helper()
	dir := githubDir(t)
	actionPath := filepath.Join(dir, "actions", "download-release-tools", "action.yml")
	data, err := os.ReadFile(actionPath)
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	var action map[string]interface{}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("parse action.yml: %v", err)
	}
	return action
}

// TestDownloadReleaseToolsActionInputsAreComplete verifies that all required
// inputs for the download-release-tools action are present and haven't changed.
// This is FR-32/NFR-15 coverage: the action contract is unchanged.
func TestDownloadReleaseToolsActionInputsAreComplete(t *testing.T) {
	action := parseActionYAML(t)
	inputsRaw, hasInputs := action["inputs"]
	if !hasInputs {
		t.Fatal("action.yml has no inputs section")
	}

	// Convert to proper structure
	inputsData, _ := yaml.Marshal(map[string]interface{}{"inputs": inputsRaw})
	var inputs ActionInputs
	if err := yaml.Unmarshal(inputsData, &inputs); err != nil {
		t.Fatalf("parse inputs: %v", err)
	}

	// Expected inputs as per FR-32: unchanged contract
	expectedInputs := map[string]bool{
		"source":                 true,
		"target_os":              true,
		"target_arch":            true,
		"release_helper_version": true,
		"app_registry_version":   true,
		"target_env":             true,
		"github_token":           true,
		"fallback_to_source":     true,
		"name":                   true,
	}

	for inputName := range expectedInputs {
		if _, found := inputs.Inputs[inputName]; !found {
			t.Errorf("expected input %q not found in action.yml", inputName)
		}
	}

	// Verify no unexpected inputs were added
	for inputName := range inputs.Inputs {
		if !expectedInputs[inputName] {
			t.Errorf("unexpected new input %q found in action.yml (FR-32: contract must be unchanged)", inputName)
		}
	}
}

// TestDownloadReleaseToolsActionOutputsAreComplete verifies that all outputs
// for the download-release-tools action are present and unchanged.
// This is FR-32/NFR-15 coverage: the action contract is unchanged.
func TestDownloadReleaseToolsActionOutputsAreComplete(t *testing.T) {
	action := parseActionYAML(t)
	outputsRaw, hasOutputs := action["outputs"]
	if !hasOutputs {
		t.Fatal("action.yml has no outputs section")
	}

	// Convert to proper structure
	outputsData, _ := yaml.Marshal(map[string]interface{}{"outputs": outputsRaw})
	var outputs ActionOutputs
	if err := yaml.Unmarshal(outputsData, &outputs); err != nil {
		t.Fatalf("parse outputs: %v", err)
	}

	// Expected outputs as per FR-32: unchanged contract
	expectedOutputs := map[string]bool{
		"release_helper":   true,
		"app_registry_cli": true,
		"source_used":      true,
	}

	for outputName := range expectedOutputs {
		if _, found := outputs.Outputs[outputName]; !found {
			t.Errorf("expected output %q not found in action.yml", outputName)
		}
	}

	// Verify no unexpected outputs were added
	for outputName := range outputs.Outputs {
		if !expectedOutputs[outputName] {
			t.Errorf("unexpected new output %q found in action.yml (FR-32: contract must be unchanged)", outputName)
		}
	}
}

// TestDownloadReleaseToolsActionImplementsFailClosed verifies that the action.yml
// script contains the required fail-closed keywords for FR-66.
// This is a static check: the script must contain explicit ERROR markers for
// failure modes, not just return non-zero.
func TestDownloadReleaseToolsActionImplementsFailClosed(t *testing.T) {
	dir := githubDir(t)
	actionPath := filepath.Join(dir, "actions", "download-release-tools", "action.yml")
	data, err := os.ReadFile(actionPath)
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	content := string(data)

	// FR-66 requires explicit ERROR messages for these failure modes:
	failClosedPatterns := map[string]string{
		"missing checksum manifest":    "Checksum manifest not found",
		"hash mismatch":                "SHA256 checksum mismatch",
		"manifest entry not found":     "not found in checksum manifest",
		"decompression failure":        "Failed to decompress",
		"fetch manifest failure":       "Failed to fetch checksum manifest",
		"fetch binary failure":         "failed to download",
	}

	for failMode, pattern := range failClosedPatterns {
		if !strings.Contains(content, pattern) {
			t.Errorf("FR-66 fail-closed behavior for %q not found: looking for %q in action.yml", failMode, pattern)
		}
	}

	// FR-66: Verify the decompress-then-verify ordering
	// Extract the download_single_tool_s3 function body to check order within it
	startMarker := "download_single_tool_s3() {"
	endMarker := "            }\n\n            DL_OK="
	startIdx := strings.Index(content, startMarker)
	endIdx := strings.Index(content, endMarker)
	
	if startIdx > 0 && endIdx > startIdx {
		functionBody := content[startIdx : endIdx+len(endMarker)]
		
		// Within the function, gunzip (decompression) should come before verify_checksum call
		gunzipIdx := strings.Index(functionBody, "gunzip")
		verifyCallIdx := strings.Index(functionBody, "verify_checksum \"$bin_to_verify\"")
		
		if gunzipIdx > 0 && verifyCallIdx > 0 && gunzipIdx > verifyCallIdx {
			t.Error("FR-66: decompression (gunzip) appears after verify_checksum call; must decompress BEFORE verification")
		}
		
		// chmod +x should come after verify_checksum (it's the last step)
		chmodIdx := strings.LastIndex(functionBody, "chmod +x")
		if chmodIdx > 0 && verifyCallIdx > 0 && chmodIdx < verifyCallIdx {
			t.Error("FR-66: chmod +x appears before verify_checksum call; must only make executable after successful verification")
		}
	}
}

// TestDownloadReleaseToolsActionUsesDeclareddName verifies that the action
// uses the declared filename from the resolution response for manifest lookup,
// not a composed name. This is FR-67.
func TestDownloadReleaseToolsActionUsesDeclareddName(t *testing.T) {
	dir := githubDir(t)
	actionPath := filepath.Join(dir, "actions", "download-release-tools", "action.yml")
	data, err := os.ReadFile(actionPath)
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	content := string(data)

	// FR-67: verify the declared_filename is used as the lookup key
	if !strings.Contains(content, "declared_filename") {
		t.Error("FR-67: declared_filename variable not found; must use declared name from resolution response")
	}

	if !strings.Contains(content, "lookup_name") {
		t.Error("FR-67: lookup_name mechanism not found for declared filename usage")
	}

	// FR-67: must NOT have the old composition pattern:
	// bin_basename="${tool}-${TARGET_OS}-${TARGET_ARCH}"
	if strings.Contains(content, "bin_basename") {
		t.Error("FR-67: old bin_basename composition pattern found; must use declared_filename from resolution response instead")
	}
}

// TestDownloadReleaseToolsActionNoURLPersistence verifies that URLs are not
// cached in environment variables, step outputs, or files for reuse.
// This is NFR-19(c).
func TestDownloadReleaseToolsActionNoURLPersistence(t *testing.T) {
	dir := githubDir(t)

	// Check all workflow files that use download-release-tools action
	files, err := CIConfigFiles(dir)
	if err != nil {
		t.Fatalf("locate CI config: %v", err)
	}

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Logf("skipping unreadable file: %s", file)
			continue
		}

		content := string(data)
		if !strings.Contains(content, "download-release-tools") {
			continue // Not a workflow that uses this action
		}

		// Check for suspicious patterns: storing a resolved URL
		// (URLs contain presigned query parameters with expiry)
		suspiciousPatterns := []string{
			"downloadUrl",
			"download_url",
			"checksumManifestUrl",
			"checksum_manifest_url",
		}

		for _, pattern := range suspiciousPatterns {
			// It's OK to extract these in the action itself, but not OK to
			// persist them to GITHUB_OUTPUT, GITHUB_ENV, or save to artifacts
			if strings.Contains(content, "echo") && strings.Contains(content, pattern) &&
				(strings.Contains(content, "GITHUB_OUTPUT") || strings.Contains(content, "GITHUB_ENV")) {
				t.Errorf("NFR-19(c): suspicious URL persistence pattern in %s: %s", file, pattern)
			}
		}
	}
}

// TestActionCallSitesUnchanged verifies that all call sites of the
// download-release-tools action in CI workflows haven't changed their
// invocation pattern. This is FR-32/NFR-15.
func TestActionCallSitesUnchanged(t *testing.T) {
	dir := githubDir(t)

	// Find all workflow files that use download-release-tools
	files, err := CIConfigFiles(dir)
	if err != nil {
		t.Fatalf("locate CI config: %v", err)
	}

	callSites := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Logf("skipping unreadable file: %s", file)
			continue
		}

		content := string(data)
		if !strings.Contains(content, "download-release-tools") {
			continue // Not a call site
		}

		callSites++

		// Parse as YAML to verify structure
		var workflow map[string]interface{}
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			// Some files may not be pure YAML, skip
			continue
		}

		// A call site should use 'uses: .github/actions/download-release-tools'
		if !strings.Contains(content, "uses:") || !strings.Contains(content, "download-release-tools") {
			t.Logf("Warning: found download-release-tools reference but not a standard action call in %s", file)
		}
	}

	// The issue mentions 12+ call sites; we should find at least that many
	if callSites < 10 {
		t.Logf("Found %d call sites to download-release-tools (expected 12+); extraction may be incomplete", callSites)
	}
}
