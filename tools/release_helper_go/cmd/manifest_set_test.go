package cmd

import (
	"regexp"
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

// containsJSONField reports whether stdout contains a `"key": "value"` pair,
// tolerating protojson's deliberately-randomized whitespace after the colon
// (google.golang.org/protobuf/internal/detrand: seeded from a hash of the
// compiled test binary itself, specifically to stop code from depending on
// exact byte-for-byte formatting — it flips between one and two spaces
// across different builds of the SAME source, so asserting a literal
// `":  "` is a latent bug regardless of what it's checking). Matches only a
// string-typed value; see containsJSONNumberField for numeric/bare values.
func containsJSONField(t *testing.T, stdout, key, value string) bool {
	t.Helper()
	pattern := `"` + regexp.QuoteMeta(key) + `":\s*"` + regexp.QuoteMeta(value) + `"`
	matched, err := regexp.MatchString(pattern, stdout)
	if err != nil {
		t.Fatalf("bad regexp %q: %v", pattern, err)
	}
	return matched
}

// containsJSONNumberField is containsJSONField for a bare (unquoted)
// numeric value, e.g. an enum serialized as its wire integer instead of its
// name.
func containsJSONNumberField(t *testing.T, stdout, key, value string) bool {
	t.Helper()
	pattern := `"` + regexp.QuoteMeta(key) + `":\s*` + regexp.QuoteMeta(value) + `\D`
	matched, err := regexp.MatchString(pattern, stdout)
	if err != nil {
		t.Fatalf("bad regexp %q: %v", pattern, err)
	}
	return matched
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
					if !containsJSONField(t, stdout, "name", "hello-go") {
						t.Errorf("expected app in output, got: %s", stdout)
					}
					if !containsJSONField(t, stdout, "name", "helm-manmanv2-control-services") {
						t.Errorf("expected chart in output, got: %s", stdout)
					}
					if !containsJSONField(t, stdout, "gitSha", "deadbeefcafe") {
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
					if !containsJSONField(t, stdout, "deployUnit", "DEPLOY_UNIT_IMAGE") {
						t.Errorf("expected deployUnit to serialize as its enum name, got: %s", stdout)
					}
					if containsJSONNumberField(t, stdout, "deployUnit", "2") {
						t.Errorf("deployUnit serialized as an integer, not a name: %s", stdout)
					}
				})
			})
		})
	})
}

// TestManifestSet_SourceCommittedAtFromGitLog covers the happy path: `git
// log -1 --format=%ct <sha>` succeeds and its value lands on
// source_committed_at, the app registry's reconcile-watermark ordering key
// (see appmeta.proto and issue #545).
func TestManifestSet_SourceCommittedAtFromGitLog(t *testing.T) {
	bazel := buildFakeAppAndChartInfra(nil, nil)
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "deadbeefcafe"},
		fakeGitCall{argsContain: []string{"log", "-1", "--format=%ct", "deadbeefcafe"}, output: "1700000000\n"},
	)

	withFS(newFakeFS(), func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withWorkspace(fakeWorkspaceRoot, func() {
					stdout, _, err := runTest([]string{"manifest-set"})
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if !containsJSONField(t, stdout, "sourceCommittedAt", "1700000000") {
						t.Errorf("expected sourceCommittedAt from git log, got: %s", stdout)
					}
				})
			})
		})
	})
}

// TestManifestSet_SourceCommittedAtFallsBackToZeroOnGitFailure covers the
// "git isn't available or the sha can't be resolved" case from issue #545:
// the command must NOT fail, and source_committed_at must be left at its
// zero value so the server falls back to discovered_at (see
// appmeta.proto's doc comment on source_committed_at). protojson omits a
// zero-valued scalar field entirely by default (no EmitUnpopulated), so the
// expected shape is simply "the field is absent," not "the field is 0".
func TestManifestSet_SourceCommittedAtFallsBackToZeroOnGitFailure(t *testing.T) {
	bazel := buildFakeAppAndChartInfra(nil, nil)
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "deadbeefcafe"},
		// No "log" call registered -- fakeGitRunner.Run returns an error,
		// which resolveSourceCommittedAt must swallow.
	)

	withFS(newFakeFS(), func() {
		withBazel(bazel, func() {
			withGit(git, func() {
				withWorkspace(fakeWorkspaceRoot, func() {
					stdout, _, err := runTest([]string{"manifest-set"})
					if err != nil {
						t.Fatalf("expected manifest-set to succeed despite git log failing, got: %v", err)
					}
					if strings.Contains(stdout, `"sourceCommittedAt"`) {
						t.Errorf("expected sourceCommittedAt to be omitted (zero value) when git log fails, got: %s", stdout)
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
					if !containsJSONField(t, stdout, "gitSha", "explicit-sha") {
						t.Errorf("expected explicit --git-sha to be used, got: %s", stdout)
					}
				})
			})
		})
	})
}
