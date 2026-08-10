package cmd

import (
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
