//go:build integration

// This file only builds under the "integration" build tag, same as
// server_test.go's precedent for this package and the pending_restart_test
// go_test target -- see //libs/go/dbtest's README for how to run it. It
// exercises the actual DB-enforced guarantees the issue calls out
// (unique partial index, atomic UPDATE...RETURNING claim/expire) that no
// in-memory fake can verify.
//
// Schema here is hand-written, self-contained DDL mirroring exactly the
// pieces of manmanv2/migrate/migrations/036_pending_restarts.up.sql and its
// FK targets (servers, games, game_configs, server_game_configs, sessions
// from 001_initial_schema.up.sql) -- per dbtest's README ("Options.Schema
// should be self-contained DDL -- do not depend on another package's
// migrations").
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
)

// pendingRestartSchema mirrors 001_initial_schema.up.sql's servers, games,
// game_configs, server_game_configs, sessions tables (scoped down to only
// the columns a pending_restarts row's FKs require) plus
// 036_pending_restarts.up.sql verbatim, including its three indexes.
const pendingRestartSchema = `
	CREATE TABLE servers (
		server_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE games (
		game_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE game_configs (
		config_id BIGSERIAL PRIMARY KEY,
		game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		name VARCHAR(255) NOT NULL,
		image VARCHAR(500) NOT NULL,
		UNIQUE(game_id, name)
	);

	CREATE TABLE server_game_configs (
		sgc_id BIGSERIAL PRIMARY KEY,
		server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
		game_config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
		status VARCHAR(50) NOT NULL DEFAULT 'inactive',
		UNIQUE(server_id, game_config_id)
	);

	CREATE TABLE sessions (
		session_id BIGSERIAL PRIMARY KEY,
		sgc_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
		status VARCHAR(50) NOT NULL DEFAULT 'pending'
	);

	CREATE TABLE pending_restarts (
		pending_restart_id BIGSERIAL PRIMARY KEY,
		server_game_config_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id),
		gating_session_id BIGINT NOT NULL REFERENCES sessions(session_id),
		status TEXT NOT NULL,
		stall_deadline TIMESTAMPTZ NOT NULL,
		started_session_id BIGINT,
		failure_reason TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		resolved_at TIMESTAMPTZ
	);

	CREATE UNIQUE INDEX pending_restarts_one_pending_per_sgc
		ON pending_restarts(server_game_config_id) WHERE status = 'pending';

	CREATE INDEX pending_restarts_gating_session_pending
		ON pending_restarts(gating_session_id) WHERE status = 'pending';

	CREATE INDEX pending_restarts_stall_deadline
		ON pending_restarts(stall_deadline) WHERE status = 'pending';
`

func newPendingRestartTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: pendingRestartSchema})
	return db.Pool
}

// seedSGC creates a server + game + game_config + server_game_config chain
// and returns the resulting sgc_id, so tests can focus on pending_restarts
// itself.
func seedSGC(t *testing.T, pool *pgxpool.Pool, label string) int64 {
	t.Helper()
	ctx := context.Background()

	var serverID int64
	if err := pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, "server-"+label).Scan(&serverID); err != nil {
		t.Fatalf("seed server %s: %v", label, err)
	}
	var gameID int64
	if err := pool.QueryRow(ctx, `INSERT INTO games (name) VALUES ($1) RETURNING game_id`, "game-"+label).Scan(&gameID); err != nil {
		t.Fatalf("seed game %s: %v", label, err)
	}
	var configID int64
	if err := pool.QueryRow(ctx, `INSERT INTO game_configs (game_id, name, image) VALUES ($1, $2, 'image') RETURNING config_id`, gameID, "config-"+label).Scan(&configID); err != nil {
		t.Fatalf("seed game_config %s: %v", label, err)
	}
	var sgcID int64
	if err := pool.QueryRow(ctx, `INSERT INTO server_game_configs (server_id, game_config_id) VALUES ($1, $2) RETURNING sgc_id`, serverID, configID).Scan(&sgcID); err != nil {
		t.Fatalf("seed server_game_config %s: %v", label, err)
	}
	return sgcID
}

// seedSession creates a session gated to sgcID and returns its session_id.
func seedSession(t *testing.T, pool *pgxpool.Pool, sgcID int64) int64 {
	t.Helper()
	var sessionID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sessions (sgc_id) VALUES ($1) RETURNING session_id`, sgcID).Scan(&sessionID); err != nil {
		t.Fatalf("seed session for sgc %d: %v", sgcID, err)
	}
	return sessionID
}

// TestCreate_SecondPendingForSameSGCFailsWithSentinel proves item 1: a
// second Create for the same SGC while the first is still 'pending' returns
// ErrPendingRestartExists, asserting the unique partial index
// (pending_restarts_one_pending_per_sgc), not application-level checking.
func TestCreate_SecondPendingForSameSGCFailsWithSentinel(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	sgcID := seedSGC(t, pool, "a")
	session1 := seedSession(t, pool, sgcID)
	session2 := seedSession(t, pool, sgcID)

	first, err := repo.Create(ctx, sgcID, session1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("first Create should succeed: %v", err)
	}
	if first.Status != "pending" {
		t.Fatalf("expected status 'pending', got %q", first.Status)
	}

	_, err = repo.Create(ctx, sgcID, session2, time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("second Create for the same SGC while the first is pending succeeded; pending_restarts_one_pending_per_sgc did not fire")
	}
	if err != repository.ErrPendingRestartExists {
		t.Fatalf("expected repository.ErrPendingRestartExists, got: %v", err)
	}
}

// TestCreate_AllowedAgainAfterTerminalStatus proves item 2: once the first
// record reaches a terminal status, a fresh Create for the same SGC
// succeeds -- the partial index must not block a *later* restart.
func TestCreate_AllowedAgainAfterTerminalStatus(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	sgcID := seedSGC(t, pool, "a")
	session1 := seedSession(t, pool, sgcID)
	session2 := seedSession(t, pool, sgcID)

	first, err := repo.Create(ctx, sgcID, session1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("first Create should succeed: %v", err)
	}

	claimed, err := repo.ClaimForSession(ctx, session1)
	if err != nil {
		t.Fatalf("ClaimForSession: %v", err)
	}
	if claimed == nil || claimed.PendingRestartID != first.PendingRestartID {
		t.Fatalf("expected to claim the first record, got %+v", claimed)
	}
	if err := repo.MarkFailed(ctx, first.PendingRestartID, "test-terminal"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	second, err := repo.Create(ctx, sgcID, session2, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Create after a prior record reached a terminal status should succeed, got: %v", err)
	}
	if second.PendingRestartID == first.PendingRestartID {
		t.Fatal("expected a new record, got the same PendingRestartID back")
	}
}

// TestClaimForSession_SecondCallReturnsNilNil proves item 3: ClaimForSession
// returns the record once; a second call for the same gating_session_id
// returns (nil, nil) -- the FR10/NFR10 at-least-once-delivery case.
func TestClaimForSession_SecondCallReturnsNilNil(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	sgcID := seedSGC(t, pool, "a")
	sessionID := seedSession(t, pool, sgcID)

	created, err := repo.Create(ctx, sgcID, sessionID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	first, err := repo.ClaimForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("first ClaimForSession: %v", err)
	}
	if first == nil || first.PendingRestartID != created.PendingRestartID {
		t.Fatalf("expected to claim the created record, got %+v", first)
	}
	if first.Status != "started" {
		t.Fatalf("expected status 'started' after claim, got %q", first.Status)
	}

	second, err := repo.ClaimForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("second ClaimForSession should not error: %v", err)
	}
	if second != nil {
		t.Fatalf("second ClaimForSession for an already-claimed session should return nil, got %+v", second)
	}
}

// TestClaimForSession_UnknownSessionReturnsNilNilNoError proves item 4:
// ClaimForSession for an unknown/never-pending session returns (nil, nil),
// not an error.
func TestClaimForSession_UnknownSessionReturnsNilNilNoError(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	pr, err := repo.ClaimForSession(ctx, 999999)
	if err != nil {
		t.Fatalf("expected no error for an unknown session, got: %v", err)
	}
	if pr != nil {
		t.Fatalf("expected nil for an unknown session, got %+v", pr)
	}
}

// TestExpireStalled_OnlyMovesPendingPastDeadline proves item 5:
// ExpireStalled moves only records past stall_deadline and still 'pending';
// a record already 'started' is untouched; the returned slice contains
// exactly the expired ones.
func TestExpireStalled_OnlyMovesPendingPastDeadline(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	now := time.Now()

	// Record A: pending, past deadline -- should expire.
	sgcA := seedSGC(t, pool, "a")
	sessionA := seedSession(t, pool, sgcA)
	recA, err := repo.Create(ctx, sgcA, sessionA, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}

	// Record B: pending, not yet past deadline -- should stay pending.
	sgcB := seedSGC(t, pool, "b")
	sessionB := seedSession(t, pool, sgcB)
	recB, err := repo.Create(ctx, sgcB, sessionB, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	// Record C: already started, past deadline -- must be untouched by
	// ExpireStalled even though it's past its stall_deadline.
	sgcC := seedSGC(t, pool, "c")
	sessionC := seedSession(t, pool, sgcC)
	recC, err := repo.Create(ctx, sgcC, sessionC, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Create C: %v", err)
	}
	if _, err := repo.ClaimForSession(ctx, sessionC); err != nil {
		t.Fatalf("claim C: %v", err)
	}

	expired, err := repo.ExpireStalled(ctx, now)
	if err != nil {
		t.Fatalf("ExpireStalled: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expected exactly 1 expired record, got %d: %+v", len(expired), expired)
	}
	if expired[0].PendingRestartID != recA.PendingRestartID {
		t.Fatalf("expected record A to be the one expired, got PendingRestartID=%d", expired[0].PendingRestartID)
	}
	if expired[0].Status != "expired" {
		t.Fatalf("expected expired record's status to be 'expired', got %q", expired[0].Status)
	}

	latest, err := repo.GetLatestBySGCIDs(ctx, []int64{sgcA, sgcB, sgcC})
	if err != nil {
		t.Fatalf("GetLatestBySGCIDs: %v", err)
	}
	if latest[sgcA].Status != "expired" {
		t.Fatalf("expected A to be expired, got %q", latest[sgcA].Status)
	}
	if latest[sgcB].Status != "pending" {
		t.Fatalf("expected B to remain pending (not past deadline), got %q", latest[sgcB].Status)
	}
	if latest[sgcC].Status != "started" {
		t.Fatalf("expected C to remain started (already claimed before deadline check), got %q", latest[sgcC].Status)
	}
	_ = recB
	_ = recC
}

// TestExpireStalledAndClaimForSession_RaceResolvesExactlyOnce proves item 6:
// ExpireStalled and ClaimForSession racing on the same record resolve it
// exactly once -- one returns it, the other returns empty/nil.
func TestExpireStalledAndClaimForSession_RaceResolvesExactlyOnce(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	sgcID := seedSGC(t, pool, "a")
	sessionID := seedSession(t, pool, sgcID)

	// Deadline already in the past, so ExpireStalled(now) is eligible to
	// expire it at the same instant ClaimForSession tries to claim it.
	if _, err := repo.Create(ctx, sgcID, sessionID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	claimCh := make(chan struct {
		claimed bool
		err     error
	}, 1)
	expireCh := make(chan struct {
		expiredCount int
		err          error
	}, 1)

	go func() {
		pr, err := repo.ClaimForSession(ctx, sessionID)
		claimCh <- struct {
			claimed bool
			err     error
		}{claimed: pr != nil, err: err}
	}()
	go func() {
		recs, err := repo.ExpireStalled(ctx, time.Now())
		expireCh <- struct {
			expiredCount int
			err          error
		}{expiredCount: len(recs), err: err}
	}()

	claimRes := <-claimCh
	expireRes := <-expireCh

	if claimRes.err != nil {
		t.Fatalf("ClaimForSession error: %v", claimRes.err)
	}
	if expireRes.err != nil {
		t.Fatalf("ExpireStalled error: %v", expireRes.err)
	}

	claimedCount := 0
	if claimRes.claimed {
		claimedCount++
	}

	if claimedCount+expireRes.expiredCount != 1 {
		t.Fatalf("expected exactly one of ClaimForSession/ExpireStalled to resolve the record, got claimed=%d expired=%d", claimedCount, expireRes.expiredCount)
	}

	latest, err := repo.GetLatestBySGCIDs(ctx, []int64{sgcID})
	if err != nil {
		t.Fatalf("GetLatestBySGCIDs: %v", err)
	}
	if latest[sgcID].Status != "started" && latest[sgcID].Status != "expired" {
		t.Fatalf("expected final status to be exactly one of started/expired, got %q", latest[sgcID].Status)
	}
}

// TestGetLatestBySGCIDs_OneEntryPerSGCWithRecordAndEmptyInputShortCircuits
// proves item 7: GetLatestBySGCIDs returns one entry per SGC that has a
// record and omits SGCs with none; an empty input slice returns an empty
// map without querying.
func TestGetLatestBySGCIDs_OneEntryPerSGCWithRecordAndEmptyInputShortCircuits(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	sgcWithRecord := seedSGC(t, pool, "with-record")
	sgcWithoutRecord := seedSGC(t, pool, "without-record")
	sessionID := seedSession(t, pool, sgcWithRecord)

	if _, err := repo.Create(ctx, sgcWithRecord, sessionID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	latest, err := repo.GetLatestBySGCIDs(ctx, []int64{sgcWithRecord, sgcWithoutRecord})
	if err != nil {
		t.Fatalf("GetLatestBySGCIDs: %v", err)
	}
	if _, ok := latest[sgcWithRecord]; !ok {
		t.Fatalf("expected an entry for the SGC that has a record")
	}
	if _, ok := latest[sgcWithoutRecord]; ok {
		t.Fatalf("expected no entry for the SGC without a record")
	}
	if len(latest) != 1 {
		t.Fatalf("expected exactly 1 entry, got %d: %+v", len(latest), latest)
	}

	empty, err := repo.GetLatestBySGCIDs(ctx, nil)
	if err != nil {
		t.Fatalf("GetLatestBySGCIDs with nil input should not error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected an empty map for an empty input slice, got %+v", empty)
	}
}

// TestGetLatestBySGCIDs_VisibilityWindowExcludesOldResolved proves #1735's
// visibility-window trim: a resolved ('failed') record older than
// pendingRestartVisibilityWindow is excluded from GetLatestBySGCIDs, while
// one resolved inside the window is still returned. resolved_at is backdated
// directly via SQL after MarkFailed -- the repository has no write path that
// takes an explicit resolved_at, and the trim is a read-time predicate, so
// this is the only way to put a row on either side of the boundary.
func TestGetLatestBySGCIDs_VisibilityWindowExcludesOldResolved(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	// Old: resolved well outside the visibility window -- must be excluded.
	sgcOld := seedSGC(t, pool, "old")
	sessionOld := seedSession(t, pool, sgcOld)
	recOld, err := repo.Create(ctx, sgcOld, sessionOld, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Create old: %v", err)
	}
	if err := repo.MarkFailed(ctx, recOld.PendingRestartID, "old failure"); err != nil {
		t.Fatalf("MarkFailed old: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pending_restarts SET resolved_at = $1 WHERE pending_restart_id = $2`,
		time.Now().Add(-(pendingRestartVisibilityWindow + time.Minute)), recOld.PendingRestartID); err != nil {
		t.Fatalf("backdate resolved_at old: %v", err)
	}

	// Recent: resolved just inside the visibility window -- must be included.
	sgcRecent := seedSGC(t, pool, "recent")
	sessionRecent := seedSession(t, pool, sgcRecent)
	recRecent, err := repo.Create(ctx, sgcRecent, sessionRecent, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Create recent: %v", err)
	}
	if err := repo.MarkFailed(ctx, recRecent.PendingRestartID, "recent failure"); err != nil {
		t.Fatalf("MarkFailed recent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pending_restarts SET resolved_at = $1 WHERE pending_restart_id = $2`,
		time.Now().Add(-(pendingRestartVisibilityWindow - time.Minute)), recRecent.PendingRestartID); err != nil {
		t.Fatalf("backdate resolved_at recent: %v", err)
	}

	latest, err := repo.GetLatestBySGCIDs(ctx, []int64{sgcOld, sgcRecent})
	if err != nil {
		t.Fatalf("GetLatestBySGCIDs: %v", err)
	}
	if _, ok := latest[sgcOld]; ok {
		t.Fatalf("expected the old resolved record to be excluded by the visibility window, got %+v", latest[sgcOld])
	}
	if got, ok := latest[sgcRecent]; !ok || got.PendingRestartID != recRecent.PendingRestartID {
		t.Fatalf("expected the recently-resolved record to be included, got %+v (present=%v)", got, ok)
	}
}

// TestCancelForSGCs_MovesMatchingPendingRecordsToTerminalState proves
// #2366/FR18 item 1: CancelForSGCs moves every 'pending' record for the
// given SGCs to a terminal ('failed', reusing MarkFailed's semantics per
// this method's doc comment) state with the given reason, and reports how
// many rows it moved.
func TestCancelForSGCs_MovesMatchingPendingRecordsToTerminalState(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	sgcA := seedSGC(t, pool, "a")
	sessionA := seedSession(t, pool, sgcA)
	recA, err := repo.Create(ctx, sgcA, sessionA, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}

	sgcB := seedSGC(t, pool, "b")
	sessionB := seedSession(t, pool, sgcB)
	recB, err := repo.Create(ctx, sgcB, sessionB, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	cancelled, err := repo.CancelForSGCs(ctx, []int64{sgcA, sgcB}, "host 1 draining")
	if err != nil {
		t.Fatalf("CancelForSGCs: %v", err)
	}
	if cancelled != 2 {
		t.Fatalf("expected 2 rows cancelled, got %d", cancelled)
	}

	latest, err := repo.GetLatestBySGCIDs(ctx, []int64{sgcA, sgcB})
	if err != nil {
		t.Fatalf("GetLatestBySGCIDs: %v", err)
	}
	for sgcID, rec := range map[int64]*manman.PendingRestart{sgcA: recA, sgcB: recB} {
		got, ok := latest[sgcID]
		if !ok {
			t.Fatalf("expected an entry for sgc %d", sgcID)
		}
		if got.PendingRestartID != rec.PendingRestartID {
			t.Fatalf("expected the same record for sgc %d, got PendingRestartID=%d", sgcID, got.PendingRestartID)
		}
		if got.Status != "failed" {
			t.Fatalf("expected sgc %d's record to be moved to 'failed', got %q", sgcID, got.Status)
		}
		if got.FailureReason == nil || *got.FailureReason != "host 1 draining" {
			t.Fatalf("expected sgc %d's failure_reason to be the drain reason, got %v", sgcID, got.FailureReason)
		}
		if got.ResolvedAt == nil {
			t.Fatalf("expected sgc %d's resolved_at to be set", sgcID)
		}
	}
}

// TestCancelForSGCs_NoOpForNonMatchingSGCs proves #2366/FR18 item 2:
// CancelForSGCs for an SGC with no 'pending' record (unknown SGC, or an SGC
// whose record is already 'started'/terminal) is a no-op -- it returns 0
// and leaves other SGCs' records untouched.
func TestCancelForSGCs_NoOpForNonMatchingSGCs(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	// sgcStarted already had its pending restart claimed -- CancelForSGCs
	// must not touch a 'started' record; there is nothing left to cancel.
	sgcStarted := seedSGC(t, pool, "started")
	sessionStarted := seedSession(t, pool, sgcStarted)
	recStarted, err := repo.Create(ctx, sgcStarted, sessionStarted, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repo.ClaimForSession(ctx, sessionStarted); err != nil {
		t.Fatalf("ClaimForSession: %v", err)
	}

	// sgcUnknown has no pending_restarts row at all.
	sgcUnknown := seedSGC(t, pool, "unknown")

	cancelled, err := repo.CancelForSGCs(ctx, []int64{sgcStarted, sgcUnknown}, "host 1 draining")
	if err != nil {
		t.Fatalf("CancelForSGCs: %v", err)
	}
	if cancelled != 0 {
		t.Fatalf("expected 0 rows cancelled for non-matching SGCs, got %d", cancelled)
	}

	latest, err := repo.GetLatestBySGCIDs(ctx, []int64{sgcStarted, sgcUnknown})
	if err != nil {
		t.Fatalf("GetLatestBySGCIDs: %v", err)
	}
	if got, ok := latest[sgcStarted]; !ok || got.PendingRestartID != recStarted.PendingRestartID || got.Status != "started" {
		t.Fatalf("expected sgcStarted's record to remain 'started' and untouched, got %+v (present=%v)", got, ok)
	}
	if _, ok := latest[sgcUnknown]; ok {
		t.Fatalf("expected no entry for sgcUnknown")
	}
}

// TestCancelForSGCs_IdempotentSecondCallIsNoOp proves #2366/FR18 item 3:
// calling CancelForSGCs twice for the same SGCs is idempotent -- the first
// call cancels the pending record, the second call finds nothing left in
// 'pending' state and returns 0 without erroring or double-cancelling.
func TestCancelForSGCs_IdempotentSecondCallIsNoOp(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	sgcID := seedSGC(t, pool, "a")
	sessionID := seedSession(t, pool, sgcID)
	rec, err := repo.Create(ctx, sgcID, sessionID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	first, err := repo.CancelForSGCs(ctx, []int64{sgcID}, "host 1 draining")
	if err != nil {
		t.Fatalf("first CancelForSGCs: %v", err)
	}
	if first != 1 {
		t.Fatalf("expected first call to cancel 1 row, got %d", first)
	}

	second, err := repo.CancelForSGCs(ctx, []int64{sgcID}, "host 1 draining")
	if err != nil {
		t.Fatalf("second CancelForSGCs: %v", err)
	}
	if second != 0 {
		t.Fatalf("expected second call to be a no-op (0 rows), got %d", second)
	}

	latest, err := repo.GetLatestBySGCIDs(ctx, []int64{sgcID})
	if err != nil {
		t.Fatalf("GetLatestBySGCIDs: %v", err)
	}
	if got, ok := latest[sgcID]; !ok || got.PendingRestartID != rec.PendingRestartID || got.Status != "failed" {
		t.Fatalf("expected the record to remain 'failed' after the second call, got %+v (present=%v)", got, ok)
	}
}

// TestCancelForSGCs_EmptyInputIsNoOp proves the empty-slice short-circuit:
// CancelForSGCs with no SGCs does nothing and does not query the DB.
func TestCancelForSGCs_EmptyInputIsNoOp(t *testing.T) {
	pool := newPendingRestartTestDB(t)
	ctx := context.Background()
	repo := NewPendingRestartRepository(pool)

	cancelled, err := repo.CancelForSGCs(ctx, nil, "host 1 draining")
	if err != nil {
		t.Fatalf("expected no error for empty input, got: %v", err)
	}
	if cancelled != 0 {
		t.Fatalf("expected 0 for empty input, got %d", cancelled)
	}
}
