package cmd

import (
	"fmt"
	"strings"
	"testing"
)

// sampleHelmMetaJSON returns a helm chart metadata JSON blob for use in fakeFS.
func sampleHelmMetaJSON(name, domain string, apps []string) []byte {
	appsJSON := `[]`
	if len(apps) > 0 {
		quoted := make([]string, len(apps))
		for i, a := range apps {
			quoted[i] = fmt.Sprintf("%q", a)
		}
		appsJSON = "[" + strings.Join(quoted, ",") + "]"
	}
	return []byte(fmt.Sprintf(
		`{"name":%q,"domain":%q,"namespace":%q,"apps":%s,"chart_target":":chart","version":"0.0.0-dev"}`,
		name, domain, domain, appsJSON,
	))
}

// helmMetaPath returns the fakeFS path for a helm chart metadata target.
func helmMetaPath(pkg, targetName string) string {
	return fakeWorkspaceRoot + "/bazel-bin/" + pkg + "/" + targetName + "_chart_metadata.json"
}

type fakeHelmChart struct {
	pkg        string // e.g., "manmanv2"
	targetName string // e.g., "manmanv2_chart_chart_metadata"
	name       string // e.g., "helm-manmanv2-control-services"
	domain     string // e.g., "manmanv2"
	apps       []string
}

func buildFakeHelmInfra(charts []fakeHelmChart) (*fakeFS, *fakeBazelRunner) {
	fs := newFakeFS()

	queryLines := make([]string, len(charts))
	cqueryLines := make([]string, len(charts))
	for i, c := range charts {
		plain := "//" + c.pkg + ":" + c.targetName
		queryLines[i] = plain
		cqueryLines[i] = "@@" + plain + "\t" + string(sampleHelmMetaJSON(c.name, c.domain, c.apps))
	}

	bazelCalls := []fakeBazelCall{
		{argsContain: []string{"query", "kind(helm_chart_metadata"}, argsNotContain: []string{"cquery"}, output: strings.Join(queryLines, "\n")},
		{argsContain: []string{"cquery"}, output: strings.Join(cqueryLines, "\n")},
	}
	return fs, newFakeBazel(bazelCalls...)
}

func makeTestHelmCharts() ([]fakeHelmChart, *fakeFS, *fakeBazelRunner) {
	charts := []fakeHelmChart{
		{pkg: "demo", targetName: "fastapi_chart_chart_metadata", name: "helm-demo-fastapi", domain: "demo", apps: []string{"hello-fastapi"}},
		{pkg: "manmanv2", targetName: "manmanv2_chart_chart_metadata", name: "helm-manmanv2-control-services", domain: "manmanv2", apps: []string{"control-api", "event-processor"}},
		{pkg: "leaflab", targetName: "leaflab_chart_chart_metadata", name: "helm-leaflab-leaflab", domain: "leaflab", apps: []string{"processor"}},
		{pkg: "friendly_computing_machine", targetName: "fcm_chart_chart_metadata", name: "helm-friendly-computing-machine-bot-services", domain: "friendly-computing-machine", apps: []string{"bot"}},
	}
	fs, bazel := buildFakeHelmInfra(charts)
	return charts, fs, bazel
}

// ── HelmChartMetadataFilePath ────────────────────────────────────────────────

func TestHelmChartMetadataFilePath(t *testing.T) {
	tests := []struct {
		target  string
		want    string
		wantErr bool
	}{
		{
			target: "//manmanv2:manmanv2_chart_chart_metadata",
			want:   fakeWorkspaceRoot + "/bazel-bin/manmanv2/manmanv2_chart_chart_metadata_chart_metadata.json",
		},
		{
			target: "//friendly_computing_machine:fcm_chart_chart_metadata",
			want:   fakeWorkspaceRoot + "/bazel-bin/friendly_computing_machine/fcm_chart_chart_metadata_chart_metadata.json",
		},
		{target: "invalid", wantErr: true},
		{target: "//nodash", wantErr: true},
	}
	for _, tt := range tests {
		got, err := helmChartMetadataFilePath(fakeWorkspaceRoot, tt.target)
		if tt.wantErr {
			if err == nil {
				t.Errorf("helmChartMetadataFilePath(%q): expected error, got %q", tt.target, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("helmChartMetadataFilePath(%q): unexpected error: %v", tt.target, err)
			continue
		}
		if got != tt.want {
			t.Errorf("helmChartMetadataFilePath(%q) = %q, want %q", tt.target, got, tt.want)
		}
	}
}

// ── GetHelmChartMetadata ─────────────────────────────────────────────────────

func TestGetHelmChartMetadata(t *testing.T) {
	target := "//manmanv2:manmanv2_chart_chart_metadata"
	path := helmMetaPath("manmanv2", "manmanv2_chart_chart_metadata")
	fs := newFakeFS().add(path, sampleHelmMetaJSON("helm-manmanv2-control-services", "manmanv2", []string{"control-api"}))
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"build", target}})

	meta, err := GetHelmChartMetadata(target, bazel, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Name != "helm-manmanv2-control-services" {
		t.Errorf("Name = %q, want %q", meta.Name, "helm-manmanv2-control-services")
	}
	if meta.Domain != "manmanv2" {
		t.Errorf("Domain = %q, want %q", meta.Domain, "manmanv2")
	}
	if meta.BazelTarget != target {
		t.Errorf("BazelTarget = %q, want %q", meta.BazelTarget, target)
	}
}

func TestGetHelmChartMetadataBuildFails(t *testing.T) {
	target := "//manmanv2:manmanv2_chart_chart_metadata"
	bazel := newFakeBazel()
	fs := newFakeFS()
	_, err := GetHelmChartMetadata(target, bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error when bazel build fails")
	}
}

func TestGetHelmChartMetadataFileMissing(t *testing.T) {
	target := "//manmanv2:manmanv2_chart_chart_metadata"
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"build", target}})
	fs := newFakeFS()
	_, err := GetHelmChartMetadata(target, bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error when metadata file is missing")
	}
}

// ── ListAllHelmCharts ────────────────────────────────────────────────────────

func TestListAllHelmChartsQueryError(t *testing.T) {
	bazel := newFakeBazel()
	fs := newFakeFS()
	_, err := ListAllHelmCharts(bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error when bazel cquery fails")
	}
}

func TestListAllHelmChartsEmpty(t *testing.T) {
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"query", "kind(helm_chart_metadata"}, argsNotContain: []string{"cquery"}, output: ""})
	fs := newFakeFS()
	result, err := ListAllHelmCharts(bazel, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 charts, got %d", len(result))
	}
}

func TestListAllHelmChartsSorted(t *testing.T) {
	_, fs, bazel := makeTestHelmCharts()
	result, err := ListAllHelmCharts(bazel, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 4 {
		t.Fatalf("expected 4 charts, got %d", len(result))
	}
	// Results must be sorted by name
	for i := 1; i < len(result); i++ {
		if result[i].Name < result[i-1].Name {
			t.Errorf("charts not sorted: %q before %q", result[i-1].Name, result[i].Name)
		}
	}
}

// ── plan-helm-release command ────────────────────────────────────────────────

func TestPlanHelmReleaseAll(t *testing.T) {
	_, fs, bazel := makeTestHelmCharts()
	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				stdout, _, err := runTest([]string{"plan-helm-release", "--charts", "all", "--version", "v1.0.0"})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(stdout, "helm-manmanv2-control-services") {
					t.Errorf("expected manmanv2 chart in output, got: %s", stdout)
				}
				// demo excluded by default
				if strings.Contains(stdout, "helm-demo-fastapi") {
					t.Errorf("demo chart should be excluded from 'all'")
				}
			})
		})
	})
}

func TestPlanHelmReleaseAllIncludeDemo(t *testing.T) {
	_, fs, bazel := makeTestHelmCharts()
	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				stdout, _, err := runTest([]string{"plan-helm-release", "--charts", "all", "--version", "v1.0.0", "--include-demo"})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(stdout, "helm-demo-fastapi") {
					t.Errorf("demo chart should be included with --include-demo, got: %s", stdout)
				}
			})
		})
	})
}

func TestPlanHelmReleaseByDomain(t *testing.T) {
	_, fs, bazel := makeTestHelmCharts()
	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				stdout, _, err := runTest([]string{"plan-helm-release", "--charts", "manmanv2", "--version", "v1.0.0"})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(stdout, "helm-manmanv2-control-services") {
					t.Errorf("expected manmanv2 chart, got: %s", stdout)
				}
				if strings.Contains(stdout, "helm-leaflab") {
					t.Errorf("unexpected leaflab chart in domain-filtered output")
				}
			})
		})
	})
}

func TestPlanHelmReleaseGithubFormat(t *testing.T) {
	_, fs, bazel := makeTestHelmCharts()
	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				stdout, _, err := runTest([]string{"plan-helm-release", "--charts", "manmanv2", "--format", "github", "--version", "v2.0.0"})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.HasPrefix(stdout, "matrix=") {
					t.Errorf("expected github format starting with 'matrix=', got: %s", stdout)
				}
			})
		})
	})
}
