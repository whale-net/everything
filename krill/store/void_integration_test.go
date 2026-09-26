//go:build integration

// Real-Postgres coverage for VoidStore (void.go, FR d38d726e / FR
// 2a3a8eef): the SCD2 close-WITHOUT-successor. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" tag, and amend_integration_test.go for the
// fixture pattern -- each go_test target here compiles only the srcs
// BUILD.bazel lists for it, so this file provisions its own store rather
// than sharing newAmendTestStore.
//
// The assertions here are deliberately shaped to fail for the reasons this
// milestone has actually shipped defects, not merely to pass:
//
//   - Refusal PLACEMENT is checked with a pgx query tracer, not by
//     asserting "no row was written". A void that checked its guards AFTER
//     the tombstoning UPDATE and then rolled back would satisfy a
//     "writes nothing" assertion while still being wrong, so these tests
//     assert that no write statement is ever ISSUED on a refused call.
//
//   - Every existence check gets a CROSS-SCOPE case, because a guard that
//     silently lost its scope_id argument would leave this suite green
//     while breaking LB1.
//
//   - The display-number retirement is proven against the specific
//     failure mode -- voiding the HIGHEST-numbered row of a product and
//     showing the next create does not receive its number -- rather than
//     against a weaker "the number changed" assertion.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:void_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// statementTracer records every SQL statement a pool issues, so a test can
// assert on what was SENT rather than only on what survived a rollback.
// This is what makes refusal placement observable: a guard that ran after
// the tombstoning UPDATE would still have sent that UPDATE, and a rollback
// afterwards would erase every trace of it from the database.
type statementTracer struct {
	mu   sync.Mutex
	seen []string
}

func (tr *statementTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.seen = append(tr.seen, data.SQL)
	return ctx
}

// TraceQueryEnd is required by pgx.QueryTracer but records nothing: the
// statement text is all these tests need, and it is available at start.
func (tr *statementTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
}

func (tr *statementTracer) reset() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.seen = nil
}

// writesUnder reports whether any recorded statement targeting table is a
// write -- an INSERT or an UPDATE. Reads (SELECT ... FOR UPDATE, the
// EXISTS probes) are not writes and are deliberately not counted: a void
// must lock and probe before it refuses, and that is correct.
func (tr *statementTracer) writesUnder(table string) []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var writes []string
	for _, sql := range tr.seen {
		upper := strings.ToUpper(strings.Join(strings.Fields(sql), " "))
		if !strings.Contains(upper, "INSERT") && !strings.Contains(upper, "UPDATE") {
			continue
		}
		// Match the write VERB immediately followed by the table, not the
		// table name anywhere: a `SELECT ... FROM feature` read is not a
		// write to `feature`, and matching the bare name would count the
		// void's own locking read as the tombstone UPDATE.
		t := strings.ToUpper(table)
		if strings.Contains(upper, "UPDATE "+t+" ") || strings.Contains(upper, "INTO "+t+" ") {
			writes = append(writes, sql)
		}
	}
	return writes
}

func (tr *statementTracer) wroteToVoidEvent() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var writes []string
	for _, sql := range tr.seen {
		if strings.Contains(strings.ToUpper(sql), "INTO VOID_EVENT") {
			writes = append(writes, sql)
		}
	}
	return writes
}

func newVoidTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	return store.New(db.Pool), db
}

// newTracedVoidStore returns a second *store.Store over the same database
// whose pool records every statement it issues, for the placement tests.
func newTracedVoidStore(t *testing.T, db *dbtest.Postgres) (*store.Store, *statementTracer) {
	t.Helper()
	tracer := &statementTracer{}
	cfg, err := pgxpool.ParseConfig(db.ConnString)
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return store.New(pool), tracer
}

func newVoidScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "void-scope-"+uuid.NewString()).Scan(&id))
	return id
}

// voidFixture is one product with a FeatureSet and a Feature already in it,
// which is the smallest tree every void test needs.
type voidFixture struct {
	scopeID    uuid.UUID
	product    store.Product
	featureSet store.FeatureSet
	feature    store.Feature
}

func newVoidFixture(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres) voidFixture {
	t.Helper()
	scopeID := newVoidScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "vision")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	f, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)
	return voidFixture{scopeID: scopeID, product: product, featureSet: fs, feature: f}
}

// newChildlessFeatureSet returns a FeatureSet with no Feature under it, for
// tests that need to isolate one child kind: the shared newVoidFixture
// always seeds a Feature, and the `feature` guard fires before the
// load_bearing_decision one, masking whatever the test meant to exercise.
func newChildlessFeatureSet(t *testing.T, ctx context.Context, s *store.Store, fx voidFixture) store.FeatureSet {
	t.Helper()
	fs, err := s.FeatureSets().Create(ctx, fx.scopeID, fx.product.ID, "Childless "+uuid.NewString(), nil)
	require.NoError(t, err)
	return fs
}

// childlessFixture is a scope whose only row is a Product, so whichever
// product child kind a test seeds is the one the `feature_set` guard --
// which productChildren checks first -- cannot pre-empt. The shared
// newVoidFixture always seeds a FeatureSet, which would mask every other
// product guard.
type childlessFixture struct {
	scopeID uuid.UUID
	product store.Product
}

func newChildlessProduct(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres) childlessFixture {
	t.Helper()
	scopeID := newVoidScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "vision")
	require.NoError(t, err)
	return childlessFixture{scopeID: scopeID, product: product}
}

// productMilestone seeds a bare `milestone_ref` row against productID, with
// no entity_milestone association -- the state a product is in as soon as a
// milestone is merely authored against it. This is what the milestone_ref
// child guard is for; deliveredFixture would instead deliver a Feature,
// which never makes the PRODUCT spoken for.
func productMilestone(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID, productID uuid.UUID) uuid.UUID {
	t.Helper()
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, $3) RETURNING id
	`, scopeID, productID, "M-"+uuid.NewString()).Scan(&milestoneID))
	return milestoneID
}

func voidActor() store.Subject {
	return store.Subject{Iss: "whale_net", Sub: "alex", Kind: store.SubjectKindHuman}
}

// isCurrent reports whether the table still has a current row for id --
// i.e. whether the void left the row untouched.
func isCurrent(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string, id uuid.UUID) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1 AND valid_to IS NULL)`, id).Scan(&exists))
	return exists
}

func revisionCount(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string, id uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM `+table+` WHERE id = $1`, id).Scan(&n))
	return n
}

// ---------------------------------------------------------------------------
// The tombstone itself (FR d38d726e)
// ---------------------------------------------------------------------------

// TestVoidFeature_TombstonesWithoutSuccessor is the core LB3 claim: void
// closes the current row and inserts NO successor. It asserts both halves
// separately -- the closed row exists with a valid_to, and the id's
// revision count did not grow -- because "the row is not current" alone
// would also be true of an amend.
func TestVoidFeature_TombstonesWithoutSuccessor(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()))

	assert.False(t, isCurrent(t, ctx, db, "feature", fx.feature.ID),
		"a voided Feature must have no current row -- that is what removes it from every current-slice read and from render")
	assert.Equal(t, 1, revisionCount(t, ctx, db, "feature", fx.feature.ID),
		"a void must insert NO successor revision: the id's row count stays at 1. An amend would make this 2 -- that difference IS the LB3 distinction")

	// The closed row is still physically there, carrying its original id
	// and display number (FR (c)).
	var (
		gotID      uuid.UUID
		gotDisplay int
	)
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT id, display_number FROM feature WHERE id = $1`, fx.feature.ID).Scan(&gotID, &gotDisplay))
	assert.Equal(t, fx.feature.ID, gotID, "the tombstoned row keeps its original surrogate id (LB2)")
	assert.Equal(t, fx.feature.DisplayNumber, gotDisplay, "the tombstoned row keeps its original display number")
}

// TestVoidFeature_GoneFromCurrentSliceReads pins the "excluded from every
// current-slice read" claim against the real slice queries rather than
// against the store's own by-id accessor -- the slice reads are what
// render is built on, and they are separate SQL with their own predicates.
func TestVoidFeature_GoneFromCurrentSliceReads(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	before, err := s.Slices().ListFeaturesByProduct(ctx, fx.product.ID)
	require.NoError(t, err)
	require.Len(t, before, 1, "the fixture feature must be visible before the void")

	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()))

	after, err := s.Slices().ListFeaturesByProduct(ctx, fx.product.ID)
	require.NoError(t, err)
	assert.Empty(t, after, "a voided Feature must be absent from the product-wide current slice read -- this is the read krill/render is built on")

	// A sibling that was never voided must still be there: void targets one
	// entity and must never disturb its neighbours.
	sibling, err := s.Features().Create(ctx, fx.scopeID, fx.featureSet.ID, "A Survivor", nil)
	require.NoError(t, err)
	after, err = s.Slices().ListFeaturesByProduct(ctx, fx.product.ID)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, sibling.ID, after[0].ID, "a void must not remove a sibling")
}

// TestVoidFeature_FreesTheName is requirement (a): a later create may
// reuse the voided row's name. The name indexes are partial on
// `valid_to IS NULL`, so this falls out of the close -- the test exists to
// pin that it stays true.
func TestVoidFeature_FreesTheName(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()))

	reused, err := s.Features().Create(ctx, fx.scopeID, fx.featureSet.ID, fx.feature.Name, nil)
	require.NoError(t, err, "a voided Feature's name must be reusable by a later create (FR d38d726e (a))")
	assert.Equal(t, fx.feature.Name, reused.Name)
}

// TestVoidFeature_RetiresTheDisplayNumber is requirement (b) -- the LB2
// edge -- and it is written against the specific way the guarantee can
// break: void the HIGHEST-numbered Feature in a product, then create
// another. If nextDisplayNumber counted only current rows, MAX would drop
// and the new Feature would be handed the voided one's number, and a `C7`
// citation already rendered for the voided Feature would silently resolve
// to this new one.
//
// The test is paired with
// TestVoidFeature_NumberStaysRetiredAfterItsFeatureSetIsVoided and
// TestVoidFeature_NumberingSpansEveryFeatureSetInTheProduct, which pin the
// two ways the same guarantee breaks when the all-rows query is narrowed
// again.
func TestVoidFeature_RetiresTheDisplayNumber(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	// fx.feature is C1. Add C2, then void C2 -- the highest number in the
	// product, which is the case the old current-rows-only MAX got wrong.
	highest, err := s.Features().Create(ctx, fx.scopeID, fx.featureSet.ID, "The One We Mistake", nil)
	require.NoError(t, err)
	require.Equal(t, 2, highest.DisplayNumber, "fixture assumption: the second Feature is C2")

	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, highest.ID, nil, voidActor(), voidActor()))

	replacement, err := s.Features().Create(ctx, fx.scopeID, fx.featureSet.ID, highest.Name, nil)
	require.NoError(t, err, "the voided Feature's name must be reusable")
	assert.Equal(t, highest.Name, replacement.Name, "the replacement reuses the NAME (FR (a))")
	assert.NotEqual(t, highest.DisplayNumber, replacement.DisplayNumber,
		"the replacement must NOT be handed the retired number -- nextDisplayNumber must count the voided row, or this new Feature silently inherits the C2 a rendered citation already points at (FR (b), LB2)")
	assert.Greater(t, replacement.DisplayNumber, highest.DisplayNumber,
		"the replacement must be handed a FRESH number above every number the product has ever issued")

	// The number must be retired twice over: no other Feature anywhere in
	// this product may hold it.
	var holders int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM feature f
		JOIN feature_set fs ON f.feature_set_id = fs.id
		WHERE fs.product_id = $1 AND f.display_number = $2
	`, fx.product.ID, highest.DisplayNumber).Scan(&holders))
	assert.Equal(t, 1, holders,
		"exactly one row -- the tombstoned one -- may hold the retired number. A second holder is exactly the silent repointing LB2 forbids")
}

// TestVoidFeature_NumberStaysRetiredAfterItsFeatureSetIsVoided is the
// subtle half of the numbering contract, and the one a per-table reasoning
// review is most likely to miss: a voided Feature's number stays retired
// even after its FeatureSet is voided too. The feature_set join in
// nextDisplayNumber is therefore unfiltered on the parent's currentness --
// a query that filtered it back to `valid_to IS NULL` would drop the whole
// voided FeatureSet out of the MAX and hand its numbers straight back out.
func TestVoidFeature_NumberStaysRetiredAfterItsFeatureSetIsVoided(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	// fx.feature is C1, the highest number in the product.
	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()))
	require.NoError(t, s.Void().VoidFeatureSet(ctx, fx.scopeID, fx.featureSet.ID, nil, voidActor(), voidActor()),
		"precondition: the FeatureSet is childless, so it can be voided once its Feature is gone")

	// A brand new FeatureSet under the same product, and a create that
	// reuses the freed name.
	fresh, err := s.FeatureSets().Create(ctx, fx.scopeID, fx.product.ID, "A Later FS", nil)
	require.NoError(t, err)
	replacement, err := s.Features().Create(ctx, fx.scopeID, fresh.ID, fx.feature.Name, nil)
	require.NoError(t, err)

	assert.NotEqual(t, fx.feature.DisplayNumber, replacement.DisplayNumber,
		"voiding the Feature's FeatureSet must not un-retire the Feature's number -- the join is unfiltered on the parent's currentness precisely so a voided FeatureSet's numbers cannot be handed back out (FR (b), LB2)")
	assert.Greater(t, replacement.DisplayNumber, fx.feature.DisplayNumber,
		"the replacement must be numbered above every number the product has ever issued, voided FeatureSet or not")

	events, err := s.Void().ListVoidEvents(ctx, fx.scopeID, store.VoidedFeature)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].RetiredDisplayNumber)
	assert.Equal(t, fx.feature.DisplayNumber, *events[0].RetiredDisplayNumber,
		"the retirement is filed against the product the Feature belonged to BEFORE the void, so it survives its FeatureSet being voided too")
}

// TestVoidFeature_NumberingSpansEveryFeatureSetInTheProduct pins the other
// half: the number sequence is per PRODUCT, not per FeatureSet, and a void
// in one FeatureSet must not let a create in another pick up its number.
// Two Features in two different FeatureSets are the smallest case that
// distinguishes the two scopes of numbering.
func TestVoidFeature_NumberingSpansEveryFeatureSetInTheProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	other, err := s.FeatureSets().Create(ctx, fx.scopeID, fx.product.ID, "A Second FS", nil)
	require.NoError(t, err)
	inOther, err := s.Features().Create(ctx, fx.scopeID, other.ID, "Lives In The Second FS", nil)
	require.NoError(t, err)

	require.Equal(t, 1, fx.feature.DisplayNumber, "precondition: the first Feature is C1")
	require.Equal(t, 2, inOther.DisplayNumber,
		"precondition: numbering is per PRODUCT, so a Feature in a second FeatureSet continues the sequence rather than restarting it")

	// Void the higher-numbered one, which lives in the OTHER FeatureSet.
	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, inOther.ID, nil, voidActor(), voidActor()))

	replacement, err := s.Features().Create(ctx, fx.scopeID, fx.featureSet.ID, inOther.Name, nil)
	require.NoError(t, err)
	assert.NotEqual(t, inOther.DisplayNumber, replacement.DisplayNumber,
		"a void in one FeatureSet must not let a create in another receive the retired number -- the MAX is taken across every FeatureSet in the product (FR (b), LB2)")
	assert.Greater(t, replacement.DisplayNumber, inOther.DisplayNumber,
		"the replacement must be numbered above every number the product has ever issued across all of its FeatureSets")
}

// TestVoidFeature_NeverReissuesAcrossManyVoids pushes the retirement
// further than the single case above: void every Feature in a product one
// at a time and confirm the numbering keeps climbing rather than
// collapsing back onto the freed numbers.
func TestVoidFeature_NeverReissuesAcrossManyVoids(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	// fx.feature is C1; add two more.
	names := []string{"Second", "Third"}
	features := []store.Feature{fx.feature}
	for _, n := range names {
		f, err := s.Features().Create(ctx, fx.scopeID, fx.featureSet.ID, n, nil)
		require.NoError(t, err)
		features = append(features, f)
	}

	// Void them in DESCENDING number order -- the order that would collapse
	// the numbering fastest under a current-rows-only MAX.
	for i := len(features) - 1; i >= 0; i-- {
		require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, features[i].ID, nil, voidActor(), voidActor()))
	}

	next, err := s.Features().Create(ctx, fx.scopeID, fx.featureSet.ID, "After Three Voids", nil)
	require.NoError(t, err)
	assert.Greater(t, next.DisplayNumber, 3,
		"after voiding C1, C2 and C3 the next number must be above all three -- the retired numbers 1, 2 and 3 must never be handed out again")

	// And no live row shares a number with any tombstoned row.
	var collisions int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM feature a JOIN feature_set sa ON a.feature_set_id = sa.id
		JOIN feature b ON b.display_number = a.display_number
		JOIN feature_set sb ON b.feature_set_id = sb.id AND sb.product_id = sa.product_id
		WHERE sa.product_id = $1 AND a.id <> b.id
	`, fx.product.ID).Scan(&collisions))
	assert.Zero(t, collisions, "no two distinct Features in one product may share a display number, current or tombstoned")
}

// TestVoidLoadBearingDecision_RetiresItsNumber is the same guarantee on
// the other display-number-bearing kind.
func TestVoidLoadBearingDecision_RetiresItsNumber(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	d, err := s.Decisions().Create(ctx, fx.scopeID, fx.featureSet.ID, "LB1", nil)
	require.NoError(t, err)
	require.Equal(t, 1, d.DisplayNumber)

	require.NoError(t, s.Void().VoidLoadBearingDecision(ctx, fx.scopeID, d.ID, nil, voidActor(), voidActor()))
	assert.False(t, isCurrent(t, ctx, db, "load_bearing_decision", d.ID))
	assert.Equal(t, 1, revisionCount(t, ctx, db, "load_bearing_decision", d.ID), "no successor revision")

	next, err := s.Decisions().Create(ctx, fx.scopeID, fx.featureSet.ID, "LB1", nil)
	require.NoError(t, err)
	assert.NotEqual(t, d.DisplayNumber, next.DisplayNumber, "a voided LoadBearingDecision's number is retired too")
}

// TestVoidRequirement_RetiresNothing is the negative half of the numbering
// contract: kinds with no stored display number must record a NULL, and
// must not be handed a made-up one.
func TestVoidRequirement_RetiresNothing(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	r, err := s.Requirements().Create(ctx, fx.scopeID, fx.feature.ID, store.RequirementKindFR, "An FR", nil)
	require.NoError(t, err)

	require.NoError(t, s.Void().VoidRequirement(ctx, fx.scopeID, r.ID, nil, voidActor(), voidActor()))

	events, err := s.Void().ListVoidEvents(ctx, fx.scopeID, store.VoidedRequirement)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Nil(t, events[0].RetiredDisplayNumber,
		"an `FR7` citation is derived at render time from position, so a voided Requirement has no stored number to retire and must record NULL")
	assert.Equal(t, r.ID, events[0].EntityID)
	assert.Equal(t, fx.product.ID, events[0].ProductID, "the resolved product must be the one the requirement actually hangs under")
}

// ---------------------------------------------------------------------------
// The audit read (FR (c))
// ---------------------------------------------------------------------------

// TestListVoidEvents_IsTheOnlyWayToReachATombstone covers requirement (c):
// the tombstone is retrievable, with its original id and number, and only
// through the audit read.
func TestListVoidEvents_IsTheOnlyWayToReachATombstone(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	reason := "created against the wrong feature set"
	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, &reason, voidActor(), voidActor()))

	// The current read cannot see it.
	_, err := s.Features().GetCurrentByID(ctx, fx.feature.ID)
	assert.ErrorIs(t, err, store.ErrNotFound, "a voided Feature must be unreachable through a current read")

	// The audit read can, and carries the original id, number, actor and reason.
	events, err := s.Void().ListVoidEvents(ctx, fx.scopeID, store.VoidedFeature)
	require.NoError(t, err)
	require.Len(t, events, 1, "the void must be audit-readable")
	ev := events[0]
	assert.Equal(t, fx.feature.ID, ev.EntityID, "the audit record carries the ORIGINAL surrogate id (LB2)")
	require.NotNil(t, ev.RetiredDisplayNumber)
	assert.Equal(t, fx.feature.DisplayNumber, *ev.RetiredDisplayNumber, "the audit record carries the ORIGINAL display number -- the number a rendered C-citation resolved to")
	assert.Equal(t, reason, *ev.Reason)
	assert.Equal(t, voidActor(), ev.CreatedByActing, "who voided it is recorded (LB4)")
	assert.Equal(t, voidActor(), ev.CreatedByOnBehalfOf)
	assert.Equal(t, store.VoidedFeature, ev.EntityKind)

	// Narrowing by kind is honoured, and an empty kind means "all kinds".
	p, err := s.Void().ListVoidEvents(ctx, fx.scopeID, store.VoidedProduct)
	require.NoError(t, err)
	assert.Empty(t, p, "narrowing to a kind with no voids must return none, not everything")
	all, err := s.Void().ListVoidEvents(ctx, fx.scopeID, "")
	require.NoError(t, err)
	assert.Len(t, all, 1, "an empty kind means every kind")
}

// TestListVoidEvents_DoesNotCrossScopes -- the audit read is scope-bound.
func TestListVoidEvents_DoesNotCrossScopes(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()))

	otherScope := newVoidScope(t, ctx, db)
	events, err := s.Void().ListVoidEvents(ctx, otherScope, "")
	require.NoError(t, err)
	assert.Empty(t, events, "one scope's audit read must never surface another scope's voids")
}

// ---------------------------------------------------------------------------
// Refusal (a): delivered (FR 2a3a8eef)
// ---------------------------------------------------------------------------

// deliveredFixture associates featureID with a milestone as a `delivers`
// row, optionally marking it shipped, and returns the scope's milestone id.
func deliveredFixture(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, scopeID, productID, featureID uuid.UUID, relation store.MilestoneRelation, ship bool) {
	t.Helper()
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, $3) RETURNING id
	`, scopeID, productID, "M-"+uuid.NewString()).Scan(&milestoneID))
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
		VALUES ($1, $2, $3, $4)
	`, scopeID, featureID, milestoneID, string(relation))
	require.NoError(t, err)
	if ship {
		require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestoneID, featureID, nil, voidActor(), voidActor()))
	}
}

// TestVoidFeature_RefusesDelivered covers refusal (a) for the plain
// `delivers` association, and -- via the tracer -- proves the refusal is
// placed BEFORE the tombstoning UPDATE rather than after it.
func TestVoidFeature_RefusesDelivered(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	traced, tracer := newTracedVoidStore(t, db)
	fx := newVoidFixture(t, ctx, s, db)
	deliveredFixture(t, ctx, s, db, fx.scopeID, fx.product.ID, fx.feature.ID, store.MilestoneRelationDelivers, false)

	tracer.reset()
	err := traced.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor())
	require.ErrorIs(t, err, store.ErrEntityDelivered, "a delivered entity must be refused (FR 2a3a8eef (a))")
	assert.Contains(t, err.Error(), "amend", "the error must point the caller at the correct path")

	assert.True(t, isCurrent(t, ctx, db, "feature", fx.feature.ID), "a refused void must leave the row current")
	assert.Empty(t, tracer.writesUnder("feature"),
		"PLACEMENT: a refused void must issue no UPDATE against the entity table at all. An empty post-rollback state would not prove this -- the statement must never be sent")
	assert.Empty(t, tracer.wroteToVoidEvent(), "PLACEMENT: a refused void must not insert a tombstone record")
}

// TestVoidFeature_RefusesShipped covers the `delivery_shipment` half of
// refusal (a) specifically -- the case the entity_milestone check alone
// would only catch by proxy.
func TestVoidFeature_RefusesShipped(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	traced, tracer := newTracedVoidStore(t, db)
	fx := newVoidFixture(t, ctx, s, db)
	deliveredFixture(t, ctx, s, db, fx.scopeID, fx.product.ID, fx.feature.ID, store.MilestoneRelationDelivers, true)

	tracer.reset()
	err := traced.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor())
	require.ErrorIs(t, err, store.ErrEntityDelivered, "a shipped entity must be refused")

	// Isolate the delivery_shipment contribution: delete the
	// entity_milestone association and confirm the shipment ALONE still
	// refuses. Without this, the test would pass on the entity_milestone
	// check alone and the delivery_shipment table would be untested.
	_, err = db.Pool.Exec(ctx, `DELETE FROM entity_milestone WHERE entity_id = $1`, fx.feature.ID)
	require.NoError(t, err)

	tracer.reset()
	err = traced.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor())
	require.ErrorIs(t, err, store.ErrEntityDelivered,
		"a delivery_shipment row alone must refuse -- that is the shipped case, which the entity_milestone check would otherwise only catch by proxy")
	assert.Empty(t, tracer.writesUnder("feature"), "PLACEMENT: still no UPDATE issued")
}

// TestVoidFeature_RefusesMustNotForeclose is a judgement call worth pinning
// explicitly, because it is stricter than the requirement's literal
// wording: a Must-not-foreclose association is a reference a reader will
// follow, so voiding the entity would orphan it just as a `delivers` row
// would. A reviewer who disagrees with that reading should see this test
// named rather than discover the behaviour in the code.
func TestVoidFeature_RefusesMustNotForeclose(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)
	deliveredFixture(t, ctx, s, db, fx.scopeID, fx.product.ID, fx.feature.ID, store.MilestoneRelationMustNotForeclose, false)

	err := s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor())
	require.ErrorIs(t, err, store.ErrEntityDelivered,
		"a Must-not-foreclose association is a live reference and must block a void")
	assert.True(t, isCurrent(t, ctx, db, "feature", fx.feature.ID))
}

// TestVoidFeature_DeliveryRefusalIsScopeQualified -- the cross-scope case
// for the delivery check. Another scope's association against this entity
// must not block this scope's void.
func TestVoidFeature_DeliveryRefusalIsScopeQualified(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	// A milestone and association in a DIFFERENT scope, pointing at this
	// scope's feature.
	otherScope := newVoidScope(t, ctx, db)
	deliveredFixture(t, ctx, s, db, otherScope, fx.product.ID, fx.feature.ID, store.MilestoneRelationDelivers, true)

	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()),
		"another scope's delivery row must not block this scope's void (LB1)")
}

// ---------------------------------------------------------------------------
// Refusal (b): live children (FR 2a3a8eef)
// ---------------------------------------------------------------------------

// TestVoidFeature_RefusesLiveChild covers refusal (b) on the feature ->
// requirement edge, with the same placement assertion.
func TestVoidFeature_RefusesLiveChild(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	traced, tracer := newTracedVoidStore(t, db)
	fx := newVoidFixture(t, ctx, s, db)

	_, err := s.Requirements().Create(ctx, fx.scopeID, fx.feature.ID, store.RequirementKindFR, "A Child FR", nil)
	require.NoError(t, err)

	tracer.reset()
	err = traced.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor())
	require.ErrorIs(t, err, store.ErrHasLiveChildren, "an entity with a live child must be refused (FR 2a3a8eef (b))")
	assert.Contains(t, err.Error(), "requirement", "the error must name the blocking child table so the caller knows what to void first")

	assert.True(t, isCurrent(t, ctx, db, "feature", fx.feature.ID), "a refused void must leave the row current")
	assert.Empty(t, tracer.writesUnder("feature"), "PLACEMENT: a refused void must issue no UPDATE")
	assert.Empty(t, tracer.wroteToVoidEvent(), "PLACEMENT: a refused void must not insert a tombstone record")
}

// TestVoidFeatureSet_RefusesLiveChildren checks BOTH of a FeatureSet's
// child kinds, so a guard that only knew about `feature` would fail here.
func TestVoidFeatureSet_RefusesLiveChildren(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wantInMsg string
		seedChild func(t *testing.T, ctx context.Context, s *store.Store, fx voidFixture)
	}{
		{
			name:      "feature",
			wantInMsg: "feature",
			seedChild: func(t *testing.T, ctx context.Context, s *store.Store, fx voidFixture) {
				_, err := s.Features().Create(ctx, fx.scopeID, fx.featureSet.ID, "Child Feature", nil)
				require.NoError(t, err)
			},
		},
		{
			name:      "load_bearing_decision",
			wantInMsg: "load_bearing_decision",
			seedChild: func(t *testing.T, ctx context.Context, s *store.Store, fx voidFixture) {
				_, err := s.Decisions().Create(ctx, fx.scopeID, fx.featureSet.ID, "Child LB", nil)
				require.NoError(t, err)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, db := newVoidTestStore(t)
			traced, tracer := newTracedVoidStore(t, db)
			fx := newVoidFixture(t, ctx, s, db)
			// Isolate the child kind under test: seed it under a FeatureSet
			// that has no other child, so the guard that fires is the one
			// this case is about.
			fs := newChildlessFeatureSet(t, ctx, s, fx)
			fx.featureSet = fs
			tc.seedChild(t, ctx, s, fx)

			tracer.reset()
			err := traced.Void().VoidFeatureSet(ctx, fx.scopeID, fx.featureSet.ID, nil, voidActor(), voidActor())
			require.ErrorIs(t, err, store.ErrHasLiveChildren)
			assert.Contains(t, err.Error(), tc.wantInMsg, "the error must name the blocking child table")
			assert.Empty(t, tracer.writesUnder("feature_set"), "PLACEMENT: no UPDATE issued on refusal")
		})
	}
}

// TestVoidProduct_RefusesLiveChildren walks the full product child set --
// feature_set, persona, non_goal and milestone_ref -- because a guard that
// knew only about feature_set would leave three real orphaning paths open.
//
// Every case starts from a CHILDLESS product and asserts the guard that
// actually fired, by child-table name, in the refusal message. Both halves
// are load-bearing: without the childless fixture the seeded persona,
// non_goal and milestone_ref never got evaluated at all (the seeded
// FeatureSet's guard returned first), and without the message assertion a
// void refused for some other reason would satisfy the test just as well.
func TestVoidProduct_RefusesLiveChildren(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wantInMsg string
		seed      func(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, fx childlessFixture)
	}{
		{
			name:      "feature_set",
			wantInMsg: "feature_set",
			seed: func(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, fx childlessFixture) {
				_, err := s.FeatureSets().Create(ctx, fx.scopeID, fx.product.ID, "Another FS", nil)
				require.NoError(t, err)
			},
		},
		{
			name:      "persona",
			wantInMsg: "persona",
			seed: func(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, fx childlessFixture) {
				_, err := s.Personas().Create(ctx, fx.scopeID, fx.product.ID, "A Persona", nil)
				require.NoError(t, err)
			},
		},
		{
			name:      "non_goal",
			wantInMsg: "non_goal",
			seed: func(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, fx childlessFixture) {
				_, err := s.NonGoals().Create(ctx, fx.scopeID, fx.product.ID, store.NonGoalKindPermanent, "A NonGoal", nil)
				require.NoError(t, err)
			},
		},
		{
			name:      "milestone_ref",
			wantInMsg: "milestone_ref",
			seed: func(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, fx childlessFixture) {
				// A milestone hangs off a product, so voiding the product
				// would orphan the whole delivery axis. Merely authoring one
				// is enough -- no `delivers` association is involved.
				productMilestone(t, ctx, db, fx.scopeID, fx.product.ID)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, db := newVoidTestStore(t)
			traced, tracer := newTracedVoidStore(t, db)
			fx := newChildlessProduct(t, ctx, s, db)
			tc.seed(t, ctx, s, db, fx)

			tracer.reset()
			err := traced.Void().VoidProduct(ctx, fx.scopeID, fx.product.ID, nil, voidActor(), voidActor())
			require.ErrorIs(t, err, store.ErrHasLiveChildren,
				"a product with a live %s must be refused -- voiding it would orphan that child", tc.name)
			assert.Contains(t, err.Error(), tc.wantInMsg,
				"the error must name the blocking child table, so a guard that only knew about %s would fail here", "feature_set")
			assert.Empty(t, tracer.writesUnder("product"), "PLACEMENT: no UPDATE issued on refusal")
		})
	}
}

// TestVoid_SucceedsBottomUp is the counterpart to the live-children
// refusals: once the children are voided, the parent CAN be voided. It is
// what makes the refusal actionable rather than a dead end, and it pins
// that "live" really does mean valid_to IS NULL.
func TestVoid_SucceedsBottomUp(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	r, err := s.Requirements().Create(ctx, fx.scopeID, fx.feature.ID, store.RequirementKindFR, "A Child FR", nil)
	require.NoError(t, err)

	require.ErrorIs(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()), store.ErrHasLiveChildren,
		"precondition: the parent is refused while the child lives")

	require.NoError(t, s.Void().VoidRequirement(ctx, fx.scopeID, r.ID, nil, voidActor(), voidActor()))
	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()),
		"once the child is voided the parent is free to be voided -- a voided child must not keep its parent alive")

	require.NoError(t, s.Void().VoidFeatureSet(ctx, fx.scopeID, fx.featureSet.ID, nil, voidActor(), voidActor()))
	require.NoError(t, s.Void().VoidProduct(ctx, fx.scopeID, fx.product.ID, nil, voidActor(), voidActor()),
		"the whole chain voids bottom-up")

	// The product is the top of the tree, so with it gone the whole
	// FeatureSet/Feature/Requirement chain is unreachable from any current read.
	features, err := s.Slices().ListFeaturesByProduct(ctx, fx.product.ID)
	require.NoError(t, err)
	assert.Empty(t, features)
}

// TestVoid_ChildRefusalIsScopeQualified -- the cross-scope case for the
// live-children check. A child in another scope must not block.
func TestVoid_ChildRefusalIsScopeQualified(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	// Another scope's FeatureSet, carrying a Feature, pointing at nothing
	// in fx -- this cannot be a child of fx.featureSet (different parent id),
	// so instead assert the direct case: a child row in another scope whose
	// parent column DOES hold fx.featureSet.id must not block the void.
	otherScope := newVoidScope(t, ctx, db)
	fs := newChildlessFeatureSet(t, ctx, s, fx)
	var foreignID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, display_number)
		VALUES ($1, $2, $3, 99)
		RETURNING id
	`, otherScope, fs.ID, "Another Scope's Feature").Scan(&foreignID))

	require.NoError(t, s.Void().VoidFeatureSet(ctx, fx.scopeID, fs.ID, nil, voidActor(), voidActor()),
		"a child row belonging to another scope must not block this scope's void (LB1) -- otherwise one scope could freeze another's tree")

	// The other scope's row is untouched by our void.
	assert.True(t, isCurrent(t, ctx, db, "feature", foreignID),
		"voiding a FeatureSet must not touch a row that merely cites it from another scope")
}

// ---------------------------------------------------------------------------
// Scope (LB1) on the target lookup
// ---------------------------------------------------------------------------

// TestVoid_CrossScopeIDIsNotFound is the LB1 case the sibling task in this
// milestone shipped a hole in: passing a real id that belongs to another
// scope must be REFUSED, not voided. Every Void* method is covered,
// because each takes scopeID and a method that forgot to use it would
// cross scopes silently.
func TestVoid_CrossScopeIDIsNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	r, err := s.Requirements().Create(ctx, fx.scopeID, fx.feature.ID, store.RequirementKindFR, "An FR", nil)
	require.NoError(t, err)
	p, err := s.Personas().Create(ctx, fx.scopeID, fx.product.ID, "A Persona", nil)
	require.NoError(t, err)
	ng, err := s.NonGoals().Create(ctx, fx.scopeID, fx.product.ID, store.NonGoalKindDeferred, "A NonGoal", nil)
	require.NoError(t, err)
	d, err := s.Decisions().Create(ctx, fx.scopeID, fx.featureSet.ID, "An LB", nil)
	require.NoError(t, err)

	otherScope := newVoidScope(t, ctx, db)

	for _, tc := range []struct {
		name string
		id   uuid.UUID
		void func(scopeID, id uuid.UUID) error
	}{
		{"product", fx.product.ID, func(sc, id uuid.UUID) error { return s.Void().VoidProduct(ctx, sc, id, nil, voidActor(), voidActor()) }},
		{"feature_set", fx.featureSet.ID, func(sc, id uuid.UUID) error {
			return s.Void().VoidFeatureSet(ctx, sc, id, nil, voidActor(), voidActor())
		}},
		{"feature", fx.feature.ID, func(sc, id uuid.UUID) error { return s.Void().VoidFeature(ctx, sc, id, nil, voidActor(), voidActor()) }},
		{"requirement", r.ID, func(sc, id uuid.UUID) error {
			return s.Void().VoidRequirement(ctx, sc, id, nil, voidActor(), voidActor())
		}},
		{"persona", p.ID, func(sc, id uuid.UUID) error { return s.Void().VoidPersona(ctx, sc, id, nil, voidActor(), voidActor()) }},
		{"non_goal", ng.ID, func(sc, id uuid.UUID) error { return s.Void().VoidNonGoal(ctx, sc, id, nil, voidActor(), voidActor()) }},
		{"load_bearing_decision", d.ID, func(sc, id uuid.UUID) error {
			return s.Void().VoidLoadBearingDecision(ctx, sc, id, nil, voidActor(), voidActor())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.void(otherScope, tc.id)
			require.ErrorIs(t, err, store.ErrNotFound,
				"voiding from the WRONG scope must be refused with ErrNotFound (LB1) -- a real id from another scope must never be tombstoned")
		})
	}

	// And nothing was voided by any of the seven attempts.
	assert.True(t, isCurrent(t, ctx, db, "product", fx.product.ID))
	assert.True(t, isCurrent(t, ctx, db, "feature_set", fx.featureSet.ID))
	assert.True(t, isCurrent(t, ctx, db, "feature", fx.feature.ID))
	assert.True(t, isCurrent(t, ctx, db, "requirement", r.ID))
	assert.True(t, isCurrent(t, ctx, db, "persona", p.ID))
	assert.True(t, isCurrent(t, ctx, db, "non_goal", ng.ID))
	assert.True(t, isCurrent(t, ctx, db, "load_bearing_decision", d.ID))

	events, err := s.Void().ListVoidEvents(ctx, otherScope, "")
	require.NoError(t, err)
	assert.Empty(t, events, "a cross-scope void attempt must not leave a tombstone record in the attacker's own scope either")
}

// TestVoid_UnknownIDIsNotFound -- the plain not-found path, and a second
// void of an already-voided id (which finds no current row).
func TestVoid_UnknownIDIsNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)
	fx := newVoidFixture(t, ctx, s, db)

	require.ErrorIs(t, s.Void().VoidFeature(ctx, fx.scopeID, uuid.New(), nil, voidActor(), voidActor()), store.ErrNotFound)

	require.NoError(t, s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor()))
	err := s.Void().VoidFeature(ctx, fx.scopeID, fx.feature.ID, nil, voidActor(), voidActor())
	require.ErrorIs(t, err, store.ErrNotFound, "voiding twice must find no current row the second time")

	events, err := s.Void().ListVoidEvents(ctx, fx.scopeID, "")
	require.NoError(t, err)
	assert.Len(t, events, 1, "the refused second void must not append a second tombstone record")
}

// TestVoid_AllSevenKindsTombstone is the breadth check: the generic
// voidEntity really does serve every kind, including the four leaves whose
// product resolution differs (a Product is its own product; a Requirement
// reaches its product through two joins).
func TestVoid_AllSevenKindsTombstone(t *testing.T) {
	ctx := context.Background()
	s, db := newVoidTestStore(t)

	// A second scope with a completely empty tree, so no kind has a live
	// child to refuse on.
	scopeID := newVoidScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "vision")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	f, err := s.Features().Create(ctx, scopeID, fs.ID, "F", nil)
	require.NoError(t, err)
	r, err := s.Requirements().Create(ctx, scopeID, f.ID, store.RequirementKindFR, "FR", nil)
	require.NoError(t, err)
	p, err := s.Personas().Create(ctx, scopeID, product.ID, "P", nil)
	require.NoError(t, err)
	ng, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "NG", nil)
	require.NoError(t, err)
	d, err := s.Decisions().Create(ctx, scopeID, fs.ID, "LB", nil)
	require.NoError(t, err)

	for _, tc := range []struct {
		kind store.VoidedEntityKind
		id   uuid.UUID
		void func() error
	}{
		// The leaf first: a Feature carrying a live Requirement is correctly
		// refused, so the chain has to be voided bottom-up here too.
		{store.VoidedRequirement, r.ID, func() error { return s.Void().VoidRequirement(ctx, scopeID, r.ID, nil, voidActor(), voidActor()) }},
		{store.VoidedFeature, f.ID, func() error { return s.Void().VoidFeature(ctx, scopeID, f.ID, nil, voidActor(), voidActor()) }},
		{store.VoidedPersona, p.ID, func() error { return s.Void().VoidPersona(ctx, scopeID, p.ID, nil, voidActor(), voidActor()) }},
		{store.VoidedNonGoal, ng.ID, func() error { return s.Void().VoidNonGoal(ctx, scopeID, ng.ID, nil, voidActor(), voidActor()) }},
		{store.VoidedLoadBearingDecision, d.ID, func() error {
			return s.Void().VoidLoadBearingDecision(ctx, scopeID, d.ID, nil, voidActor(), voidActor())
		}},
		{store.VoidedFeatureSet, fs.ID, func() error { return s.Void().VoidFeatureSet(ctx, scopeID, fs.ID, nil, voidActor(), voidActor()) }},
		{store.VoidedProduct, product.ID, func() error { return s.Void().VoidProduct(ctx, scopeID, product.ID, nil, voidActor(), voidActor()) }},
	} {
		require.NoError(t, tc.void(), "voiding a %s must succeed", tc.kind)
	}

	events, err := s.Void().ListVoidEvents(ctx, scopeID, "")
	require.NoError(t, err)
	require.Len(t, events, 7, "all seven kinds must be recorded")
	byKind := map[store.VoidedEntityKind]store.VoidEvent{}
	for _, e := range events {
		byKind[e.EntityKind] = e
	}
	for _, kind := range []store.VoidedEntityKind{
		store.VoidedProduct, store.VoidedFeatureSet, store.VoidedFeature,
		store.VoidedRequirement, store.VoidedPersona, store.VoidedNonGoal,
		store.VoidedLoadBearingDecision,
	} {
		e, ok := byKind[kind]
		require.True(t, ok, "no void_event recorded for %s", kind)
		assert.Equal(t, product.ID, e.ProductID,
			"every kind must resolve to the SAME product -- a wrong product_id would file the retirement against the wrong numbering sequence")
	}
	// The two display-number-bearing kinds retired a number; the rest did not.
	for _, kind := range []store.VoidedEntityKind{store.VoidedFeature, store.VoidedLoadBearingDecision} {
		assert.NotNil(t, byKind[kind].RetiredDisplayNumber, "%s carries a display number and must retire it", kind)
	}
	for _, kind := range []store.VoidedEntityKind{store.VoidedProduct, store.VoidedFeatureSet, store.VoidedRequirement, store.VoidedPersona, store.VoidedNonGoal} {
		assert.Nil(t, byKind[kind].RetiredDisplayNumber, "%s has no stored display number and must record NULL", kind)
	}
}
