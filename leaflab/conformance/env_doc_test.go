// Package conformance holds cross-package NFR17 documentation-conformance
// tests for LeafLab. These tests read real checked-in source/doc files as
// data (never a copy or a fixture) so a regression in leaflab/api/main.go,
// leaflab/ui/main.go, an ENV.md, or TOC.md is caught directly, not by
// proxy. Copied from the pattern in tools/app_registry/conformance
// (env_doc_test.go, paths_test.go) per root plan #1166's Phase 1 docs task.
package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// leaflabDir locates the leaflab/ domain root, using leaflab/BUILD.bazel
// (staged as a data dependency via the "toc" filegroup, which lives in that
// same BUILD.bazel) as the marker file. Bazel stages data relative to the
// runfiles root; a plain `go test` run from this package's own directory
// finds it by walking up. Mirrors appRegistryDir in
// tools/app_registry/conformance/paths_test.go.
func leaflabDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../..", "../../../.."} {
		marker := filepath.Join(c, "leaflab", "BUILD.bazel")
		if st, err := os.Stat(marker); err == nil && !st.IsDir() {
			return filepath.Join(c, "leaflab")
		}
	}
	t.Fatal("could not locate leaflab/BUILD.bazel -- check the data dependency in BUILD.bazel")
	return ""
}

// mustReadFile reads a file relative to leaflabDir, failing the test on any
// error.
func mustReadFile(t *testing.T, rel string) string {
	t.Helper()
	dir := leaflabDir(t)
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v (check the data dependency in BUILD.bazel)", rel, err)
	}
	return string(b)
}

// getEnvVarRe extracts every getEnv("VAR", ...) call -- the source of
// record for what a main.go actually reads. Both leaflab/api/main.go and
// leaflab/ui/main.go define their own local getEnv(key, default string)
// helper (not os.Getenv called directly at each site), so this single
// pattern covers both packages.
var getEnvVarRe = regexp.MustCompile(`getEnv\("([A-Z0-9_]+)"`)

// assertEveryVarDocumented is the shared NFR17 check: every distinct
// getEnv(...) variable name found in mainGo must appear (backtick-quoted,
// matching this repo's ENV.md convention) somewhere in envMD.
func assertEveryVarDocumented(t *testing.T, mainGo, envMD, mainGoLabel, envMDLabel string) {
	t.Helper()

	matches := getEnvVarRe.FindAllStringSubmatch(mainGo, -1)
	if len(matches) < 5 {
		t.Fatalf("only found %d getEnv(...) calls in %s -- check the data dependency in BUILD.bazel", len(matches), mainGoLabel)
	}

	seen := map[string]bool{}
	for _, m := range matches {
		v := m[1]
		if seen[v] {
			continue
		}
		seen[v] = true
		if !strings.Contains(envMD, "`"+v+"`") {
			t.Errorf("%s reads %q via getEnv but %s does not document it", mainGoLabel, v, envMDLabel)
		}
	}
}

// TestEnvDoc_APICoversEveryVariable is the NFR17 doc check for leaflab-api:
// every environment variable leaflab/api/main.go reads must be documented
// in leaflab/api/ENV.md.
func TestEnvDoc_APICoversEveryVariable(t *testing.T) {
	mainGo := mustReadFile(t, "api/main.go")
	envMD := mustReadFile(t, "api/ENV.md")
	assertEveryVarDocumented(t, mainGo, envMD, "leaflab/api/main.go", "leaflab/api/ENV.md")
}

// TestEnvDoc_UICoversEveryVariable is the NFR17 doc check for leaflab-ui:
// every environment variable leaflab/ui/main.go's LoadConfig reads must be
// documented in leaflab/ui/ENV.md.
func TestEnvDoc_UICoversEveryVariable(t *testing.T) {
	mainGo := mustReadFile(t, "ui/main.go")
	envMD := mustReadFile(t, "ui/ENV.md")
	assertEveryVarDocumented(t, mainGo, envMD, "leaflab/ui/main.go", "leaflab/ui/ENV.md")
}

// TestTOC_LinksBothNewENVDocs guards NFR17's TOC requirement directly
// against the checked-in file: leaflab/TOC.md must index both new ENV.md
// files with a Markdown link, not merely mention them in passing.
func TestTOC_LinksBothNewENVDocs(t *testing.T) {
	toc := mustReadFile(t, "TOC.md")
	for _, link := range []string{"(api/ENV.md)", "(ui/ENV.md)"} {
		if !strings.Contains(toc, link) {
			t.Errorf("leaflab/TOC.md does not link %s -- NFR17 requires both new ENV.md files to be indexed there", link)
		}
	}
}
