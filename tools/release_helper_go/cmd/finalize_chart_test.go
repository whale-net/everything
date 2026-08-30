package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/helm"
)

func writeLockfile(t *testing.T, chartDir string, lockfile helm.ChartLockfile) {
	t.Helper()
	data, err := json.Marshal(lockfile)
	if err != nil {
		t.Fatalf("marshal lockfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, helm.LockfileFileName), data, 0644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
}

// TestResolveMissingChartAppVersions_NoLockfileNoop verifies a chart
// directory with no compose-time lockfile (e.g. produced before AR-2b) is
// left untouched -- there is nothing to cross-check the batch's appVersions
// against.
func TestResolveMissingChartAppVersions_NoLockfileNoop(t *testing.T) {
	chartDir := t.TempDir()
	in := map[string]string{"demo-hello-fastapi": "v1.0.0"}

	got, err := resolveMissingChartAppVersions(context.Background(), chartDir, in, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["demo-hello-fastapi"] != "v1.0.0" {
		t.Errorf("appVersions mutated unexpectedly: %v", got)
	}
}

// TestResolveMissingChartAppVersions_ResolvesFromRegistry is the direct
// regression test for issue #1538: a chart-only release batch that never
// rebuilds a composed app (so it has no entry in appVersions) must resolve
// that app's version from the App Registry's last published artifact, not
// silently leave it missing (which upstream ships as the "latest"
// placeholder composer.go bakes in at Bazel build time).
func TestResolveMissingChartAppVersions_ResolvesFromRegistry(t *testing.T) {
	chartDir := t.TempDir()
	writeLockfile(t, chartDir, helm.ChartLockfile{
		ChartName: "helm-demo-hello-fastapi",
		Images: []helm.ImageLock{
			{AppFullName: "demo-hello-fastapi", Domain: "demo", Name: "hello-fastapi", Repository: "ghcr.io/whale-net/demo-hello-fastapi", Version: "latest"},
		},
	})

	client := newFakeArtifactClient().
		withGetArtifact("demo-hello-fastapi", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "v3.4.5")

	got, err := resolveMissingChartAppVersions(context.Background(), chartDir, map[string]string{}, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["demo-hello-fastapi"] != "v3.4.5" {
		t.Errorf("resolved version = %q, want v3.4.5 (never the lockfile's baked-in %q placeholder)", got["demo-hello-fastapi"], "latest")
	}
}

// TestResolveMissingChartAppVersions_AlreadyResolvedNotOverridden verifies an
// app already present in appVersions (part of this release batch) is used
// as-is and never overridden by a registry lookup.
func TestResolveMissingChartAppVersions_AlreadyResolvedNotOverridden(t *testing.T) {
	chartDir := t.TempDir()
	writeLockfile(t, chartDir, helm.ChartLockfile{
		ChartName: "helm-demo-hello-fastapi",
		Images: []helm.ImageLock{
			{AppFullName: "demo-hello-fastapi", Domain: "demo", Name: "hello-fastapi"},
		},
	})

	client := newFakeArtifactClient().
		withGetArtifact("demo-hello-fastapi", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "v9.9.9")

	got, err := resolveMissingChartAppVersions(context.Background(), chartDir, map[string]string{"demo-hello-fastapi": "v1.0.0"}, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["demo-hello-fastapi"] != "v1.0.0" {
		t.Errorf("resolved version = %q, want plan-pinned v1.0.0 (registry lookup must not override an already-resolved batch entry)", got["demo-hello-fastapi"])
	}
}

// TestResolveMissingChartAppVersions_NoClientErrors verifies that when
// there is no App Registry client available (not opted in, dial failed, or
// --skip-registry) and an app is missing, resolution hard-errors rather
// than falling back to the chart's baked-in "latest" placeholder -- the
// issue #1538 requirement that an unresolvable version must error, never
// silently resolve to "latest".
func TestResolveMissingChartAppVersions_NoClientErrors(t *testing.T) {
	chartDir := t.TempDir()
	writeLockfile(t, chartDir, helm.ChartLockfile{
		ChartName: "helm-demo-hello-fastapi",
		Images: []helm.ImageLock{
			{AppFullName: "demo-hello-fastapi", Domain: "demo", Name: "hello-fastapi"},
		},
	})

	_, err := resolveMissingChartAppVersions(context.Background(), chartDir, map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error when no App Registry client is available to resolve a missing app version")
	}
}

// TestResolveMissingChartAppVersions_RegistryErrorFails verifies a registry
// lookup failure is a hard error, not a silent fallback.
func TestResolveMissingChartAppVersions_RegistryErrorFails(t *testing.T) {
	chartDir := t.TempDir()
	writeLockfile(t, chartDir, helm.ChartLockfile{
		ChartName: "helm-demo-hello-fastapi",
		Images: []helm.ImageLock{
			{AppFullName: "demo-hello-fastapi", Domain: "demo", Name: "hello-fastapi"},
		},
	})

	client := newFakeArtifactClient().
		withGetArtifactError("demo-hello-fastapi", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, errors.New("registry connection refused"))

	_, err := resolveMissingChartAppVersions(context.Background(), chartDir, map[string]string{}, client)
	if err == nil {
		t.Fatal("expected error to propagate from App Registry GetArtifact failure")
	}
}

// TestResolveMissingChartAppVersions_NoPublishedArtifactErrors verifies an
// app with no published artifact at all (e.g. never released) hard-errors
// instead of resolving to an empty/placeholder version.
func TestResolveMissingChartAppVersions_NoPublishedArtifactErrors(t *testing.T) {
	chartDir := t.TempDir()
	writeLockfile(t, chartDir, helm.ChartLockfile{
		ChartName: "helm-demo-hello-fastapi",
		Images: []helm.ImageLock{
			{AppFullName: "demo-hello-fastapi", Domain: "demo", Name: "hello-fastapi"},
		},
	})

	client := newFakeArtifactClient().withGetArtifact("demo-hello-fastapi", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")

	_, err := resolveMissingChartAppVersions(context.Background(), chartDir, map[string]string{}, client)
	if err == nil {
		t.Fatal("expected error when App Registry returns no published artifact")
	}
	if !strings.Contains(err.Error(), "no published artifact") {
		t.Errorf("unexpected error: %v", err)
	}
}
