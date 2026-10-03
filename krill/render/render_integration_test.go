//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and krill/slice/query_integration_test.go
// for the pattern this file follows: spin up a throwaway Postgres via
// dbtest, apply krill's own real embedded migrations
// (//krill/migrate/schema), seed a full product graph -- FeatureSet,
// Features, Decisions, a Persona, both NonGoal kinds, and a milestone with
// associations -- with krill/store's real Create methods, then render it
// through render.NewStoreSource (store_source.go), the one file in this
// package that ever imports krill/store directly.
//
// This is issue #2495's Testing section's real-Postgres half:
//   - rendering a seeded product produces the four-file layout end to end
//     through StoreSource + slice.Querier + MilestoneStore, not just
//     against a hand-built fakeSource (render_test.go's job);
//   - FR15's read-only-role proof: Render succeeds against a Postgres
//     connection pool whose every session has had write privileges
//     revoked via default_transaction_read_only, empirically proving the
//     render path issues no writes -- not merely inspecting render.go's
//     import graph.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/render:render_integration_test --test_output=all
package render_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newTestStore mirrors krill/slice/query_integration_test.go's helper of
// the same name: an isolated, migrated Postgres database seeded with
// krill's own real embedded schema, plus the *pgxpool.Pool underneath it
// for the one raw INSERT (scope) that has no store method of its own.
func newTestStore(t *testing.T) (*store.Store, *pgxpool.Pool, string) {
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

	return store.New(pool), pool, db.ConnString
}

func createScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

func strPtr2(s string) *string { return &s }

// seededProduct is a full product graph seeded via real store.Store Create
// calls: one FeatureSet with two Features, two LoadBearingDecisions, one
// Persona, one NonGoal of each kind, and a milestone (M1) that delivers the
// first Feature and must-not-foreclose the first Decision -- enough to
// exercise every renderer section against real rows.
type seededProduct struct {
	ScopeID   uuid.UUID
	ProductID uuid.UUID
}

func seedProduct(t *testing.T, ctx context.Context, entities *store.Store, scopeID uuid.UUID) seededProduct {
	t.Helper()

	product, err := entities.Products().Create(ctx, scopeID, "Widgets", "Make great widgets.")
	require.NoError(t, err)

	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "Core", nil)
	require.NoError(t, err)

	f1, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	_, err = entities.Features().Create(ctx, scopeID, featureSet.ID, "F2", nil)
	require.NoError(t, err)

	// Real Requirements against F1, with the kind of body krill actually
	// holds -- a prohibition and a refuted-hypothesis record. A summary of
	// either would reproduce the problem rendering Requirements exists to
	// fix, so the integration path asserts the full text survives.
	_, err = entities.Requirements().Create(ctx, scopeID, f1.ID, store.RequirementKindFR, "Render the brief", strPtr2("A rendered brief must not drop a prohibition.\n\n- never hand-edit a generated file\n- refuted hypothesis: reconciling a hand edit is not possible here"))
	require.NoError(t, err)
	_, err = entities.Requirements().Create(ctx, scopeID, f1.ID, store.RequirementKindNFR, "Stay one-way", nil)
	require.NoError(t, err)

	// A decision whose body cross-references the note recorded below --
	// the shape whagent_net's three LoadBearingDecisions have, and the
	// dangling-reference defect this milepebble's guard test exists for.
	lb1, err := entities.Decisions().Create(ctx, scopeID, featureSet.ID, "Keep it simple", strPtr2("Simplicity beats cleverness. See the mapping note on this Product."))
	require.NoError(t, err)
	_, err = entities.Decisions().Create(ctx, scopeID, featureSet.ID, "Ship fast", strPtr2("velocity over polish"))
	require.NoError(t, err)

	_, err = entities.Personas().Create(ctx, scopeID, product.ID, "Operator", strPtr2("runs the fleet"))
	require.NoError(t, err)

	_, err = entities.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "Rendering other domains' docs", nil)
	require.NoError(t, err)
	_, err = entities.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindDeferred, "Multi-tenant scopes", strPtr2("later, not now"))
	require.NoError(t, err)

	milestone, err := entities.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, "M1")
	require.NoError(t, err)
	require.NoError(t, entities.Milestones().AddAssociation(ctx, scopeID, f1.ID, milestone.ID))
	require.NoError(t, entities.Milestones().AddAssociation(ctx, scopeID, lb1.ID, milestone.ID))

	// A second milestone with real status history, and M1 deliberately
	// left untransitioned, so the rendered roadmap covers both the
	// recorded-status and the derived-"not started" paths end to end
	// through the real milestone_status_event table.
	self := store.Subject{Iss: "https://issuer.example.com", Sub: "render-test", Kind: store.SubjectKindService}
	productNoteKind := store.NoteEntityKindProduct
	m2, err := entities.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, "M2")
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, m2.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, m2.ID, store.MilestoneStatusShipped, nil, self, self)
	require.NoError(t, err)

	// A real note against the Product, of the kind entity bodies point at
	// ("see the mapping note on this Product").
	_, err = entities.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID:    scopeID,
		EntityKind: &productNoteKind,
		EntityID:   &product.ID,
		Kind:       store.NoteKindComment,
		Body:       "CAPABILITY RENUMBERING.\n\n  krill C1  = brief C1  (/wai)   Now\n  krill C5  = brief C12 (/wpoll) Now",
		Acting:     self,
		OnBehalfOf: self,
	})
	require.NoError(t, err)

	return seededProduct{ScopeID: scopeID, ProductID: product.ID}
}

// TestRender_SeededProduct_ProducesFourFileLayout is #2495's "rendering a
// seeded product produces exactly the four files in the specified layout"
// case, exercised through the real store path (render.NewStoreSource),
// including the milestone's Delivers/Must-not-foreclose reconstruction
// from real entity_milestone rows.
func TestRender_SeededProduct_ProducesFourFileLayout(t *testing.T) {
	ctx := context.Background()
	entities, pool, _ := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/render-fr13-test")
	seeded := seedProduct(t, ctx, entities, scopeID)

	files, err := render.Render(ctx, render.NewStoreSource(entities), seeded.ScopeID, seeded.ProductID, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.ProductMD, "# Widgets — Product brief")
	assert.Contains(t, files.ProductMD, "## Vision\n\nMake great widgets.")
	assert.Contains(t, files.ProductMD, "- **Operator** — runs the fleet")
	assert.Contains(t, files.ProductMD, "LB1 — Keep it simple")
	assert.Contains(t, files.ProductMD, "LB2 — Ship fast")
	assert.Contains(t, files.ProductMD, "- **Rendering other domains' docs.**")
	assert.Contains(t, files.ProductMD, "- **Multi-tenant scopes.** later, not now")

	assert.Contains(t, files.ProductMD, "## Notes")
	assert.Contains(t, files.ProductMD, "CAPABILITY RENUMBERING.",
		"a Product note's body must reach the rendered brief verbatim")
	assert.Contains(t, files.ProductMD, "krill C5  = brief C12 (/wpoll) Now",
		"a multi-line body must survive intact")

	assert.Contains(t, files.CapabilityMapMD, "## Core")
	// F1 carries Requirements and so renders as a heading with them
	// beneath; F2 does not and keeps the compact one-line form.
	assert.Contains(t, files.CapabilityMapMD, "### C1 — F1")
	assert.Contains(t, files.CapabilityMapMD, "**FR1** — Render the brief")
	assert.Contains(t, files.CapabilityMapMD, "never hand-edit a generated file",
		"a Requirement body must reach the rendered map in full")
	assert.Contains(t, files.CapabilityMapMD,
		"refuted hypothesis: reconciling a hand edit is not possible here")
	assert.Contains(t, files.CapabilityMapMD, "**NFR1** — Stay one-way")
	assert.Contains(t, files.CapabilityMapMD, "_No body recorded._")
	assert.Contains(t, files.CapabilityMapMD, "- **C2** — F2")

	assert.Contains(t, files.RoadmapMD, "### M1")
	assert.Contains(t, files.RoadmapMD, "Delivers: C1")
	assert.Contains(t, files.RoadmapMD, "Must not foreclose: LB1")

	// M1 has no milestone_status_event row at all; M2 shipped. Both must
	// render an honest current status, the first from the absence of
	// history rather than from a default the renderer invented.
	assert.Contains(t, files.RoadmapMD, "### M1\n\n- Status: not started")
	assert.Contains(t, files.RoadmapMD, "### M2\n\n- Status: shipped")

	for name, content := range files.FileMap() {
		assert.Contains(t, content, render.GeneratedMarker, "file %s must carry the non-hand-editable marker", name)
		assert.Contains(t, content, `Product "Widgets"`, "file %s must name the product it was rendered from", name)
	}

	// The dangling-reference guard, against a realistic product slice
	// built through the real store: LB1's body points at the note, and the
	// note really is rendered, so the reference resolves. The unit half
	// (dangling_reference_test.go) proves the guard fails when it does not.
	assert.Contains(t, files.ProductMD, "See the mapping note on this Product")
	assertNoDanglingReferences(t, files)
}

// TestRender_ReadOnlyDatabaseHandle_Succeeds is FR15's proof: Render
// succeeds against a Postgres connection pool whose every session has had
// default_transaction_read_only forced on -- i.e. any write the render
// path attempted would itself fail at the database, not merely "happen not
// to be called" this run. The second assertion (attempting a real INSERT
// through the same pool) is what makes this an empirical proof rather than
// a trivial one: it confirms the harness genuinely blocks writes, so
// Render's success above cannot be explained by AfterConnect silently
// failing to take effect.
func TestRender_ReadOnlyDatabaseHandle_Succeeds(t *testing.T) {
	ctx := context.Background()
	entities, pool, connString := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/render-fr15-test")
	seeded := seedProduct(t, ctx, entities, scopeID)

	roCfg, err := pgxpool.ParseConfig(connString)
	require.NoError(t, err)
	roCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET default_transaction_read_only = on")
		return err
	}
	roPool, err := pgxpool.NewWithConfig(ctx, roCfg)
	require.NoError(t, err)
	t.Cleanup(roPool.Close)

	// Prove the harness itself: this pool must reject a write. If this
	// assertion ever went green on a pool that in fact still permitted
	// writes, the success assertion below would prove nothing.
	_, writeErr := roPool.Exec(ctx, `INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2)`, "whale-net/should-never-be-written", "main")
	require.Error(t, writeErr, "the read-only pool must reject a write")
	assert.Contains(t, writeErr.Error(), "read-only transaction")

	roStore := store.New(roPool)
	files, err := render.Render(ctx, render.NewStoreSource(roStore), seeded.ScopeID, seeded.ProductID, render.WithDetail())
	require.NoError(t, err, "Render must succeed reading through a connection that cannot write")
	assert.Contains(t, files.ProductMD, "# Widgets — Product brief")
}

// TestRender_AmendedDeferralRendersOnce is migration 024's renderer
// regression: a deferral whose text has been amended by supersession must
// still render as exactly one "Deliberately deferred:" line carrying the
// CURRENT text.
//
// This is the only place the whole path is exercised end to end --
// AmendDeferral closing the original row, StoreSource.ListMilestoneDeferrals
// delegating to MilestoneAuthoringStore.ListDeferrals, the `valid_to IS
// NULL` filter that store method carries, and renderMilestones emitting one
// line per row it is handed. A filter missing anywhere in that chain shows
// up here as a doubled line, which no fakeSource-driven test can catch: the
// fake is handed whatever the test tells it to hand over, so it proves
// nothing about what the store returns.
func TestRender_AmendedDeferralRendersOnce(t *testing.T) {
	ctx := context.Background()
	entities, pool, _ := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/render-deferral-amend-test")
	seeded := seedProduct(t, ctx, entities, scopeID)

	subject := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}
	milestone, err := entities.Milestones().GetOrCreateRef(ctx, scopeID, seeded.ProductID, "M1")
	require.NoError(t, err)

	stale, err := entities.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "C4 ships in a later milestone", "M2", nil, subject, subject)
	require.NoError(t, err)

	// The stale text a renumbering leaves behind: it cites a capability
	// number that no longer means what it meant, which is the case this
	// whole verb exists to correct.
	_, err = entities.Amend().AmendDeferral(ctx, stale.ID, "C5 ships in a later milestone", "M2", nil)
	require.NoError(t, err)

	files, err := render.Render(ctx, render.NewStoreSource(entities), seeded.ScopeID, seeded.ProductID, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "Deliberately deferred: C5 ships in a later milestone (→ M2)",
		"the amended text must be what renders")
	assert.NotContains(t, files.RoadmapMD, "C4 ships in a later milestone",
		"the superseded revision must not render")
	assert.Equal(t, 1, strings.Count(files.RoadmapMD, "Deliberately deferred:"),
		"exactly one deferred line -- a second would mean the store handed the renderer both revisions")

	// Two successive amends: the read path must still narrow to one row, so
	// this pins "one line" as a property of the filter rather than of there
	// happening to be only one closed row.
	_, err = entities.Amend().AmendDeferral(ctx, stale.ID, "C5 ships in M2, which now also carries C6", "M2", nil)
	require.NoError(t, err)

	files, err = render.Render(ctx, render.NewStoreSource(entities), seeded.ScopeID, seeded.ProductID, render.WithDetail())
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(files.RoadmapMD, "Deliberately deferred:"),
		"still exactly one deferred line after two amendments")
	assert.Contains(t, files.RoadmapMD, "Deliberately deferred: C5 ships in M2, which now also carries C6 (→ M2)")
}

// A stored survey renders verbatim (>= 100 KB, markdown-hostile content
// untouched); with none stored the fixed placeholder remains.
func TestRender_StoredCurrentState_VerbatimAndPlaceholderFallback(t *testing.T) {
	ctx := context.Background()
	entities, pool, _ := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/render-current-state-test")
	product, err := entities.Products().Create(ctx, scopeID, "Widgets", "Make great widgets.")
	require.NoError(t, err)

	files, err := render.Render(ctx, render.NewStoreSource(entities), scopeID, product.ID)
	require.NoError(t, err)
	assert.Contains(t, files.CurrentStateMD, "This section is intentionally not rendered.")

	survey := "# Survey\n\n<!-- raw -->\n| a | b |\n```\n*unescaped* `<tag>` \\ & \"q\"\n" +
		strings.Repeat("0123456789abcdef\n", 7000) + "END-MARKER\n"
	require.Greater(t, len(survey), 100*1024)
	_, err = entities.Amend().SetProductCurrentState(ctx, product.ID, survey)
	require.NoError(t, err)

	files, err = render.Render(ctx, render.NewStoreSource(entities), scopeID, product.ID)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(files.CurrentStateMD, "\n# Current state\n\nPart of the [Widgets product brief](../PRODUCT.md).\n\n"+survey))
	assert.NotContains(t, files.CurrentStateMD, "This section is intentionally not rendered.")
}
