//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. It proves FR18/FR19 (issue #2497): krill self-hosts by importing its
// **own** committed brief (`krill/PRODUCT.md` + `krill/product/*.md`, the
// real files on `main` -- staged in as Bazel `data`, never a fixture copy)
// through #2492's importer, then rendering the imported Product back
// through #2495's renderer, and proving every entity FR16's report says it
// created is present in what comes back out -- keyed by entity id against
// the report, never by diffing rendered text against the original
// document (the roadmap's own success condition explicitly rejects
// byte-equivalence: capability numbering in krill's own capability map is
// allocated, not sequential -- see product/02-capability-map.md's own
// "Numbering is by allocation, not by bucket" note -- so the renderer's
// FR14 position-derived Cn citations are *expected* to renumber some
// capabilities on the way out; LBn citations are not, because krill's own
// seven Load-bearing decisions have no such gaps).
//
// Only krill's own brief is imported here. Importing `whagent_net` is
// C27/M2 and explicitly out of scope for this task -- this file never
// stages or reads whagent_net's docs at all.
//
// See Testing.md-equivalent, issue #2497's Testing section:
//   - the round-trip completeness proof
//     (TestRoundTrip_KrillOwnBrief_EveryFR16EntityPresentInRenderedOutput);
//   - the stability regression -- rendering twice from the same imported
//     state produces identical output
//     (TestRoundTrip_RenderTwice_ProducesIdenticalOutput);
//   - a gated, `bazel run`-only test that (re)generates the checked-in
//     FR16 report artifact under testdata/ (TestGenerateFR16ReportArtifact).
//
// Run the round-trip tests explicitly (requires a working Docker daemon):
//
//	bazel test //krill/conformance:roundtrip_integration_test --test_output=all
package conformance

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/importer"
	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// testEnv mirrors krill/importer/importer_integration_test.go's helper of
// the same name: a migrated, throwaway Postgres database plus a minted
// krill session (FR3), ready to pass to importer.Import.
type testEnv struct {
	store     *store.Store
	sessions  store.SessionStore
	sessionID uuid.UUID
	scopeID   uuid.UUID
	pool      *pgxpool.Pool
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	pool, err := pgxpool.New(ctx, db.ConnString)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, "whale-net/everything", "main").Scan(&scopeID))

	sessions := store.NewSessionStore(pool)
	self := store.Subject{Iss: "https://issuer.example.com", Sub: "roundtrip-conformance-test", Kind: store.SubjectKindService}
	sessID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	return testEnv{
		store:     store.New(pool),
		sessions:  sessions,
		sessionID: uuid.UUID(sessID),
		scopeID:   scopeID,
		pool:      pool,
	}
}

// krillDocsRoot locates krill's own real, committed doc set through Bazel
// runfiles (data deps //krill:docs and //krill/product:docs on this
// test's go_test target) -- the actual files on `main`, not a fixture.
// Mirrors krill/importer/importer_integration_test.go's use of
// whagent_net's docs as a read-only parser fixture, pointed at krill's own
// brief instead.
func krillDocsRoot(t *testing.T) string {
	t.Helper()
	productMD, err := runfiles.Rlocation("_main/krill/PRODUCT.md")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(krill/PRODUCT.md): %v (is //krill:docs still a data dep of this test target?)", err)
	}
	return filepath.Dir(productMD)
}

// numberByOrder assigns a 1-based display number to every element of
// entities in the order the slice already arrives in, mirroring
// krill/render's own unexported numberByOrder (render.go). This is a
// deliberately independent re-implementation -- not a call into
// krill/render's internals -- so this test proves the renderer's actual
// numbering by recomputing it from the same ordered read the renderer
// itself uses (slice.Querier.GetProductSlice), rather than trusting the
// renderer's own arithmetic.
func numberByOrder[T any](entities []T, idOf func(T) uuid.UUID) map[uuid.UUID]int {
	numbers := make(map[uuid.UUID]int, len(entities))
	for i, e := range entities {
		numbers[idOf(e)] = i + 1
	}
	return numbers
}

func joinPrefixed(prefix string, nums []int) string {
	out := ""
	for i, n := range nums {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s%d", prefix, n)
	}
	return out
}

// leadingLBLabelRe mirrors render.go's own regexp of the same purpose:
// stripping a stored LoadBearingDecision.Name's embedded "LB<n> — " prefix
// so this test can predict the *current* rendered title independently of
// render.go's private helper.
var leadingLBLabelRe = regexp.MustCompile(`^LB\d+\s*[—–-]\s*`)

// TestRoundTrip_KrillOwnBrief_EveryFR16EntityPresentInRenderedOutput is
// FR18's core claim: import krill's own committed brief, render the
// imported Product back out, and prove every entity the FR16 report says
// the importer created is present in the rendered output -- resolved by
// entity id against the same read paths krill/render's Source interface
// itself calls (ListPersonas, ListNonGoals, GetProductSlice,
// ListMilestoneRefs/ListMilestoneAssociations), never by diffing the
// rendered text against the original markdown.
func TestRoundTrip_KrillOwnBrief_EveryFR16EntityPresentInRenderedOutput(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := krillDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root)
	require.NoError(t, err, "importing krill's own committed brief must succeed")
	require.Equal(t, "krill", report.ProductName, "must import krill's own brief, never a different domain's (whagent_net is explicitly out of scope -- C27/M2)")
	require.NotEmpty(t, report.Entries, "expected krill's own brief to produce at least one entity of each kind")

	products, err := env.store.Products().ListCurrentByScope(ctx, env.scopeID)
	require.NoError(t, err)
	require.Len(t, products, 1, "this scope must hold exactly the one product just imported -- no whagent_net or other product snuck in")
	assert.Equal(t, "krill", products[0].Name)

	files, err := render.Render(ctx, render.NewStoreSource(env.store), env.scopeID, report.ProductID)
	require.NoError(t, err, "rendering the freshly imported product must succeed")

	// Build lookup tables from exactly the read paths render.Source
	// exposes (see krill/render/render.go's Source interface) -- so a
	// failure here is a genuine completeness gap in what feeds the
	// renderer, not an artifact of a second, differently-shaped query.
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
	featureNumbers := numberByOrder(doc.Features, func(f slice.FeatureEntity) uuid.UUID { return f.ID })
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
			require.True(t, ok, "persona %q (entity %s) is in the FR16 report but missing from ListCurrentByProduct -- the read render.Source itself calls", e.SourceID, e.EntityID)
			assert.Equal(t, e.Name, p.Name, "persona %q's name must survive the round trip unchanged", e.SourceID)
			assert.Contains(t, files.ProductMD, "**"+p.Name+"**", "persona %q must appear in rendered PRODUCT.md", e.SourceID)

		case "non_goal":
			ng, ok := nonGoalByID[e.EntityID]
			require.True(t, ok, "non-goal %q (entity %s) is in the FR16 report but missing from ListCurrentByProduct", e.SourceID, e.EntityID)
			assert.Equal(t, e.Name, ng.Name, "non-goal %q's name must survive the round trip unchanged", e.SourceID)
			assert.Contains(t, files.ProductMD, "**"+ng.Name+".**", "non-goal %q must appear in rendered PRODUCT.md", e.SourceID)

		case "decision":
			d, ok := decisionByID[e.EntityID]
			require.True(t, ok, "decision %s (entity %s) is in the FR16 report but missing from GetProductSlice's Decisions -- the read render.Source itself calls", e.SourceID, e.EntityID)
			num, ok := decisionNumbers[e.EntityID]
			require.True(t, ok, "decision %s: expected a computed render position", e.SourceID)
			gotSourceID := fmt.Sprintf("LB%d", num)
			// krill's own seven Load-bearing decisions are numbered
			// sequentially with no gaps (unlike the capability map --
			// see this file's package doc), so the renderer's
			// position-derived citation is expected to reproduce the
			// same "LBn" the source document used.
			assert.Equal(t, e.SourceID, gotSourceID, "decision entity %s: imported as %s but the renderer's own position-derived numbering would call it %s -- citation drifted across the round trip", e.EntityID, e.SourceID, gotSourceID)
			assert.Equal(t, e.Name, d.Name, "decision %s's title must survive the round trip unchanged", e.SourceID)
			title := leadingLBLabelRe.ReplaceAllString(e.Name, "")
			assert.Contains(t, files.ProductMD, fmt.Sprintf("LB%d — %s\n", num, title), "decision %s must render at its round-tripped number with its title preserved", e.SourceID)

		case "capability":
			f, ok := featureByID[e.EntityID]
			require.True(t, ok, "capability %s (entity %s) is in the FR16 report but missing from GetProductSlice's Features -- the read render.Source itself calls", e.SourceID, e.EntityID)
			assert.Equal(t, e.Name, f.Name, "capability %s's description must survive the round trip unchanged", e.SourceID)
			// Deliberately not asserting e.SourceID's numeral matches the
			// renderer's computed number here: krill's own capability map
			// numbers "by allocation, not by bucket" (round 2/3 entries
			// C25-C28 appended out of sequence), while FR14 always
			// computes a citation from current sibling position. A
			// renumbering here is the *expected*, documented divergence
			// FR19's semantic-equivalence review must record, not a
			// round-trip defect -- see this file's package doc.
			_, ok = featureNumbers[e.EntityID]
			require.True(t, ok, "capability %s: expected a computed render position", e.SourceID)
			assert.Contains(t, files.CapabilityMapMD, "— "+f.Name, "capability %s's description must appear in the rendered capability map", e.SourceID)

		case "milestone":
			ref, ok := milestoneByID[e.EntityID]
			require.True(t, ok, "milestone %s (entity %s) is in the FR16 report but missing from ListRefsByProduct -- the read render.Source itself calls", e.SourceID, e.EntityID)
			assert.Equal(t, e.SourceID, ref.Name, "milestone identifier must survive the round trip unchanged")
			assert.Contains(t, files.RoadmapMD, "### "+ref.Name, "milestone %s must appear in the rendered roadmap", e.SourceID)

		default:
			t.Fatalf("unhandled FR16 report entry kind %q for %s -- add a case above so this test actually covers it", e.Kind, e.SourceID)
		}
	}

	// Sanity: every kind FR16 documents (report.go's ReportEntry doc
	// comment) actually showed up at least once for krill's own brief --
	// otherwise the switch above could pass by vacuously covering zero
	// entries of some kind.
	for _, kind := range []string{"persona", "non_goal", "decision", "capability", "milestone"} {
		assert.Positive(t, seenKinds[kind], "expected krill's own brief to import at least one %q entity", kind)
	}

	// Delivers:/Must not foreclose: lists reconstructed from association
	// rows (LB6), never carried over as prose from the imported document.
	// Prove this per milestone by recomputing the expected token list
	// straight from entity_milestone rows plus the same freshly-computed
	// citation numbers used above, then asserting the *rendered* roadmap
	// carries exactly that -- an independent recomputation from entity
	// ids, not a comparison against the original roadmap's own text.
	for _, ref := range milestoneRefs {
		assocs, err := env.store.Milestones().ListAssociationsByMilestone(ctx, ref.ID)
		require.NoError(t, err)
		require.NotEmpty(t, assocs, "milestone %s: expected at least one association row (krill's own roadmap gives every milestone a Delivers and/or Must not foreclose line)", ref.Name)

		var delivers, mustNot []int
		for _, a := range assocs {
			if n, ok := featureNumbers[a.EntityID]; ok {
				delivers = append(delivers, n)
				continue
			}
			if n, ok := decisionNumbers[a.EntityID]; ok {
				mustNot = append(mustNot, n)
				continue
			}
			t.Fatalf("milestone %s: association entity %s is neither a known Feature nor a known LoadBearingDecision", ref.Name, a.EntityID)
		}
		sort.Ints(delivers)
		sort.Ints(mustNot)

		if len(delivers) > 0 {
			want := "Delivers: " + joinPrefixed("C", delivers)
			assert.Contains(t, files.RoadmapMD, want, "milestone %s's Delivers list must be reconstructed from entity_milestone rows", ref.Name)
		}
		if len(mustNot) > 0 {
			want := "Must not foreclose: " + joinPrefixed("LB", mustNot)
			assert.Contains(t, files.RoadmapMD, want, "milestone %s's Must not foreclose list must be reconstructed from entity_milestone rows", ref.Name)
		}
	}
}

// timestampInHeaderRe isolates the one part of a rendered file's
// provenance header (render.go's header()) that is expected to differ
// between two Render calls made moments apart: the "at <RFC3339
// timestamp>" clause. Everything else -- including the revision id, which
// does not change between two reads with no write in between -- must be
// byte-identical.
var timestampInHeaderRe = regexp.MustCompile(`, at [0-9TZ:+-]+\.`)

func normalizeTimestamp(s string) string {
	return timestampInHeaderRe.ReplaceAllString(s, ", at <normalized>.")
}

// TestRoundTrip_RenderTwice_ProducesIdenticalOutput is the Testing
// section's stability regression: rendering twice from the same imported
// state (no write in between) must produce identical output, not merely
// "close enough" output -- a renderer whose output depended on read
// ordering, map iteration, or anything else nondeterministic would
// silently break FR19's "regenerate and diff" workflow.
func TestRoundTrip_RenderTwice_ProducesIdenticalOutput(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := krillDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root)
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

// TestGenerateFR16ReportArtifact (re)generates the checked-in FR16
// entity-id report (testdata/fr16_report.txt) so the mapping from
// krill's own source sections to the entity ids the importer minted is
// human-reviewable without spinning up Postgres. It is gated behind
// KRILL_CAPTURE_REPORT=1 and requires BUILD_WORKSPACE_DIRECTORY (i.e. it
// only does anything under `bazel run`, never `bazel test` or CI) because
// it writes into the real checkout -- mirroring
// tools/appmeta/rule_to_proto_test.go's BUILD_WORKSPACE_DIRECTORY use for
// the same reason (a sandboxed test action has no path back to the source
// tree; `bazel run` is the one invocation that does).
//
// Regenerate with:
//
//	bazel run //krill/conformance:roundtrip_integration_test \
//	  --test_env=KRILL_CAPTURE_REPORT=1 --test_filter=TestGenerateFR16ReportArtifact
//
// The entity ids captured are real, randomly-generated surrogate keys
// from whichever run produced them -- this artifact is a point-in-time
// snapshot for review, not a golden file the automated round-trip tests
// above compare against (they compute their own report fresh every run,
// exactly because those ids are not stable across runs).
func TestGenerateFR16ReportArtifact(t *testing.T) {
	if os.Getenv("KRILL_CAPTURE_REPORT") != "1" {
		t.Skip("set KRILL_CAPTURE_REPORT=1 and run via `bazel run //krill/conformance:roundtrip_integration_test --test_env=KRILL_CAPTURE_REPORT=1 --test_filter=TestGenerateFR16ReportArtifact` to (re)generate testdata/fr16_report.txt")
	}
	workspaceRoot := os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	require.NotEmpty(t, workspaceRoot, "this test writes into the real checkout and only works under `bazel run`, not `bazel test` (BUILD_WORKSPACE_DIRECTORY is unset)")

	ctx := context.Background()
	env := newTestEnv(t)
	root := krillDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root)
	require.NoError(t, err)

	out := filepath.Join(workspaceRoot, "krill", "conformance", "testdata", "fr16_report.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(out), 0o755))
	require.NoError(t, os.WriteFile(out, []byte(report.Render()), 0o644))
	t.Logf("wrote %s", out)
}
