package deps

import "testing"

// TestSymbolsResolveExpectedPackages asserts each entry in Symbols comes
// from the package it claims to vendor (MCP Go SDK, YouTube Data API v3,
// YouTube Analytics API v2), per the M1 Testing task for this scaffold
// (#1567). This is a compile-only smoke package with no product
// behavior, so the test exists to catch two failure modes go_library
// alone would miss:
//   - a future edit collapsing Symbols to fewer than three entries
//     (deps silently stop being smoke-tested, see "reduce Symbols to
//     two entries" break case below)
//   - a copy/paste mistake pointing two entries at the same package
//     (the loss wouldn't fail `bazel build`, since removing the now-dead
//     import in deps_smoke.go alongside it would still compile clean)
func TestSymbolsResolveExpectedPackages(t *testing.T) {
	want := []string{
		"github.com/modelcontextprotocol/go-sdk/mcp",
		"google.golang.org/api/youtube/v3",
		"google.golang.org/api/youtubeanalytics/v2",
	}

	if len(Symbols) != len(want) {
		t.Fatalf("len(Symbols) = %d, want %d", len(Symbols), len(want))
	}

	seen := make(map[string]bool, len(Symbols))
	for i, typ := range Symbols {
		if typ == nil {
			t.Fatalf("Symbols[%d] is nil", i)
		}
		pkgPath := typ.PkgPath()
		if pkgPath != want[i] {
			t.Errorf("Symbols[%d].PkgPath() = %q, want %q", i, pkgPath, want[i])
		}
		if seen[pkgPath] {
			t.Errorf("Symbols[%d].PkgPath() = %q is a duplicate; each entry must come from a distinct vendored package", i, pkgPath)
		}
		seen[pkgPath] = true
	}
}
