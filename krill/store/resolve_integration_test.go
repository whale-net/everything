//go:build integration

// Real-Postgres coverage for ResolveStore (resolve.go, FR d0021a0f /
// FR 19123858): settling a `deferred` Non-Goal as PROMOTE or RETIRE. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" tag, and void_integration_test.go for the
// fixture and tracer pattern -- each go_test target here compiles only the
// srcs BUILD.bazel lists for it, so this file provisions its own store.
//
// The assertions are shaped to fail for the reasons this milestone has
// actually shipped defects, not merely to pass:
//
//   - Refusal PLACEMENT is checked with a pgx query tracer, not by
//     asserting "no row was written". A `deferred` check that ran AFTER
//     the closing UPDATE and then rolled back would satisfy a "writes
//     nothing" assertion while still being wrong, so these assert that no
//     write statement is ever ISSUED on a refused call.
//
//   - Every claim about what a resolution preserves is asserted on the
//     specific field. A promote carries name, body, position, product_id
//     and id forward; a test that checked only the id would pass even if
//     the body were dropped, and "its body preserved" is half the
//     requirement.
//
//   - The two outcomes are asserted to DIFFER, not merely to succeed. A
//     retire that silently promoted, or a promote that silently retired,
//     would satisfy any test that only checks "resolve worked".
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:resolve_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

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
// the closing UPDATE would still have sent that UPDATE, and a rollback
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

// writesUnder reports every write statement targeting table -- an INSERT
// or an UPDATE. Reads (SELECT ... FOR UPDATE) are deliberately not counted:
// a resolve must lock and probe before it writes, and that is correct.
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
		// table name anywhere: a `SELECT ... FROM non_goal` read is not a
		// write, and matching the bare name would count resolve's own
		// locking read as the closing UPDATE.
		t := strings.ToUpper(table)
		if strings.Contains(upper, "UPDATE "+t+" ") || strings.Contains(upper, "INTO "+t+" ") {
			writes = append(writes, sql)
		}
	}
	return writes
}

func newResolveTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
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

// newTracedResolveStore returns a second *store.Store over the same
// database whose pool records every statement it issues, for the
// placement tests.
func newTracedResolveStore(t *testing.T, db *dbtest.Postgres) (*store.Store, *statementTracer) {
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

func newResolveScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "resolve-scope-"+uuid.NewString()).Scan(&id))
	return id
}

// resolveFixture is one product carrying a `deferred` and a `permanent`
// Non-Goal, plus the values the promote-preservation assertions check
// against. The two rows are separate ids, so a test that means to resolve
// the `deferred` one can never be served the `permanent` one by accident
// -- which is the mistake that made three of the void task's subtests
// permanently green.
type resolveFixture struct {
	scopeID   uuid.UUID
	product   store.Product
	deferred  store.NonGoal
	permanent store.NonGoal
}

var resolveBody = "deliberately held back at authoring time"

func newResolveFixture(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres) resolveFixture {
	t.Helper()
	scopeID := newResolveScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "vision")
	require.NoError(t, err)
	deferred, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindDeferred, "Cross-product decisions", &resolveBody)
	require.NoError(t, err)
	permanent, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "A standing rule", nil)
	require.NoError(t, err)
	return resolveFixture{scopeID: scopeID, product: product, deferred: deferred, permanent: permanent}
}

func resolveActor() store.Subject {
	return store.Subject{Iss: "whale_net", Sub: "alex", Kind: store.SubjectKindHuman}
}

func resolveMistakeReason() *string {
	reason := "a standing rule that turned out to be a mistake"
	return &reason
}

func resolveReason() *string {
	reason := "settled at signoff"
	return &reason
}

// nonGoalRevisionCount counts every row the id has ever had, current or
// closed. This is the number that separates the two outcomes: a promote
// makes it 2, a retire leaves it at 1.
func nonGoalRevisionCount(t *testing.T, ctx context.Context, db *dbtest.Postgres, id uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM non_goal WHERE id = $1`, id).Scan(&n))
	return n
}

func isNonGoalCurrent(t *testing.T, ctx context.Context, db *dbtest.Postgres, id uuid.UUID) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM non_goal WHERE id = $1 AND valid_to IS NULL)`, id).Scan(&exists))
	return exists
}

func currentNonGoals(t *testing.T, ctx context.Context, s *store.Store, productID uuid.UUID) []store.NonGoal {
	t.Helper()
	list, err := s.NonGoals().ListCurrentByProduct(ctx, productID)
	require.NoError(t, err)
	return list
}

func findNonGoal(list []store.NonGoal, id uuid.UUID) (store.NonGoal, bool) {
	for _, ng := range list {
		if ng.ID == id {
			return ng, true
		}
	}
	return store.NonGoal{}, false
}

// ---------------------------------------------------------------------------
// PROMOTE -- close + successor, re-kinded (FR d0021a0f)
// ---------------------------------------------------------------------------

// TestResolveNonGoal_PromoteOpensASuccessorUnderTheSameID is the promote
// half of the LB3 claim. It asserts the closed row and the successor
// SEPARATELY, because "the row is not the one we started with" would also
// be true of a retire, and "the id still has a current row" would also be
// true of an amend. The combination -- same id, one closed predecessor,
// kind changed -- is what only a promote produces.
func TestResolveNonGoal_PromoteOpensASuccessorUnderTheSameID(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	promoted, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, resolveReason(), resolveActor(), resolveActor())
	require.NoError(t, err)

	assert.Equal(t, 2, nonGoalRevisionCount(t, ctx, db, fx.deferred.ID),
		"a promote must insert exactly one successor: the id's row count goes 1 -> 2, where a retire would leave it at 1")
	assert.True(t, isNonGoalCurrent(t, ctx, db, fx.deferred.ID),
		"a promote leaves a current row -- that is what distinguishes it from void and retire, which leave none")
	assert.Equal(t, store.NonGoalKindPermanent, promoted.Kind, "the successor's kind is the whole point of a promote")
	assert.Equal(t, fx.deferred.ID, promoted.ID, "the successor is inserted under the SAME surrogate id (LB2), so a citation already rendered for the deferred row still resolves")

	// The predecessor is closed, not deleted, and still carries the row as
	// it was -- the audit-readable half of the SCD2 shape.
	var (
		closedValidTo *string
		closedKind    string
		closedName    string
	)
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT valid_to::text, kind, name FROM non_goal WHERE id = $1 AND valid_to IS NOT NULL`, fx.deferred.ID).
		Scan(&closedValidTo, &closedKind, &closedName))
	require.NotNil(t, closedValidTo, "the promote must set valid_to on the predecessor, not remove it")
	assert.Equal(t, store.NonGoalKindDeferred, closedKind, "the closed revision keeps the kind it had -- a promote changes the successor, never the record of what was there")
	assert.Equal(t, fx.deferred.Name, closedName)
}

// TestResolveNonGoal_PromotePreservesEverythingButTheKind is the "its body
// preserved" half of FR d0021a0f, asserted field by field. A promote that
// dropped the body, reset the position, or reparented the row would satisfy
// the test above, which is why this one exists: it names each carried
// field rather than trusting the INSERT's argument list to be right.
func TestResolveNonGoal_PromotePreservesEverythingButTheKind(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	// The row this test promotes is deliberately NOT the fixture's first
	// Non-Goal. `fx.deferred` sits at position 0, and a successor that
	// reset `position` to its default would then match it exactly -- the
	// assertion would be unfalsifiable. `moved` is created second, so its
	// position is non-zero and a reset is visible.
	moved, err := s.NonGoals().Create(ctx, fx.scopeID, fx.product.ID, store.NonGoalKindDeferred, "Second deferred", &resolveBody)
	require.NoError(t, err)
	require.NotZero(t, moved.Position,
		"this test is only meaningful if the promoted row's position is non-zero -- a zero would coincide with the default a buggy successor would take")

	promoted, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, moved.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)

	assert.Equal(t, moved.Name, promoted.Name, "a promote changes what the Non-Goal asserts, not which Non-Goal it is")
	require.NotNil(t, promoted.Body, "the fixture seeds a non-NULL body; a promote that dropped it would make this nil")
	assert.Equal(t, resolveBody, *promoted.Body, "FR d0021a0f requires the body be preserved across the re-kind")
	assert.Equal(t, fx.product.ID, promoted.ProductID, "a promote never reparents (FR f0f6bc18)")
	assert.Equal(t, moved.Position, promoted.Position, "position is carried forward, not reissued")
	assert.Equal(t, fx.scopeID, promoted.ScopeID)
	assert.Equal(t, moved.ID, promoted.ID, "the successor keeps the same surrogate id, so the assertion above is about a re-kind rather than about a fresh row")

	// And the promoted row is a genuinely new physical revision, not the
	// closed one re-read: same id, different revision_id.
	var closedRevisionID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT revision_id FROM non_goal WHERE id = $1 AND valid_to IS NOT NULL`, fx.deferred.ID).Scan(&closedRevisionID))
	assert.NotEqual(t, promoted.RevisionID, closedRevisionID,
		"the successor must be a new row, not the closed predecessor re-read -- equal revision ids would mean the SCD2 interval was never actually rolled")
}

// TestResolveNonGoal_PromoteDoesNotGoThroughTheAmendVerb pins the claim
// that makes promote a separate verb at all. AmendNonGoal carries `kind`
// forward, so routing a promote through it would leave the row `deferred`
// and accomplish nothing; the test states that directly rather than
// assuming the two verbs cannot drift into each other.
func TestResolveNonGoal_PromoteDoesNotGoThroughTheAmendVerb(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	amended, err := s.Amend().AmendNonGoal(ctx, fx.deferred.ID, "Renamed", nil)
	require.NoError(t, err)
	assert.Equal(t, store.NonGoalKindDeferred, amended.Kind,
		"amend must NOT re-kind -- this is the guarantee (FR f0f6bc18) that forces promote to be its own verb, and it is what would break if promote were routed through amend")

	promoted, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)
	assert.Equal(t, store.NonGoalKindPermanent, promoted.Kind, "promote IS the verb that re-kinds")
	assert.Equal(t, "Renamed", promoted.Name, "and it composes with the amend that came before it, rather than replacing it")
}

// ---------------------------------------------------------------------------
// RETIRE -- close without successor, reusing void (FR d0021a0f, d38d726e)
// ---------------------------------------------------------------------------

// TestResolveNonGoal_RetireOpensNoSuccessor is the retire half. It asserts
// both halves separately for the same reason the promote test does: "the
// row is not current" alone would also be true of a promote's predecessor,
// and the row COUNT is the only thing that separates retire from amend.
func TestResolveNonGoal_RetireOpensNoSuccessor(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolveRetire, resolveReason(), resolveActor(), resolveActor())
	require.NoError(t, err)

	assert.Equal(t, 1, nonGoalRevisionCount(t, ctx, db, fx.deferred.ID),
		"a retire must insert NO successor -- the id's row count stays at 1. A promote would make this 2; that difference IS the LB3 distinction")
	assert.False(t, isNonGoalCurrent(t, ctx, db, fx.deferred.ID),
		"a retired Non-Goal has no current row, which is what removes it from every current read and from render")

	// Still physically present, audit-readable under its original id
	// (FR d38d726e (c)), carrying the row exactly as it was.
	var (
		gotID   uuid.UUID
		gotKind string
		gotName string
		gotBody *string
	)
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT id, kind, name, body FROM non_goal WHERE id = $1`, fx.deferred.ID).Scan(&gotID, &gotKind, &gotName, &gotBody))
	assert.Equal(t, fx.deferred.ID, gotID, "the tombstone keeps its original surrogate id (LB2)")
	assert.Equal(t, store.NonGoalKindDeferred, gotKind)
	assert.Equal(t, fx.deferred.Name, gotName)
	require.NotNil(t, gotBody, "a tombstone must retain the body it was authored with -- an auditor reads the retired row, not a summary of it")
	assert.Equal(t, resolveBody, *gotBody)
}

// TestResolveNonGoal_TheTwoOutcomesProduceDifferentRows is the pairwise
// claim, kept separate from the two tests above on purpose. Each of those
// would still pass if the other outcome were substituted for its own, so
// only a test that runs both and compares can catch a retire that silently
// promoted or vice versa.
func TestResolveNonGoal_TheTwoOutcomesProduceDifferentRows(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	other, err := s.NonGoals().Create(ctx, fx.scopeID, fx.product.ID, store.NonGoalKindDeferred, "Second deferred", &resolveBody)
	require.NoError(t, err)

	_, err = s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)
	_, err = s.Resolve().ResolveNonGoal(ctx, fx.scopeID, other.ID, store.ResolveRetire, nil, resolveActor(), resolveActor())
	require.NoError(t, err)

	assert.Equal(t, 2, nonGoalRevisionCount(t, ctx, db, fx.deferred.ID), "promote: closed predecessor + successor")
	assert.Equal(t, 1, nonGoalRevisionCount(t, ctx, db, other.ID), "retire: closed predecessor only")

	// A later genuine void of the promoted Non-Goal must still be
	// possible. It would NOT be if promote had written a void_event row:
	// that table's (scope, kind, entity_id) unique index means "voided at
	// most once, ever", and a promote-time entry would permanently consume
	// it. This is the concrete failure behind promote's own register.
	require.NoError(t, s.Void().VoidNonGoal(ctx, fx.scopeID, fx.deferred.ID, resolveMistakeReason(), resolveActor(), resolveActor()),
		"a promoted Non-Goal must remain voidable -- a promote records no tombstone, so void_event's at-most-once index is still free for the real void")
}

// ---------------------------------------------------------------------------
// The `deferred`-only check, and its placement
// ---------------------------------------------------------------------------

// TestResolveNonGoal_RefusesAPermanentNonGoal is FR d0021a0f's last
// sentence. It runs BOTH outcomes against a `permanent` row: a guard that
// only covered one branch would leave the other writing, which is exactly
// the permanently-green-subtest shape this milestone has shipped before.
func TestResolveNonGoal_RefusesAPermanentNonGoal(t *testing.T) {
	for _, outcome := range []store.ResolveOutcome{store.ResolvePromote, store.ResolveRetire} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			s, db := newResolveTestStore(t)
			fx := newResolveFixture(t, ctx, s, db)

			_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.permanent.ID, outcome, nil, resolveActor(), resolveActor())

			require.Error(t, err, "a `permanent` Non-Goal is the terminal state of both outcomes -- there is nothing left to resolve")
			assert.ErrorIs(t, err, store.ErrNotDeferred)
			assert.Contains(t, err.Error(), "permanent",
				"the refusal must name the kind it actually found, or a caller cannot tell 'already resolved' from 'never deferred'")
			assert.Contains(t, err.Error(), fx.permanent.ID.String(), "and it must name the row, so the caller knows which Non-Goal was refused")

			// Unchanged, and the `deferred` sibling is still resolvable --
			// so the guard fired on the KIND, not on the id or the fixture.
			assert.True(t, isNonGoalCurrent(t, ctx, db, fx.permanent.ID), "a refused resolve must leave the row exactly as it found it")
			promoted, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
			require.NoError(t, err, "the fixture's `deferred` sibling must still resolve -- if this fails, the guard is rejecting the fixture rather than the kind")
			assert.Equal(t, store.NonGoalKindPermanent, promoted.Kind)
		})
	}
}

// TestResolveNonGoal_TheDeferredCheckWritesNothing checks the guard's
// PLACEMENT with a pgx tracer rather than with "no row was written". A
// check that ran after the closing UPDATE and then rolled back would
// satisfy a writes-nothing assertion while still being misordered -- the
// row would have been briefly observable half-closed, and a concurrent
// reader in that window would have seen a Non-Goal that existed in no
// state. This asserts no write statement is ever ISSUED.
func TestResolveNonGoal_TheDeferredCheckWritesNothing(t *testing.T) {
	for _, outcome := range []store.ResolveOutcome{store.ResolvePromote, store.ResolveRetire} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			s, db := newResolveTestStore(t)
			traced, tracer := newTracedResolveStore(t, db)
			fx := newResolveFixture(t, ctx, s, db)

			_, err := traced.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.permanent.ID, outcome, nil, resolveActor(), resolveActor())
			require.Error(t, err)

			assert.Empty(t, tracer.writesUnder("non_goal"),
				"a refused resolve must not even ISSUE the closing UPDATE to non_goal -- checking after the write would leave a window in which the row is half-closed")
			assert.Empty(t, tracer.writesUnder("non_goal_promotion"),
				"and it must not record a promotion for a resolution that did not happen")
			assert.Empty(t, tracer.writesUnder("void_event"),
				"nor a tombstone: a refused retire must not leave a void_event row an auditor would read as a real retirement")
		})
	}
}

// TestResolveNonGoal_CheckPrecedesTheWrite is the same placement claim from
// the other direction: a traced resolve that IS allowed to proceed must
// have issued its check before its first write. Recorded in statement
// order, so a guard that had been moved below the UPDATE would flip the
// two indices.
func TestResolveNonGoal_CheckPrecedesTheWrite(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	traced, tracer := newTracedResolveStore(t, db)
	fx := newResolveFixture(t, ctx, s, db)

	_, err := traced.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)

	checkAt, closeAt := -1, -1
	for i, sql := range tracer.seen {
		upper := strings.ToUpper(strings.Join(strings.Fields(sql), " "))
		if checkAt < 0 && strings.Contains(upper, "FROM NON_GOAL") && strings.Contains(upper, "FOR UPDATE") {
			checkAt = i
		}
		if closeAt < 0 && strings.Contains(upper, "UPDATE NON_GOAL SET VALID_TO") {
			closeAt = i
		}
	}
	require.NotEqual(t, -1, checkAt, "the deferred check must be a locking read of the current row")
	require.NotEqual(t, -1, closeAt, "the promote must close the current row")
	assert.Less(t, checkAt, closeAt, "the `deferred` check must be ISSUED before the closing UPDATE, not after it with a rollback to hide behind")
}

// ---------------------------------------------------------------------------
// FR 19123858 -- visibility until resolved, and history after
// ---------------------------------------------------------------------------

// TestResolveNonGoal_DeferredIsVisibleUntilResolved pins the first half
// of FR 19123858 against the real current-read query, not against the
// store's own by-id accessor -- the slice read is what render is built on
// and it is separate SQL with its own predicates.
func TestResolveNonGoal_DeferredIsVisibleUntilResolved(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	before := currentNonGoals(t, ctx, s, fx.product.ID)
	got, ok := findNonGoal(before, fx.deferred.ID)
	require.True(t, ok, "an unresolved `deferred` Non-Goal must appear in the current Non-Goal read -- it is rendered in the brief's deferred bucket")
	assert.Equal(t, store.NonGoalKindDeferred, got.Kind)

	promoted, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)

	after := currentNonGoals(t, ctx, s, fx.product.ID)
	got, ok = findNonGoal(after, fx.deferred.ID)
	require.True(t, ok, "a promoted Non-Goal is still current -- it was settled, not dropped")
	assert.Equal(t, store.NonGoalKindPermanent, got.Kind,
		"and it leaves the `deferred` set, which is what the rendered brief's deferred bucket is filtered on (krill/render filters on Kind)")
	assert.Equal(t, promoted.Kind, got.Kind, "the read must agree with what the verb returned")

	// The sibling the test never touched must be unaffected.
	_, ok = findNonGoal(after, fx.permanent.ID)
	assert.True(t, ok, "resolve targets exactly one Non-Goal and must never disturb its neighbours")
	assert.Len(t, after, 2, "no current Non-Goal is created or dropped by a promote")
}

// TestResolveNonGoal_RetireRemovesItFromTheCurrentRead is the retire half
// of the same claim. Promote and retire reach "no longer a current
// `deferred` Non-Goal" by opposite mechanisms, so they need opposite
// assertions: one keeps the row and changes its kind, the other removes
// the row entirely.
func TestResolveNonGoal_RetireRemovesItFromTheCurrentRead(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolveRetire, nil, resolveActor(), resolveActor())
	require.NoError(t, err)

	after := currentNonGoals(t, ctx, s, fx.product.ID)
	_, ok := findNonGoal(after, fx.deferred.ID)
	assert.False(t, ok, "a retired Non-Goal must be absent from the current read -- neither bucket of the rendered brief can show it")
	assert.Len(t, after, 1, "only the retired row is gone")
	_, ok = findNonGoal(after, fx.permanent.ID)
	assert.True(t, ok, "and the sibling is untouched")
}

// TestResolveNonGoal_RetireRecordsOutcomeActorAndTimestamp is FR 19123858's
// history half on the retire side, asserted against void_event -- the
// register the retire reuses. The point is that the row is TELLABLE from
// retire: outcome, actor, and a real timestamp, all read back rather than
// assumed from the arguments that were passed in.
func TestResolveNonGoal_RetireRecordsOutcomeActorAndTimestamp(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	before := timeNow(t, ctx, db, "non_goal", fx.deferred.ID)
	_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolveRetire, resolveReason(), resolveActor(), resolveActor())
	require.NoError(t, err)

	events, err := s.Void().ListVoidEvents(ctx, fx.scopeID, store.VoidedNonGoal)
	require.NoError(t, err)
	require.Len(t, events, 1, "a retire records exactly one tombstone")

	e := events[0]
	assert.Equal(t, store.VoidOutcomeRetire, e.Outcome, "the register must distinguish a settle from a mistaken create -- both leave the same shape")
	assert.Equal(t, fx.deferred.ID, e.EntityID, "under the row's ORIGINAL id, which is the only way an auditor finds it (LB2)")
	assert.Equal(t, fx.product.ID, e.ProductID)
	assert.Equal(t, resolveActor().Iss, e.CreatedByActing.Iss, "FR 19123858 requires the actor in the row's history")
	assert.Equal(t, resolveActor().Sub, e.CreatedByActing.Sub)
	assert.Equal(t, store.SubjectKindHuman, e.CreatedByActing.Kind)
	assert.Equal(t, resolveActor(), e.CreatedByOnBehalfOf, "both LB4 subjects are recorded, not just the acting one")
	require.NotNil(t, e.Reason)
	assert.Equal(t, "settled at signoff", *e.Reason)
	assert.True(t, e.CreatedAt.After(before),
		"FR 19123858 requires a timestamp, and it must be this resolution's -- a zero time or one predating the resolve would satisfy a mere non-nil check")
}

// TestVoidEvent_OutcomeTellsAVoidFromARetire is the reason the column
// exists. Both rows are indistinguishable in the SCD2 table, so without
// `outcome` an auditor reading a tombstone would have to guess whether
// the Non-Goal was retracted or had been settled -- and the two want
// opposite follow-up actions.
func TestVoidEvent_OutcomeTellsAVoidFromARetire(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	// Row 1: a `deferred` Non-Goal settled. Row 2: a `permanent` one that
	// was simply a mistake. Same table, same shape, different intent.
	_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolveRetire, nil, resolveActor(), resolveActor())
	require.NoError(t, err)
	require.NoError(t, s.Void().VoidNonGoal(ctx, fx.scopeID, fx.permanent.ID, nil, resolveActor(), resolveActor()))

	events, err := s.Void().ListVoidEvents(ctx, fx.scopeID, store.VoidedNonGoal)
	require.NoError(t, err)
	require.Len(t, events, 2)

	byEntity := map[uuid.UUID]store.VoidEventOutcome{}
	for _, e := range events {
		byEntity[e.EntityID] = e.Outcome
	}
	assert.Equal(t, store.VoidOutcomeRetire, byEntity[fx.deferred.ID], "the settled Non-Goal records `retire`")
	assert.Equal(t, store.VoidOutcomeVoid, byEntity[fx.permanent.ID], "the mistaken create records `void`")
	assert.NotEqual(t, byEntity[fx.deferred.ID], byEntity[fx.permanent.ID],
		"the two must be distinguishable at all -- an equal pair would mean the column is being written from the wrong constant")
}

// TestResolveNonGoal_PromoteRecordsOutcomeActorAndTimestamp is the promote
// half of FR 19123858's history claim, against the promote register.
func TestResolveNonGoal_PromoteRecordsOutcomeActorAndTimestamp(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	before := timeNow(t, ctx, db, "non_goal", fx.deferred.ID)
	_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, resolveReason(), resolveActor(), resolveActor())
	require.NoError(t, err)

	promotions, err := s.Resolve().ListNonGoalPromotions(ctx, fx.scopeID, nil)
	require.NoError(t, err)
	require.Len(t, promotions, 1, "a promote records exactly one history row")

	p := promotions[0]
	assert.Equal(t, fx.deferred.ID, p.NonGoalID, "under the row's original id")
	assert.Equal(t, fx.product.ID, p.ProductID)
	assert.Equal(t, store.NonGoalKindDeferred, p.FromKind)
	assert.Equal(t, store.NonGoalKindPermanent, p.ToKind, "the register records WHICH way the row moved, which the SCD2 row alone cannot")
	assert.Equal(t, resolveActor().Iss, p.CreatedByActing.Iss, "FR 19123858 requires the actor")
	assert.Equal(t, resolveActor().Sub, p.CreatedByActing.Sub)
	assert.Equal(t, store.SubjectKindHuman, p.CreatedByActing.Kind)
	assert.Equal(t, resolveActor(), p.CreatedByOnBehalfOf)
	require.NotNil(t, p.Reason)
	assert.Equal(t, "settled at signoff", *p.Reason)
	assert.True(t, p.CreatedAt.After(before), "and a timestamp that belongs to THIS resolution")

	// A promote must NOT leave a tombstone: the row is still current, so a
	// void_event entry would be a false record an auditor would act on.
	events, err := s.Void().ListVoidEvents(ctx, fx.scopeID, store.VoidedNonGoal)
	require.NoError(t, err)
	assert.Empty(t, events, "a promote writes no void_event row -- the Non-Goal was not tombstoned")
}

// TestListNonGoalPromotions_FiltersByProduct covers the audit read's one
// filter. An unexercised filter is a filter nobody has checked, and the
// ProductID pointer is exactly the kind of optional argument that silently
// stops narrowing.
func TestListNonGoalPromotions_FiltersByProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	otherProduct, err := s.Products().Create(ctx, fx.scopeID, "Other", "vision")
	require.NoError(t, err)
	otherDeferred, err := s.NonGoals().Create(ctx, fx.scopeID, otherProduct.ID, store.NonGoalKindDeferred, "Elsewhere", nil)
	require.NoError(t, err)

	_, err = s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)
	_, err = s.Resolve().ResolveNonGoal(ctx, fx.scopeID, otherDeferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)

	all, err := s.Resolve().ListNonGoalPromotions(ctx, fx.scopeID, nil)
	require.NoError(t, err)
	assert.Len(t, all, 2, "an omitted product_id must mean every product, not none")

	scoped, err := s.Resolve().ListNonGoalPromotions(ctx, fx.scopeID, &fx.product.ID)
	require.NoError(t, err)
	require.Len(t, scoped, 1, "a supplied product_id must actually narrow")
	assert.Equal(t, fx.deferred.ID, scoped[0].NonGoalID)

	empty, err := s.Resolve().ListNonGoalPromotions(ctx, fx.scopeID, &uuid.Nil)
	require.NoError(t, err)
	assert.Empty(t, empty, "and a product with no promotions must return none rather than everything")
}

// ---------------------------------------------------------------------------
// The two outcomes really do differ, observably
// ---------------------------------------------------------------------------

// TestResolveNonGoal_OnlyRetireFreesTheName is the behavioural
// difference between the outcomes that a caller can act on. Both close the
// current row, so both would pass a "the row is closed" assertion; only
// one leaves the name free for a later create, because only one opens a
// successor to keep occupying it.
func TestResolveNonGoal_OnlyRetireFreesTheName(t *testing.T) {
	ctx := context.Background()

	t.Run("retire frees the name", func(t *testing.T) {
		s, db := newResolveTestStore(t)
		fx := newResolveFixture(t, ctx, s, db)

		_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolveRetire, nil, resolveActor(), resolveActor())
		require.NoError(t, err)

		reused, err := s.NonGoals().Create(ctx, fx.scopeID, fx.product.ID, store.NonGoalKindPermanent, fx.deferred.Name, nil)
		require.NoError(t, err, "every name index is partial on `valid_to IS NULL`, so a tombstone stops occupying its name")
		assert.Equal(t, fx.deferred.Name, reused.Name)
	})

	t.Run("promote keeps the name", func(t *testing.T) {
		s, db := newResolveTestStore(t)
		fx := newResolveFixture(t, ctx, s, db)

		_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
		require.NoError(t, err)

		_, err = s.NonGoals().Create(ctx, fx.scopeID, fx.product.ID, store.NonGoalKindPermanent, fx.deferred.Name, nil)
		assert.Error(t, err, "a promote leaves a CURRENT row carrying the name, so a second Non-Goal may not claim it -- the name index is partial on `valid_to IS NULL` and this row is current")
	})
}

// TestResolveNonGoal_RetireInheritsTheDeliveredRefusal covers the
// documented asymmetry between the outcomes. A retire tombstones the row
// and would orphan a milestone's Delivers reference, so it inherits
// void's delivery refusal (FR 2a3a8eef) and names promote as the way out.
// A promote cannot orphan anything -- the id survives -- so it is allowed,
// and that difference is why the refusal is not lifted for retire.
func TestResolveNonGoal_RetireInheritsTheDeliveredRefusal(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	delivered, err := s.NonGoals().Create(ctx, fx.scopeID, fx.product.ID, store.NonGoalKindDeferred, "Delivered deferred", &resolveBody)
	require.NoError(t, err)
	seedDelivery(t, ctx, db, fx.scopeID, fx.product.ID, delivered.ID)

	_, err = s.Resolve().ResolveNonGoal(ctx, fx.scopeID, delivered.ID, store.ResolveRetire, nil, resolveActor(), resolveActor())
	require.Error(t, err, "retiring a delivered Non-Goal would leave the Delivers pointing at a row no current read can see")
	assert.ErrorIs(t, err, store.ErrEntityDelivered, "the refusal comes from void's own check -- the retire path reuses the close rather than reimplementing it")
	assert.True(t, isNonGoalCurrent(t, ctx, db, delivered.ID), "and a refused retire writes nothing")

	promoted, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, delivered.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err, "promote keeps the id, so the delivery reference stays valid and needs no refusal")
	assert.Equal(t, store.NonGoalKindPermanent, promoted.Kind)
}

// ---------------------------------------------------------------------------
// Scope qualification and single-shot semantics
// ---------------------------------------------------------------------------

// TestResolveNonGoal_IsScopeQualified covers LB1 on both arguments. A guard
// that lost its scope_id on either side would leave this suite green while
// letting one scope resolve another scope's Non-Goal, or read another
// scope's promotion register.
func TestResolveNonGoal_IsScopeQualified(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	otherScope := newResolveScope(t, ctx, db)

	_, err := s.Resolve().ResolveNonGoal(ctx, otherScope, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.Error(t, err, "another scope's real id must be refused, not resolved")
	assert.ErrorIs(t, err, store.ErrNotFound, "and reported as not-found rather than as a kind refusal -- the row is perfectly resolvable, it is just not this caller's")
	assert.True(t, isNonGoalCurrent(t, ctx, db, fx.deferred.ID), "the owning scope's row must be untouched")

	_, err = s.Resolve().ResolveNonGoal(ctx, otherScope, fx.deferred.ID, store.ResolveRetire, nil, resolveActor(), resolveActor())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound, "retire is scope-qualified on the same lookup -- it runs void's close, which qualifies on both sides")
	assert.Equal(t, 1, nonGoalRevisionCount(t, ctx, db, fx.deferred.ID))

	// And the audit read must not cross scopes either.
	_, err = s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)
	foreign, err := s.Resolve().ListNonGoalPromotions(ctx, otherScope, nil)
	require.NoError(t, err)
	assert.Empty(t, foreign, "a scope's promotion register must not show another scope's promotions")
	own, err := s.Resolve().ListNonGoalPromotions(ctx, fx.scopeID, nil)
	require.NoError(t, err)
	assert.Len(t, own, 1)
}

// TestResolveNonGoal_ResolvingTwiceIsRefused is the single-shot claim. A
// second resolve of the same id must not promote twice: the second call
// finds the row `permanent` and is refused by the kind check, so the
// non_goal_promotion unique index is never what stops it.
func TestResolveNonGoal_ResolvingTwiceIsRefused(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.NoError(t, err)

	_, err = s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.Error(t, err, "a promote is terminal -- its successor is `permanent`, and only a `deferred` Non-Goal is resolvable")
	assert.ErrorIs(t, err, store.ErrNotDeferred)
	assert.Contains(t, err.Error(), "permanent")
	assert.Equal(t, 2, nonGoalRevisionCount(t, ctx, db, fx.deferred.ID), "exactly one successor, not two")

	promotions, err := s.Resolve().ListNonGoalPromotions(ctx, fx.scopeID, nil)
	require.NoError(t, err)
	assert.Len(t, promotions, 1, "and exactly one history row")
}

// TestResolveNonGoal_UnknownOutcomeIsRejected closes the two-outcome set.
// A caller that reaches for a third outcome -- or omits the field -- gets
// a named error rather than a silent default, because guessing between "the
// row survives" and "the row is tombstoned" is the one thing a resolution
// must never do.
func TestResolveNonGoal_UnknownOutcomeIsRejected(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)
	fx := newResolveFixture(t, ctx, s, db)

	for _, outcome := range []store.ResolveOutcome{"", "delete", "PROMOTE", "close"} {
		t.Run(string(outcome), func(t *testing.T) {
			_, err := s.Resolve().ResolveNonGoal(ctx, fx.scopeID, fx.deferred.ID, outcome, nil, resolveActor(), resolveActor())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "promote", "the error must name the two legal outcomes")
			assert.Contains(t, err.Error(), "retire")
			assert.True(t, isNonGoalCurrent(t, ctx, db, fx.deferred.ID), "an unrecognised outcome must not have written anything")
		})
	}
}

// TestResolveNonGoal_UnknownIDIsNotFound separates "no such row" from
// "wrong kind", so a caller retrying after a typo is not told the row is
// permanently unresolvable.
func TestResolveNonGoal_UnknownIDIsNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newResolveTestStore(t)

	// A scope with no rows at all, so the id genuinely does not exist --
	// a fixture that merely used a wrong id would let the kind check fire
	// first and mask the not-found path.
	emptyScope := newResolveScope(t, ctx, db)
	_, err := s.Resolve().ResolveNonGoal(ctx, emptyScope, uuid.New(), store.ResolvePromote, nil, resolveActor(), resolveActor())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound)
	assert.NotErrorIs(t, err, store.ErrNotDeferred, "a missing row is not a kind refusal -- the two need different messages")
}

// timeNow reads a row's valid_from, used as the "before" mark a history
// timestamp must beat. Reading a real column rather than calling
// time.Now() keeps the comparison on the same clock the database wrote.
func timeNow(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string, id uuid.UUID) time.Time {
	t.Helper()
	var at time.Time
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT valid_from FROM `+table+` WHERE id = $1`, id).Scan(&at))
	return at
}

// seedDelivery associates entityID with a milestone as a `delivers` row,
// which is what makes void's delivery refusal fire.
func seedDelivery(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID, productID, entityID uuid.UUID) {
	t.Helper()
	var milestoneID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name) VALUES ($1, $2, $3) RETURNING id
	`, scopeID, productID, "M-"+uuid.NewString()).Scan(&milestoneID))
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
		VALUES ($1, $2, $3, 'delivers')
	`, scopeID, entityID, milestoneID)
	require.NoError(t, err)
}
