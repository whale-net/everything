package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// TestNFR5_NoBulkSGCEnvOverridesSymbolExists is the second NFR5/LB4
// assertion issue #2274 calls for: a grep-style guard that no
// bulk/convenience env-write symbol (e.g. a "SetSGCEnvOverrides(map[string]
// string)"-shaped function or method) exists anywhere in this package.
// M5 introduces exactly two env write paths (FR13's GameConfig.env_template
// write and FR11's per-key server_game_config patch write) and no third
// store or bulk form -- see the plan's NFR5/LB4 text on issue #2274. A
// bulk write would persist as an ordinary ConfigurationPatch and so pass
// any storage-shape test; only a check on declared symbol names (this
// test) and on live request granularity (handlers_deployment_settings_test.go's
// TestDeploymentSettingsBlade_NFR5_PerKeyRequestGranularity) catches it.
//
// This inspects this package's own source files' function/method
// declarations directly (go/parser against the runfiles-provided source,
// same technique as manmanv2/host/workshop/nfr7_dependency_guard_test.go's
// import guard -- see this package's BUILD.bazel ui_test "data" attribute
// for how the sources are made available here) rather than shelling out to
// `bazel query`/grep, which are unavailable inside a sandboxed `bazel test`
// run.
func TestNFR5_NoBulkSGCEnvOverridesSymbolExists(t *testing.T) {
	forbiddenNameSubstrings := []string{
		"setsgcenvoverrides",
		"bulksgcenvoverrides",
		"sgcenvoverridesbulk",
		"setenvoverridesbulk",
		"bulkenvoverride",
		"setallenvoverrides",
	}

	anchor, err := runfiles.Rlocation("_main/manmanv2/ui/main.go")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(main.go): %v (is manmanv2/ui's ui_test data glob still present?)", err)
	}
	dir := filepath.Dir(anchor)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir(%s): %v", dir, err)
	}

	var srcFiles []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		srcFiles = append(srcFiles, name)
	}
	if len(srcFiles) == 0 {
		t.Fatalf("no non-test .go files discovered in %s -- guard is not checking anything", dir)
	}

	checked := 0
	for _, srcFile := range srcFiles {
		resolved := filepath.Join(dir, srcFile)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, resolved, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", resolved, err)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			checked++
			lower := strings.ToLower(fn.Name.Name)
			for _, forbidden := range forbiddenNameSubstrings {
				if strings.Contains(lower, forbidden) {
					t.Errorf("%s declares %s, which is a bulk/convenience env-write symbol shape NFR5/LB4 forbids -- FR11 writes per-key only, through the shipped handleSGCEnvSet/handleSGCEnvRemove", filepath.Base(resolved), fn.Name.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no function declarations found across %v -- guard is not checking anything", srcFiles)
	}
}
