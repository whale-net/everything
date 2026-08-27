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
// household_grant/audit_log (FR7) were added to authzTestSchema below
// alongside household/household_membership/board -- household_grant_
// integration_test.go (this package) reuses newAuthzTestServer/
// authzTestSchema/insertHousehold/insertMembership/authzCtxFor from this
// file rather than duplicating them, per this package's established
// "list the shared file in both go_test targets" convention (see
// dbtest_helpers_integration_test.go's doc comment).
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
// Repository.ListBoards actually read.
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

	-- FR7: mirrors migration 018_household_grant's shape. Not SCD2
	-- (NFR6.3) -- no valid_to column; revocation sets revoked_at.
	CREATE TABLE household_grant (
		grant_id           BIGSERIAL PRIMARY KEY,
		household_id       BIGINT NOT NULL REFERENCES household(household_id),
		grantee_subject    TEXT NOT NULL,
		granted_by_subject TEXT NOT NULL,
		granted_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at         TIMESTAMPTZ NOT NULL,
		revoked_at         TIMESTAMPTZ NULL,
		reason             TEXT NULL
	);
	CREATE INDEX idx_household_grant_grantee_subject_active
		ON household_grant(grantee_subject) WHERE revoked_at IS NULL;
	CREATE INDEX idx_household_grant_household_id_active
		ON household_grant(household_id) WHERE revoked_at IS NULL;

	-- FR8: mirrors migration 016_audit_log's column set (schema only --
	-- the append-only trigger/REVOKE aren't reproduced here, same
	-- rationale as dbtest_helpers_integration_test.go's testSchema).
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

// insertGrant inserts a household_grant row directly, bypassing
// GrantHouseholdAccess -- these tests exercise ScopeForPrincipal's grant
// union at the resolver boundary, not the RPC (household_grant_
// integration_test.go covers the RPC surface). expiresAt is passed
// explicitly so a test can insert an already-expired grant directly,
// rather than needing a real wall-clock sleep or a fake clock, to prove
// FR7's "a grant past expires_at stops working with no revocation and no
// background job" -- expiry is evaluated against the database's real NOW()
// at query time either way.
func insertGrant(t *testing.T, pool *pgxpool.Pool, householdID int64, granteeSubject string, grantedBySubject string, expiresAt time.Time) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household_grant (household_id, grantee_subject, granted_by_subject, expires_at) VALUES ($1, $2, $3, $4) RETURNING grant_id`,
		householdID, granteeSubject, grantedBySubject, expiresAt).Scan(&id); err != nil {
		t.Fatalf("insert household_grant for %q: %v", granteeSubject, err)
	}
	return id
}

func revokeGrantDirectly(t *testing.T, pool *pgxpool.Pool, grantID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE household_grant SET revoked_at = NOW() WHERE grant_id = $1`, grantID); err != nil {
		t.Fatalf("revoke household_grant %d: %v", grantID, err)
	}
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

// -- FR7: household_grant's effect on ScopeForPrincipal, over real SQL -------
//
// These exercise authz.PGResolver.ScopeForPrincipal's actual UNION query
// (household_membership UNION household_grant) rather than the fake-
// Queryer-backed unit tests in leaflab/api/authz/resolver_test.go, which
// can't distinguish a membership-derived row from a grant-derived one --
// the fake just returns whatever rows the test hands it either way. Only
// real SQL proves the grant half of the UNION, and its expires_at > NOW()
// / revoked_at IS NULL predicates, actually work.

// TestScopeForPrincipal_GrantWithoutMembership_ConfersHouseholdReach is
// FR7's core claim at the mechanism every write handler actually checks:
// a principal holding an active household_grant, and no household_
// membership row at all, gets a Scope that permits their granted
// household -- "a grant confers write capability equal to a member's"
// starts here, not at any one RPC.
func TestScopeForPrincipal_GrantWithoutMembership_ConfersHouseholdReach(t *testing.T) {
	_, pool := newAuthzTestServer(t)
	resolver := authz.NewPGResolver(pool)

	household := insertHousehold(t, pool)
	insertGrant(t, pool, household, "helper", "alice", time.Now().Add(time.Hour))
	// "helper" deliberately has no household_membership row.

	scope, err := resolver.ScopeForPrincipal(context.Background(), "helper")
	if err != nil {
		t.Fatalf("ScopeForPrincipal: %v", err)
	}
	if !scope.Permits(authz.EntityRef{Kind: authz.EntityHousehold, ID: household}, authz.Resolution{HouseholdID: household}) {
		t.Error("a grantee's Scope does not permit their granted household, want it to (FR7: a grant confers write capability without membership)")
	}
}

// TestScopeForPrincipal_ExpiredGrant_NoLongerConfersReach is FR7's "a grant
// past expires_at stops working with no revocation and no background job":
// a grant whose expires_at is already in the past (inserted directly, no
// wall-clock sleep needed -- expiry is evaluated against the database's
// real NOW() in the query itself) confers no reach, even though the row
// still exists and was never revoked.
func TestScopeForPrincipal_ExpiredGrant_NoLongerConfersReach(t *testing.T) {
	_, pool := newAuthzTestServer(t)
	resolver := authz.NewPGResolver(pool)

	household := insertHousehold(t, pool)
	insertGrant(t, pool, household, "helper", "alice", time.Now().Add(-time.Hour))

	scope, err := resolver.ScopeForPrincipal(context.Background(), "helper")
	if err != nil {
		t.Fatalf("ScopeForPrincipal: %v", err)
	}
	if scope.Permits(authz.EntityRef{Kind: authz.EntityHousehold, ID: household}, authz.Resolution{HouseholdID: household}) {
		t.Error("an expired grant still confers household reach, want it to stop working past expires_at with no revocation and no background job (FR7)")
	}
}

// TestScopeForPrincipal_RevokedGrant_NoLongerConfersReach is FR7's
// "revocable in one action, takes effect on the next request": a grant
// revoked directly (revoked_at set) confers no reach on the very next
// ScopeForPrincipal call, even though expires_at is still in the future.
func TestScopeForPrincipal_RevokedGrant_NoLongerConfersReach(t *testing.T) {
	_, pool := newAuthzTestServer(t)
	resolver := authz.NewPGResolver(pool)

	household := insertHousehold(t, pool)
	grantID := insertGrant(t, pool, household, "helper", "alice", time.Now().Add(time.Hour))

	before, err := resolver.ScopeForPrincipal(context.Background(), "helper")
	if err != nil {
		t.Fatalf("ScopeForPrincipal (before revoke): %v", err)
	}
	if !before.Permits(authz.EntityRef{Kind: authz.EntityHousehold, ID: household}, authz.Resolution{HouseholdID: household}) {
		t.Fatal("test setup: active grant does not confer reach before revocation")
	}

	revokeGrantDirectly(t, pool, grantID)

	after, err := resolver.ScopeForPrincipal(context.Background(), "helper")
	if err != nil {
		t.Fatalf("ScopeForPrincipal (after revoke): %v", err)
	}
	if after.Permits(authz.EntityRef{Kind: authz.EntityHousehold, ID: household}, authz.Resolution{HouseholdID: household}) {
		t.Error("a revoked grant still confers household reach on the next request, want revocation to take effect immediately (FR7)")
	}
}

// TestScopeForPrincipal_MembershipAndGrant_BothConferReach_Union proves
// ScopeForPrincipal's UNION treats membership-derived and grant-derived
// reach identically: a principal who is a member of one household and a
// grantee of a different one sees both, over real SQL (the Go-level
// UnionScope multi-membership case is covered in leaflab/api/authz/
// resolver_test.go; this is its real-SQL, mixed-source counterpart).
func TestScopeForPrincipal_MembershipAndGrant_BothConferReach_Union(t *testing.T) {
	_, pool := newAuthzTestServer(t)
	resolver := authz.NewPGResolver(pool)

	memberHousehold := insertHousehold(t, pool)
	grantHousehold := insertHousehold(t, pool)
	otherHousehold := insertHousehold(t, pool)
	insertMembership(t, pool, memberHousehold, "bob")
	insertGrant(t, pool, grantHousehold, "bob", "alice", time.Now().Add(time.Hour))

	scope, err := resolver.ScopeForPrincipal(context.Background(), "bob")
	if err != nil {
		t.Fatalf("ScopeForPrincipal: %v", err)
	}
	if !scope.Permits(authz.EntityRef{Kind: authz.EntityHousehold, ID: memberHousehold}, authz.Resolution{HouseholdID: memberHousehold}) {
		t.Error("Scope does not permit bob's membership household, want it to")
	}
	if !scope.Permits(authz.EntityRef{Kind: authz.EntityHousehold, ID: grantHousehold}, authz.Resolution{HouseholdID: grantHousehold}) {
		t.Error("Scope does not permit bob's granted household, want it to")
	}
	if scope.Permits(authz.EntityRef{Kind: authz.EntityHousehold, ID: otherHousehold}, authz.Resolution{HouseholdID: otherHousehold}) {
		t.Error("Scope permits a household bob has neither membership nor a grant for, want false")
	}
}
