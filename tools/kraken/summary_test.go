package kraken

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateReleaseSummaryNoApps(t *testing.T) {
	matrixJSON, _ := json.Marshal(MatrixConfig{Include: nil})
	result := GenerateReleaseSummary(string(matrixJSON), "v1.0.0", "pull_request", false, "")

	if !strings.Contains(result, "## 🚀 Release Summary") {
		t.Error("expected release summary header")
	}
	if !strings.Contains(result, "🔍 **Result:** No apps detected for release") {
		t.Error("expected no apps message")
	}
}

func TestGenerateReleaseSummarySingleApp(t *testing.T) {
	matrix := MatrixConfig{
		Include: []MatrixEntry{
			{App: "hello_python", BazelTarget: "//demo/hello_python:hello_python_metadata", Version: "v1.0.0"},
		},
	}
	matrixJSON, _ := json.Marshal(matrix)
	result := GenerateReleaseSummary(string(matrixJSON), "v1.0.0", "workflow_dispatch", false, "")

	if !strings.Contains(result, "✅ **Result:** Release completed") {
		t.Error("expected release completed message")
	}
	if !strings.Contains(result, "📦 **Apps:** hello_python") {
		t.Error("expected apps list")
	}
	if !strings.Contains(result, "🏷️  **Version:** v1.0.0") {
		t.Error("expected version")
	}
}

func TestGenerateReleaseSummaryMultipleAppsSameVersion(t *testing.T) {
	matrix := MatrixConfig{
		Include: []MatrixEntry{
			{App: "hello_python", Version: "v1.0.0"},
			{App: "hello_go", Version: "v1.0.0"},
		},
	}
	matrixJSON, _ := json.Marshal(matrix)
	result := GenerateReleaseSummary(string(matrixJSON), "v1.0.0", "tag_push", false, "")

	if !strings.Contains(result, "📦 **Apps:** hello_python, hello_go") {
		t.Error("expected both apps in list")
	}
	if !strings.Contains(result, "🏷️  **Version:** v1.0.0") {
		t.Error("expected single version")
	}
}

func TestGenerateReleaseSummaryMultipleAppsDifferentVersions(t *testing.T) {
	matrix := MatrixConfig{
		Include: []MatrixEntry{
			{App: "hello_python", Version: "v1.0.0"},
			{App: "hello_go", Version: "v1.1.0"},
			{App: "status_service", Version: "v2.0.0"},
		},
	}
	matrixJSON, _ := json.Marshal(matrix)
	result := GenerateReleaseSummary(string(matrixJSON), "v1.0.0", "workflow_dispatch", false, "")

	if !strings.Contains(result, "🏷️  **Versions:**") {
		t.Error("expected versions header for multiple versions")
	}
	if !strings.Contains(result, "hello_python: v1.0.0") {
		t.Error("expected hello_python version")
	}
	if !strings.Contains(result, "hello_go: v1.1.0") {
		t.Error("expected hello_go version")
	}
	if !strings.Contains(result, "status_service: v2.0.0") {
		t.Error("expected status_service version")
	}
}

func TestGenerateReleaseSummaryInvalidJSON(t *testing.T) {
	result := GenerateReleaseSummary("invalid json", "v1.0.0", "pull_request", false, "")
	if !strings.Contains(result, "🔍 **Result:** No apps detected for release") {
		t.Error("expected no apps message for invalid JSON")
	}
}

func TestGenerateReleaseSummaryEmptyJSON(t *testing.T) {
	result := GenerateReleaseSummary("", "v1.0.0", "push", false, "")
	if !strings.Contains(result, "🔍 **Result:** No apps detected for release") {
		t.Error("expected no apps message for empty JSON")
	}
}

func TestGenerateReleaseSummaryDryRun(t *testing.T) {
	matrix := MatrixConfig{
		Include: []MatrixEntry{
			{App: "hello_python", Version: "v1.0.0"},
		},
	}
	matrixJSON, _ := json.Marshal(matrix)
	result := GenerateReleaseSummary(string(matrixJSON), "v1.0.0", "workflow_dispatch", true, "")

	if !strings.Contains(result, "🧪 **Mode:** Dry run (no images published)") {
		t.Error("expected dry run mode message")
	}
}

func TestGenerateReleaseSummaryWorkflowDispatchTrigger(t *testing.T) {
	matrix := MatrixConfig{
		Include: []MatrixEntry{
			{App: "hello_python", Version: "v1.0.0"},
		},
	}
	matrixJSON, _ := json.Marshal(matrix)
	result := GenerateReleaseSummary(string(matrixJSON), "v1.0.0", "workflow_dispatch", false, "")

	if !strings.Contains(result, "📝 **Trigger:** Manual dispatch") {
		t.Error("expected manual dispatch trigger")
	}
}

func TestGenerateReleaseSummaryTagPushTrigger(t *testing.T) {
	matrix := MatrixConfig{
		Include: []MatrixEntry{
			{App: "hello_python", Version: "v1.0.0"},
		},
	}
	matrixJSON, _ := json.Marshal(matrix)
	result := GenerateReleaseSummary(string(matrixJSON), "v1.0.0", "tag_push", false, "")

	if !strings.Contains(result, "📝 **Trigger:** Git tag push") {
		t.Error("expected git tag push trigger")
	}
}

func TestGenerateReleaseSummaryLatestVersion(t *testing.T) {
	matrix := MatrixConfig{
		Include: []MatrixEntry{
			{App: "hello_python", Version: "latest"},
		},
	}
	matrixJSON, _ := json.Marshal(matrix)
	result := GenerateReleaseSummary(string(matrixJSON), "latest", "push", false, "")

	if !strings.Contains(result, "🏷️  **Version:** latest") {
		t.Error("expected latest version")
	}
}

func TestGenerateReleaseSummaryMixedVersionsWithFallback(t *testing.T) {
	matrix := MatrixConfig{
		Include: []MatrixEntry{
			{App: "hello_python", Version: "v1.0.0"},
			{App: "hello_go"}, // No version - should fallback
		},
	}
	matrixJSON, _ := json.Marshal(matrix)
	result := GenerateReleaseSummary(string(matrixJSON), "v1.2.0", "workflow_dispatch", false, "")

	if !strings.Contains(result, "🏷️  **Versions:**") {
		t.Error("expected versions header for mixed versions")
	}
	if !strings.Contains(result, "hello_python: v1.0.0") {
		t.Error("expected hello_python version")
	}
	if !strings.Contains(result, "hello_go: v1.2.0") {
		t.Error("expected hello_go fallback version")
	}
}
