//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. It shares this package's target (BUILD.bazel) and testEnv helper
// with roundtrip_integration_test.go and
// design_milestone_query_integration_test.go, the same way those two share
// it with each other.
//
// FR11 (issue #2549): a one-pass import of whagent_net's current product
// brief into krill as scope-qualified entities, with a report identifying,
// for each source item, the entity id it became -- "confirmation that
// nothing was lost in that import, so that I can trust krill as the new
// source of truth." whagent_net is the first product krill holds that
// krill did not author (C27's adoption proof) -- see krill/ARCHITECTURE.md
// "The markdown importer".
//
// Testing section (issue #2549), scaffolded here as skeletons; each case
// is filled in and red/green-verified in this task's Testing phase:
//
//  1. Parse-only, no store: Parse over whagent_net's real doc set yields a
//     ParsedProduct with every persona, decision, capability, non-goal, and
//     milestone, asserted by count and by naming specific items verbatim.
//  2. Full import into a real Postgres: every parsed item becomes an
//     entity, and the FR11 report names an entity id for each.
//  3. Nothing lost, checked the strong way: for each source item, the
//     reported entity id resolves through slice.Querier.GetProductSlice
//     (or the relevant store getter) to content matching the source text.
//  4. Capability-map entries land as Feature rows under a FeatureSet named
//     for their bucket; load-bearing decisions land under the synthetic
//     "Load-bearing decisions" FeatureSet -- the same mapping krill's own
//     import uses.
//  5. Milestone refs and entity_milestone associations are created for
//     every `### M<n>` in whagent_net's roadmap.
//  6. A doc set with a deliberately unmapped heading (a small synthetic
//     fixture, not whagent_net's real files) produces a non-zero unmapped
//     count, a WARNING log, and a non-zero exit without --allow-unmapped.
//  7. Import is scope-qualified (LB1): every created row carries the
//     session's scope_id.
//  8. The import records an import_completion row (#2548) and a second run
//     refuses.
//  9. Regression: this package's existing krill-self-import tests
//     (roundtrip_integration_test.go, design_milestone_query_integration_test.go)
//     stay green.
//
// Run explicitly (requires a working Docker daemon):
//
//	bazel test //krill/conformance:roundtrip_integration_test --test_output=all
package conformance

import (
	"path/filepath"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// whagentNetDocsRoot locates whagent_net's own real, committed doc set
// through Bazel runfiles (data deps //whagent_net:docs and
// //whagent_net/product:docs on this package's go_test target), mirroring
// krillDocsRoot's (roundtrip_integration_test.go) use of krill's own
// brief. whagent_net is parsed and imported here as a read fixture only --
// nothing in this file ever writes back to whagent_net/*.
func whagentNetDocsRoot(t *testing.T) string {
	t.Helper()
	productMD, err := runfiles.Rlocation("_main/whagent_net/PRODUCT.md")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(whagent_net/PRODUCT.md): %v (is //whagent_net:docs still a data dep of this test target?)", err)
	}
	return filepath.Dir(productMD)
}

func TestFR11_WhagentNetImport_ParseOnly_EverySourceItemPresent(t *testing.T) {
	t.Skip("TODO(#2549 Testing phase): item 1 -- Parse over whagentNetDocsRoot(t), assert exact counts and verbatim names for personas, decisions, capabilities, non-goals, milestones")
}

func TestFR11_WhagentNetImport_FullImport_ReportNamesEntityIDForEverySourceItem(t *testing.T) {
	t.Skip("TODO(#2549 Testing phase): item 2 -- importer.Import(whagentNetDocsRoot(t)), assert report.Entries has exactly the parsed set, no zero-id entry")
}

func TestFR11_WhagentNetImport_NothingLost_ContentMatchesSourceViaGetProductSlice(t *testing.T) {
	t.Skip("TODO(#2549 Testing phase): item 3 -- for each reported entity id, look it up (slice.Querier.GetProductSlice or the relevant store getter) and assert content matches the source text; mirror roundtrip_integration_test.go's per-kind switch")
}

func TestFR11_WhagentNetImport_CapabilityMapEntries_LandUnderBucketFeatureSets(t *testing.T) {
	t.Skip("TODO(#2549 Testing phase): item 4 -- assert Now/Next/Later buckets each produced their own FeatureSet, and load-bearing decisions landed under the synthetic \"Load-bearing decisions\" FeatureSet (importer.loadBearingFeatureSetName), not a whagent_net-specific one")
}

func TestFR11_WhagentNetImport_MilestoneAssociations_CreatedForEveryRoadmapMilestone(t *testing.T) {
	t.Skip("TODO(#2549 Testing phase): item 5 -- assert an entity_milestone row per Delivers:/Must not foreclose: citation for M1, M2, M3")
}

func TestFR11_WhagentNetImport_UnmappedHeading_NonZeroExitWithoutAllowUnmapped(t *testing.T) {
	t.Skip("TODO(#2549 Testing phase): item 6 -- against a small synthetic fixture (not whagent_net's real files) with a deliberately unmapped heading, assert a non-zero unmapped count, a WARNING log, and Import/cmd's non-zero exit without --allow-unmapped")
}

func TestFR11_WhagentNetImport_ScopeQualified_EveryRowCarriesScopeID(t *testing.T) {
	t.Skip("TODO(#2549 Testing phase): item 7 -- LB1: assert every row importer.Import wrote for whagent_net carries the session's scope_id")
}

func TestFR11_WhagentNetImport_RecordsImportCompletion_SecondRunRefuses(t *testing.T) {
	t.Skip("TODO(#2549 Testing phase): item 8 -- assert an import_completion row is recorded and a second Import against the same (scope, path) refuses (importer.ErrAlreadyImported), mirroring krill/importer/importer_integration_test.go's FR12 cases")
}
