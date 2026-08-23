//go:build integration

// Real-Postgres integration coverage for promotionRepo and writebackRepo
// (promotion.go, writeback.go): Promote/GetCurrent/GetPrevious/StateAt,
// the writeback outbox (Enqueue/ClaimBatch/MarkDone/MarkFailed), and
// ListPromotions/ListPromotionEvents pagination. See
// postgres_integration_helpers_test.go's doc comment for why this package
// builds these files under the "integration" tag, and TESTING.md for how
// to run them.
package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

func currentPromotionCount(t *testing.T, pool *pgxpool.Pool, environmentID, targetKey string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM promotion WHERE environment_id = $1 AND target_key = $2 AND valid_to IS NULL`,
		environmentID, targetKey).Scan(&n); err != nil {
		t.Fatalf("count current promotions: %v", err)
	}
	return n
}

// TestPromotion_CurrentIdxRejectsConcurrentCurrentRows proves the real
// partial unique index promotion_current_idx -- not application logic --
// makes two "current" rows for the same (environment_id, target_key)
// structurally impossible. PLAN.md's AR-2d carry-over note flags this
// exact index as deliberately deferred until the promotion table existed;
// this is that follow-up.
func TestPromotion_CurrentIdxRejectsConcurrentCurrentRows(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-promo-idx")
	artifactID := seedArtifact(t, pool, appID, buildID, "sha256:promo-idx", "v1.0.0")

	insert := func() error {
		_, err := pool.Exec(ctx, `
			INSERT INTO promotion (environment_id, target_key, artifact_id) VALUES ($1, $2, $3)`,
			envID, "image:acme-widget", artifactID)
		return err
	}

	if err := insert(); err != nil {
		t.Fatalf("first insert (should succeed, nothing current yet): %v", err)
	}
	err := insert()
	if err == nil {
		t.Fatalf("expected a second concurrent 'current' row for the same target to be rejected, got nil error")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got: %v (%T)", err, err)
	}
	if pgErr.Code != sqlStateUniqueViolation {
		t.Fatalf("expected SQLSTATE %s (unique_violation) from promotion_current_idx, got %s: %v", sqlStateUniqueViolation, pgErr.Code, err)
	}
	if n := currentPromotionCount(t, pool, envID, "image:acme-widget"); n != 1 {
		t.Fatalf("expected exactly 1 current row to survive, found %d", n)
	}
}

// seedArtifact inserts a minimal image artifact row directly, for tests that
// only need a valid artifact_id to hang a promotion off of.
// seedArtifact inserts a minimal, already-PUBLISHED image artifact row
// directly (bypassing the repository layer), for tests that only need a
// valid artifact_id to hang a promotion off of. state/provenance/
// version_source are NOT NULL as of migration 007 (AR-7b) -- state and
// version_source have no safe default (see that migration's comments), so
// every raw INSERT in this file must set them explicitly.
// seedArtifact seeds a 'published' image artifact directly. Promotability
// is no longer a column to seed (issue #833, migration 014) -- it is
// derived live from appID's deploy_unit on read; callers of this helper
// pass an appID created via seedApp(..., "image") when they need
// DerivePromotability(IMAGE, IMAGE) = PROMOTABLE. manifest_id is left
// NULL: these promotion/writeback tests don't exercise manifest
// attribution, only that an artifact row exists to promote.
func seedArtifact(t *testing.T, pool *pgxpool.Pool, appID, buildID, digest, version string) string {
	t.Helper()
	var artifactID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO artifact (kind, app_id, repository, version, digest, build_id, state, provenance, version_source)
		VALUES ('image', $1, 'ghcr.io/acme/widget', $2, $3, $4, 'published', 'observed', 'tag')
		RETURNING artifact_id`, appID, version, digest, buildID).Scan(&artifactID)
	if err != nil {
		t.Fatalf("seed artifact %s: %v", digest, err)
	}
	return artifactID
}

// TestPromotionRepo_PromoteTwice_ExactlyOneCurrentRow proves the SCD2
// close-and-open write end to end through the repository layer against real
// Postgres: promoting a second artifact for the same target closes the
// first row (valid_to set) and leaves exactly one row with valid_to IS
// NULL.
func TestPromotionRepo_PromoteTwice_ExactlyOneCurrentRow(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-promo-twice")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:promo-twice-1", "v1.0.0")
	art2 := seedArtifact(t, pool, appID, buildID, "sha256:promo-twice-2", "v2.0.0")

	targetKey := "image:acme-widget"
	first, superseded1, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art1})
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if superseded1 != nil {
		t.Fatalf("expected no superseded row on the first-ever promotion, got %+v", superseded1)
	}

	second, superseded2, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art2})
	if err != nil {
		t.Fatalf("second promote: %v", err)
	}
	if superseded2 == nil || superseded2.PromotionID != first.PromotionID {
		t.Fatalf("expected the second promote to supersede the first, got %+v", superseded2)
	}
	if superseded2.ValidTo == nil {
		t.Fatalf("expected the superseded row's valid_to to be set")
	}
	if second.ValidTo != nil {
		t.Fatalf("expected the new current row's valid_to to be nil, got %v", second.ValidTo)
	}

	if n := currentPromotionCount(t, pool, envID, targetKey); n != 1 {
		t.Fatalf("expected exactly 1 row with valid_to IS NULL after two promotions, found %d", n)
	}
	var closedCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM promotion WHERE environment_id = $1 AND target_key = $2 AND valid_to IS NOT NULL`,
		envID, targetKey).Scan(&closedCount); err != nil {
		t.Fatalf("count closed rows: %v", err)
	}
	if closedCount != 1 {
		t.Fatalf("expected exactly 1 closed (superseded) row, found %d", closedCount)
	}
}

// TestPromotionRepo_StateAt_HistoricalWindow proves the SCD2 "state at time
// T" window query (StateAt) against real Postgres: a timestamp between two
// promotions returns the first artifact, not the second -- the exact
// property GetEnvironmentState --at <T> depends on. Unlike the
// handler-level tests, this controls valid_from/valid_to directly via SQL
// so it isn't subject to wall-clock/second-resolution flakiness.
func TestPromotionRepo_StateAt_HistoricalWindow(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-stateat")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:stateat-1", "v1.0.0")
	art2 := seedArtifact(t, pool, appID, buildID, "sha256:stateat-2", "v2.0.0")
	targetKey := "image:acme-widget"

	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)         // v1 promoted
	t2 := t0.Add(2 * time.Hour)         // v1 superseded by v2
	between := t0.Add(90 * time.Minute) // strictly between t1 and t2

	var promo1ID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO promotion (environment_id, target_key, artifact_id, valid_from, valid_to)
		VALUES ($1, $2, $3, $4, $5) RETURNING promotion_id`,
		envID, targetKey, art1, t1, t2).Scan(&promo1ID); err != nil {
		t.Fatalf("seed historical promotion 1: %v", err)
	}
	var promo2ID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO promotion (environment_id, target_key, artifact_id, valid_from, valid_to)
		VALUES ($1, $2, $3, $4, NULL) RETURNING promotion_id`,
		envID, targetKey, art2, t2).Scan(&promo2ID); err != nil {
		t.Fatalf("seed current promotion 2: %v", err)
	}

	state, err := reg.Promotions().StateAt(ctx, envID, &between)
	if err != nil {
		t.Fatalf("StateAt(between): %v", err)
	}
	if len(state) != 1 || state[0].PromotionID != promo1ID || state[0].Digest != "sha256:stateat-1" {
		t.Fatalf("expected exactly promotion 1 (v1) live at %v, got %+v", between, state)
	}

	stateNow, err := reg.Promotions().StateAt(ctx, envID, nil)
	if err != nil {
		t.Fatalf("StateAt(now): %v", err)
	}
	if len(stateNow) != 1 || stateNow[0].PromotionID != promo2ID {
		t.Fatalf("expected current state to be promotion 2 (v2), got %+v", stateNow)
	}
}

// TestPromotionRepo_Rollback_RoundTrips proves GetPrevious + Promote --
// exactly what handlers.PromotionServer.Rollback composes -- round-trips
// correctly against real Postgres: rolling back after v1 -> v2 re-promotes
// v1 and supersedes v2.
func TestPromotionRepo_Rollback_RoundTrips(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-rollback")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:rollback-1", "v1.0.0")
	art2 := seedArtifact(t, pool, appID, buildID, "sha256:rollback-2", "v2.0.0")
	targetKey := "image:acme-widget"

	v1, _, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art1})
	if err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	v2, _, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art2})
	if err != nil {
		t.Fatalf("promote v2: %v", err)
	}

	previous, err := reg.Promotions().GetPrevious(ctx, envID, targetKey)
	if err != nil {
		t.Fatalf("GetPrevious: %v", err)
	}
	if previous.PromotionID != v1.PromotionID {
		t.Fatalf("expected GetPrevious to return v1's promotion %s, got %s", v1.PromotionID, previous.PromotionID)
	}

	rollback, supersededByRollback, err := promoteTx(t, reg, repository.Promotion{
		EnvironmentID: envID, TargetKey: targetKey, ArtifactID: previous.ArtifactID,
	})
	if err != nil {
		t.Fatalf("rollback promote: %v", err)
	}
	if rollback.ArtifactID != art1 {
		t.Fatalf("expected rollback to re-promote v1's artifact %s, got %s", art1, rollback.ArtifactID)
	}
	if supersededByRollback == nil || supersededByRollback.PromotionID != v2.PromotionID {
		t.Fatalf("expected rollback to supersede v2's promotion %s, got %+v", v2.PromotionID, supersededByRollback)
	}
	if n := currentPromotionCount(t, pool, envID, targetKey); n != 1 {
		t.Fatalf("expected exactly 1 current row after rollback, found %d", n)
	}
}

// TestPromotionRepo_Promote_TransactionAbortLeavesNoPartialWrite covers the
// hazard AGENTS.md and this phase's assignment both flag explicitly: a
// failed statement aborts the whole transaction, so the close half of
// close-and-open must not survive if the open half fails. Here the INSERT
// is forced to fail by pointing ArtifactID at a nonexistent artifact_id
// (violates the artifact_id FK) *after* a real prior promotion exists to
// close -- proving the earlier UPDATE (the close) does not commit on its
// own when the surrounding transaction as a whole rolls back.
func TestPromotionRepo_Promote_TransactionAbortLeavesNoPartialWrite(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-abort")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:abort-1", "v1.0.0")
	targetKey := "image:acme-widget"

	first, _, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art1})
	if err != nil {
		t.Fatalf("seed first promotion: %v", err)
	}

	_, _, err = promoteTx(t, reg, repository.Promotion{
		EnvironmentID: envID, TargetKey: targetKey, ArtifactID: "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatalf("expected the second promote (nonexistent artifact_id) to fail on the artifact_id FK, got nil error")
	}

	// The whole transaction -- both the UPDATE that closed `first` and the
	// failed INSERT -- must have rolled back together. If it didn't, `first`
	// would now show valid_to set with no new current row: a target
	// promoted once that now has ZERO current rows, exactly the partial
	// write PLAN.md and AGENTS.md warn about.
	if n := currentPromotionCount(t, pool, envID, targetKey); n != 1 {
		t.Fatalf("expected the original promotion to still be the sole current row after the aborted transaction, found %d current rows", n)
	}
	var validTo *time.Time
	if err := pool.QueryRow(ctx, `SELECT valid_to FROM promotion WHERE promotion_id = $1`, first.PromotionID).Scan(&validTo); err != nil {
		t.Fatalf("read back first promotion: %v", err)
	}
	if validTo != nil {
		t.Fatalf("expected the original promotion's valid_to to remain NULL after the aborted transaction, got %v", *validTo)
	}
}

// --- 7. writeback_outbox (AR-4b) ---------------------------------------

// promoteWithOutboxTx mirrors handlers.PromotionServer.Promote's real
// write path end to end: Promote (SCD2 close-and-open), RecordEvent, then
// Enqueue -- all inside one WithTx transaction, exactly like
// server/handlers/promotion.go's enqueueWriteback. forceBadEventID, when
// non-empty, is used as the outbox row's event_id instead of the real
// event's id, so the INSERT trips the event_id foreign key -- used by
// TestWriteback_EnqueueFailureRollsBackWholeTransaction below to prove the
// promotion does not survive when the outbox insert fails. domain is
// passed straight through to the Enqueue call, standing in for what
// enqueueWriteback's real ownerDomain lookup would resolve -- see
// 015_writeback_outbox_domain.up.sql.
func promoteWithOutboxTx(t *testing.T, reg *Registry, p repository.Promotion, domain, forceBadEventID string) (*repository.Promotion, *repository.WritebackOutbox, error) {
	t.Helper()
	var current *repository.Promotion
	var outbox *repository.WritebackOutbox
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var perr error
		current, _, perr = r.Promotions().Promote(ctx, p)
		if perr != nil {
			return perr
		}
		event, eerr := r.Promotions().RecordEvent(ctx, repository.PromotionEvent{
			PromotionID: current.PromotionID,
			Action:      repository.PromotionActionPromote,
			Actor:       "integration-test",
		})
		if eerr != nil {
			return eerr
		}
		eventID := event.EventID
		if forceBadEventID != "" {
			eventID = forceBadEventID
		}
		var oerr error
		outbox, oerr = r.Writeback().Enqueue(ctx, repository.WritebackOutbox{
			PromotionID:    current.PromotionID,
			EnvironmentID:  p.EnvironmentID,
			EnvironmentKey: p.EnvironmentKey,
			Domain:         domain,
			EventID:        eventID,
			StateHash:      "test-hash",
		})
		return oerr
	})
	return current, outbox, err
}

// TestWriteback_EnqueueCommitsAtomicallyWithPromotion proves the core
// AR-4b property end to end against real Postgres: a promotion and its
// outbox row are written by the same transaction and both are visible
// after commit, with the outbox row correctly linked back to the
// promotion.
func TestWriteback_EnqueueCommitsAtomicallyWithPromotion(t *testing.T) {
	reg, pool := newTestRegistry(t)

	envID := devEnvironmentID(t, reg)
	envKey := "dev"
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-outbox-atomic")
	art := seedArtifact(t, pool, appID, buildID, "sha256:outbox-atomic", "v1.0.0")

	current, outbox, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, EnvironmentKey: envKey, TargetKey: "image:acme-widget", ArtifactID: art,
	}, "acme", "")
	if err != nil {
		t.Fatalf("promote+enqueue: %v", err)
	}
	if outbox.PromotionID != current.PromotionID {
		t.Fatalf("expected outbox row promotion_id %s to match the promotion %s", outbox.PromotionID, current.PromotionID)
	}
	if outbox.Status != repository.WritebackOutboxStatusPending {
		t.Fatalf("expected a freshly enqueued outbox row to be pending, got %q", outbox.Status)
	}
	// Domain round-trips through Enqueue exactly like EnvironmentKey does --
	// see 015_writeback_outbox_domain.up.sql and enqueueWriteback's
	// ownerDomain call.
	if outbox.Domain != "acme" {
		t.Fatalf("expected outbox row domain %q, got %q", "acme", outbox.Domain)
	}

	// Read back with a fresh query against the pool (not through Registry),
	// confirming the row genuinely committed rather than only existing in
	// the transaction-scoped Go struct returned above.
	var count int
	var domain string
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*), max(domain) FROM writeback_outbox WHERE promotion_id = $1 AND status = 'pending'`,
		current.PromotionID).Scan(&count, &domain); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 committed pending outbox row for promotion %s, found %d", current.PromotionID, count)
	}
	if domain != "acme" {
		t.Fatalf("expected the committed row's domain to be %q, got %q", "acme", domain)
	}
}

// TestWriteback_EnqueueFailureRollsBackWholeTransaction is the atomicity
// hazard in the other direction: a failing outbox insert (event_id foreign
// key violation, forced via promoteWithOutboxTx's forceBadEventID) must
// roll back the promotion and promotion_event rows written earlier in the
// same transaction, too -- otherwise the registry would believe a
// promotion succeeded with no writeback intent ever recorded for it, the
// exact split-brain PLAN.md and ARCHITECTURE.md's "Writeback: outbox ->
// Temporal" warn about.
func TestWriteback_EnqueueFailureRollsBackWholeTransaction(t *testing.T) {
	reg, pool := newTestRegistry(t)

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-outbox-abort")
	art := seedArtifact(t, pool, appID, buildID, "sha256:outbox-abort", "v1.0.0")
	targetKey := "image:acme-widget"

	_, _, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, EnvironmentKey: "dev", TargetKey: targetKey, ArtifactID: art,
	}, "acme", "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatalf("expected the outbox insert (bad event_id) to fail the transaction, got nil error")
	}

	if n := currentPromotionCount(t, pool, envID, targetKey); n != 0 {
		t.Fatalf("expected the promotion to have rolled back along with the failed outbox insert, found %d current promotion(s)", n)
	}
	var eventCount, outboxCount int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM promotion_event`).Scan(&eventCount); err != nil {
		t.Fatalf("count promotion_event rows: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM writeback_outbox`).Scan(&outboxCount); err != nil {
		t.Fatalf("count writeback_outbox rows: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("expected the promotion_event row written earlier in the same aborted transaction to have rolled back too, found %d", eventCount)
	}
	if outboxCount != 0 {
		t.Fatalf("expected zero writeback_outbox rows after the aborted transaction, found %d", outboxCount)
	}
}

// TestWritebackOutbox_ClaimBatch_SkipsLockedAndReclaimsStale exercises the
// worker-facing side of the outbox against real Postgres: ClaimBatch's
// single atomic statement (`UPDATE ... WHERE outbox_id IN (SELECT ... FOR
// UPDATE SKIP LOCKED)`) claims pending rows, a second call claims nothing
// more (nothing pending, nothing stale yet), and after the claim is treated
// as stale (staleAfter=0) a second worker successfully reclaims it -- the
// mechanism that makes a worker killed mid-run (AR-4b's exit criterion)
// recoverable instead of stuck.
func TestWritebackOutbox_ClaimBatch_SkipsLockedAndReclaimsStale(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-outbox-claim")
	art := seedArtifact(t, pool, appID, buildID, "sha256:outbox-claim", "v1.0.0")

	current, _, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, EnvironmentKey: "dev", TargetKey: "image:acme-widget", ArtifactID: art,
	}, "acme", "")
	if err != nil {
		t.Fatalf("promote+enqueue: %v", err)
	}

	// worker-a claims the only pending row.
	claimedA, err := reg.Writeback().ClaimBatch(ctx, "worker-a", 10, time.Hour)
	if err != nil {
		t.Fatalf("worker-a claim: %v", err)
	}
	if len(claimedA) != 1 || claimedA[0].PromotionID != current.PromotionID {
		t.Fatalf("expected worker-a to claim exactly the 1 pending row, got %+v", claimedA)
	}

	// Immediately after: nothing pending, and the claim is fresh (well
	// within a 1-hour staleness window), so worker-b claims nothing --
	// this is the "SKIP LOCKED prevents double-claim" property, observed
	// through the staleness window rather than true concurrency (which
	// would need two goroutines racing inside the same transaction
	// window; the UPDATE...SELECT...FOR UPDATE SKIP LOCKED subquery
	// pattern is what Postgres guarantees atomic here, not this test).
	claimedB, err := reg.Writeback().ClaimBatch(ctx, "worker-b", 10, time.Hour)
	if err != nil {
		t.Fatalf("worker-b claim (should find nothing): %v", err)
	}
	if len(claimedB) != 0 {
		t.Fatalf("expected worker-b to claim nothing while worker-a's claim is fresh, got %+v", claimedB)
	}

	// A worker killed mid-run leaves its claim stale. staleAfter=0 makes
	// every claimed row immediately eligible, standing in for "time has
	// passed the staleness window" without a real sleep.
	reclaimed, err := reg.Writeback().ClaimBatch(ctx, "worker-c", 10, 0)
	if err != nil {
		t.Fatalf("worker-c reclaim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].OutboxID != claimedA[0].OutboxID {
		t.Fatalf("expected worker-c to reclaim the stale-claimed row, got %+v", reclaimed)
	}
	if reclaimed[0].Attempts != 2 {
		t.Fatalf("expected attempts to increment across the two claims, got %d", reclaimed[0].Attempts)
	}

	// MarkDone retires it -- no further claim, however stale, picks it up.
	if err := reg.Writeback().MarkDone(ctx, reclaimed[0].OutboxID, current.PromotionID, "run-1"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	final, err := reg.Writeback().ClaimBatch(ctx, "worker-d", 10, 0)
	if err != nil {
		t.Fatalf("worker-d claim after done: %v", err)
	}
	if len(final) != 0 {
		t.Fatalf("expected a done row to never be reclaimed, got %+v", final)
	}
}

// TestWritebackOutbox_RecordResult_PersistsLocationAndCommitSHA is FR7a's
// (issue #1029) real-Postgres coverage for RecordResult: it must persist
// location/commit_sha onto the outbox row matching promotion_id, and must
// NOT disturb status/completed_at -- the distinction from MarkDone (which
// fires when the workflow merely STARTS) that RecordResult's doc comment
// calls out. Runs RecordResult AFTER MarkDone (the real WritebackWorkflow
// order: MarkDone happens when outbox.startWorkflow starts the workflow,
// RecordResult happens once its Publish activity has actually completed)
// so the "does not disturb" assertion is meaningful rather than vacuous.
func TestWritebackOutbox_RecordResult_PersistsLocationAndCommitSHA(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-outbox-record-result")
	art := seedArtifact(t, pool, appID, buildID, "sha256:outbox-record-result", "v1.0.0")

	current, outbox, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, EnvironmentKey: "dev", TargetKey: "image:acme-widget", ArtifactID: art,
	}, "acme", "")
	if err != nil {
		t.Fatalf("promote+enqueue: %v", err)
	}
	// Newly enqueued: location/commit_sha both default to '' (migration
	// 021), same NOT NULL DEFAULT '' convention as workflow_id/last_error.
	if outbox.Location != "" || outbox.CommitSHA != "" {
		t.Fatalf("expected a freshly enqueued outbox row to have empty location/commit_sha, got %+v", outbox)
	}

	if err := reg.Writeback().MarkDone(ctx, outbox.OutboxID, current.PromotionID, "run-1"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	beforeStatus, beforeCompletedAt := readOutboxStatusAndCompletedAt(t, pool, outbox.OutboxID)

	wantLocation := "acme/acme-widget/versions/dev.yaml"
	wantCommitSHA := "0123456789abcdef0123456789abcdef01234567"
	if err := reg.Writeback().RecordResult(ctx, outbox.OutboxID, current.PromotionID, wantLocation, wantCommitSHA); err != nil {
		t.Fatalf("record result: %v", err)
	}

	got, err := reg.Writeback().Get(ctx, outbox.OutboxID)
	if err != nil {
		t.Fatalf("get outbox row: %v", err)
	}
	if got.Location != wantLocation {
		t.Fatalf("expected location %q, got %q", wantLocation, got.Location)
	}
	if got.CommitSHA != wantCommitSHA {
		t.Fatalf("expected commit_sha %q, got %q", wantCommitSHA, got.CommitSHA)
	}
	// RecordResult must not disturb status/completed_at -- a distinct write
	// from MarkDone, see that method's and RecordResult's own doc comments.
	afterStatus, afterCompletedAt := readOutboxStatusAndCompletedAt(t, pool, outbox.OutboxID)
	if afterStatus != beforeStatus {
		t.Fatalf("expected status to stay %q after RecordResult, got %q", beforeStatus, afterStatus)
	}
	if !afterCompletedAt.Equal(beforeCompletedAt) {
		t.Fatalf("expected completed_at to stay %v after RecordResult, got %v", beforeCompletedAt, afterCompletedAt)
	}
}

// TestWritebackOutbox_RecordResult_UnknownPromotionReturnsNotFound proves
// RecordResult surfaces repository.ErrNotFound (rather than silently
// no-op-ing) when no outbox row's promotion_id matches.
func TestWritebackOutbox_RecordResult_UnknownPromotionReturnsNotFound(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	err := reg.Writeback().RecordResult(ctx, "", "00000000-0000-0000-0000-000000000000", "some/path.yaml", "deadbeef")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected repository.ErrNotFound for an unknown promotion_id, got %v", err)
	}
}

// readOutboxStatusAndCompletedAt reads status/completed_at directly
// against the pool (not through Registry) so
// TestWritebackOutbox_RecordResult_PersistsLocationAndCommitSHA can prove
// RecordResult genuinely leaves both untouched in the database, not just
// in whatever Go struct a caller happens to hold.
func readOutboxStatusAndCompletedAt(t *testing.T, pool *pgxpool.Pool, outboxID string) (string, time.Time) {
	t.Helper()
	var status string
	var completedAt time.Time
	if err := pool.QueryRow(context.Background(), `
		SELECT status, completed_at FROM writeback_outbox WHERE outbox_id = $1`, outboxID).Scan(&status, &completedAt); err != nil {
		t.Fatalf("read outbox status/completed_at: %v", err)
	}
	return status, completedAt
}

// seedHistoricalPromotionRow inserts one already-superseded `promotion` row
// directly (valid_to set, so promotion_current_idx's partial uniqueness --
// at most one "current" row per (environment_id, target_key) -- never
// applies), for issue #603's ListPromotions pagination tests, which need
// many rows sharing one target and exact, caller-chosen valid_from values.
func seedHistoricalPromotionRow(t *testing.T, pool *pgxpool.Pool, envID, targetKey, artifactID string, validFrom time.Time) string {
	t.Helper()
	var promotionID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO promotion (environment_id, target_key, artifact_id, valid_from, valid_to)
		VALUES ($1, $2, $3, $4, $5) RETURNING promotion_id`,
		envID, targetKey, artifactID, validFrom, validFrom.Add(time.Second)).Scan(&promotionID)
	if err != nil {
		t.Fatalf("seed historical promotion: %v", err)
	}
	return promotionID
}

// TestListPromotions_Pagination_MatchesFullOrderedScan_Postgres is the
// real-Postgres analogue of TestListBuilds_Pagination_MatchesFullOrderedScan_Postgres
// for ListPromotions (issue #603): pages through with a small page_size and
// confirms the full traversal matches a single unfiltered `ORDER BY
// valid_from DESC, promotion_id DESC` scan exactly, in order, with no
// duplicates and no omissions -- including across a duplicate-valid_from
// pair, the case a naive keyset implementation missing the promotion_id
// tie-break gets wrong.
func TestListPromotions_Pagination_MatchesFullOrderedScan_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()
	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "promotion-page-app", "image")
	buildID := seedBuild(t, pool, "run-promotion-page")
	artifactID := seedArtifact(t, pool, appID, buildID, "sha256:promotion-page", "v1.0.0")
	targetKey := "image:acme-promotion-page-app"

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tie := base.Add(3 * time.Hour)
	seedHistoricalPromotionRow(t, pool, envID, targetKey, artifactID, base)
	seedHistoricalPromotionRow(t, pool, envID, targetKey, artifactID, base.Add(1*time.Hour))
	seedHistoricalPromotionRow(t, pool, envID, targetKey, artifactID, tie)
	seedHistoricalPromotionRow(t, pool, envID, targetKey, artifactID, tie)
	seedHistoricalPromotionRow(t, pool, envID, targetKey, artifactID, base.Add(4*time.Hour))

	rows, err := pool.Query(ctx, `SELECT promotion_id FROM promotion WHERE target_key = $1 ORDER BY valid_from DESC, promotion_id DESC`, targetKey)
	if err != nil {
		t.Fatalf("ground-truth scan: %v", err)
	}
	var want []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan ground-truth row: %v", err)
		}
		want = append(want, id)
	}
	rows.Close()
	if len(want) != 5 {
		t.Fatalf("expected 5 rows in ground-truth scan, got %d", len(want))
	}

	var got []string
	seen := map[string]bool{}
	token := ""
	for page := 0; page < 6; page++ {
		promotions, next, err := reg.Promotions().ListPromotions(ctx, repository.PromotionListFilter{EnvironmentKey: "dev", IncludeHistory: true}, 2, token)
		if err != nil {
			t.Fatalf("ListPromotions page %d: %v", page, err)
		}
		if len(promotions) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(promotions) > 2 {
			t.Fatalf("page %d: expected at most page_size=2 rows, got %d", page, len(promotions))
		}
		for _, p := range promotions {
			if seen[p.PromotionID] {
				t.Fatalf("duplicate row %s across pages", p.PromotionID)
			}
			seen[p.PromotionID] = true
			got = append(got, p.PromotionID)
		}
		token = next
		if token == "" {
			break
		}
	}

	if len(got) != len(want) {
		t.Fatalf("expected %d rows across all pages, got %d (want order %v, got order %v)", len(want), len(got), want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: expected %s (ground-truth ORDER BY), got %s", i, want[i], got[i])
		}
	}
}

// TestListPromotionEvents_Pagination_MatchesFullOrderedScan_Postgres is the
// real-Postgres analogue for ListEvents (issue #603): pages through with a
// small page_size and confirms the full traversal matches a single
// unfiltered `ORDER BY occurred_at DESC, event_id DESC` scan exactly, in
// order, with no duplicates and no omissions -- including across a
// duplicate-occurred_at pair, the case a naive keyset implementation missing
// the event_id tie-break gets wrong.
func TestListPromotionEvents_Pagination_MatchesFullOrderedScan_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()
	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "promotion-event-page-app", "image")
	buildID := seedBuild(t, pool, "run-promotion-event-page")
	artifactID := seedArtifact(t, pool, appID, buildID, "sha256:promotion-event-page", "v1.0.0")
	targetKey := "image:acme-promotion-event-page-app"
	promotionID := seedHistoricalPromotionRow(t, pool, envID, targetKey, artifactID, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tie := base.Add(3 * time.Hour)
	seedEvent := func(occurredAt time.Time) string {
		var eventID string
		err := pool.QueryRow(ctx, `
			INSERT INTO promotion_event (promotion_id, action, actor, occurred_at)
			VALUES ($1, 'promote', 'integration-test', $2) RETURNING event_id`,
			promotionID, occurredAt).Scan(&eventID)
		if err != nil {
			t.Fatalf("seed promotion event: %v", err)
		}
		return eventID
	}
	seedEvent(base)
	seedEvent(base.Add(1 * time.Hour))
	seedEvent(tie)
	seedEvent(tie)
	seedEvent(base.Add(4 * time.Hour))

	rows, err := pool.Query(ctx, `SELECT event_id FROM promotion_event WHERE promotion_id = $1 ORDER BY occurred_at DESC, event_id DESC`, promotionID)
	if err != nil {
		t.Fatalf("ground-truth scan: %v", err)
	}
	var want []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan ground-truth row: %v", err)
		}
		want = append(want, id)
	}
	rows.Close()
	if len(want) != 5 {
		t.Fatalf("expected 5 rows in ground-truth scan, got %d", len(want))
	}

	var got []string
	seen := map[string]bool{}
	token := ""
	for page := 0; page < 6; page++ {
		events, next, err := reg.Promotions().ListEvents(ctx, repository.PromotionEventListFilter{PromotionID: promotionID}, 2, token)
		if err != nil {
			t.Fatalf("ListEvents page %d: %v", page, err)
		}
		if len(events) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(events) > 2 {
			t.Fatalf("page %d: expected at most page_size=2 rows, got %d", page, len(events))
		}
		for _, e := range events {
			if seen[e.EventID] {
				t.Fatalf("duplicate row %s across pages", e.EventID)
			}
			seen[e.EventID] = true
			got = append(got, e.EventID)
		}
		token = next
		if token == "" {
			break
		}
	}

	if len(got) != len(want) {
		t.Fatalf("expected %d rows across all pages, got %d (want order %v, got order %v)", len(want), len(got), want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: expected %s (ground-truth ORDER BY), got %s", i, want[i], got[i])
		}
	}
}
