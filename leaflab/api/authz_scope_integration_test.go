//go:build integration

// Real-Postgres integration coverage for FR4/FR5/NFR2's per-entity
// household authorization: ListBoards' household scoping (including the
// FR75 multi-household UnionScope case and the FR5.1 "no household" empty
// case), GetDeviceConfig's NFR2 no-existence-oracle refusal (both the
// response-body and the timing half of that requirement), and FR5.2's
// "an aggregate doesn't leak another household's existence" guarantee.
//
// This exercises a real authz.PGResolver against a real household /
// household_membership / board schema -- the Go-level authz unit tests in
// leaflab/api/authz (resolver_test.go, scope_test.go) prove the same
// contracts against a fake Queryer; this file proves the actual SQL
// (the household_membership join, the board join) behaves the same way.
// Schema is self-contained hand-written DDL, deliberately not shared with
// dbtest_helpers_integration_test.go's testSchema (which has no household
// concept at all -- its allScope/stubAuthz fixtures exist precisely so
// FR59/FR61/FR64's response-contract tests don't need one). See
// //libs/go/dbtest/README.md for how to run integration tests like this
// one; same tag set as this package's other integration go_test targets.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:authz_scope_integration_test --test_output=all
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// authzTestSchema mirrors the authorization-relevant shape of migration
// 015_ownership (board.household_id as a nullable current-value cache,
// FR1.1's Unclaimed exception; household_membership in SCD2 shape per
// AGENTS.md's valid_from/valid_to convention) plus the board/device_config
// tables leaflab/api/repository.go touches. It is intentionally narrower
// than the real migration -- only what authz.PGResolver and
// Repository.ListBoards actually read. admin_elevation (migration
// 029_admin_elevation) is included too: authorizeBoardAccess's
// elevatedBoardScope (FR10.3) queries it unconditionally on every
// out-of-scope board read, admin caller or not, so this schema needs the
// table even though no test in this file exercises elevation itself --
// see admin_elevation_integration_test.go's TestGetDeviceConfig_ElevatedAdmin_FR10_3
// for that coverage, against a schema built for it.
const authzTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE household_membership (
		membership_id     BIGSERIAL PRIMARY KEY,
		household_id      BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject TEXT NOT NULL,
		valid_from        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to          TIMESTAMPTZ
	);
	CREATE INDEX idx_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		household_id  BIGINT REFERENCES household(household_id),
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at    TIMESTAMPTZ
	);
	CREATE INDEX idx_board_active ON board(board_id) WHERE retired_at IS NULL;

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);

	CREATE TABLE admin_elevation (
		elevation_id        BIGSERIAL PRIMARY KEY,
		admin_subject        TEXT NOT NULL,
		target_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		reason                TEXT NOT NULL,
		started_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at            TIMESTAMPTZ NOT NULL,
		ended_at              TIMESTAMPTZ NULL
	);
`

// newAuthzTestServer starts a real Postgres container with authzTestSchema
// applied and returns a LeafLabAPIServer backed by a real Repository and a
// real authz.PGResolver -- unlike newTestServer (dbtest_helpers), which
// uses stubAuthz/allScope specifically to stay decoupled from a household
// schema. publisher is nil: no test here reaches PushDeviceConfig's
// publish step.
func newAuthzTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: authzTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return NewLeafLabAPIServer(repo, resolver, nil, nil, discardLogger()), db.Pool
}

func authzCtxFor(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func insertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

func insertScopedBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`,
		deviceID, householdID).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

func boardDeviceIDs(resp *pb.ListBoardsResponse) map[string]bool {
	got := map[string]bool{}
	for _, b := range resp.Boards {
		got[b.DeviceId] = true
	}
	return got
}

// -- FR5.1/FR5.2: ListBoards household scoping --------------------------------

// TestListBoards_ScopedToCallerHousehold_ExcludesOtherHouseholds is FR5.1's
// core assertion over real SQL: a member of household A sees only A's
// boards, never B's.
func TestListBoards_ScopedToCallerHousehold_ExcludesOtherHouseholds(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	householdA := insertHousehold(t, pool)
	householdB := insertHousehold(t, pool)
	insertMembership(t, pool, householdA, "alice")
	insertScopedBoard(t, pool, "device-a1", householdA)
	insertScopedBoard(t, pool, "device-b1", householdB)

	resp, err := server.ListBoards(authzCtxFor("alice"), &pb.ListBoardsRequest{})
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}

	got := boardDeviceIDs(resp)
	if !got["device-a1"] {
		t.Error("household A's own board is missing from ListBoards")
	}
	if got["device-b1"] {
		t.Error("household B's board leaked into household A member's ListBoards result")
	}
	if len(resp.Boards) != 1 {
		t.Errorf("len(Boards) = %d, want 1 (only household A's board)", len(resp.Boards))
	}
}

// TestListBoards_MultiHouseholdMembership_UnionScope is FR75's case: a
// principal with two *current* household_membership rows sees boards from
// both households, not just the first one found.
func TestListBoards_MultiHouseholdMembership_UnionScope(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	householdA := insertHousehold(t, pool)
	householdB := insertHousehold(t, pool)
	householdC := insertHousehold(t, pool)
	insertMembership(t, pool, householdA, "bob")
	insertMembership(t, pool, householdB, "bob")
	insertScopedBoard(t, pool, "device-a1", householdA)
	insertScopedBoard(t, pool, "device-b1", householdB)
	insertScopedBoard(t, pool, "device-c1", householdC)

	resp, err := server.ListBoards(authzCtxFor("bob"), &pb.ListBoardsRequest{})
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}

	got := boardDeviceIDs(resp)
	if !got["device-a1"] || !got["device-b1"] {
		t.Errorf("multi-household member is missing a board from one of their households: got %v", got)
	}
	if got["device-c1"] {
		t.Error("board from a household bob is not a member of leaked into ListBoards")
	}
	if len(resp.Boards) != 2 {
		t.Errorf("len(Boards) = %d, want 2 (A and B, not C)", len(resp.Boards))
	}
}

// TestListBoards_NoHouseholdMembership_ReturnsEmptyList_NotError proves
// FR5.1's exact phrasing: a caller in no household gets an empty list, not
// an error and not everything -- even though boards exist in the database
// for other households.
func TestListBoards_NoHouseholdMembership_ReturnsEmptyList_NotError(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	householdA := insertHousehold(t, pool)
	insertScopedBoard(t, pool, "device-a1", householdA)
	insertScopedBoard(t, pool, "device-a2", householdA)
	// "carol" has no household_membership row at all.

	resp, err := server.ListBoards(authzCtxFor("carol"), &pb.ListBoardsRequest{})
	if err != nil {
		t.Fatalf("ListBoards for a caller with no household returned an error, want an empty list: %v", err)
	}
	if len(resp.Boards) != 0 {
		t.Errorf("len(Boards) = %d, want 0 -- a caller with no household must never see everything", len(resp.Boards))
	}
}

// TestListBoards_AggregateUnaffectedByOtherHouseholdBoard is FR5.2's
// "aggregate counts do not leak the existence of another household"
// applied to the one listing/aggregate-shaped RPC this phase has: the
// number of boards a caller sees for their own household must not change
// when a board is added to a household they don't belong to.
func TestListBoards_AggregateUnaffectedByOtherHouseholdBoard(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	householdA := insertHousehold(t, pool)
	householdB := insertHousehold(t, pool)
	insertMembership(t, pool, householdA, "alice")
	insertScopedBoard(t, pool, "device-a1", householdA)
	insertScopedBoard(t, pool, "device-a2", householdA)

	before, err := server.ListBoards(authzCtxFor("alice"), &pb.ListBoardsRequest{})
	if err != nil {
		t.Fatalf("ListBoards (before): %v", err)
	}
	if len(before.Boards) != 2 {
		t.Fatalf("test setup: len(Boards) before = %d, want 2", len(before.Boards))
	}

	// A board is added to a household alice is not a member of.
	insertScopedBoard(t, pool, "device-b1", householdB)

	after, err := server.ListBoards(authzCtxFor("alice"), &pb.ListBoardsRequest{})
	if err != nil {
		t.Fatalf("ListBoards (after): %v", err)
	}
	if len(after.Boards) != len(before.Boards) {
		t.Errorf("board count changed from %d to %d after another household's board was added -- FR5.2 forbids this leak", len(before.Boards), len(after.Boards))
	}
	if got := boardDeviceIDs(after); got["device-b1"] {
		t.Error("the other household's newly added board leaked into alice's ListBoards result")
	}
}

// -- NFR2: GetDeviceConfig's no-existence-oracle over real SQL ----------------

// TestGetDeviceConfig_OutOfScopeBoard_SameFailureAsNonexistent_RealDB is the
// real-Postgres counterpart of server_test.go's fake-backed equivalent:
// proves the actual authz.PGResolver/Repository SQL, not just the Go
// dispatch logic, produces byte-identical refusals for a genuinely
// nonexistent device_id and a device_id that resolves to a real board
// outside the caller's household.
func TestGetDeviceConfig_OutOfScopeBoard_SameFailureAsNonexistent_RealDB(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	householdA := insertHousehold(t, pool)
	householdB := insertHousehold(t, pool)
	insertMembership(t, pool, householdA, "alice")
	insertScopedBoard(t, pool, "device-b1", householdB)

	_, nonexistentErr := server.GetDeviceConfig(authzCtxFor("alice"), &pb.GetDeviceConfigRequest{DeviceId: "does-not-exist"})
	if nonexistentErr == nil {
		t.Fatal("GetDeviceConfig for a nonexistent device_id returned nil error, want a refusal")
	}
	_, outOfScopeErr := server.GetDeviceConfig(authzCtxFor("alice"), &pb.GetDeviceConfigRequest{DeviceId: "device-b1"})
	if outOfScopeErr == nil {
		t.Fatal("GetDeviceConfig for household B's board (alice is not a member) returned nil error, want a refusal")
	}

	nonexistentDetail, ok := contract.FromError(nonexistentErr)
	if !ok {
		t.Fatal("nonexistent-device error carries no Failure detail")
	}
	outOfScopeDetail, ok := contract.FromError(outOfScopeErr)
	if !ok {
		t.Fatal("out-of-scope error carries no Failure detail")
	}
	if !proto.Equal(nonexistentDetail, outOfScopeDetail) {
		t.Errorf("Failure details differ: nonexistent=%v, out-of-scope=%v", nonexistentDetail, outOfScopeDetail)
	}

	nonexistentBytes, err := proto.Marshal(status.Convert(nonexistentErr).Proto())
	if err != nil {
		t.Fatalf("marshal nonexistent status: %v", err)
	}
	outOfScopeBytes, err := proto.Marshal(status.Convert(outOfScopeErr).Proto())
	if err != nil {
		t.Fatalf("marshal out-of-scope status: %v", err)
	}
	if string(nonexistentBytes) != string(outOfScopeBytes) {
		t.Errorf("marshaled gRPC status differs between nonexistent and out-of-scope refusals against real SQL")
	}
}

// TestGetDeviceConfig_NFR2_TimingIndistinguishable is the task's named
// timing test: NFR2 requires that "a lookup that short-circuits on 'no
// such row' leaks through latency even when the body is identical" not
// apply here -- refusal and absence must take the same code path (one
// caller-scope query, one board-resolution query) regardless of outcome,
// so their latency distributions should not be reliably separable.
//
// Method: N=40 timed calls per path (after 5 discarded warm-up calls per
// path, to let connection-pool/plan-cache effects settle so they don't
// masquerade as a path-dependent signal), interleaved one-nonexistent/
// one-out-of-scope so both paths see the same conditions over the run.
// Tolerance: the two paths' mean latencies must not differ by more than
// 75% of the larger mean. This is deliberately loose -- a real timing
// side-channel from an extra query or a SELECT-then-branch shortcut would
// show up as a multiple-of latency difference (a whole extra round trip),
// not a fraction of one; 75% comfortably separates "structurally
// identical, noisy" from "structurally different", while staying stable
// on a shared/CI machine where per-call jitter can be large in absolute
// terms for two sub-millisecond local Postgres queries.
func TestGetDeviceConfig_NFR2_TimingIndistinguishable(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	householdA := insertHousehold(t, pool)
	householdB := insertHousehold(t, pool)
	insertMembership(t, pool, householdA, "alice")
	insertScopedBoard(t, pool, "device-b1", householdB)

	const warmup = 5
	const n = 40
	const toleranceRatio = 0.75

	callNonexistent := func() time.Duration {
		start := time.Now()
		if _, err := server.GetDeviceConfig(authzCtxFor("alice"), &pb.GetDeviceConfigRequest{DeviceId: "does-not-exist"}); err == nil {
			t.Fatal("want a refusal for a nonexistent device_id")
		}
		return time.Since(start)
	}
	callOutOfScope := func() time.Duration {
		start := time.Now()
		if _, err := server.GetDeviceConfig(authzCtxFor("alice"), &pb.GetDeviceConfigRequest{DeviceId: "device-b1"}); err == nil {
			t.Fatal("want a refusal for an out-of-scope device")
		}
		return time.Since(start)
	}

	for i := 0; i < warmup; i++ {
		callNonexistent()
		callOutOfScope()
	}

	var totalNonexistent, totalOutOfScope time.Duration
	for i := 0; i < n; i++ {
		totalNonexistent += callNonexistent()
		totalOutOfScope += callOutOfScope()
	}

	meanNonexistent := totalNonexistent / time.Duration(n)
	meanOutOfScope := totalOutOfScope / time.Duration(n)

	diff := meanNonexistent - meanOutOfScope
	if diff < 0 {
		diff = -diff
	}
	larger := meanNonexistent
	if meanOutOfScope > larger {
		larger = meanOutOfScope
	}
	if larger == 0 {
		t.Fatal("both mean latencies measured as 0 -- test setup problem, timer resolution or call not actually executing")
	}
	if ratio := float64(diff) / float64(larger); ratio > toleranceRatio {
		t.Errorf("mean latency for nonexistent (%v) vs out-of-scope (%v) differs by %.0f%% of the larger mean over N=%d, want <= %.0f%% -- this is the timing side channel NFR2 forbids",
			meanNonexistent, meanOutOfScope, ratio*100, n, toleranceRatio*100)
	}
}
