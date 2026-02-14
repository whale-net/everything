package kraken

import (
	"strings"
	"testing"
)

func TestParseTagInfoSuccess(t *testing.T) {
	domain, appName, version, err := ParseTagInfo("demo-hello_python.v1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if domain != "demo" {
		t.Errorf("expected domain 'demo', got '%s'", domain)
	}
	if appName != "hello_python" {
		t.Errorf("expected appName 'hello_python', got '%s'", appName)
	}
	if version != "v1.0.0" {
		t.Errorf("expected version 'v1.0.0', got '%s'", version)
	}
}

func TestParseTagInfoWithMultipleDashes(t *testing.T) {
	domain, appName, version, err := ParseTagInfo("data-processing-ml-service.v2.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Last dash separates domain from app
	if domain != "data-processing-ml" {
		t.Errorf("expected domain 'data-processing-ml', got '%s'", domain)
	}
	if appName != "service" {
		t.Errorf("expected appName 'service', got '%s'", appName)
	}
	if version != "v2.1.0" {
		t.Errorf("expected version 'v2.1.0', got '%s'", version)
	}
}

func TestParseTagInfoInvalidNoVersion(t *testing.T) {
	_, _, _, err := ParseTagInfo("demo-hello_python")
	if err == nil {
		t.Error("expected error for tag without version")
	}
}

func TestParseTagInfoInvalidNoDash(t *testing.T) {
	_, _, _, err := ParseTagInfo("demohello.v1.0.0")
	if err == nil {
		t.Error("expected error for tag without dash")
	}
}

func TestValidateTagFormatValid(t *testing.T) {
	if !ValidateTagFormat("demo-hello_python.v1.0.0") {
		t.Error("expected valid tag format")
	}
}

func TestValidateTagFormatInvalid(t *testing.T) {
	if ValidateTagFormat("invalid-tag") {
		t.Error("expected invalid tag format")
	}
}

func TestAppReleaseDataCommitCount(t *testing.T) {
	data := &AppReleaseData{
		Commits: []ReleaseNote{
			{CommitSHA: "abc123", CommitMessage: "test"},
			{CommitSHA: "def456", CommitMessage: "test2"},
		},
	}
	if data.CommitCount() != 2 {
		t.Errorf("expected 2 commits, got %d", data.CommitCount())
	}
}

func TestAppReleaseDataHasChanges(t *testing.T) {
	data := &AppReleaseData{
		Commits: []ReleaseNote{{CommitSHA: "abc123"}},
	}
	if !data.HasChanges() {
		t.Error("expected HasChanges to be true")
	}

	emptyData := &AppReleaseData{}
	if emptyData.HasChanges() {
		t.Error("expected HasChanges to be false for empty data")
	}
}

func TestAppReleaseDataSummary(t *testing.T) {
	data := &AppReleaseData{
		AppName: "test_app",
		Commits: []ReleaseNote{{CommitSHA: "abc123"}},
	}
	summary := data.Summary()
	if !strings.Contains(summary, "1 commits affecting test_app") {
		t.Errorf("unexpected summary: %s", summary)
	}

	emptyData := &AppReleaseData{AppName: "test_app"}
	emptySummary := emptyData.Summary()
	if !strings.Contains(emptySummary, "No changes affecting test_app found") {
		t.Errorf("unexpected empty summary: %s", emptySummary)
	}
}

func TestFormatMarkdownWithChanges(t *testing.T) {
	data := &AppReleaseData{
		AppName:     "test_app",
		CurrentTag:  "demo-test_app.v1.0.0",
		PreviousTag: "demo-test_app.v0.9.0",
		ReleasedAt:  "2025-01-15 10:00:00 UTC",
		Commits: []ReleaseNote{
			{
				CommitSHA:     "abc12345",
				CommitMessage: "Add feature X",
				Author:        "dev",
				Date:          "2025-01-15",
				FilesChanged:  []string{"main.py"},
			},
		},
	}

	result := FormatMarkdown(data)
	if !strings.Contains(result, "**Released:**") {
		t.Error("expected Released header in markdown")
	}
	if !strings.Contains(result, "## Changes") {
		t.Error("expected Changes header in markdown")
	}
	if !strings.Contains(result, "abc12345") {
		t.Error("expected commit SHA in markdown")
	}
	if !strings.Contains(result, "Add feature X") {
		t.Error("expected commit message in markdown")
	}
}

func TestFormatMarkdownNoChanges(t *testing.T) {
	data := &AppReleaseData{
		AppName:     "test_app",
		CurrentTag:  "demo-test_app.v1.0.0",
		PreviousTag: "demo-test_app.v0.9.0",
		ReleasedAt:  "2025-01-15 10:00:00 UTC",
	}

	result := FormatMarkdown(data)
	if !strings.Contains(result, "No changes affecting test_app found") {
		t.Error("expected no changes message in markdown")
	}
}

func TestFormatPlainTextWithChanges(t *testing.T) {
	data := &AppReleaseData{
		AppName:     "test_app",
		CurrentTag:  "demo-test_app.v1.0.0",
		PreviousTag: "demo-test_app.v0.9.0",
		ReleasedAt:  "2025-01-15 10:00:00 UTC",
		Commits: []ReleaseNote{
			{
				CommitSHA:     "abc12345",
				CommitMessage: "Add feature X",
				Author:        "dev",
				Date:          "2025-01-15",
			},
		},
	}

	result := FormatPlainText(data)
	if !strings.Contains(result, "demo test_app v1.0.0") {
		t.Error("expected parsed title in plain text")
	}
	if !strings.Contains(result, "1. [abc12345] Add feature X") {
		t.Error("expected numbered commit in plain text")
	}
}

func TestFormatJSONWithChanges(t *testing.T) {
	data := &AppReleaseData{
		AppName:     "test_app",
		CurrentTag:  "demo-test_app.v1.0.0",
		PreviousTag: "demo-test_app.v0.9.0",
		ReleasedAt:  "2025-01-15 10:00:00 UTC",
		Commits: []ReleaseNote{
			{
				CommitSHA:     "abc12345",
				CommitMessage: "Add feature X",
				Author:        "dev",
				Date:          "2025-01-15",
			},
		},
	}

	result, err := FormatJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "\"app\": \"test_app\"") {
		t.Error("expected app name in JSON")
	}
	if !strings.Contains(result, "\"commit_count\": 1") {
		t.Error("expected commit count in JSON")
	}
}

func TestFormatMarkdownManyFiles(t *testing.T) {
	data := &AppReleaseData{
		AppName:     "test_app",
		CurrentTag:  "demo-test_app.v1.0.0",
		PreviousTag: "demo-test_app.v0.9.0",
		ReleasedAt:  "2025-01-15 10:00:00 UTC",
		Commits: []ReleaseNote{
			{
				CommitSHA:     "abc12345",
				CommitMessage: "Big change",
				Author:        "dev",
				Date:          "2025-01-15",
				FilesChanged:  []string{"a.py", "b.py", "c.py", "d.py", "e.py", "f.py", "g.py"},
			},
		},
	}

	result := FormatMarkdown(data)
	if !strings.Contains(result, "... and 2 more files") {
		t.Error("expected truncation message for many files")
	}
}
