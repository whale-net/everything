//go:build integration

// This file shares whagent_net_import_integration_test.go's target and
// testEnv/whagentNetDocsRoot helpers (see this package's BUILD.bazel). It
// is M2's C27/FR12 second-clause proof (issue #2550): rendering
// whagent_net's just-imported Product back through //krill/render must
// account for every FR11-reported entity, exactly the way
// roundtrip_integration_test.go proves it for krill's own brief -- and
// rendering twice from the same imported state must be stable.
//
// Unlike krill's own round trip, whagent_net's capability numbering is
// **allocated, not sequential** in its own committed doc (see
// whagent_net/product/02-capability-map.md's "New capabilities are
// appended to Later with the next free Cn ... never renumbered" and this
// package's parse test's own "C1-C27, allocated not sequential -- C27
// lands in Next" comment) -- so this file, like
// roundtrip_integration_test.go's own capability case, does not assert
// that a rendered capability's citation reproduces its source `Cn`. It
// goes one step further than krill's own test, though, because the gap
// here is not a same-bucket allocation skip (krill's own C25-C28 append
// order) but a **cross-bucket** one: C27 sits inside the "Next" bucket
// after C13-C18, ahead of "Later"'s C19-C26. Render's FR14 position
// numbering is computed per FeatureSet-then-feature order (krill/store's
// ORDER BY feature_set.position, feature.position -- see
// krill/render/render.go's numberByOrder doc), so this reorders every one
// of C19-C27, not just C27 itself -- TestRoundTrip_WhagentNetBrief_
// CapabilityNumberingDrift below proves the exact drift so it is a
// documented, empirically-verified fact (see this task's scope note) and
// not a guess.
package conformance

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/importer"
	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// TestRoundTrip_WhagentNetBrief_EveryFR16EntityPresentInRenderedOutput
// mirrors roundtrip_integration_test.go's krill-own-brief test, but for
// whagent_net: import whagent_net's real committed brief, render the
// imported Product back out, and prove every FR11-reported entity is
// present in the rendered output -- resolved by entity id against the
// same read paths render.Source itself calls, never by diffing rendered
// text against the original document.
func TestRoundTrip_WhagentNetBrief_EveryFR16EntityPresentInRenderedOutput(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := whagentNetDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root, testSourceRevision)
	require.NoError(t, err, "importing whagent_net's own committed brief must succeed")
	require.Equal(t, "whagent-net", report.ProductName)
	require.NotEmpty(t, report.Entries)

	files, err := render.Render(ctx, render.NewStoreSource(env.store), env.scopeID, report.ProductID)
	require.NoError(t, err, "rendering the freshly imported whagent_net product must succeed")

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
	decisionNumbers := numberByOrder(doc.Decisions, func(d slice.DecisionEntity) uuid.UUID { return d.ID })

	milestoneRefs, err := env.store.Milestones().ListRefsByProduct(ctx, env.scopeID, report.ProductID)
	require.NoError(t, err)
	milestoneByID := make(map[uuid.UUID]store.MilestoneRef, len(milestoneRefs))
	for _, m := range milestoneRefs {
		milestoneByID[m.ID] = m
	}

	seenKinds := map[string]int{}
	for _, e := range report.Entries {
		seenKinds[e.Kind]++
		switch e.Kind {
		case "persona":
			p, ok := personaByID[e.EntityID]
			require.True(t, ok, "persona %q (entity %s) is in the FR11 report but missing from ListCurrentByProduct", e.SourceID, e.EntityID)
			assert.Equal(t, e.Name, p.Name)
			assert.Contains(t, files.ProductMD, "**"+p.Name+"**", "persona %q must appear in rendered PRODUCT.md", e.SourceID)

		case "non_goal":
			ng, ok := nonGoalByID[e.EntityID]
			require.True(t, ok, "non-goal %q (entity %s) is in the FR11 report but missing from ListCurrentByProduct", e.SourceID, e.EntityID)
			assert.Equal(t, e.Name, ng.Name)
			assert.Contains(t, files.ProductMD, "**"+ng.Name+".**", "non-goal %q must appear in rendered PRODUCT.md", e.SourceID)

		case "decision":
			d, ok := decisionByID[e.EntityID]
			require.True(t, ok, "decision %s (entity %s) is in the FR11 report but missing from GetProductSlice's Decisions", e.SourceID, e.EntityID)
			num, ok := decisionNumbers[e.EntityID]
			require.True(t, ok, "decision %s: expected a computed render position", e.SourceID)
			// whagent_net's seven Load-bearing decisions are numbered
			// sequentially with no gaps (LB1-LB7, confirmed by this
			// package's parse test), so -- unlike capabilities below --
			// the renderer's position-derived citation is expected to
			// reproduce the source's own "LBn" exactly (this task's
			// "confirm the rendered numbers match the source's" check).
			gotSourceID := "LB" + strconv.Itoa(num)
			assert.Equal(t, e.SourceID, gotSourceID, "decision entity %s: imported as %s but the renderer's own position-derived numbering would call it %s -- citation drifted across the round trip", e.EntityID, e.SourceID, gotSourceID)
			assert.Equal(t, e.Name, d.Name)

		case "capability":
			f, ok := featureByID[e.EntityID]
			require.True(t, ok, "capability %s (entity %s) is in the FR11 report but missing from GetProductSlice's Features", e.SourceID, e.EntityID)
			assert.Equal(t, e.Name, f.Name, "capability %s's description must survive the round trip unchanged", e.SourceID)
			// Deliberately not asserting e.SourceID's numeral matches the
			// renderer's computed number -- see this file's package doc:
			// whagent_net's own capability map numbers by allocation, not
			// bucket order (C27 sits in "Next" ahead of "Later"'s
			// C19-C26), so a renumbering here is expected, not a defect.
			assert.Contains(t, files.CapabilityMapMD, "— "+f.Name, "capability %s's description must appear in the rendered capability map", e.SourceID)

		case "milestone":
			ref, ok := milestoneByID[e.EntityID]
			require.True(t, ok, "milestone %s (entity %s) is in the FR11 report but missing from ListRefsByProduct", e.SourceID, e.EntityID)
			assert.Equal(t, e.SourceID, ref.Name)
			assert.Contains(t, files.RoadmapMD, "### "+ref.Name, "milestone %s must appear in the rendered roadmap", e.SourceID)

		default:
			t.Fatalf("unhandled FR11 report entry kind %q for %s -- add a case above so this test actually covers it", e.Kind, e.SourceID)
		}
	}

	for _, kind := range []string{"persona", "non_goal", "decision", "capability", "milestone"} {
		assert.Positive(t, seenKinds[kind], "expected whagent_net's real brief to import at least one %q entity", kind)
	}
}

// TestRoundTrip_WhagentNetBrief_CapabilityNumberingDrift is the empirical
// proof behind this file's package doc and this task's scope note: C27
// (allocated out of bucket order -- it sits in "Next" after C13-C18,
// ahead of "Later"'s C19-C26) causes every one of C19-C27 to render under
// a different `Cn` than whagent_net's own committed capability map uses.
// This is not a defect in krill/render (FR14's position-derived numbering
// is by design, LB2: no stored display-number column) -- it is a
// structural mismatch between that design and whagent_net's own
// pre-existing "never renumbered" allocation convention, and it is
// exactly why this task's scope note exists rather than silently shipping
// a capability map whose Cn citations no longer match every other Cn
// citation in the repo (this plan's own issues included -- see #2550's
// own title, "C27's adoption proof").
func TestRoundTrip_WhagentNetBrief_CapabilityNumberingDrift(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := whagentNetDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root, testSourceRevision)
	require.NoError(t, err)

	doc, err := slice.NewQuerier(env.store).GetProductSlice(ctx, report.ProductID)
	require.NoError(t, err)
	featureNumbers := numberByOrder(doc.Features, func(f slice.FeatureEntity) uuid.UUID { return f.ID })

	sourceIDByEntity := map[uuid.UUID]string{}
	for _, e := range report.Entries {
		if e.Kind == "capability" {
			sourceIDByEntity[e.EntityID] = e.SourceID
		}
	}

	drifted := 0
	for id, num := range featureNumbers {
		sourceID, ok := sourceIDByEntity[id]
		require.True(t, ok, "feature %s has no corresponding capability report entry", id)
		gotSourceID := "C" + strconv.Itoa(num)
		if gotSourceID != sourceID {
			drifted++
			t.Logf("capability numbering drift: source %s renders as %s", sourceID, gotSourceID)
		}
	}
	// C27's out-of-bucket-order allocation shifts every one of C19-C27
	// (9 capabilities): C13-C18 stay put (positions 13-18 in both source
	// and render), but C27 renders as C19, and C19-C26 each shift up by
	// one to C20-C27.
	assert.Equal(t, 9, drifted, "expected exactly C19-C27 (9 capabilities) to drift; if this number changed, whagent_net's capability map's bucket layout changed and this task's scope note needs updating to match")
}

// TestRoundTrip_WhagentNetBrief_RenderTwice_ProducesIdenticalOutput mirrors
// roundtrip_integration_test.go's krill-own-brief stability regression:
// rendering twice from the same imported state (no write in between) must
// produce identical output modulo the header's own timestamp clause.
func TestRoundTrip_WhagentNetBrief_RenderTwice_ProducesIdenticalOutput(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := whagentNetDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root, testSourceRevision)
	require.NoError(t, err)

	src := render.NewStoreSource(env.store)
	first, err := render.Render(ctx, src, env.scopeID, report.ProductID)
	require.NoError(t, err)
	second, err := render.Render(ctx, src, env.scopeID, report.ProductID)
	require.NoError(t, err)

	firstMap, secondMap := first.FileMap(), second.FileMap()
	require.Equal(t, len(firstMap), len(secondMap))
	for name, want := range firstMap {
		got, ok := secondMap[name]
		require.True(t, ok, "second render dropped file %s", name)
		assert.Equal(t, normalizeTimestamp(want), normalizeTimestamp(got), "rendering twice from the same imported state must produce identical output for %s", name)
	}
}

// TestGenerateWhagentNetRenderedDocs (re)generates the actual committed
// whagent_net/PRODUCT.md + whagent_net/product/*.md from a fresh import of
// whagent_net's own current doc set, mirroring
// roundtrip_integration_test.go's TestGenerateFR16ReportArtifact pattern
// (gated, BUILD_WORKSPACE_DIRECTORY-only, writes into the real checkout).
// This is the actual cutover mechanism issue #2550 describes as
// `bazel run //krill/render/cmd:render -- --product whagent-net --out
// whagent_net/` against a Postgres holding the FR11 import -- run here
// against a throwaway Postgres instead of a long-lived one, since no
// persistent krill deployment exists yet; the point (per #2550's own
// Testing section) is that a *second* run of this same test against the
// resulting state is a no-op diff (modulo the header's timestamp).
//
// Regenerate with:
//
//	bazel run //krill/conformance:roundtrip_integration_test \
//	  --test_env=KRILL_RENDER_WHAGENT_NET=1 --test_filter=TestGenerateWhagentNetRenderedDocs
func TestGenerateWhagentNetRenderedDocs(t *testing.T) {
	if os.Getenv("KRILL_RENDER_WHAGENT_NET") != "1" {
		t.Skip("set KRILL_RENDER_WHAGENT_NET=1 and run via `bazel run //krill/conformance:roundtrip_integration_test --test_env=KRILL_RENDER_WHAGENT_NET=1 --test_filter=TestGenerateWhagentNetRenderedDocs` to (re)generate whagent_net/PRODUCT.md + whagent_net/product/*.md")
	}
	workspaceRoot := os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	require.NotEmpty(t, workspaceRoot, "this test writes into the real checkout and only works under `bazel run`, not `bazel test` (BUILD_WORKSPACE_DIRECTORY is unset)")

	sourceRevision := os.Getenv("KRILL_RENDER_WHAGENT_NET_SOURCE_REVISION")
	require.NotEmpty(t, sourceRevision, "set KRILL_RENDER_WHAGENT_NET_SOURCE_REVISION=<commit sha> to record which commit this cutover render was taken from (NFR3)")

	ctx := context.Background()
	env := newTestEnv(t)
	root := whagentNetDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root, sourceRevision)
	require.NoError(t, err)

	files, err := render.Render(ctx, render.NewStoreSource(env.store), env.scopeID, report.ProductID)
	require.NoError(t, err)

	out := filepath.Join(workspaceRoot, "whagent_net")
	for rel, content := range files.FileMap() {
		path := filepath.Join(out, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		t.Logf("wrote %s", path)
	}
}
