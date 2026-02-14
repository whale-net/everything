package kraken

import (
	"testing"
)

func TestShouldIgnoreFileGitHubWorkflows(t *testing.T) {
	if !ShouldIgnoreFile(".github/workflows/ci.yml") {
		t.Error("expected .github/workflows/ci.yml to be ignored")
	}
	if !ShouldIgnoreFile(".github/actions/setup/action.yml") {
		t.Error("expected .github/actions/setup/action.yml to be ignored")
	}
}

func TestShouldIgnoreFileDocs(t *testing.T) {
	if !ShouldIgnoreFile("docs/README.md") {
		t.Error("expected docs/README.md to be ignored")
	}
	if !ShouldIgnoreFile("CHANGELOG.md") {
		t.Error("expected CHANGELOG.md to be ignored")
	}
}

func TestShouldIgnoreFileCopilotInstructions(t *testing.T) {
	if !ShouldIgnoreFile("copilot-instructions.md") {
		t.Error("expected copilot-instructions.md to be ignored")
	}
}

func TestShouldNotIgnoreSourceFiles(t *testing.T) {
	files := []string{
		"main.py",
		"tools/release_helper/core.py",
		"demo/hello_python/main.py",
		"BUILD.bazel",
		"go.mod",
	}

	for _, f := range files {
		if ShouldIgnoreFile(f) {
			t.Errorf("expected %s to NOT be ignored", f)
		}
	}
}
