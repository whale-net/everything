// Package imageintegration holds krill's NFR2 cross-compilation and
// image-integration validation (issue #2499). See image_integration_test.go
// for the runtime smoke test and ../ARCHITECTURE.md for the images it
// exercises.
package imageintegration

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// TestChartIdentity_KrillChartComposesAllThreeM1Binaries guards NFR2's
// closing acceptance criterion: release_helm_chart in krill/BUILD.bazel
// must list migrate's, api's, and mcp's metadata targets and produce the
// "helm-krill-krill" chart identity (domain "krill" + chart_name "krill").
//
// This reads the actual generated chart_metadata.json rather than parsing
// krill/BUILD.bazel's source text, so a change that keeps the macro call
// looking right but alters its resolved identity (e.g. a domain/chart_name
// typo, or a dropped app) still fails this test. Unlike
// image_integration_test.go, this needs no Docker/QEMU -- it is a plain,
// always-on part of `bazel test //krill/...`.
func TestChartIdentity_KrillChartComposesAllThreeM1Binaries(t *testing.T) {
	p, err := runfiles.Rlocation("_main/krill/krill_chart_chart_metadata_chart_metadata.json")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(krill_chart_chart_metadata_chart_metadata.json): %v "+
			"(is //krill:krill_chart_chart_metadata still a data dep of this test target?)", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read chart metadata: %v", err)
	}

	var meta struct {
		Name    string   `json:"name"`
		Domain  string   `json:"domain"`
		Apps    []string `json:"apps"`
		AppRefs []string `json:"app_refs"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse chart metadata: %v", err)
	}

	if meta.Name != "helm-krill-krill" {
		t.Errorf("chart identity = %q, want %q", meta.Name, "helm-krill-krill")
	}
	if meta.Domain != "krill" {
		t.Errorf("chart domain = %q, want %q", meta.Domain, "krill")
	}

	wantApps := map[string]bool{"migrate": true, "api": true, "mcp": true}
	for _, a := range meta.Apps {
		if !wantApps[a] {
			t.Errorf("unexpected app %q in chart apps %v", a, meta.Apps)
		}
		delete(wantApps, a)
	}
	if len(wantApps) != 0 {
		remaining := make([]string, 0, len(wantApps))
		for a := range wantApps {
			remaining = append(remaining, a)
		}
		t.Errorf("chart apps %v is missing: %v", meta.Apps, remaining)
	}
}
