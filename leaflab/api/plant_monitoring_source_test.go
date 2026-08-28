package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// TestPlantMonitoringStatus_CallsAttributionResolver_NotReimplemented is a
// structural regression guard for FR56's Testing-phase requirement ("The
// rule is not reimplemented"): plant_monitoring.go must resolve attribution
// through attribution.Resolver, never by re-deriving FR23's nearest-ancestor
// stopping condition itself. plant_monitoring.go is located via the Bazel
// runfiles manifest (see this target's data attribute in BUILD.bazel),
// mirroring plants_lifecycle_integration_test.go's identical technique for
// its own structural assertion
// (TestPlantWritePaths_NoHardDeleteOfPlantRow) -- this test does not need a
// live database, so it lives alongside the no-Docker api_lib_test suite
// rather than the integration-tagged targets.
func TestPlantMonitoringStatus_CallsAttributionResolver_NotReimplemented(t *testing.T) {
	path, err := runfiles.Rlocation("_main/leaflab/api/plant_monitoring.go")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(plant_monitoring.go): %v (is it listed in this target's data attribute?)", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plant_monitoring.go: %v", err)
	}
	source := string(src)

	if !strings.Contains(source, `"github.com/whale-net/everything/leaflab/api/attribution"`) {
		t.Error("plant_monitoring.go does not import leaflab/api/attribution -- FR56 must resolve attribution through the shared Phase 3 rule, not reimplement it")
	}
	if !strings.Contains(source, "attribution.NewResolver(") {
		t.Error("plant_monitoring.go does not construct an attribution.Resolver -- FR56 must call the shared rule")
	}
	if !strings.Contains(source, ".ResolvePlants(") {
		t.Error("plant_monitoring.go never calls Resolver.ResolvePlants -- FR56 must call the shared rule, not a query of its own for the nearest-ancestor stopping condition")
	}
	// attribution.go's own doc comment names path_ids (v_region_path's
	// root-to-leaf array) as its specific technique for walking ancestors
	// leaf-toward-root (see attribution.go's ResolvePlants). Its presence
	// here would mean this file duplicated that walk instead of delegating
	// to the resolver.
	if strings.Contains(source, "path_ids") {
		t.Error("plant_monitoring.go references path_ids -- that is attribution.Resolver's own ancestor-walk technique; FR56 must not reimplement it here")
	}
}
