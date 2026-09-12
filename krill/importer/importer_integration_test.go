//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. It exercises Import (importer.go, write.go) end to end against
// krill's real embedded schema (//krill/migrate/schema), plus Parse alone
// against whagent_net's real doc set as a read-only parser fixture -- see
// issue #2492's Testing section:
//   - every section kind (capability, decision, persona, non-goal,
//     milestone) lands as an entity and appears in the report with its id;
//   - a `Must not foreclose: LB1, LB4` line produces two entity_milestone
//     rows, not prose;
//   - a document referencing an undefined milestone fails loudly, naming
//     it;
//   - importing twice does not duplicate milestone_ref rows for the same
//     `M<n>`;
//   - whagent_net's PRODUCT.md + product/*.md (the hardest shape in the
//     repo) parses cleanly as a read-only fixture -- parse-and-report only,
//     never imported (that is C27/M2 and out of scope here).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/importer:importer_integration_test --test_output=all
package importer_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/importer"
	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// testSourceRevision is the placeholder commit SHA every Import call in
// this file passes for its now-required sourceRevision argument (FR12,
// NFR3, issue #2548) -- this file's own tests are not about what value
// that argument carries, only that it is recorded and that a second
// Import for the same path refuses; see
// import_completion_integration_test.go and this file's own
// FR12-specific cases for coverage of the value itself.
const testSourceRevision = "test-fixture-revision"

// testEnv is a migrated Postgres database plus a minted krill session (FR3)
// ready to pass to importer.Import, mirroring
// krill/store/session_integration_test.go's newTestSessionStore.
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
	`, "whale-net/importer-test", "main").Scan(&scopeID))

	sessions := store.NewSessionStore(pool)
	self := store.Subject{Iss: "https://issuer.example.com", Sub: "importer-test", Kind: store.SubjectKindService}
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

// TestImport_EverySectionKind_LandsAsEntityAndAppearsInReport proves FR16's
// core claim against testdata/valid: a persona, two load-bearing decisions,
// a non-goal, a capability, and a milestone each become an entity and each
// appear in the report with the entity id they became -- not merely get
// written silently.
func TestImport_EverySectionKind_LandsAsEntityAndAppearsInReport(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, "testdata/valid", testSourceRevision)
	require.NoError(t, err)

	byKindAndSourceID := map[string]*importer.ReportEntry{}
	for i := range report.Entries {
		e := &report.Entries[i]
		byKindAndSourceID[e.Kind+":"+e.SourceID] = e
	}

	persona := byKindAndSourceID["persona:Operator"]
	require.NotNil(t, persona, "expected a persona entry for \"Operator\" in the report: %+v", report.Entries)
	assert.NotEqual(t, uuid.Nil, persona.EntityID)

	nonGoal := byKindAndSourceID["non_goal:Being a real product"]
	require.NotNil(t, nonGoal, "expected a non_goal entry for \"Being a real product\" in the report: %+v", report.Entries)
	assert.NotEqual(t, uuid.Nil, nonGoal.EntityID)

	lb1 := byKindAndSourceID["decision:LB1"]
	require.NotNil(t, lb1, "expected a decision entry for LB1 in the report: %+v", report.Entries)
	assert.NotEqual(t, uuid.Nil, lb1.EntityID)

	lb4 := byKindAndSourceID["decision:LB4"]
	require.NotNil(t, lb4, "expected a decision entry for LB4 in the report: %+v", report.Entries)
	assert.NotEqual(t, uuid.Nil, lb4.EntityID)

	c1 := byKindAndSourceID["capability:C1"]
	require.NotNil(t, c1, "expected a capability entry for C1 in the report: %+v", report.Entries)
	assert.NotEqual(t, uuid.Nil, c1.EntityID)

	m1 := byKindAndSourceID["milestone:M1"]
	require.NotNil(t, m1, "expected a milestone entry for M1 in the report: %+v", report.Entries)
	assert.NotEqual(t, uuid.Nil, m1.EntityID)

	// Render must not panic and must mention every source id -- the
	// human-readable half of FR16.
	rendered := report.Render()
	for _, id := range []string{"Operator", "LB1", "LB4", "C1", "M1"} {
		assert.Contains(t, rendered, id, "expected the rendered report to mention %s", id)
	}
}

// TestImport_MustNotForeclose_ProducesOneAssociationRowPerCitedDecision
// proves LB6/FR17's "a `Must not foreclose: LB1, LB4` line becomes two
// rows, one per cited entity ... never prose kept only in a rendered doc."
// testdata/valid/product/03-roadmap.md's M1 cites exactly LB1 and LB4 and
// nothing else (no `Delivers:` line), so ListAssociationsByMilestone must
// return exactly those two rows.
func TestImport_MustNotForeclose_ProducesOneAssociationRowPerCitedDecision(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, "testdata/valid", testSourceRevision)
	require.NoError(t, err)

	var milestoneID uuid.UUID
	for _, e := range report.Entries {
		if e.Kind == "milestone" && e.SourceID == "M1" {
			milestoneID = e.EntityID
		}
	}
	require.NotEqual(t, uuid.Nil, milestoneID, "expected an M1 milestone entry in the report")

	associations, err := env.store.Milestones().ListAssociationsByMilestone(ctx, milestoneID)
	require.NoError(t, err)
	assert.Len(t, associations, 2, "\"Must not foreclose: LB1, LB4\" must produce exactly two entity_milestone rows, not prose: %+v", associations)

	var lb1ID, lb4ID uuid.UUID
	for _, e := range report.Entries {
		switch e.SourceID {
		case "LB1":
			lb1ID = e.EntityID
		case "LB4":
			lb4ID = e.EntityID
		}
	}
	gotEntities := map[uuid.UUID]bool{}
	for _, a := range associations {
		gotEntities[a.EntityID] = true
	}
	assert.True(t, gotEntities[lb1ID], "expected an entity_milestone row for LB1")
	assert.True(t, gotEntities[lb4ID], "expected an entity_milestone row for LB4")
}

// TestImport_UndefinedMilestoneReference_FailsLoudlyNamingIt is the
// red/green case FR17 states explicitly: "a silent create is the failure
// mode". testdata/undefined_milestone's roadmap names M2 in prose with no
// `### M2` heading of its own; Import must fail, and the error must name
// "M2" so the failure is diagnosable, not merely "import failed".
func TestImport_UndefinedMilestoneReference_FailsLoudlyNamingIt(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, "testdata/undefined_milestone", testSourceRevision)
	require.Error(t, err, "importing a document that references an undefined milestone must fail")
	assert.Nil(t, report)
	assert.Contains(t, err.Error(), "M2", "the error must name the undefined milestone (M2), not fail silently or vaguely")

	// And nothing must have been written: Parse fails before write() is
	// ever called (importer.go's Import), so no product exists for this
	// scope from this attempt.
	products, err := env.store.Products().ListCurrentByScope(ctx, env.scopeID)
	require.NoError(t, err)
	assert.Empty(t, products, "a Parse-time failure must write nothing -- Parse runs entirely before write() is called")
}

// TestImport_TwiceSameSession_DoesNotDuplicateMilestoneRef proves the
// specific idempotency guarantee FR17 states for milestone_ref: "creating
// it on first reference if absent for that Product ... a second import of
// the same document resolves to this same row rather than inserting a
// duplicate" (migration 004_milestone_assoc.up.sql's comment,
// MilestoneStore.GetOrCreateRef's doc comment).
//
// The second Import call here fails -- FR12's pre-parse refusal check
// (importer.go's refuseIfAlreadyImported, issue #2548) sees that
// "testdata/valid" already has an import_completion row for this scope
// from the first call and refuses with importer.ErrAlreadyImported before
// Parse or write() ever run a second time, so the second call never
// reaches any entity write, milestone_ref included. (Before #2548,
// testdata/valid's Product name colliding with the first call's under
// migration 002's product_scope_name_current_idx, scope-qualified
// uniqueness LB1, was what stopped the second call at write()'s first
// statement instead -- FR12's guard now fires earlier than that, for the
// same reason: cmd/main.go's own doc comment states the importer is "a
// Swarm Operator-triggered, one-time write", and refusing a second run
// against an already-imported path rather than silently duplicating is
// the correct shape for that one-time contract.) What this test actually
// pins down is the row count after both calls: exactly one milestone_ref
// row for (scope, product, "M1"), never two.
func TestImport_TwiceSameSession_DoesNotDuplicateMilestoneRef(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	_, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, "testdata/valid", testSourceRevision)
	require.NoError(t, err, "first import must succeed")

	_, err = importer.Import(ctx, env.store, env.sessions, env.sessionID, "testdata/valid", testSourceRevision)
	// Whether or not a future amend/reimport flow changes this call to
	// succeed, the row-count assertion below is what the acceptance
	// criterion actually requires; record today's behavior so a change is
	// a deliberate, visible diff rather than a silent regression.
	t.Logf("second Import call returned: %v", err)

	products, err := env.store.Products().ListCurrentByScope(ctx, env.scopeID)
	require.NoError(t, err)
	require.NotEmpty(t, products, "expected at least the first import's Product to exist")
	productID := products[0].ID

	var count int
	require.NoError(t, env.pool.QueryRow(ctx, `
		SELECT count(*) FROM milestone_ref WHERE scope_id = $1 AND product_id = $2 AND name = 'M1'
	`, env.scopeID, productID).Scan(&count))
	assert.Equal(t, 1, count, "a second import of the same document must not duplicate the milestone_ref row for M1")

	// The scenario a re-import of an *existing* Product would actually hit
	// -- write()'s per-milestone loop calling GetOrCreateRef a second time
	// for the same (scopeID, productID, "M1") -- is exactly
	// MilestoneStore.GetOrCreateRef's own idempotency contract. Call it
	// directly here, against the real productID the first import minted,
	// to prove that primitive holds even though the CLI-level "one-time
	// write" boundary above never lets a literal second Import reach it.
	ref, err := env.store.Milestones().GetOrCreateRef(ctx, env.scopeID, productID, "M1")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, ref.ID)

	require.NoError(t, env.pool.QueryRow(ctx, `
		SELECT count(*) FROM milestone_ref WHERE scope_id = $1 AND product_id = $2 AND name = 'M1'
	`, env.scopeID, productID).Scan(&count))
	assert.Equal(t, 1, count, "GetOrCreateRef called a second time for the same (scope, product, name) must resolve the existing row, not insert a duplicate")
}

// TestParse_WhagentNetFixture_ParsesWithoutImporting exercises Parse (never
// write) against whagent_net's real PRODUCT.md + product/*.md -- "the
// hardest shape in the repo" (issue #2492's Testing section): a single
// fenced block wrapping all seven load-bearing decisions rather than one
// fence per entry, an unmarked flat non-goals list (defaulting every entry
// to Permanent, per parseNonGoals's doc comment), bare (non-bold)
// capability lines, and h3 `### M<n>` roadmap headings. Parse-and-report
// only: whagent_net is never imported into krill here or anywhere else
// (that is C27/M2, out of scope for this task) -- this test never
// constructs a *store.Store or krill session at all, so there is no write
// path available to use even by accident.
func TestParse_WhagentNetFixture_ParsesWithoutImporting(t *testing.T) {
	productMD, err := runfiles.Rlocation("_main/whagent_net/PRODUCT.md")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(whagent_net/PRODUCT.md): %v (is //whagent_net:docs still a data dep of this test target?)", err)
	}
	root := filepath.Dir(productMD)

	parsed, err := importer.Parse(root)
	require.NoError(t, err, "whagent_net's own doc set must parse cleanly as a read-only fixture")

	assert.Equal(t, "whagent-net", parsed.Name)
	assert.NotEmpty(t, parsed.Vision)
	assert.NotEmpty(t, parsed.Personas, "expected whagent_net's Personas section to parse")
	assert.Len(t, parsed.Decisions, 7, "expected all seven LB1-LB7 decisions to parse out of whagent_net's single shared fence")
	assert.NotEmpty(t, parsed.NonGoals, "expected whagent_net's flat non-goals list to parse")
	for _, ng := range parsed.NonGoals {
		assert.Equal(t, importer.NonGoalPermanent, ng.Kind, "an unmarked non-goals list must default every entry to Permanent")
	}
	assert.NotEmpty(t, parsed.Buckets, "expected whagent_net's Now/Next/Later capability buckets to parse")
	assert.NotEmpty(t, parsed.Milestones, "expected whagent_net's M1-M3 roadmap headings to parse")
}
