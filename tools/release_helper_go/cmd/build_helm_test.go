package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// ── findChartApp / resolveChartAppVersions ─────────────────────────────────

func testApps() []AppMetadata {
	return []AppMetadata{
		{AppManifest: &appmetapb.AppManifest{Name: "control-api", Domain: "manmanv2"}, BazelTarget: "//manmanv2/api:control-api_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "event-processor", Domain: "manmanv2"}, BazelTarget: "//manmanv2/processor:event-processor_metadata"},
	}
}

// TestResolveChartAppVersionsKeysByFullName pins the app-versions map to
// composer.go's values.yaml key convention ("<domain>-<name>", i.e.
// AppMetadata.FullName()). packageChartWithVersion looks up
// appVersions[appKey] against the values.yaml "apps" map, which composer.go
// keys by FullName() — keying by the bare app name here would make every
// lookup silently miss and leave the chart's baked-in "latest" imageTag in
// place, which is exactly the bug this test guards against.
func TestResolveChartAppVersionsKeysByFullName(t *testing.T) {
	chart := HelmChartMetadata{ChartManifest: &appmetapb.ChartManifest{
		Name: "helm-manmanv2-control-services", Domain: "manmanv2",
		Apps: []string{"control-api", "event-processor"},
	}}
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--list", "manmanv2-control-api.*"}, output: "manmanv2-control-api.v1.2.3"},
		fakeGitCall{argsContain: []string{"tag", "--list", "manmanv2-event-processor.*"}, output: "manmanv2-event-processor.v0.2.18"},
	)

	versions, err := resolveChartAppVersions(chart, testApps(), git)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]string{
		"manmanv2-control-api":     "v1.2.3",
		"manmanv2-event-processor": "v0.2.18",
	}
	for key, ver := range want {
		if got := versions[key]; got != ver {
			t.Errorf("versions[%q] = %q, want %q (full versions map: %v)", key, got, ver, versions)
		}
	}
	// Bare app names must NOT be used as keys.
	if _, ok := versions["event-processor"]; ok {
		t.Errorf("versions must be keyed by FullName(), not bare app name; got bare-name key in %v", versions)
	}
}

func TestFindChartAppNotFound(t *testing.T) {
	if _, err := findChartApp("nonexistent", "manmanv2", testApps()); err == nil {
		t.Fatal("expected error for unknown app")
	}
}

func TestFindChartAppPrefersChartDomain(t *testing.T) {
	apps := []AppMetadata{
		{AppManifest: &appmetapb.AppManifest{Name: "worker", Domain: "other"}},
		{AppManifest: &appmetapb.AppManifest{Name: "worker", Domain: "manmanv2"}},
	}
	matched, err := findChartApp("worker", "manmanv2", apps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched.Domain != "manmanv2" {
		t.Errorf("expected match in chart's own domain, got domain %q", matched.Domain)
	}
}

// ── packageChartWithVersion's values.yaml imageTag guardrail ───────────────
//
// These tests only exercise the appVersions-key-mismatch failure path,
// which returns before packageChartWithVersion ever shells out to `helm
// package` — the Bazel go_test target has no `helm` binary as a data
// dependency, so a success-path test would fail on `helm: executable file
// not found` rather than testing anything meaningful here.

func TestPackageChartWithVersionErrorsOnKeyMismatch(t *testing.T) {
	chartDir := t.TempDir()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "name: control-services\nversion: 0.0.0-dev\n")
	// values.yaml keyed the way composer.go actually keys it.
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "apps:\n  manmanv2-event-processor:\n    imageTag: latest\n")

	// A bare app name, as the pre-fix resolveChartAppVersions produced,
	// must not silently no-op against the domain-prefixed values.yaml key.
	appVersions := map[string]string{"event-processor": "v0.2.18"}

	_, err := packageChartWithVersion(chartDir, "helm-manmanv2-control-services", "v0.2.18", t.TempDir(), appVersions)
	if err == nil {
		t.Fatal("expected error when appVersions key doesn't match values.yaml apps key, got nil")
	}
	if !strings.Contains(err.Error(), "event-processor") {
		t.Errorf("expected error to name the unmatched key, got: %v", err)
	}
}

func TestPackageChartWithVersionErrorsOnMissingAppsMap(t *testing.T) {
	chartDir := t.TempDir()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "name: control-services\nversion: 0.0.0-dev\n")
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "global:\n  namespace: manmanv2\n")

	appVersions := map[string]string{"manmanv2-event-processor": "v0.2.18"}
	_, err := packageChartWithVersion(chartDir, "helm-manmanv2-control-services", "v0.2.18", t.TempDir(), appVersions)
	if err == nil {
		t.Fatal("expected error when values.yaml has no apps map, got nil")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
