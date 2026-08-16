package conformance

import (
	"regexp"
	"strings"
	"testing"
)

// TestChartMembership_UIIsInTheExistingAppRegistryChart guards that the UI
// (FR-47/48/49) is wired into the single, existing App Registry Helm chart
// -- not a second chart that would need its own release/deploy plumbing.
func TestChartMembership_UIIsInTheExistingAppRegistryChart(t *testing.T) {
	build := mustReadFile(t, "BUILD.bazel")

	if !strings.Contains(build, `"//tools/app_registry/ui:ui_metadata"`) {
		t.Error(`tools/app_registry/BUILD.bazel does not list "//tools/app_registry/ui:ui_metadata" ` +
			`in its apps -- the UI must be a deployment in the existing App Registry chart`)
	}

	// Exactly one call to the release_helm_chart macro in the domain --
	// no second chart. A count-based check rather than a single Contains,
	// so this still fails loudly if someone adds a second chart
	// definition even though that macro name trivially remains present.
	// Only a `name(` at the start of a line counts as a call, not the
	// `load(...)` statement or a comment mentioning the macro by name.
	calls := regexp.MustCompile(`(?m)^release_helm_chart\(`).FindAllString(build, -1)
	if len(calls) != 1 {
		t.Errorf("expected exactly 1 release_helm_chart macro call in tools/app_registry/BUILD.bazel, found %d -- "+
			"the UI must ship as a deployment in the existing chart, not a second one", len(calls))
	}

	// The UI's metadata target must appear inside APP_REGISTRY_APPS, the
	// list actually passed to release_helm_chart -- not merely referenced
	// somewhere else in the file (e.g. a stray comment).
	appsBlock := regexp.MustCompile(`(?s)APP_REGISTRY_APPS\s*=\s*\[(.*?)\]`).FindStringSubmatch(build)
	if appsBlock == nil {
		t.Fatal("could not find APP_REGISTRY_APPS = [...] in tools/app_registry/BUILD.bazel")
	}
	if !strings.Contains(appsBlock[1], "ui:ui_metadata") {
		t.Error("APP_REGISTRY_APPS does not include the UI's metadata target -- " +
			"the UI must appear as a deployment in the existing App Registry chart")
	}
}
