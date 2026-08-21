package cmd

import (
	"path/filepath"
	"testing"

	"github.com/whale-net/everything/tools/helm"
)

// TestResolveContainedImagesFromDigests verifies finalize-chart's digest
// path (issue #928): unlike resolveContainedImages, it never calls a
// DockerRunner -- digests come from the caller-supplied map (the build
// job's own per-app manifest), keyed by AppFullName exactly as the
// lockfile stores it.
func TestResolveContainedImagesFromDigests(t *testing.T) {
	chartDir := "/chart"
	fs := newFakeFS().add(filepath.Join(chartDir, helm.LockfileFileName), []byte(`{
		"chart_name": "helm-demo",
		"images": [
			{"app_full_name": "demo-widget", "domain": "demo", "name": "widget", "repository": "ghcr.io/whale-net/demo-widget", "version": "latest"},
			{"app_full_name": "demo-gadget", "domain": "demo", "name": "gadget", "repository": "ghcr.io/whale-net/demo-gadget", "version": "latest"}
		]
	}`))

	appVersions := map[string]string{"demo-widget": "v1.2.3"}
	appDigests := map[string]string{"demo-widget": "sha256:aaa"}

	got := resolveContainedImagesFromDigests(chartDir, appVersions, appDigests, fs)

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 contained image (demo-gadget has no digest and must be skipped), got %d: %+v", len(got), got)
	}
	img := got[0]
	if img.AppFullName != "demo-widget" || img.Digest != "sha256:aaa" || img.Version != "v1.2.3" || img.Repository != "ghcr.io/whale-net/demo-widget" {
		t.Fatalf("unexpected contained image: %+v", img)
	}
}

func TestResolveContainedImagesFromDigests_NoLockfileReturnsNil(t *testing.T) {
	fs := newFakeFS()
	got := resolveContainedImagesFromDigests("/chart", nil, nil, fs)
	if got != nil {
		t.Fatalf("expected nil when no lockfile is present, got %+v", got)
	}
}
