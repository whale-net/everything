package appmeta

import (
	"bytes"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// TestAllManifestsDecodeAgainstProto is the rule -> proto direction of the
// contract: every app_metadata / helm_chart_metadata target in the repo is
// discovered the same way release_helper_go discovers them (see ListAllApps
// in //tools/release_helper_go/cmd/metadata.go and ListAllHelmCharts in
// plan_helm.go), and each one must decode with DiscardUnknown: false. A
// Starlark rule attr with no corresponding proto field is a hard failure
// here.
//
// This needs a real `bazel query`/`bazel cquery` against the full repo
// checkout. That can't work hermetically inside a sandboxed `bazel test`
// action — an action only sees its declared inputs, not the whole source
// tree, and even "no-sandbox" doesn't help: the test binary's working
// directory still lives under bazel's execroot/cache tree, which is not an
// ancestor of the real checkout, so there is no path to walk up to find it.
// The one thing that *does* carry the real checkout path into a subprocess
// is BUILD_WORKSPACE_DIRECTORY, and Bazel only sets that for `bazel run`.
//
// So: this target is tagged "manual" (excluded from `//tools/...` and
// `//...` wildcards, same idiom as //tools/scripts:test_cross_compilation)
// and must be invoked with `run`, not `test`:
//
//	bazel run //tools/appmeta:manifest_contract_test
//
// `bazel test //tools/appmeta:manifest_contract_test` still works and still
// runs TestFixtureLeavesNoFieldUnset (fully hermetic), but this rule -> proto
// half will skip with an explanatory message, since BUILD_WORKSPACE_DIRECTORY
// isn't set under `test`. CI invokes it with `run` explicitly (see
// .github/workflows/ci.yml) so the check still executes on every build.
func TestAllManifestsDecodeAgainstProto(t *testing.T) {
	bazelBin, err := locateBazel()
	if err != nil {
		t.Skipf("no bazel binary found; this test requires a real bazel invocation, see doc comment: %v", err)
	}
	workspaceRoot, err := findWorkspaceRootForTest()
	if err != nil {
		t.Skipf("could not locate the real workspace checkout (BUILD_WORKSPACE_DIRECTORY unset); "+
			"run this target with `bazel run //tools/appmeta:manifest_contract_test` instead of `bazel test`: %v", err)
	}

	var (
		discoveryUniversePackages = []string{
			"//demo/...",
			"//firmware/...",
			"//friendly_computing_machine/...",
			"//leaflab/...",
			"//libs/...",
			"//manman/...",
			"//manmanv2/...",
			"//tools/...",
		}
		discoveryPackagesPattern = strings.Join(discoveryUniversePackages, " + ")
		discoveryUniverseScope   = "--universe_scope=" + strings.Join(discoveryUniversePackages, ",")
	)

	t.Run("app_metadata", func(t *testing.T) {
		lines := discoverMetadata(t, bazelBin, workspaceRoot,
			"kind(app_metadata, "+discoveryPackagesPattern+")",
			discoveryUniverseScope,
			"//tools/bazel:release.bzl%AppMetadataInfo")
		for label, jsonPart := range lines {
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(
				[]byte(jsonPart), proto.Message(&appmetapb.AppManifest{})); err != nil {
				t.Errorf("%s: metadata has a key with no matching AppManifest field (rule/proto drift): %v\n  json: %s", label, err, jsonPart)
			}
		}
	})
	t.Run("helm_chart_metadata", func(t *testing.T) {
		lines := discoverMetadata(t, bazelBin, workspaceRoot,
			"kind(helm_chart_metadata, "+discoveryPackagesPattern+")",
			discoveryUniverseScope,
			"//tools/bazel:release.bzl%HelmChartMetadataInfo")
		for label, jsonPart := range lines {
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(
				[]byte(jsonPart), proto.Message(&appmetapb.ChartManifest{})); err != nil {
				t.Errorf("%s: metadata has a key with no matching ChartManifest field (rule/proto drift): %v\n  json: %s", label, err, jsonPart)
			}
		}
	})
}

// discoverMetadata mirrors ListAllApps/ListAllHelmCharts: a loading-phase
// query lists target labels, then a single cquery scoped to those labels
// reads the provider's metadata dict as JSON. Returns label -> raw JSON.
func discoverMetadata(t *testing.T, bazelBin, workspaceRoot, queryExpr, universeScope, providerExpr string) map[string]string {
	t.Helper()

	labelsOut := runBazel(t, bazelBin, workspaceRoot, "query", queryExpr, universeScope, "--noimplicit_deps", "--nodep_deps", "--output=label")
	labels := splitNonEmptyLines(labelsOut)
	if len(labels) == 0 {
		t.Skip("no targets found for " + queryExpr)
	}

	starlarkExpr := `str(target.label) + "\t" + json.encode(providers(target)["` + providerExpr + `"].metadata)`
	out := runBazel(t, bazelBin, workspaceRoot, "cquery", strings.Join(labels, " + "),
		"--output=starlark", "--starlark:expr="+starlarkExpr)

	lines := splitNonEmptyLines(out)
	if len(lines) == 0 {
		t.Fatalf("cquery returned no metadata lines for %s", queryExpr)
	}

	result := make(map[string]string, len(lines))
	for _, line := range lines {
		label, jsonPart, ok := strings.Cut(line, "\t")
		if !ok {
			t.Errorf("malformed cquery line: %q", line)
			continue
		}
		result[label] = jsonPart
	}
	return result
}

func runBazel(t *testing.T, bazelBin, workspaceRoot string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bazelBin, args...)
	cmd.Dir = workspaceRoot
	// Build a clean environment for the nested bazel invocation: PATH and
	// HOME so bazelisk (what "bazel" resolves to on dev machines) can find
	// its cache dir, and nothing else — in particular not TEST_TMPDIR,
	// TEST_SRCDIR, or RUNFILES_DIR, which bazel's own test-running machinery
	// sets on *this* process and which otherwise leak into the nested bazel
	// and make it think it's running from inside a bazel output directory.
	cmd.Env = []string{
		"HOME=" + realHome(),
		"PATH=" + filepath.Dir(bazelBin) + ":/usr/local/bin:/usr/bin:/bin",
	}
	// Bazel writes progress ("Loading: ...") to stderr, but keep stdout and
	// stderr separate rather than using CombinedOutput: interleaving would
	// let a stray progress line land between label lines and corrupt the
	// query-expression union we build from them below.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		t.Fatalf("%s %s (dir=%s): %v\n%s", bazelBin, strings.Join(args, " "), workspaceRoot, err, stderr.String())
	}
	return stdout.String()
}

// realHome resolves a usable HOME for the nested bazel invocation even
// though the test action's own HOME was scrubbed.
func realHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h := os.Getenv("BAZEL_TEST_HOME"); h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return "/root"
}

// locateBazel finds a real bazel binary to shell out to. PATH is scrubbed
// inside bazel test actions (--incompatible_strict_action_env in .bazelrc),
// so exec.LookPath alone is not reliable even with the "no-sandbox" tag —
// check a BAZEL_BIN override, then common install locations, before falling
// back to PATH.
func locateBazel() (string, error) {
	if bin := os.Getenv("BAZEL_BIN"); bin != "" {
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}
	candidates := []string{
		"/usr/local/bin/bazel",
		"/usr/bin/bazel",
		"/home/linuxbrew/.linuxbrew/bin/bazel",
	}
	if home := os.Getenv("HOME"); home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".linuxbrew", "bin", "bazel"),
			filepath.Join(home, "bin", "bazel"),
			filepath.Join(home, ".local", "bin", "bazel"),
			filepath.Join(home, "go", "bin", "bazelisk"),
		)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return exec.LookPath("bazel")
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// findWorkspaceRootForTest locates the real source checkout so the nested
// bazel invocation runs against the full repo, not the test's sandboxed
// runfiles tree. Mirrors findWorkspaceRoot in
// //tools/release_helper_go/cmd/workspace.go.
func findWorkspaceRootForTest() (string, error) {
	if dir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); dir != "" {
		return dir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		// A bazel execroot mirrors MODULE.bazel (and, it turns out, even
		// .git) as symlinked inputs, so neither alone disambiguates it from
		// the real checkout. Reject any candidate under a known bazel
		// output/cache path outright.
		if !strings.Contains(dir, "/execroot/") && !strings.Contains(dir, "/.cache/bazel") &&
			!strings.Contains(dir, "/bazel-out/") && !strings.Contains(dir, "/sandbox/") {
			if _, err := os.Stat(filepath.Join(dir, "MODULE.bazel")); err == nil {
				if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
					return dir, nil
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}
