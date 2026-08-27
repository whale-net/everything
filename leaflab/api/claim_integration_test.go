//go:build integration

// Real-Postgres integration coverage for FR76's self-service board claim
// (#1342): requirement 1's "uniform initiation, no oracle at open" (both
// the response-body and timing halves), requirement 5/6's uniform
// terminal-state refusal (an exhausted/expired challenge and a discharged-
// against-a-real-household challenge must be indistinguishable from each
// other and from "not discharged"), requirement 6's discharge ->
// ownership-move outcome (including FR75's create-on-first-claim), FR77's
// server-side discharge record, and NFR10's device_id/principal-keyed rate
// limiting behaving identically whether or not device_id resolves.
//
// r>=2 round bookkeeping itself (t0/bound window matching, the "one restart
// satisfies at most one round, ever" uniqueness guarantee, and the uint32
// ms-wrap-safe restart signal) is covered in
// leaflab/processor/repository_claim_integration_test.go instead -- that
// logic lives entirely in leaflab/processor's Repository
// (SatisfyOpenClaimRound/CheckAndUpdateUptimeWatermark), not here. This
// file simulates "a challenge discharged" by writing claim_challenge's
// state/rounds_satisfied/discharged_at columns directly via SQL, exactly
// the shape leaflab/processor's writes would leave behind, so
// CompleteClaim's own logic can be tested without duplicating the
// processor's round-window integration coverage.
//
// Schema is self-contained hand-written DDL, mirroring migration
// 021_claim_challenge's claim_challenge/claim_challenge_round/
// claim_cooldown tables (trimmed: no FK to sensor_reading, since nothing
// here writes satisfied_by_reading_id) plus migration 015_ownership's
// household/household_membership/board/board_ownership shape (including
// household.is_unadopted, per that migration -- see claim.go's CompleteClaim
// query) and postgres.go's audit_log column set, same as
// households_integration_test.go's householdsTestSchema.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:claim_integration_test --test_output=all
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/claim"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const claimTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY,
		name         TEXT NOT NULL,
		is_unadopted BOOLEAN NOT NULL DEFAULT FALSE,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE household_membership (
		household_membership_id BIGSERIAL PRIMARY KEY,
		household_id             BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		principal_subject        TEXT NOT NULL,
		valid_from                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to                  TIMESTAMPTZ
	);
	CREATE INDEX idx_household_membership_principal_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		household_id  BIGINT REFERENCES household(household_id),
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE board_ownership (
		ownership_id  BIGSERIAL PRIMARY KEY,
		board_id      BIGINT NOT NULL REFERENCES board(board_id),
		household_id  BIGINT NOT NULL REFERENCES household(household_id),
		valid_from    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to      TIMESTAMPTZ
	);

	CREATE TABLE claim_challenge (
		challenge_id      BIGSERIAL PRIMARY KEY,
		handle             TEXT NOT NULL UNIQUE,
		principal_subject  TEXT NOT NULL,
		device_id          TEXT NOT NULL,
		rounds_required    INT NOT NULL,
		rounds_satisfied   INT NOT NULL DEFAULT 0,
		attempts_used      INT NOT NULL DEFAULT 0,
		opened_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at         TIMESTAMPTZ NOT NULL,
		state              TEXT NOT NULL DEFAULT 'open'
		                       CHECK (state IN ('open', 'discharged', 'not_discharged')),
		discharged_at      TIMESTAMPTZ NULL
	);
	CREATE UNIQUE INDEX idx_claim_challenge_open_per_principal_device
		ON claim_challenge(principal_subject, device_id) WHERE state = 'open';
	CREATE INDEX idx_claim_challenge_handle ON claim_challenge(handle);

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
		UNIQUE (challenge_id, round_index)
	);

	CREATE TABLE claim_cooldown (
		principal_subject  TEXT NOT NULL,
		device_id          TEXT NOT NULL,
		until               TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (principal_subject, device_id)
	);

	CREATE TABLE audit_log (
		audit_id             BIGSERIAL PRIMARY KEY,
		actor_subject        TEXT NOT NULL,
		actor_kind           TEXT NOT NULL,
		target_household_id  BIGINT NULL REFERENCES household(household_id),
		action                TEXT NOT NULL,
		entity_kind           TEXT NOT NULL,
		entity_id             TEXT NULL,
		reason                TEXT NULL,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id        TEXT NULL
	);
`

// newClaimTestServer starts a real Postgres container with claimTestSchema
// applied. authzSvc/discardLogger/allScope/stubAuthz come from
// dbtest_helpers_integration_test.go (this target lists that file in its
// srcs -- see BUILD.bazel): nothing exercised by this file's tests reaches
// ListBoards/GetDeviceConfig's Scope-checked paths, only the claim RPCs
// (which authorize purely on principal_subject/challenge ownership, not
// authz.Scope), so stubAuthz is sufficient here exactly as it is for
// response_contract_integration_test.go. limiterConfigs is passed straight
// to ratelimit.NewInMemoryLimiter (nil uses ratelimit.DefaultConfigs) so
// rate-limit tests can supply small, deterministic windows without waiting
// out the real hour-long defaults.
func newClaimTestServer(t *testing.T, limiterConfigs map[ratelimit.Bucket]ratelimit.WindowConfig) (*LeafLabAPIServer, *Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: claimTestSchema})
	repo := NewRepository(db.Pool)
	server := NewLeafLabAPIServer(repo, stubAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(limiterConfigs))
	return server, repo, db.Pool
}

func claimCtxFor(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func insertClaimHousehold(t *testing.T, pool *pgxpool.Pool, name string, isUnadopted bool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household (name, is_unadopted) VALUES ($1, $2) RETURNING household_id`, name, isUnadopted).Scan(&id); err != nil {
		t.Fatalf("insert household %q: %v", name, err)
	}
	return id
}

func insertClaimHouseholdMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`, householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

// insertClaimBoard inserts a board row, optionally owned by householdID (0 =
// never-claimed, board.household_id NULL).
func insertClaimBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	var hh any
	if householdID != 0 {
		hh = householdID
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`, deviceID, hh).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	if householdID != 0 {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO board_ownership (board_id, household_id) VALUES ($1, $2)`, id, householdID); err != nil {
			t.Fatalf("insert board_ownership for board %d: %v", id, err)
		}
	}
	return id
}

// dischargeChallenge writes exactly the terminal state
// leaflab/processor's SatisfyOpenClaimRound would leave behind once
// rounds_satisfied reaches rounds_required -- see this file's doc comment
// for why round-window bookkeeping itself isn't re-tested here.
func dischargeChallenge(t *testing.T, pool *pgxpool.Pool, handle string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE claim_challenge SET state = 'discharged', rounds_satisfied = rounds_required, discharged_at = NOW()
		WHERE handle = $1
	`, handle); err != nil {
		t.Fatalf("discharge challenge %q: %v", handle, err)
	}
}

// expireChallenge forces handle's challenge past its lifetime, so the next
// call touching it (MarkClaimRound/GetClaimChallengeStatus/CompleteClaim)
// exhausts it (requirement 5).
func expireChallenge(t *testing.T, pool *pgxpool.Pool, handle string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE claim_challenge SET expires_at = NOW() - interval '1 second' WHERE handle = $1`, handle); err != nil {
		t.Fatalf("expire challenge %q: %v", handle, err)
	}
}

func claimChallengeExists(t *testing.T, pool *pgxpool.Pool, principalSubject, deviceID string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM claim_challenge WHERE principal_subject = $1 AND device_id = $2)`,
		principalSubject, deviceID).Scan(&exists); err != nil {
		t.Fatalf("check claim_challenge existence: %v", err)
	}
	return exists
}

// -- Requirement 1: uniform initiation, no oracle at open --------------------

// claimBoardFixtures sets up the four device_id categories requirement 1
// names: never-claimed, resolves-to-Unadopted, owned by a real household,
// and does not exist at all. Returns the four device_ids.
type claimFourWayFixtures struct {
	neverClaimed     string
	unadopted        string
	ownedByHousehold string
	nonexistent      string
}

func setupClaimFourWayFixtures(t *testing.T, pool *pgxpool.Pool) claimFourWayFixtures {
	t.Helper()
	unadoptedHousehold := insertClaimHousehold(t, pool, "Unadopted", true)
	realHousehold := insertClaimHousehold(t, pool, "Real Household", false)

	f := claimFourWayFixtures{
		neverClaimed:     "leaflab-never-claimed",
		unadopted:        "leaflab-unadopted",
		ownedByHousehold: "leaflab-owned",
		nonexistent:      "leaflab-does-not-exist",
	}
	insertClaimBoard(t, pool, f.neverClaimed, 0)
	insertClaimBoard(t, pool, f.unadopted, unadoptedHousehold)
	insertClaimBoard(t, pool, f.ownedByHousehold, realHousehold)
	// f.nonexistent: deliberately no board row at all.
	return f
}

// TestOpenClaimChallenge_UniformInitiation_FourWayIndistinguishable is
// requirement 1's core assertion: opening a possession challenge against
// each of the four device_id categories succeeds identically -- same
// status (no error), same RoundsRequired, same Instructions, and an
// ExpiresAt consistent with the same configured lifetime. Each call uses a
// distinct principal so requirement 2's one-open-challenge-per-pair
// constraint doesn't interfere between the four sub-cases.
func TestOpenClaimChallenge_UniformInitiation_FourWayIndistinguishable(t *testing.T) {
	server, _, pool := newClaimTestServer(t, nil)
	f := setupClaimFourWayFixtures(t, pool)

	cases := []struct {
		name     string
		deviceID string
	}{
		{"never-claimed", f.neverClaimed},
		{"unadopted", f.unadopted},
		{"owned-by-real-household", f.ownedByHousehold},
		{"nonexistent", f.nonexistent},
	}

	var responses []*pb.OpenClaimChallengeResponse
	for _, c := range cases {
		principal := "principal-" + c.name
		resp, err := server.OpenClaimChallenge(claimCtxFor(principal), &pb.OpenClaimChallengeRequest{DeviceId: c.deviceID})
		if err != nil {
			t.Fatalf("OpenClaimChallenge(%s): %v", c.name, err)
		}
		if resp.GetChallengeHandle() == "" {
			t.Errorf("OpenClaimChallenge(%s): empty challenge_handle", c.name)
		}
		responses = append(responses, resp)
	}

	for i := 1; i < len(responses); i++ {
		if responses[i].GetRoundsRequired() != responses[0].GetRoundsRequired() {
			t.Errorf("%s: RoundsRequired = %d, want %d (same as %s)", cases[i].name, responses[i].GetRoundsRequired(), responses[0].GetRoundsRequired(), cases[0].name)
		}
		if responses[i].GetInstructions() != responses[0].GetInstructions() {
			t.Errorf("%s: Instructions differs from %s", cases[i].name, cases[0].name)
		}
		// ExpiresAt must be computed from the same configured lifetime for
		// every case -- within a generous tolerance for wall-clock drift
		// between the four sequential calls in this test, not for any
		// path-dependent difference.
		diff := contract.FromInstant(responses[i].GetExpiresAt()).Sub(contract.FromInstant(responses[0].GetExpiresAt()))
		if diff < -5*time.Second || diff > 5*time.Second {
			t.Errorf("%s: ExpiresAt differs from %s by %v, want within 5s (same configured lifetime)", cases[i].name, cases[0].name, diff)
		}
	}
}

// TestOpenClaimChallenge_NeverQueriesBoard_SucceedsForNonexistentDeviceID
// proves requirement 1's "does not exist at all" case succeeds on its own
// terms: a challenge row is created for a device_id naming no board row,
// which could only happen if OpenClaimChallenge never required board to
// exist first.
func TestOpenClaimChallenge_NeverQueriesBoard_SucceedsForNonexistentDeviceID(t *testing.T) {
	server, _, pool := newClaimTestServer(t, nil)

	resp, err := server.OpenClaimChallenge(claimCtxFor("alice"), &pb.OpenClaimChallengeRequest{DeviceId: "leaflab-totally-unknown"})
	if err != nil {
		t.Fatalf("OpenClaimChallenge for a nonexistent device_id: %v", err)
	}
	if resp.GetChallengeHandle() == "" {
		t.Fatal("empty challenge_handle for a nonexistent device_id")
	}
	if !claimChallengeExists(t, pool, "alice", "leaflab-totally-unknown") {
		t.Error("no claim_challenge row was created for a nonexistent device_id")
	}
}

// TestOpenClaimChallenge_NFR2ish_TimingIndistinguishable is requirement 1's
// timing half: opening a challenge against a nonexistent device_id and
// against a real household's board must not be separable by latency, since
// OpenClaimChallenge never queries board for either. Same method/tolerance
// as leaflab/api's authz_scope_integration_test.go
// TestGetDeviceConfig_NFR2_TimingIndistinguishable.
func TestOpenClaimChallenge_NFR2ish_TimingIndistinguishable(t *testing.T) {
	server, _, pool := newClaimTestServer(t, nil)
	f := setupClaimFourWayFixtures(t, pool)

	const warmup = 5
	const n = 40
	const toleranceRatio = 0.75

	// Both arms reuse a single fixed principal throughout: after the first
	// call, every subsequent call for that (principal, device_id) pair
	// follows OpenClaimChallenge's idempotent-reopen path (the same
	// existing-open-challenge SELECT, no INSERT) rather than the initial
	// insert path -- but since the same is true symmetrically for both
	// arms, this keeps them doing identical-shaped work throughout the run,
	// not just on the very first call.
	callNonexistent := func() time.Duration {
		start := time.Now()
		if _, err := server.OpenClaimChallenge(claimCtxFor("timing-nonexistent"), &pb.OpenClaimChallengeRequest{DeviceId: f.nonexistent}); err != nil {
			t.Fatalf("OpenClaimChallenge (nonexistent): %v", err)
		}
		return time.Since(start)
	}
	callOwned := func() time.Duration {
		start := time.Now()
		if _, err := server.OpenClaimChallenge(claimCtxFor("timing-owned"), &pb.OpenClaimChallengeRequest{DeviceId: f.ownedByHousehold}); err != nil {
			t.Fatalf("OpenClaimChallenge (owned): %v", err)
		}
		return time.Since(start)
	}

	var totalNonexistent, totalOwned time.Duration
	for j := 0; j < warmup; j++ {
		callNonexistent()
		callOwned()
	}
	for j := 0; j < n; j++ {
		totalNonexistent += callNonexistent()
		totalOwned += callOwned()
	}

	meanNonexistent := totalNonexistent / time.Duration(n)
	meanOwned := totalOwned / time.Duration(n)

	diff := meanNonexistent - meanOwned
	if diff < 0 {
		diff = -diff
	}
	larger := meanNonexistent
	if meanOwned > larger {
		larger = meanOwned
	}
	if larger == 0 {
		t.Fatal("both mean latencies measured as 0 -- test setup problem")
	}
	if ratio := float64(diff) / float64(larger); ratio > toleranceRatio {
		t.Errorf("mean latency for nonexistent (%v) vs owned-by-real-household (%v) differs by %.0f%% of the larger mean over N=%d, want <= %.0f%%",
			meanNonexistent, meanOwned, ratio*100, n, toleranceRatio*100)
	}
}

// -- Requirement 5/6: terminal-state uniform refusal --------------------------

// TestCompleteClaim_TerminalStates_FourWayIndistinguishable is the issue's
// "same four-way indistinguishability for a failed/expired/exhausted
// challenge" bullet: an expired-but-never-discharged challenge against each
// of the four device_id categories fails CompleteClaim identically.
func TestCompleteClaim_TerminalStates_FourWayIndistinguishable(t *testing.T) {
	server, _, pool := newClaimTestServer(t, nil)
	f := setupClaimFourWayFixtures(t, pool)

	cases := []struct {
		name     string
		deviceID string
	}{
		{"never-claimed", f.neverClaimed},
		{"unadopted", f.unadopted},
		{"owned-by-real-household", f.ownedByHousehold},
		{"nonexistent", f.nonexistent},
	}

	var errs []error
	for _, c := range cases {
		principal := "expired-" + c.name
		openResp, err := server.OpenClaimChallenge(claimCtxFor(principal), &pb.OpenClaimChallengeRequest{DeviceId: c.deviceID})
		if err != nil {
			t.Fatalf("OpenClaimChallenge(%s): %v", c.name, err)
		}
		expireChallenge(t, pool, openResp.GetChallengeHandle())

		_, err = server.CompleteClaim(claimCtxFor(principal), &pb.CompleteClaimRequest{ChallengeHandle: openResp.GetChallengeHandle()})
		if err == nil {
			t.Fatalf("CompleteClaim(%s) on an expired, never-discharged challenge returned nil error, want a refusal", c.name)
		}
		errs = append(errs, err)
	}

	first, ok := contract.FromError(errs[0])
	if !ok {
		t.Fatal("first refusal carries no Failure detail")
	}
	for i := 1; i < len(errs); i++ {
		detail, ok := contract.FromError(errs[i])
		if !ok {
			t.Fatalf("%s refusal carries no Failure detail", cases[i].name)
		}
		if !proto.Equal(first, detail) {
			t.Errorf("%s Failure detail differs from %s: %v vs %v", cases[i].name, cases[0].name, detail, first)
		}
		firstBytes, err := proto.Marshal(status.Convert(errs[0]).Proto())
		if err != nil {
			t.Fatalf("marshal %s status: %v", cases[0].name, err)
		}
		iBytes, err := proto.Marshal(status.Convert(errs[i]).Proto())
		if err != nil {
			t.Fatalf("marshal %s status: %v", cases[i].name, err)
		}
		if string(firstBytes) != string(iBytes) {
			t.Errorf("%s marshaled gRPC status differs from %s", cases[i].name, cases[0].name)
		}
	}
}

// TestCompleteClaim_DischargedAgainstRealHousehold_FailsIdenticallyButRecordsDischargedAt
// is requirement 6's central caveat: a discharged challenge against a board
// a real household already owns confers nothing and fails exactly like a
// not-discharged challenge -- yet the discharge fact is still recorded
// server-side (FR77 evidence), not silently dropped.
func TestCompleteClaim_DischargedAgainstRealHousehold_FailsIdenticallyButRecordsDischargedAt(t *testing.T) {
	server, _, pool := newClaimTestServer(t, nil)
	realHousehold := insertClaimHousehold(t, pool, "Real Household", false)
	insertClaimHouseholdMembership(t, pool, realHousehold, "owner@example.com")
	deviceID := "leaflab-owned-discharge"
	boardID := insertClaimBoard(t, pool, deviceID, realHousehold)

	openResp, err := server.OpenClaimChallenge(claimCtxFor("attacker@example.com"), &pb.OpenClaimChallengeRequest{DeviceId: deviceID})
	if err != nil {
		t.Fatalf("OpenClaimChallenge: %v", err)
	}
	dischargeChallenge(t, pool, openResp.GetChallengeHandle())

	// Compare against a genuinely not-discharged (never-claimed) challenge's
	// refusal for the "fails identically" half of this test.
	neverClaimedDeviceID := "leaflab-never-claimed-compare"
	insertClaimBoard(t, pool, neverClaimedDeviceID, 0)
	neverClaimedOpen, err := server.OpenClaimChallenge(claimCtxFor("attacker2@example.com"), &pb.OpenClaimChallengeRequest{DeviceId: neverClaimedDeviceID})
	if err != nil {
		t.Fatalf("OpenClaimChallenge (never-claimed compare): %v", err)
	}
	expireChallenge(t, pool, neverClaimedOpen.GetChallengeHandle())

	_, dischargedErr := server.CompleteClaim(claimCtxFor("attacker@example.com"), &pb.CompleteClaimRequest{ChallengeHandle: openResp.GetChallengeHandle()})
	if dischargedErr == nil {
		t.Fatal("CompleteClaim discharged against a real household's board returned nil error, want a refusal")
	}
	_, notDischargedErr := server.CompleteClaim(claimCtxFor("attacker2@example.com"), &pb.CompleteClaimRequest{ChallengeHandle: neverClaimedOpen.GetChallengeHandle()})
	if notDischargedErr == nil {
		t.Fatal("CompleteClaim on the not-discharged compare challenge returned nil error, want a refusal")
	}

	dischargedDetail, ok := contract.FromError(dischargedErr)
	if !ok {
		t.Fatal("discharged-against-real-household refusal carries no Failure detail")
	}
	notDischargedDetail, ok := contract.FromError(notDischargedErr)
	if !ok {
		t.Fatal("not-discharged refusal carries no Failure detail")
	}
	if !proto.Equal(dischargedDetail, notDischargedDetail) {
		t.Errorf("discharged-against-real-household refusal differs from not-discharged: %v vs %v", dischargedDetail, notDischargedDetail)
	}

	// Board ownership must be unchanged -- CompleteClaim conferred nothing.
	var ownerHouseholdID *int64
	if err := pool.QueryRow(context.Background(), `SELECT household_id FROM board WHERE board_id = $1`, boardID).Scan(&ownerHouseholdID); err != nil {
		t.Fatalf("query board household_id: %v", err)
	}
	if ownerHouseholdID == nil || *ownerHouseholdID != realHousehold {
		t.Errorf("board household_id after a discharged-but-refused claim = %v, want unchanged %d", ownerHouseholdID, realHousehold)
	}

	// FR77: the discharge fact itself is recorded server-side even though
	// the claim outcome was refused.
	var dischargedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT discharged_at FROM claim_challenge WHERE handle = $1`, openResp.GetChallengeHandle()).Scan(&dischargedAt); err != nil {
		t.Fatalf("query discharged_at: %v", err)
	}
	if dischargedAt == nil {
		t.Error("discharged_at is NULL for a challenge that reached the discharged state -- FR77's evidence must be recorded regardless of CompleteClaim's outcome")
	}
}

// -- Requirement 6: discharge -> ownership move (never-claimed / Unadopted) --

// TestCompleteClaim_DischargedAgainstNeverClaimed_MovesOwnership proves the
// success path for a never-claimed board: ownership moves to the
// claimant's household and a board_ownership row opens.
func TestCompleteClaim_DischargedAgainstNeverClaimed_MovesOwnership(t *testing.T) {
	server, _, pool := newClaimTestServer(t, nil)
	claimantHousehold := insertClaimHousehold(t, pool, "Claimant Household", false)
	insertClaimHouseholdMembership(t, pool, claimantHousehold, "claimant@example.com")
	deviceID := "leaflab-never-claimed-success"
	boardID := insertClaimBoard(t, pool, deviceID, 0)

	openResp, err := server.OpenClaimChallenge(claimCtxFor("claimant@example.com"), &pb.OpenClaimChallengeRequest{DeviceId: deviceID})
	if err != nil {
		t.Fatalf("OpenClaimChallenge: %v", err)
	}
	dischargeChallenge(t, pool, openResp.GetChallengeHandle())

	resp, err := server.CompleteClaim(claimCtxFor("claimant@example.com"), &pb.CompleteClaimRequest{ChallengeHandle: openResp.GetChallengeHandle()})
	if err != nil {
		t.Fatalf("CompleteClaim: %v", err)
	}
	if resp.GetHousehold().GetHouseholdId() != claimantHousehold {
		t.Errorf("CompleteClaim household_id = %d, want %d", resp.GetHousehold().GetHouseholdId(), claimantHousehold)
	}

	var boardHousehold *int64
	if err := pool.QueryRow(context.Background(), `SELECT household_id FROM board WHERE board_id = $1`, boardID).Scan(&boardHousehold); err != nil {
		t.Fatalf("query board: %v", err)
	}
	if boardHousehold == nil || *boardHousehold != claimantHousehold {
		t.Errorf("board.household_id after claim = %v, want %d", boardHousehold, claimantHousehold)
	}

	var ownershipCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM board_ownership WHERE board_id = $1 AND household_id = $2 AND valid_to IS NULL`,
		boardID, claimantHousehold).Scan(&ownershipCount); err != nil {
		t.Fatalf("query board_ownership: %v", err)
	}
	if ownershipCount != 1 {
		t.Errorf("current board_ownership rows for claimed board = %d, want 1", ownershipCount)
	}
}

// TestCompleteClaim_DischargedAgainstUnadopted_MovesOwnership_ClosesPriorOwnership
// proves the Unadopted case: the prior (Unadopted) board_ownership row is
// closed (SCD2, AGENTS.md) and a new current one opens for the claimant's
// household.
func TestCompleteClaim_DischargedAgainstUnadopted_MovesOwnership_ClosesPriorOwnership(t *testing.T) {
	server, _, pool := newClaimTestServer(t, nil)
	unadoptedHousehold := insertClaimHousehold(t, pool, "Unadopted", true)
	claimantHousehold := insertClaimHousehold(t, pool, "Claimant Household", false)
	insertClaimHouseholdMembership(t, pool, claimantHousehold, "claimant@example.com")
	deviceID := "leaflab-unadopted-success"
	boardID := insertClaimBoard(t, pool, deviceID, unadoptedHousehold)

	openResp, err := server.OpenClaimChallenge(claimCtxFor("claimant@example.com"), &pb.OpenClaimChallengeRequest{DeviceId: deviceID})
	if err != nil {
		t.Fatalf("OpenClaimChallenge: %v", err)
	}
	dischargeChallenge(t, pool, openResp.GetChallengeHandle())

	if _, err := server.CompleteClaim(claimCtxFor("claimant@example.com"), &pb.CompleteClaimRequest{ChallengeHandle: openResp.GetChallengeHandle()}); err != nil {
		t.Fatalf("CompleteClaim: %v", err)
	}

	var unadoptedValidTo *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT valid_to FROM board_ownership WHERE board_id = $1 AND household_id = $2`,
		boardID, unadoptedHousehold).Scan(&unadoptedValidTo); err != nil {
		t.Fatalf("query prior (Unadopted) board_ownership: %v", err)
	}
	if unadoptedValidTo == nil {
		t.Error("prior Unadopted board_ownership row still has NULL valid_to -- want it closed (SCD2)")
	}

	var currentHouseholdID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT household_id FROM board_ownership WHERE board_id = $1 AND valid_to IS NULL`, boardID).Scan(&currentHouseholdID); err != nil {
		t.Fatalf("query current board_ownership: %v", err)
	}
	if currentHouseholdID != claimantHousehold {
		t.Errorf("current board_ownership household_id = %d, want %d", currentHouseholdID, claimantHousehold)
	}
}

// TestCompleteClaim_FirstSuccessfulClaim_NoHousehold_CreatesOne is the
// issue's "a first successful claim by a principal with no household
// creates one (FR75 integration)" bullet.
func TestCompleteClaim_FirstSuccessfulClaim_NoHousehold_CreatesOne(t *testing.T) {
	server, _, pool := newClaimTestServer(t, nil)
	deviceID := "leaflab-first-claim"
	insertClaimBoard(t, pool, deviceID, 0)

	openResp, err := server.OpenClaimChallenge(claimCtxFor("newcomer@example.com"), &pb.OpenClaimChallengeRequest{DeviceId: deviceID})
	if err != nil {
		t.Fatalf("OpenClaimChallenge: %v", err)
	}
	dischargeChallenge(t, pool, openResp.GetChallengeHandle())

	resp, err := server.CompleteClaim(claimCtxFor("newcomer@example.com"), &pb.CompleteClaimRequest{ChallengeHandle: openResp.GetChallengeHandle()})
	if err != nil {
		t.Fatalf("CompleteClaim: %v", err)
	}
	if resp.GetHousehold().GetHouseholdId() == 0 {
		t.Fatal("CompleteClaim for a principal with no prior household returned a zero household_id")
	}

	var memberCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM household_membership WHERE household_id = $1 AND principal_subject = $2 AND valid_to IS NULL`,
		resp.GetHousehold().GetHouseholdId(), "newcomer@example.com").Scan(&memberCount); err != nil {
		t.Fatalf("query household_membership: %v", err)
	}
	if memberCount != 1 {
		t.Errorf("household_membership rows for the newly created household/principal = %d, want 1", memberCount)
	}
}

// -- NFR10: claim_open rate limiting is identical for a resolving and a -----
// -- non-resolving device_id --------------------------------------------------

// TestOpenClaimChallenge_RateLimiting_ResolvingAndNonResolvingDeviceID_Identical
// is the issue's "rate limiting a resolving and a non-resolving device_id
// behaves identically" bullet: with a tight claim_open window (limit=2),
// the Nth call that trips the limit does so at the same N whether or not
// device_id resolves to a real board -- since the rate-limit key is built
// from the submitted device_id/principal strings alone (claimOpenRateLimitKey,
// server.go), never from a board lookup.
func TestOpenClaimChallenge_RateLimiting_ResolvingAndNonResolvingDeviceID_Identical(t *testing.T) {
	server, _, pool := newClaimTestServer(t, map[ratelimit.Bucket]ratelimit.WindowConfig{
		ratelimit.BucketClaimOpen: {Limit: 2, Window: time.Hour},
	})
	realHousehold := insertClaimHousehold(t, pool, "Real Household", false)
	resolvingDeviceID := "leaflab-rl-resolving"
	insertClaimBoard(t, pool, resolvingDeviceID, realHousehold)
	nonResolvingDeviceID := "leaflab-rl-nonresolving"

	// server.go's OpenClaimChallenge calls limiter.Allow *before* delegating
	// to the repository, so every call -- including one that will turn out
	// to hit the idempotent-reopen path -- consumes one Allow slot. Same
	// principal reused across all 3 attempts per device_id, so each arm
	// exercises the real limiter counter, not a fresh-pair reset.
	attempt := func(deviceID string) error {
		principal := deviceID + "-caller"
		_, err := server.OpenClaimChallenge(claimCtxFor(principal), &pb.OpenClaimChallengeRequest{DeviceId: deviceID})
		return err
	}

	var resolvingResults, nonResolvingResults []error
	for i := 0; i < 3; i++ {
		resolvingResults = append(resolvingResults, attempt(resolvingDeviceID))
		nonResolvingResults = append(nonResolvingResults, attempt(nonResolvingDeviceID))
	}

	for i := range resolvingResults {
		resolvingLimited := resolvingResults[i] != nil
		nonResolvingLimited := nonResolvingResults[i] != nil
		if resolvingLimited != nonResolvingLimited {
			t.Errorf("attempt %d: resolving device_id limited=%v (err=%v), non-resolving limited=%v (err=%v) -- want identical", i+1, resolvingLimited, resolvingResults[i], nonResolvingLimited, nonResolvingResults[i])
		}
	}

	// With Limit=2, the 3rd attempt against either device_id must be the
	// one that trips the limit -- proving the symmetry above isn't just
	// "both always succeed" by coincidence.
	if resolvingResults[0] != nil || resolvingResults[1] != nil {
		t.Errorf("resolving device_id: attempts 1-2 = %v, %v, want both nil (within Limit=2)", resolvingResults[0], resolvingResults[1])
	}
	if resolvingResults[2] == nil {
		t.Error("resolving device_id: attempt 3 succeeded, want it rate-limited (Limit=2)")
	}
	if nonResolvingResults[0] != nil || nonResolvingResults[1] != nil {
		t.Errorf("non-resolving device_id: attempts 1-2 = %v, %v, want both nil (within Limit=2)", nonResolvingResults[0], nonResolvingResults[1])
	}
	if nonResolvingResults[2] == nil {
		t.Error("non-resolving device_id: attempt 3 succeeded, want it rate-limited (Limit=2)")
	}
}
