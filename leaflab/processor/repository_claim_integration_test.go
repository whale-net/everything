//go:build integration

// Real-Postgres integration coverage for FR76's restart-signal evidence
// (#1342): Repository.CheckAndUpdateUptimeWatermark's uint32-ms-wrap-safe
// regression detection and Repository.SatisfyOpenClaimRound's round-window
// matching and "at most one round, ever" uniqueness guarantee -- the two
// pieces of this task's own residual-risk caveat (SB-2.2) that only a real
// database can prove, since both rely on actual SQL (a partial unique
// index, a FOR UPDATE-guarded window match) rather than just Go control
// flow.
//
// Schema is self-contained hand-written DDL, narrower than the real
// migrations: only claim_challenge/claim_challenge_round/
// board_uptime_watermark plus the minimal board/sensor_reading shape these
// two repository methods actually touch. sensor_reading here mirrors its
// real shape closely enough to carry the composite (reading_id,
// recorded_at) primary key migration 021's fk_claim_challenge_round_reading
// constraint needs -- see that migration's doc comment for why
// reading_id alone can't be the FK target.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/processor:repository_claim_integration_test --test_output=all
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const claimTestSchema = `
	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE board_uptime_watermark (
		board_id      BIGINT PRIMARY KEY REFERENCES board(board_id) ON DELETE CASCADE,
		last_uptime_s INT NOT NULL,
		observed_at   TIMESTAMPTZ NOT NULL
	);

	CREATE TABLE sensor_reading (
		reading_id   BIGSERIAL,
		recorded_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		sensor_id    BIGINT NOT NULL,
		value        DOUBLE PRECISION NOT NULL,
		valid        BOOLEAN NOT NULL DEFAULT TRUE,
		uptime_s     INT NOT NULL,
		PRIMARY KEY (reading_id, recorded_at)
	);

	CREATE TABLE claim_challenge (
		challenge_id      BIGSERIAL PRIMARY KEY,
		handle            TEXT NOT NULL UNIQUE,
		principal_subject TEXT NOT NULL,
		device_id         TEXT NOT NULL,
		rounds_required   INT NOT NULL,
		rounds_satisfied  INT NOT NULL DEFAULT 0,
		attempts_used     INT NOT NULL DEFAULT 0,
		opened_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at        TIMESTAMPTZ NOT NULL,
		state             TEXT NOT NULL DEFAULT 'open'
		                     CHECK (state IN ('open', 'discharged', 'not_discharged')),
		discharged_at     TIMESTAMPTZ NULL
	);

	CREATE TABLE claim_challenge_round (
		round_id                          BIGSERIAL PRIMARY KEY,
		challenge_id                      BIGINT NOT NULL REFERENCES claim_challenge(challenge_id) ON DELETE CASCADE,
		device_id                         TEXT NOT NULL,
		round_index                       INT NOT NULL,
		t0                                TIMESTAMPTZ NOT NULL,
		bound_expires_at                  TIMESTAMPTZ NOT NULL,
		satisfied_by_reading_id           BIGINT NULL,
		satisfied_by_reading_recorded_at  TIMESTAMPTZ NULL,
		satisfied_by_manifest_at          TIMESTAMPTZ NULL,
		evidence_class                    TEXT NULL
		                                      CHECK (evidence_class IN ('uptime_regression', 'manifest_exception')),
		closed_at                         TIMESTAMPTZ NULL,
		UNIQUE (challenge_id, round_index),
		CONSTRAINT fk_claim_challenge_round_reading
			FOREIGN KEY (satisfied_by_reading_id, satisfied_by_reading_recorded_at)
			REFERENCES sensor_reading (reading_id, recorded_at)
	);

	-- "A restart signal may satisfy at most one round, ever" (Implementation
	-- section) -- the exact constraint under test by
	-- TestSatisfyOpenClaimRound_OneReadingCannotSatisfyTwoRounds below.
	CREATE UNIQUE INDEX idx_claim_challenge_round_reading_once
		ON claim_challenge_round(satisfied_by_reading_id) WHERE satisfied_by_reading_id IS NOT NULL;

	CREATE INDEX idx_claim_challenge_round_device_id_open
		ON claim_challenge_round(device_id) WHERE closed_at IS NULL;
`

func newClaimTestRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: claimTestSchema})
	return NewRepository(db.Pool), db.Pool
}

func insertClaimBoard(t *testing.T, pool *pgxpool.Pool, deviceID string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

// insertOpenChallengeWithRound creates a claim_challenge (state='open') for
// (principalSubject, deviceID) with roundsRequired total rounds, plus a
// single already-marked round (round_index=1) whose window is [t0,
// boundExpiresAt] -- standing in for what api.Repository.MarkClaimRound
// would have written, without pulling in the api package (SatisfyOpenClaimRound
// only needs the round row to exist; who wrote it is irrelevant to this
// package's tests).
func insertOpenChallengeWithRound(t *testing.T, pool *pgxpool.Pool, principalSubject, deviceID string, roundsRequired, roundIndex int, t0, boundExpiresAt time.Time) (challengeID, roundID int64) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO claim_challenge (handle, principal_subject, device_id, rounds_required, rounds_satisfied, expires_at)
		VALUES ($1, $2, $3, $4, $5, NOW() + interval '15 minutes')
		RETURNING challenge_id
	`, "cc_"+deviceID+"_"+principalSubject, principalSubject, deviceID, roundsRequired, roundIndex-1).Scan(&challengeID); err != nil {
		t.Fatalf("insert claim_challenge for %s/%s: %v", principalSubject, deviceID, err)
	}
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO claim_challenge_round (challenge_id, device_id, round_index, t0, bound_expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING round_id
	`, challengeID, deviceID, roundIndex, t0, boundExpiresAt).Scan(&roundID); err != nil {
		t.Fatalf("insert claim_challenge_round for challenge %d: %v", challengeID, err)
	}
	return challengeID, roundID
}

func challengeState(t *testing.T, pool *pgxpool.Pool, challengeID int64) (state string, roundsSatisfied int, dischargedAt *time.Time) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT state, rounds_satisfied, discharged_at FROM claim_challenge WHERE challenge_id = $1`,
		challengeID).Scan(&state, &roundsSatisfied, &dischargedAt); err != nil {
		t.Fatalf("query claim_challenge %d: %v", challengeID, err)
	}
	return state, roundsSatisfied, dischargedAt
}

// insertClaimRound adds a round to an already-existing challengeID -- for
// tests exercising a challenge's 2nd (or later) round without re-creating
// the challenge row (which insertOpenChallengeWithRound does for round 1's
// caller convenience, but must not be called again for the same challenge).
func insertClaimRound(t *testing.T, pool *pgxpool.Pool, challengeID int64, deviceID string, roundIndex int, t0, boundExpiresAt time.Time) int64 {
	t.Helper()
	var roundID int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO claim_challenge_round (challenge_id, device_id, round_index, t0, bound_expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING round_id
	`, challengeID, deviceID, roundIndex, t0, boundExpiresAt).Scan(&roundID); err != nil {
		t.Fatalf("insert claim_challenge_round (round %d) for challenge %d: %v", roundIndex, challengeID, err)
	}
	return roundID
}

func roundClosed(t *testing.T, pool *pgxpool.Pool, roundID int64) bool {
	t.Helper()
	var closedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT closed_at FROM claim_challenge_round WHERE round_id = $1`, roundID).Scan(&closedAt); err != nil {
		t.Fatalf("query claim_challenge_round %d: %v", roundID, err)
	}
	return closedAt != nil
}

func insertClaimReading(t *testing.T, pool *pgxpool.Pool, recordedAt time.Time) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO sensor_reading (recorded_at, sensor_id, value, uptime_s) VALUES ($1, 1, 1.0, 0)
		RETURNING reading_id
	`, recordedAt).Scan(&id); err != nil {
		t.Fatalf("insert sensor_reading: %v", err)
	}
	return id
}

// -- CheckAndUpdateUptimeWatermark: uint32 ms-wrap-safe restart detection ---

// TestCheckAndUpdateUptimeWatermark_FirstReading_NeverARestart proves a
// board with no prior watermark is never flagged as a restart -- there is
// no prior value to regress against (also covers the "device_id with zero
// readings ever" precondition the manifest exception cares about, even
// though that exception itself lives in handler.go, not here).
func TestCheckAndUpdateUptimeWatermark_FirstReading_NeverARestart(t *testing.T) {
	repo, pool := newClaimTestRepository(t)
	boardID := insertClaimBoard(t, pool, "leaflab-first")

	isRestart, err := repo.CheckAndUpdateUptimeWatermark(context.Background(), boardID, 10, time.Now(), 300)
	if err != nil {
		t.Fatalf("CheckAndUpdateUptimeWatermark: %v", err)
	}
	if isRestart {
		t.Error("first-ever reading for a board was flagged as a restart, want false (no prior watermark)")
	}

	var lastUptimeS int
	if err := pool.QueryRow(context.Background(), `SELECT last_uptime_s FROM board_uptime_watermark WHERE board_id = $1`, boardID).Scan(&lastUptimeS); err != nil {
		t.Fatalf("query watermark: %v", err)
	}
	if lastUptimeS != 10 {
		t.Errorf("watermark last_uptime_s = %d, want 10", lastUptimeS)
	}
}

// TestCheckAndUpdateUptimeWatermark_GenuineRegression_BelowThreshold_IsRestart
// is the requirement 4 core case: a value both lower than the watermark and
// below the configured threshold is a restart.
func TestCheckAndUpdateUptimeWatermark_GenuineRegression_BelowThreshold_IsRestart(t *testing.T) {
	repo, pool := newClaimTestRepository(t)
	boardID := insertClaimBoard(t, pool, "leaflab-restart")

	if _, err := repo.CheckAndUpdateUptimeWatermark(context.Background(), boardID, 3600, time.Now(), 300); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	isRestart, err := repo.CheckAndUpdateUptimeWatermark(context.Background(), boardID, 5, time.Now(), 300)
	if err != nil {
		t.Fatalf("CheckAndUpdateUptimeWatermark: %v", err)
	}
	if !isRestart {
		t.Error("uptime_s dropped from 3600 to 5 (below the 300s threshold), want isRestart=true")
	}
}

// TestCheckAndUpdateUptimeWatermark_WrapLikeRegression_AboveThreshold_NotRestart
// is the issue's named uint32-wrap test: an uptime_s sequence that decreases
// but stays above the configured threshold (the "lower-but-large" shape a
// ~49.7-day millis() wrap produces, per this repository method's own doc
// comment) must never be counted as a restart.
func TestCheckAndUpdateUptimeWatermark_WrapLikeRegression_AboveThreshold_NotRestart(t *testing.T) {
	repo, pool := newClaimTestRepository(t)
	boardID := insertClaimBoard(t, pool, "leaflab-wrap")

	const nearWrapBoundarySeconds = 4294960 // just under 2^32 ms / 1000
	if _, err := repo.CheckAndUpdateUptimeWatermark(context.Background(), boardID, nearWrapBoundarySeconds, time.Now(), 300); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	// A lower value that is still comfortably above the 300s threshold --
	// the wrap's "lower-but-large" shape.
	const afterWrapButLarge = 4294000
	isRestart, err := repo.CheckAndUpdateUptimeWatermark(context.Background(), boardID, afterWrapButLarge, time.Now(), 300)
	if err != nil {
		t.Fatalf("CheckAndUpdateUptimeWatermark: %v", err)
	}
	if isRestart {
		t.Errorf("uptime_s dropped from %d to %d (still above the 300s threshold -- the ms-wrap shape) was flagged as a restart, want false", nearWrapBoundarySeconds, afterWrapButLarge)
	}
}

// TestCheckAndUpdateUptimeWatermark_Increase_NotRestart is the baseline
// non-regression case: an increasing uptime_s is never a restart regardless
// of the threshold.
func TestCheckAndUpdateUptimeWatermark_Increase_NotRestart(t *testing.T) {
	repo, pool := newClaimTestRepository(t)
	boardID := insertClaimBoard(t, pool, "leaflab-increase")

	if _, err := repo.CheckAndUpdateUptimeWatermark(context.Background(), boardID, 10, time.Now(), 300); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
	isRestart, err := repo.CheckAndUpdateUptimeWatermark(context.Background(), boardID, 20, time.Now(), 300)
	if err != nil {
		t.Fatalf("CheckAndUpdateUptimeWatermark: %v", err)
	}
	if isRestart {
		t.Error("an increasing uptime_s was flagged as a restart, want false")
	}
}

// -- SatisfyOpenClaimRound: round-window matching, r>=2, uniqueness --------

// TestSatisfyOpenClaimRound_TwoRestarts_DischargesChallenge_OneRestartDoesNot
// is the issue's named r=2 test: one restart advances rounds_satisfied but
// leaves the challenge open; a second, independent restart against round 2
// discharges it.
func TestSatisfyOpenClaimRound_TwoRestarts_DischargesChallenge_OneRestartDoesNot(t *testing.T) {
	repo, pool := newClaimTestRepository(t)
	deviceID := "leaflab-r2"
	now := time.Now()

	challengeID, round1 := insertOpenChallengeWithRound(t, pool, "alice", deviceID, 2, 1, now.Add(-time.Minute), now.Add(2*time.Minute))

	reading1 := insertClaimReading(t, pool, now)
	if err := repo.SatisfyOpenClaimRound(context.Background(), deviceID, reading1, now); err != nil {
		t.Fatalf("SatisfyOpenClaimRound (round 1): %v", err)
	}

	state, roundsSatisfied, dischargedAt := challengeState(t, pool, challengeID)
	if state != "open" {
		t.Errorf("challenge state after 1 of 2 rounds = %q, want %q (one restart must not discharge an r=2 challenge)", state, "open")
	}
	if roundsSatisfied != 1 {
		t.Errorf("rounds_satisfied after round 1 = %d, want 1", roundsSatisfied)
	}
	if dischargedAt != nil {
		t.Error("discharged_at set after only 1 of 2 rounds, want nil")
	}
	if !roundClosed(t, pool, round1) {
		t.Error("round 1 not closed after being satisfied")
	}

	// Round 2 is marked (by the caller, per requirement 3 -- "round n+1's
	// bound opens only after round n closes") against the SAME challenge and
	// satisfied by a second, independent restart.
	round2 := insertClaimRound(t, pool, challengeID, deviceID, 2, now.Add(time.Minute), now.Add(4*time.Minute))
	reading2 := insertClaimReading(t, pool, now.Add(2*time.Minute))
	if err := repo.SatisfyOpenClaimRound(context.Background(), deviceID, reading2, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("SatisfyOpenClaimRound (round 2): %v", err)
	}

	state, roundsSatisfied, dischargedAt = challengeState(t, pool, challengeID)
	if state != "discharged" {
		t.Errorf("challenge state after 2 of 2 rounds = %q, want %q", state, "discharged")
	}
	if roundsSatisfied != 2 {
		t.Errorf("rounds_satisfied after round 2 = %d, want 2", roundsSatisfied)
	}
	if dischargedAt == nil {
		t.Error("discharged_at not set after the 2nd of 2 (r=2) rounds was satisfied")
	}
	if !roundClosed(t, pool, round2) {
		t.Error("round 2 not closed after being satisfied")
	}
}

// TestSatisfyOpenClaimRound_RestartBeforeT0_NotCounted proves a restart
// signal observed strictly before the round's t0 (the challenger-marked
// start) is ignored -- the round stays open, unsatisfied.
func TestSatisfyOpenClaimRound_RestartBeforeT0_NotCounted(t *testing.T) {
	repo, pool := newClaimTestRepository(t)
	deviceID := "leaflab-before-t0"
	now := time.Now()
	t0 := now
	boundExpiresAt := now.Add(3 * time.Minute)

	challengeID, roundID := insertOpenChallengeWithRound(t, pool, "alice", deviceID, 2, 1, t0, boundExpiresAt)

	// Observed one minute *before* t0.
	reading := insertClaimReading(t, pool, t0.Add(-time.Minute))
	if err := repo.SatisfyOpenClaimRound(context.Background(), deviceID, reading, t0.Add(-time.Minute)); err != nil {
		t.Fatalf("SatisfyOpenClaimRound: %v", err)
	}

	if roundClosed(t, pool, roundID) {
		t.Error("round was closed by a restart signal observed before t0, want it to remain open")
	}
	_, roundsSatisfied, _ := challengeState(t, pool, challengeID)
	if roundsSatisfied != 0 {
		t.Errorf("rounds_satisfied = %d, want 0 -- a pre-t0 signal must not count", roundsSatisfied)
	}
}

// TestSatisfyOpenClaimRound_RestartAfterBound_NotCounted proves a restart
// signal observed after the round's bound_expires_at is ignored.
func TestSatisfyOpenClaimRound_RestartAfterBound_NotCounted(t *testing.T) {
	repo, pool := newClaimTestRepository(t)
	deviceID := "leaflab-after-bound"
	now := time.Now()
	t0 := now.Add(-10 * time.Minute)
	boundExpiresAt := t0.Add(3 * time.Minute)

	challengeID, roundID := insertOpenChallengeWithRound(t, pool, "alice", deviceID, 2, 1, t0, boundExpiresAt)

	// Observed 5 minutes after t0 -- 2 minutes past the 3-minute bound.
	observedAt := t0.Add(5 * time.Minute)
	reading := insertClaimReading(t, pool, observedAt)
	if err := repo.SatisfyOpenClaimRound(context.Background(), deviceID, reading, observedAt); err != nil {
		t.Fatalf("SatisfyOpenClaimRound: %v", err)
	}

	if roundClosed(t, pool, roundID) {
		t.Error("round was closed by a restart signal observed after its bound, want it to remain open")
	}
	_, roundsSatisfied, _ := challengeState(t, pool, challengeID)
	if roundsSatisfied != 0 {
		t.Errorf("rounds_satisfied = %d, want 0 -- a post-bound signal must not count", roundsSatisfied)
	}
}

// TestSatisfyOpenClaimRound_OneReadingCannotSatisfyTwoRounds is the issue's
// named test: "one restart cannot satisfy two rounds." Two different
// principals each hold an open challenge against the same never-claimed
// device_id (permitted: requirement 2 forbids a per-board open-challenge
// cap), each with an open round waiting on the same device_id. A single
// restart signal (one sensor_reading row, one call) can only close one of
// them -- proved here by the migration's partial unique index on
// satisfied_by_reading_id: reusing the same reading_id for the second
// challenge's round is rejected, not silently allowed.
func TestSatisfyOpenClaimRound_OneReadingCannotSatisfyTwoRounds(t *testing.T) {
	repo, pool := newClaimTestRepository(t)
	deviceID := "leaflab-shared"
	now := time.Now()
	t0 := now.Add(-time.Minute)
	boundExpiresAt := now.Add(2 * time.Minute)

	_, roundA := insertOpenChallengeWithRound(t, pool, "alice", deviceID, 2, 1, t0, boundExpiresAt)
	_, roundB := insertOpenChallengeWithRound(t, pool, "bob", deviceID, 2, 1, t0, boundExpiresAt)

	reading := insertClaimReading(t, pool, now)

	if err := repo.SatisfyOpenClaimRound(context.Background(), deviceID, reading, now); err != nil {
		t.Fatalf("SatisfyOpenClaimRound (first call): %v", err)
	}

	aClosed, bClosed := roundClosed(t, pool, roundA), roundClosed(t, pool, roundB)
	if aClosed == bClosed {
		t.Fatalf("after one restart signal: round A closed=%v, round B closed=%v -- want exactly one closed", aClosed, bClosed)
	}

	// The still-open round is the only one SatisfyOpenClaimRound can find
	// next (the other is now closed_at IS NOT NULL, excluded by the WHERE
	// clause) -- reusing the SAME reading_id against it must be rejected by
	// idx_claim_challenge_round_reading_once, not silently double-satisfy.
	if err := repo.SatisfyOpenClaimRound(context.Background(), deviceID, reading, now); err == nil {
		t.Fatal("SatisfyOpenClaimRound (second call, same reading_id) succeeded, want the unique-index violation rejecting it")
	}

	// Neither round ends up satisfied by *both* -- the round left open
	// after the first call is still open after the rejected second call.
	stillOpenRound := roundA
	if aClosed {
		stillOpenRound = roundB
	}
	if roundClosed(t, pool, stillOpenRound) {
		t.Error("the second (rejected) SatisfyOpenClaimRound call still closed the other round -- one reading satisfied two rounds")
	}
}
