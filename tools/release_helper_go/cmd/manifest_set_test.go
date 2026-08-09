package cmd

import (
	"strings"
	"testing"
)

// buildFakeAppAndChartInfra combines buildFakeInfra and buildFakeHelmInfra's
// fakes into one BazelRunner that answers both discovery sweeps, the way
// manifest-set needs. Query calls are distinguished by which `kind(...)` they
// request; cquery calls are distinguished by which provider's Starlark expr
// they carry (AppMetadataInfo vs HelmChartMetadataInfo) — see
// appMetadataStarlarkExpr / helmChartMetadataStarlarkExpr.
func buildFakeAppAndChartInfra(apps []fakeApp, charts []fakeHelmChart) *fakeBazelRunner {
	appQueryLines := make([]string, len(apps))
	appCqueryLines := make([]string, len(apps))
	for i, app := range apps {
		plainLabel := "//" + app.pkg + ":" + app.targetSuffix
		appQueryLines[i] = plainLabel
		body := app.customJSON
		if len(body) == 0 {
			body = sampleMetaJSON(app.name, app.domain)
		}
		appCqueryLines[i] = "@@" + plainLabel + "\t" + string(body)
	}

	chartQueryLines := make([]string, len(charts))
	chartCqueryLines := make([]string, len(charts))
	for i, c := range charts {
		plain := "//" + c.pkg + ":" + c.targetName
		chartQueryLines[i] = plain
		chartCqueryLines[i] = "@@" + plain + "\t" + string(sampleHelmMetaJSON(c.name, c.domain, c.apps))
	}

	return newFakeBazel(
		fakeBazelCall{argsContain: []string{"query", "kind(app_metadata"}, argsNotContain: []string{"cquery"}, output: strings.Join(appQueryLines, "\n")},
		fakeBazelCall{argsContain: []string{"query", "kind(helm_chart_metadata"}, argsNotContain: []string{"cquery"}, output: strings.Join(chartQueryLines, "\n")},
		fakeBazelCall{argsContain: []string{"cquery", "AppMetadataInfo"}, output: strings.Join(appCqueryLines, "\n")},
		fakeBazelCall{argsContain: []string{"cquery", "HelmChartMetadataInfo"}, output: strings.Join(chartCqueryLines, "\n")},
	)
}

func TestManifestSet_ContainsAppsAndCharts(t *testing.T) {
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
	}
	charts, _, _ := makeTestHelmCharts()
	bazel := buildFakeAppAndChartInfra(apps, charts)
	git := newFakeGit(fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "deadbeefcafe"})

	withFS(newFakeFS(), func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withWorkspace(fakeWorkspaceRoot, func() {
					stdout, _, err := runTest([]string{"manifest-set"})
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if !strings.Contains(stdout, `"name":  "hello-go"`) {
						t.Errorf("expected app in output, got: %s", stdout)
					}
					if !strings.Contains(stdout, `"name":  "helm-manmanv2-control-services"`) {
						t.Errorf("expected chart in output, got: %s", stdout)
					}
					if !strings.Contains(stdout, `"gitSha":  "deadbeefcafe"`) {
						t.Errorf("expected gitSha from git rev-parse HEAD, got: %s", stdout)
					}
					if !strings.Contains(stdout, `"discoveredAt"`) {
						t.Errorf("expected discoveredAt field, got: %s", stdout)
					}
				})
			})
		})
	})
}

// TestManifestSet_EnumsSerializeAsNames guards the requirement that
// deploy_unit (and any future enum field) round-trips through protojson as
// its name, not its wire integer — encoding/json would silently emit the
// integer instead.
func TestManifestSet_EnumsSerializeAsNames(t *testing.T) {
	apps := []fakeApp{
		{
			pkg: "manmanv2/host", targetSuffix: "host-manager_metadata", name: "host-manager", domain: "manmanv2",
			customJSON: []byte(`{"name":"host-manager","domain":"manmanv2","language":"go","registry":"ghcr.io","organization":"whale-net","repo_name":"manmanv2-host-manager","version":"latest","deploy_unit":"DEPLOY_UNIT_IMAGE"}`),
		},
	}
	bazel := buildFakeAppAndChartInfra(apps, nil)
	git := newFakeGit(fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "deadbeefcafe"})

	withFS(newFakeFS(), func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withWorkspace(fakeWorkspaceRoot, func() {
					stdout, _, err := runTest([]string{"manifest-set"})
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if !strings.Contains(stdout, `"deployUnit":  "DEPLOY_UNIT_IMAGE"`) {
						t.Errorf("expected deployUnit to serialize as its enum name, got: %s", stdout)
					}
					if strings.Contains(stdout, `"deployUnit":  2`) {
						t.Errorf("deployUnit serialized as an integer, not a name: %s", stdout)
					}
				})
			})
		})
	})
}

func TestManifestSet_GitSHAFlagOverridesGit(t *testing.T) {
	bazel := buildFakeAppAndChartInfra(nil, nil)
	// No git calls registered — if the command falls back to `git rev-parse`
	// despite --git-sha being set, the fake errors and the test fails.
	git := newFakeGit()

	withFS(newFakeFS(), func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withWorkspace(fakeWorkspaceRoot, func() {
					stdout, _, err := runTest([]string{"manifest-set", "--git-sha", "explicit-sha"})
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if !strings.Contains(stdout, `"gitSha":  "explicit-sha"`) {
						t.Errorf("expected explicit --git-sha to be used, got: %s", stdout)
					}
				})
			})
		})
	})
}
