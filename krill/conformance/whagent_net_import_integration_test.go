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
// Testing section (issue #2549):
//
//  1. Parse-only, no store: Parse over whagent_net's real doc set yields a
//     ParsedProduct with every persona, decision, capability, non-goal, and
//     milestone, asserted by count and by naming specific items verbatim
//     (TestFR11_WhagentNetImport_ParseOnly_EverySourceItemPresent).
//  2. Full import into a real Postgres: every parsed item becomes an
//     entity, and the FR11 report names an entity id for each
//     (TestFR11_WhagentNetImport_FullImport_ReportNamesEntityIDForEverySourceItem).
//  3. Nothing lost, checked the strong way: for each source item, the
//     reported entity id resolves through slice.Querier.GetProductSlice
//     (or the relevant store getter) to content matching the source text
//     (TestFR11_WhagentNetImport_NothingLost_ContentMatchesSourceViaGetProductSlice).
//  4. Capability-map entries land as Feature rows under a FeatureSet named
//     for their bucket; load-bearing decisions land under the synthetic
//     "Load-bearing decisions" FeatureSet -- the same mapping krill's own
//     import uses
//     (TestFR11_WhagentNetImport_CapabilityMapEntries_LandUnderBucketFeatureSets).
//  5. Milestone refs and entity_milestone associations are created for
//     every `### M<n>` in whagent_net's roadmap
//     (TestFR11_WhagentNetImport_MilestoneAssociations_CreatedForEveryRoadmapMilestone).
//  6. A doc set with a deliberately unmapped heading (a small synthetic
//     fixture, not whagent_net's real files) produces a non-zero unmapped
//     count and a loudly-rendered warning
//     (TestFR11_WhagentNetImport_UnmappedHeading_NonZeroExitWithoutAllowUnmapped);
//     the CLI-level half of this case -- that cmd/main.go actually turns
//     this into a non-zero exit without --allow-unmapped, and a WARNING
//     log with it -- is pinned structurally in
//     krill/importer/cmd/main_structural_test.go, mirroring
//     krill/importer/importer_nfr3_test.go's "read the source back" style
//     for a CLI contract that would otherwise need a live subprocess to
//     observe.
//  7. Import is scope-qualified (LB1): every created row carries the
//     session's scope_id
//     (TestFR11_WhagentNetImport_ScopeQualified_EveryRowCarriesScopeID).
//  8. The import records an import_completion row (#2548) and a second run
//     refuses
//     (TestFR11_WhagentNetImport_RecordsImportCompletion_SecondRunRefuses).
//  9. Regression: this package's existing krill-self-import tests
//     (roundtrip_integration_test.go, design_milestone_query_integration_test.go)
//     stay green -- no new test needed, just running this shared target.
//
// Run explicitly (requires a working Docker daemon):
//
//	bazel test //krill/conformance:roundtrip_integration_test --test_output=all
package conformance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/importer"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
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

// wantWhagentNetPersonas and wantWhagentNetNonGoals are whagent_net's real
// PRODUCT.md content (as of this task), named verbatim so a regression in
// one section (e.g. a persona silently dropped) cannot hide behind a bare
// count check -- see this file's package doc, item 1.
var (
	wantWhagentNetPersonas = []string{
		"Operator / developer",
		"Consumer-domain developer",
		"ASS Creator / Analyst",
		"Parent session",
		"Headless service caller",
		"On-call viewer",
		"Slack user",
	}
	wantWhagentNetNonGoals = []string{
		"Multi-provider abstraction in v1",
		"Multi-tenant hosting",
		"Cron-scheduled sessions as a service offering",
		"Human approval gate on tool calls",
		"Python or other non-Go consumers",
		"Owning domain tools",
		"manmanv2 integration",
		"Uncapped sessions",
	}
)

func parsedPersonaNames(personas []importer.ParsedPersona) []string {
	names := make([]string, len(personas))
	for i, p := range personas {
		names[i] = p.Name
	}
	return names
}

func parsedNonGoalNames(nonGoals []importer.ParsedNonGoal) []string {
	names := make([]string, len(nonGoals))
	for i, ng := range nonGoals {
		names[i] = ng.Name
	}
	return names
}

func parsedDecisionByID(decisions []importer.ParsedDecision) map[string]importer.ParsedDecision {
	byID := make(map[string]importer.ParsedDecision, len(decisions))
	for _, d := range decisions {
		byID[d.ID] = d
	}
	return byID
}

func parsedCapabilityByID(buckets []importer.ParsedBucket) map[string]importer.ParsedCapability {
	byID := map[string]importer.ParsedCapability{}
	for _, b := range buckets {
		for _, c := range b.Capabilities {
			byID[c.ID] = c
		}
	}
	return byID
}

func parsedMilestoneByID(milestones []importer.ParsedMilestone) map[string]importer.ParsedMilestone {
	byID := make(map[string]importer.ParsedMilestone, len(milestones))
	for _, m := range milestones {
		byID[m.ID] = m
	}
	return byID
}

// parsedTotalEntries is the count of every entity Parse should have
// produced across all five kinds -- what a full Import's report.Entries
// must equal (item 2) if nothing was lost.
func parsedTotalEntries(p *importer.ParsedProduct) int {
	n := len(p.Personas) + len(p.NonGoals) + len(p.Decisions) + len(p.Milestones)
	for _, b := range p.Buckets {
		n += len(b.Capabilities)
	}
	return n
}

// TestFR11_WhagentNetImport_ParseOnly_EverySourceItemPresent is item 1:
// Parse alone (no store) over whagent_net's real, committed doc set must
// surface every persona, decision, non-goal, capability, and milestone --
// checked by count *and* by naming specific items verbatim, so a
// regression in one section cannot hide behind a total that happens to
// still add up (e.g. one persona silently dropped and one non-goal
// double-counted).
func TestFR11_WhagentNetImport_ParseOnly_EverySourceItemPresent(t *testing.T) {
	parsed, err := importer.Parse(whagentNetDocsRoot(t))
	require.NoError(t, err, "whagent_net's real doc set must parse cleanly")

	assert.Equal(t, "whagent-net", parsed.Name)
	assert.NotEmpty(t, parsed.Vision, "expected whagent_net's Vision section to parse")

	require.Len(t, parsed.Personas, len(wantWhagentNetPersonas), "expected all seven whagent_net personas to parse: %+v", parsed.Personas)
	assert.ElementsMatch(t, wantWhagentNetPersonas, parsedPersonaNames(parsed.Personas))

	require.Len(t, parsed.Decisions, 7, "expected all seven LB1-LB7 decisions to parse out of whagent_net's single shared fence: %+v", parsed.Decisions)
	decisionByID := parsedDecisionByID(parsed.Decisions)
	assert.Equal(t, "LB1 — Transcript event record: identity, ordering, and one shape across all three tiers", decisionByID["LB1"].Name)
	assert.Equal(t, "LB7 — Event bus contract owned by one package", decisionByID["LB7"].Name)

	require.Len(t, parsed.NonGoals, len(wantWhagentNetNonGoals), "expected all eight whagent_net non-goals to parse: %+v", parsed.NonGoals)
	assert.ElementsMatch(t, wantWhagentNetNonGoals, parsedNonGoalNames(parsed.NonGoals))
	for _, ng := range parsed.NonGoals {
		assert.Equal(t, importer.NonGoalPermanent, ng.Kind, "an unmarked non-goals list must default every entry to Permanent (parseNonGoals's documented reading)")
	}

	require.Len(t, parsed.Buckets, 3, "expected whagent_net's Now/Next/Later capability buckets to parse: %+v", parsed.Buckets)
	bucketSizes := map[string]int{}
	totalCaps := 0
	for _, b := range parsed.Buckets {
		bucketSizes[b.Name] = len(b.Capabilities)
		totalCaps += len(b.Capabilities)
	}
	assert.Equal(t, 27, totalCaps, "expected all 27 capabilities (C1-C27, allocated not sequential -- C27 lands in Next) across Now/Next/Later")
	assert.Equal(t, 12, bucketSizes["Now"])
	assert.Equal(t, 7, bucketSizes["Next"])
	assert.Equal(t, 8, bucketSizes["Later"])
	capByID := parsedCapabilityByID(parsed.Buckets)
	assert.Equal(t, "An operator can start a session for a named agent, send it turns, and stop it, from Claude Code or any gRPC client.", capByID["C1"].Description)
	assert.Equal(t, "An operator can connect their Claude Code MCP client to whagent-net by signing in once through the browser, instead of manually copying a Keycloak token into their MCP client config.", capByID["C27"].Description)

	require.Len(t, parsed.Milestones, 3, "expected whagent_net's M1-M3 roadmap headings to parse: %+v", parsed.Milestones)
	msByID := parsedMilestoneByID(parsed.Milestones)
	assert.Equal(t, "An operator can run a capped, tool-enabled session as themselves against ASS's MCP server from Claude Code, and read its transcript", msByID["M1"].Title)
	assert.ElementsMatch(t, []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8", "C9", "C11", "C12"}, msByID["M1"].Delivers, "M1 Delivers must be exactly what its own roadmap line names -- it deliberately excludes C10 (deferred to M2)")
	assert.ElementsMatch(t, []string{"LB1", "LB2", "LB3", "LB4", "LB5", "LB6", "LB7"}, msByID["M1"].MustNotForeclose)
	assert.ElementsMatch(t, []string{"C10", "C13", "C14", "C15", "C16", "C17", "C18", "C27"}, msByID["M2"].Delivers)
	assert.ElementsMatch(t, []string{"LB1", "LB2", "LB3", "LB6", "LB7"}, msByID["M2"].MustNotForeclose)
	assert.ElementsMatch(t, []string{"C19", "C20"}, msByID["M3"].Delivers)
	assert.ElementsMatch(t, []string{"LB1", "LB2", "LB3", "LB7"}, msByID["M3"].MustNotForeclose)
}

// TestFR11_WhagentNetImport_FullImport_ReportNamesEntityIDForEverySourceItem
// is item 2: importing whagent_net's real doc set into a real Postgres
// must produce a report entry -- with a real, non-zero entity id -- for
// every single item Parse found, no more and no fewer.
func TestFR11_WhagentNetImport_FullImport_ReportNamesEntityIDForEverySourceItem(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := whagentNetDocsRoot(t)

	parsed, err := importer.Parse(root)
	require.NoError(t, err)
	want := parsedTotalEntries(parsed)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root, testSourceRevision)
	require.NoError(t, err, "importing whagent_net's real committed brief must succeed")
	require.Equal(t, "whagent-net", report.ProductName)

	assert.Len(t, report.Entries, want, "expected exactly one report entry per parsed item -- nothing lost, nothing duplicated")
	for _, e := range report.Entries {
		assert.NotEqual(t, uuid.Nil, e.EntityID, "%s %q must have a real entity id, not a zero id", e.Kind, e.SourceID)
	}
}

// entityByRef and its helpers below back items 3, 4, and 7: they resolve
// every reported entity through the same read paths render.Source itself
// calls (mirroring roundtrip_integration_test.go's approach), rather than
// a second, differently-shaped query.
type whagentNetResolved struct {
	env    testEnv
	report *importer.Report
	root   string
	parsed *importer.ParsedProduct

	personaByID   map[uuid.UUID]store.Persona
	nonGoalByID   map[uuid.UUID]store.NonGoal
	featureByID   map[uuid.UUID]slice.FeatureEntity
	decisionByID  map[uuid.UUID]slice.DecisionEntity
	milestoneByID map[uuid.UUID]store.MilestoneRef

	featureSetNameByID map[uuid.UUID]string
}

func importAndResolveWhagentNet(t *testing.T) whagentNetResolved {
	t.Helper()
	ctx := context.Background()
	env := newTestEnv(t)
	root := whagentNetDocsRoot(t)

	parsed, err := importer.Parse(root)
	require.NoError(t, err)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root, testSourceRevision)
	require.NoError(t, err)

	personas, err := env.store.Personas().ListCurrentByProduct(ctx, report.ProductID)
	require.NoError(t, err)
	personaByID := make(map[uuid.UUID]store.Persona, len(personas))
	for _, p := range personas {
		personaByID[p.ID] = p
	}

	nonGoals, err := env.store.NonGoals().ListCurrentByProduct(ctx, report.ProductID)
	require.NoError(t, err)
	nonGoalByID := make(map[uuid.UUID]store.NonGoal, len(nonGoals))
	for _, ng := range nonGoals {
		nonGoalByID[ng.ID] = ng
	}

	doc, err := slice.NewQuerier(env.store).GetProductSlice(ctx, report.ProductID)
	require.NoError(t, err)
	featureByID := make(map[uuid.UUID]slice.FeatureEntity, len(doc.Features))
	for _, f := range doc.Features {
		featureByID[f.ID] = f
	}
	decisionByID := make(map[uuid.UUID]slice.DecisionEntity, len(doc.Decisions))
	for _, d := range doc.Decisions {
		decisionByID[d.ID] = d
	}
	featureSetNameByID := make(map[uuid.UUID]string, len(doc.FeatureSets))
	for _, fs := range doc.FeatureSets {
		featureSetNameByID[fs.ID] = fs.Name
	}

	milestoneRefs, err := env.store.Milestones().ListRefsByProduct(ctx, env.scopeID, report.ProductID)
	require.NoError(t, err)
	milestoneByID := make(map[uuid.UUID]store.MilestoneRef, len(milestoneRefs))
	for _, m := range milestoneRefs {
		milestoneByID[m.ID] = m
	}

	return whagentNetResolved{
		env: env, report: report, root: root, parsed: parsed,
		personaByID: personaByID, nonGoalByID: nonGoalByID,
		featureByID: featureByID, decisionByID: decisionByID, milestoneByID: milestoneByID,
		featureSetNameByID: featureSetNameByID,
	}
}

// TestFR11_WhagentNetImport_NothingLost_ContentMatchesSourceViaGetProductSlice
// is item 3, the actual "trust krill as the new source of truth" proof:
// for every reported entity id, the content read back through the same
// paths render.Source itself calls must match the source document's own
// text -- id presence alone is not enough.
func TestFR11_WhagentNetImport_NothingLost_ContentMatchesSourceViaGetProductSlice(t *testing.T) {
	r := importAndResolveWhagentNet(t)

	capByID := parsedCapabilityByID(r.parsed.Buckets)
	decByID := parsedDecisionByID(r.parsed.Decisions)
	msByID := parsedMilestoneByID(r.parsed.Milestones)

	seenKinds := map[string]int{}
	for _, e := range r.report.Entries {
		seenKinds[e.Kind]++
		switch e.Kind {
		case "persona":
			p, ok := r.personaByID[e.EntityID]
			require.True(t, ok, "persona %q (entity %s) is in the report but missing from ListCurrentByProduct", e.SourceID, e.EntityID)
			assert.Equal(t, e.Name, p.Name)

		case "non_goal":
			ng, ok := r.nonGoalByID[e.EntityID]
			require.True(t, ok, "non-goal %q (entity %s) is in the report but missing from ListCurrentByProduct", e.SourceID, e.EntityID)
			assert.Equal(t, e.Name, ng.Name)

		case "decision":
			d, ok := r.decisionByID[e.EntityID]
			require.True(t, ok, "decision %s (entity %s) is in the report but missing from GetProductSlice's Decisions", e.SourceID, e.EntityID)
			want, ok := decByID[e.SourceID]
			require.True(t, ok)
			assert.Equal(t, want.Name, d.Name, "decision %s's title must survive the import unchanged", e.SourceID)
			require.NotNil(t, d.Body)
			assert.Equal(t, want.Body, *d.Body, "decision %s's body must survive the import unchanged", e.SourceID)

		case "capability":
			f, ok := r.featureByID[e.EntityID]
			require.True(t, ok, "capability %s (entity %s) is in the report but missing from GetProductSlice's Features", e.SourceID, e.EntityID)
			want, ok := capByID[e.SourceID]
			require.True(t, ok)
			assert.Equal(t, want.Description, f.Name, "capability %s's description must survive the import unchanged", e.SourceID)

		case "milestone":
			ref, ok := r.milestoneByID[e.EntityID]
			require.True(t, ok, "milestone %s (entity %s) is in the report but missing from ListRefsByProduct", e.SourceID, e.EntityID)
			assert.Equal(t, e.SourceID, ref.Name)
			want, ok := msByID[e.SourceID]
			require.True(t, ok)
			assert.Equal(t, want.Title, e.Name, "milestone %s's title must survive the import unchanged", e.SourceID)

		default:
			t.Fatalf("unhandled FR11 report entry kind %q for %s -- add a case above so this test actually covers it", e.Kind, e.SourceID)
		}
	}

	for _, kind := range []string{"persona", "non_goal", "decision", "capability", "milestone"} {
		assert.Positive(t, seenKinds[kind], "expected whagent_net's real brief to import at least one %q entity", kind)
	}
}

// TestFR11_WhagentNetImport_CapabilityMapEntries_LandUnderBucketFeatureSets
// is item 4: every capability lands as a Feature under a FeatureSet named
// for its own source bucket (Now/Next/Later), and every load-bearing
// decision lands under the synthetic "Load-bearing decisions" FeatureSet
// (importer.loadBearingFeatureSetName) -- the same mapping krill's own
// self-import uses, never a whagent_net-specific one.
func TestFR11_WhagentNetImport_CapabilityMapEntries_LandUnderBucketFeatureSets(t *testing.T) {
	r := importAndResolveWhagentNet(t)

	bucketOf := map[string]string{}
	for _, b := range r.parsed.Buckets {
		for _, c := range b.Capabilities {
			bucketOf[c.ID] = b.Name
		}
	}

	capCount, decCount := 0, 0
	for _, e := range r.report.Entries {
		switch e.Kind {
		case "capability":
			capCount++
			f, ok := r.featureByID[e.EntityID]
			require.True(t, ok)
			wantBucket := bucketOf[e.SourceID]
			gotBucket := r.featureSetNameByID[f.FeatureSetID]
			assert.Equal(t, wantBucket, gotBucket, "capability %s must land under its source bucket's FeatureSet (%q), not a whagent_net-specific one", e.SourceID, wantBucket)
			assert.NotEqual(t, "Load-bearing decisions", gotBucket, "capability %s must never land under the synthetic Load-bearing decisions FeatureSet", e.SourceID)

		case "decision":
			decCount++
			d, ok := r.decisionByID[e.EntityID]
			require.True(t, ok)
			gotFeatureSet := r.featureSetNameByID[d.FeatureSetID]
			assert.Equal(t, "Load-bearing decisions", gotFeatureSet, "decision %s must land under the synthetic Load-bearing decisions FeatureSet (importer.loadBearingFeatureSetName), not a whagent_net-specific one", e.SourceID)
		}
	}
	assert.Equal(t, 27, capCount)
	assert.Equal(t, 7, decCount)

	for _, bucket := range []string{"Now", "Next", "Later", "Load-bearing decisions"} {
		found := false
		for _, name := range r.featureSetNameByID {
			if name == bucket {
				found = true
				break
			}
		}
		assert.True(t, found, "expected a %q FeatureSet in the imported product", bucket)
	}
}

// TestFR11_WhagentNetImport_MilestoneAssociations_CreatedForEveryRoadmapMilestone
// is item 5: every `### M<n>` in whagent_net's roadmap produces a
// milestone_ref, and every capability/decision its own Delivers:/Must not
// foreclose: line names produces an entity_milestone association row --
// counted exactly, not just "at least one".
func TestFR11_WhagentNetImport_MilestoneAssociations_CreatedForEveryRoadmapMilestone(t *testing.T) {
	ctx := context.Background()
	r := importAndResolveWhagentNet(t)

	msByID := parsedMilestoneByID(r.parsed.Milestones)
	require.Len(t, r.milestoneByID, 3, "expected exactly M1, M2, M3 as milestone_ref rows")

	for _, ref := range r.milestoneByID {
		want, ok := msByID[ref.Name]
		require.True(t, ok, "unexpected milestone_ref %q with no corresponding parsed milestone", ref.Name)

		assocs, err := r.env.store.Milestones().ListAssociationsByMilestone(ctx, ref.ID)
		require.NoError(t, err)
		wantCount := len(want.Delivers) + len(want.MustNotForeclose)
		assert.Len(t, assocs, wantCount, "milestone %s: expected %d association rows (%d Delivers + %d Must not foreclose)", ref.Name, wantCount, len(want.Delivers), len(want.MustNotForeclose))
	}
}

// whagentNetSyntheticFixture writes a small, deliberately malformed doc
// set to a temp directory: a valid PRODUCT.md (one persona, one decision,
// one non-goal) and a capability-map.md whose second line looks like a
// capability (starts with a bare `C<n>`) but has no dash, so
// parseCapabilityMap's strict capabilityRe never picks it up while
// coverage.go's looser looseCapabilityRe still recognizes it as "trying to
// be a capability" -- exactly CoverageEntry's "recognized syntax with no
// entity" case. The roadmap only cites the one real capability (C1) and
// decision (LB1), so Parse and write() both succeed cleanly; only
// ComputeCoverage's accounting differs. Never whagent_net's own files --
// issue #2549 explicitly requires a synthetic fixture for this case.
func whagentNetSyntheticFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "product"), 0o755))

	productMD := "# synthetic-fixture — Product brief\n\n" +
		"## Vision\n\nA synthetic fixture for FR11's coverage completeness test.\n\n" +
		"## Personas\n\n- **Operator** — runs the synthetic fixture.\n\n" +
		"## Load-bearing decisions\n\n```\nLB1 — A synthetic decision\n  Decide now: nothing real.\n```\n\n" +
		"## Non-goals\n\n- **Being real.** This fixture is not a real product.\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "PRODUCT.md"), []byte(productMD), 0o644))

	capabilityMD := "# Capability map\n\n" +
		"### Now\n" +
		"C1 — A valid capability.\n" +
		"C2 no dash here, deliberately malformed so coverage flags it as unmapped\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "product", "02-capability-map.md"), []byte(capabilityMD), 0o644))

	roadmapMD := "# Roadmap\n\n" +
		"### M1 — A valid milestone outcome\n\n" +
		"Delivers: C1\n" +
		"Must not foreclose: LB1\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "product", "03-roadmap.md"), []byte(roadmapMD), 0o644))

	return root
}

// TestFR11_WhagentNetImport_UnmappedHeading_NonZeroExitWithoutAllowUnmapped
// is item 6's dynamic half: against a small synthetic fixture (never
// whagent_net's real files) with one deliberately unmapped capability
// line, Import itself still succeeds (only cmd/main.go's --allow-unmapped
// gate refuses -- Import's job is to report completeness, not enforce the
// CLI's exit policy), but the report's Coverage section must name exactly
// the one unmapped item, and Render() must print it loudly enough that an
// Operator/Admin cannot miss it. The CLI-level "non-zero exit without
// --allow-unmapped, WARNING log with it" half of this item is pinned
// structurally in krill/importer/cmd/main_structural_test.go -- see this
// file's package doc.
func TestFR11_WhagentNetImport_UnmappedHeading_NonZeroExitWithoutAllowUnmapped(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := whagentNetSyntheticFixture(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root, testSourceRevision)
	require.NoError(t, err, "Import itself must still succeed on a partial doc set -- only cmd/main.go's --allow-unmapped gate refuses")

	assert.Equal(t, 1, report.UnmappedTotal(), "expected exactly the one deliberately malformed capability line to be unmapped")

	var capabilityCoverage *importer.CoverageEntry
	for i := range report.Coverage {
		if report.Coverage[i].SourceFile == "product/02-capability-map.md" {
			capabilityCoverage = &report.Coverage[i]
		}
	}
	require.NotNil(t, capabilityCoverage, "expected a coverage entry for product/02-capability-map.md")
	assert.Equal(t, 1, capabilityCoverage.UnmappedCount)
	require.Len(t, capabilityCoverage.UnmappedItems, 1)
	assert.Contains(t, capabilityCoverage.UnmappedItems[0], "C2 no dash here")

	rendered := report.Render()
	assert.Contains(t, rendered, "WARNING", "a non-zero unmapped count must be printed loudly, not buried in the coverage table alone")
	assert.Contains(t, rendered, "--allow-unmapped", "the loud warning must name the acknowledgment mechanism")
}

// TestFR11_WhagentNetImport_ScopeQualified_EveryRowCarriesScopeID is item
// 7 (LB1): every row importer.Import wrote for whagent_net's real brief
// carries the importing session's scope_id.
func TestFR11_WhagentNetImport_ScopeQualified_EveryRowCarriesScopeID(t *testing.T) {
	ctx := context.Background()
	r := importAndResolveWhagentNet(t)

	require.NotEmpty(t, r.personaByID)
	for _, p := range r.personaByID {
		assert.Equal(t, r.env.scopeID, p.ScopeID)
	}
	require.NotEmpty(t, r.nonGoalByID)
	for _, ng := range r.nonGoalByID {
		assert.Equal(t, r.env.scopeID, ng.ScopeID)
	}

	featureSets, err := r.env.store.FeatureSets().ListCurrentByProduct(ctx, r.report.ProductID)
	require.NoError(t, err)
	require.NotEmpty(t, featureSets)
	for _, fs := range featureSets {
		assert.Equal(t, r.env.scopeID, fs.ScopeID)

		features, err := r.env.store.Features().ListCurrentByFeatureSet(ctx, fs.ID)
		require.NoError(t, err)
		for _, f := range features {
			assert.Equal(t, r.env.scopeID, f.ScopeID)
		}

		decisions, err := r.env.store.Decisions().ListCurrentByFeatureSet(ctx, fs.ID)
		require.NoError(t, err)
		for _, d := range decisions {
			assert.Equal(t, r.env.scopeID, d.ScopeID)
		}
	}

	require.NotEmpty(t, r.milestoneByID)
	for _, m := range r.milestoneByID {
		assert.Equal(t, r.env.scopeID, m.ScopeID)
		assocs, err := r.env.store.Milestones().ListAssociationsByMilestone(ctx, m.ID)
		require.NoError(t, err)
		for _, a := range assocs {
			assert.Equal(t, r.env.scopeID, a.ScopeID)
		}
	}
}

// TestFR11_WhagentNetImport_RecordsImportCompletion_SecondRunRefuses is
// item 8: a full whagent_net import records an import_completion row
// (#2548) naming the product, path, and revision, and a second Import
// against the same path and scope refuses with importer.ErrAlreadyImported
// rather than re-running.
func TestFR11_WhagentNetImport_RecordsImportCompletion_SecondRunRefuses(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := whagentNetDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root, testSourceRevision)
	require.NoError(t, err)

	completions, err := env.store.ImportCompletions().ListByScope(ctx, env.scopeID)
	require.NoError(t, err)
	require.Len(t, completions, 1)
	assert.Equal(t, report.ProductID, completions[0].ProductID)
	assert.Equal(t, root, completions[0].SourcePath)
	assert.Equal(t, testSourceRevision, completions[0].SourceRevision)

	_, err = importer.Import(ctx, env.store, env.sessions, env.sessionID, root, testSourceRevision)
	require.Error(t, err, "a second Import for whagent_net's already-imported path must refuse, not silently re-run")
	assert.ErrorIs(t, err, importer.ErrAlreadyImported)

	completionsAfter, err := env.store.ImportCompletions().ListByScope(ctx, env.scopeID)
	require.NoError(t, err)
	assert.Len(t, completionsAfter, 1, "a refused second import must not insert a second import_completion row")
}
