package kraken

import (
	"testing"
)

func TestPlanReleaseWorkflowDispatchNoApps(t *testing.T) {
	_, err := PlanRelease("workflow_dispatch", "", "v1.0.0", "", "", false)
	if err == nil {
		t.Error("expected error when no apps specified for manual release")
	}
}

func TestPlanReleaseWorkflowDispatchNoVersion(t *testing.T) {
	_, err := PlanRelease("workflow_dispatch", "hello_python", "", "", "", false)
	if err == nil {
		t.Error("expected error when no version specified for manual release")
	}
}

func TestPlanReleaseSpecificModeNoVersion(t *testing.T) {
	_, err := PlanRelease("workflow_dispatch", "hello_python", "", "specific", "", false)
	if err == nil {
		t.Error("expected error when specific mode has no version")
	}
}

func TestPlanReleaseIncrementModeWithVersion(t *testing.T) {
	_, err := PlanRelease("workflow_dispatch", "hello_python", "v1.0.0", "increment_minor", "", false)
	if err == nil {
		t.Error("expected error when increment mode has version specified")
	}
}

func TestPlanReleaseTagPushNoVersion(t *testing.T) {
	_, err := PlanRelease("tag_push", "", "", "", "", false)
	if err == nil {
		t.Error("expected error when tag_push has no version")
	}
}

func TestPlanReleaseUnknownEventType(t *testing.T) {
	_, err := PlanRelease("unknown_event", "", "v1.0.0", "", "", false)
	if err == nil {
		t.Error("expected error for unknown event type")
	}
}

func TestPlanReleaseInvalidVersionFormat(t *testing.T) {
	_, err := PlanRelease("workflow_dispatch", "hello_python", "1.0.0", "", "", false)
	if err == nil {
		t.Error("expected error for invalid version format (missing v prefix)")
	}
}

func TestRegistryTagsFormatting(t *testing.T) {
	tags := FormatRegistryTags("demo", "hello_python", "v1.0.0", "ghcr.io", "", "")
	if tags.Latest != "ghcr.io/demo-hello_python:latest" {
		t.Errorf("unexpected latest tag: %s", tags.Latest)
	}
	if tags.Version != "ghcr.io/demo-hello_python:v1.0.0" {
		t.Errorf("unexpected version tag: %s", tags.Version)
	}
	if tags.Commit != "" {
		t.Errorf("expected empty commit tag, got %s", tags.Commit)
	}
}

func TestRegistryTagsWithCommitSHA(t *testing.T) {
	tags := FormatRegistryTags("demo", "hello_python", "v1.0.0", "ghcr.io", "abc123", "")
	if tags.Commit != "ghcr.io/demo-hello_python:abc123" {
		t.Errorf("unexpected commit tag: %s", tags.Commit)
	}
}

func TestRegistryTagsWithPlatform(t *testing.T) {
	tags := FormatRegistryTags("demo", "hello_python", "v1.0.0", "ghcr.io", "", "amd64")
	if tags.Latest != "ghcr.io/demo-hello_python:latest-amd64" {
		t.Errorf("unexpected latest tag: %s", tags.Latest)
	}
	if tags.Version != "ghcr.io/demo-hello_python:v1.0.0-amd64" {
		t.Errorf("unexpected version tag: %s", tags.Version)
	}
}

func TestRegistryTagsWithGitHubOwner(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_OWNER", "TestOwner")
	tags := FormatRegistryTags("demo", "hello_python", "v1.0.0", "ghcr.io", "", "")
	if tags.Latest != "ghcr.io/testowner/demo-hello_python:latest" {
		t.Errorf("unexpected latest tag: %s", tags.Latest)
	}
}

func TestRegistryTagsNonGHCR(t *testing.T) {
	tags := FormatRegistryTags("demo", "hello_python", "v1.0.0", "docker.io", "", "")
	if tags.Latest != "docker.io/demo-hello_python:latest" {
		t.Errorf("unexpected latest tag: %s", tags.Latest)
	}
}

func TestTagsListWithCommit(t *testing.T) {
	tags := FormatRegistryTags("demo", "hello_python", "v1.0.0", "ghcr.io", "abc123", "")
	list := tags.TagsList()
	if len(list) != 3 {
		t.Errorf("expected 3 tags, got %d", len(list))
	}
}

func TestTagsListWithoutCommit(t *testing.T) {
	tags := FormatRegistryTags("demo", "hello_python", "v1.0.0", "ghcr.io", "", "")
	list := tags.TagsList()
	if len(list) != 2 {
		t.Errorf("expected 2 tags, got %d", len(list))
	}
}
