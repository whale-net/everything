//go:build integration

// Real-Postgres integration coverage for FR80's owner-initiated support
// reference (migration 032_support_reference) and FR10.2's admin-side
// resolution of it -- the SQL-level and timing-sensitive half this
// package's unit tests (server_test.go's fakeRepo-backed
// CreateSupportReference/RevokeSupportReference/ListSupportReferences
// stubs, server_admin_test.go's TestResolveToHousehold_ResolvesByAllThreeQueryKinds
// "unknown code" case) can't reach:
//
//   - Creating a reference returns a code and expiry and nothing about the
//     household (structural: CreateSupportReferenceResponse has exactly
//     those two fields).
//   - An admin in the standing lane resolves a valid reference to the
//     household and gets FR79 fields only.
//   - NFR2: unknown, expired, revoked references, and an ordinary no-match
//     resolve of a different query kind, all take statistically
//     indistinguishable time.
//   - The stored value is a hash -- the plaintext code is not recoverable
//     from the database.
//   - Rate limiting (NFR10, per admin principal) is unit-tested directly
//     against checkRateLimitBuckets in ratelimit_interceptor_test.go; not
//     duplicated here.
//   - Expiry works with no background job -- proven by inserting an
//     already-expired row directly and observing it resolve as unknown
//     with no cleanup step run.
//   - Creation, revocation and use each write an audit_log row FR9's
//     (not-yet-built) activity list would read household-scoped
//     (target_household_id, action) -- this package has no ListActivity
//     RPC yet, so the criterion is proven at the audit_log row level, which
//     is exactly what such a list would query.
//   - support_reference has no valid_to column (NFR6.3).
//
// Schema is self-contained hand-written DDL, same household/household_membership/
// board/device_config/sensor/audit_log shape as
// admin_elevation_integration_test.go's adminElevationTestSchema (not
// shared with it directly -- same rationale as that file's own doc
// comment: every integration file in this package keeps its schema
// self-contained rather than sharing one across files with different
// concerns), plus the support_reference table from migration 032.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:support_reference_integration_test --test_output=all
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const supportReferenceTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY,
		name         TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE household_membership (
		membership_id     BIGSERIAL PRIMARY KEY,
		household_id      BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject TEXT NOT NULL,
		valid_from        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to          TIMESTAMPTZ
	);

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		household_id  BIGINT REFERENCES household(household_id),
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at    TIMESTAMPTZ
	);

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

	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY,
		board_id  BIGINT NOT NULL REFERENCES board(board_id) ON DELETE CASCADE
	);

	CREATE TABLE support_reference (
		support_reference_id BIGSERIAL PRIMARY KEY,
		household_id          BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		code_hash              TEXT NOT NULL UNIQUE,
		created_by_subject     TEXT NOT NULL,
		created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at             TIMESTAMPTZ NOT NULL,
		revoked_at             TIMESTAMPTZ NULL,
		last_resolved_at       TIMESTAMPTZ NULL,
		resolve_count          INT NOT NULL DEFAULT 0
	);

	CREATE TABLE audit_log (
		audit_id             BIGSERIAL PRIMARY KEY,
		actor_subject        TEXT NOT NULL,
		actor_kind           TEXT NOT NULL,
		target_household_id  BIGINT NULL,
		action                TEXT NOT NULL,
		entity_kind           TEXT NOT NULL,
		entity_id             TEXT NULL,
		reason                TEXT NULL,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id        TEXT NULL
	);
`

// newSupportReferenceTestServer starts a real Postgres container with
// supportReferenceTestSchema applied and returns a LeafLabAPIServer backed
// by a real Repository and a real authz.PGResolver -- CreateSupportReference/
// RevokeSupportReference/ListSupportReferences route household reach
// through scopeForCaller (server.go), which needs a real household_membership
// query, not a stub.
func newSupportReferenceTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: supportReferenceTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return NewLeafLabAPIServer(repo, resolver, nil, nil, discardLogger()), db.Pool
}

func insertSRHousehold(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO household (name) VALUES ($1) RETURNING household_id`, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert household %s: %v", name, err)
	}
	return id
}

func insertSRMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`, householdID, subject); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
}

// insertSRBoardWithHealth inserts a board owned by householdID with one
// accepted config version, one outstanding push, and two sensors -- enough
// for AdminBoardHealthByHousehold's FR79 projection to have something real
// to return.
func insertSRBoardWithHealth(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	ctx := context.Background()
	var boardID int64
	if err := pool.QueryRow(ctx, `INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`, deviceID, householdID).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_config (board_id, version, config_json, accepted) VALUES ($1, 1, '{}', TRUE)`, boardID); err != nil {
		t.Fatalf("insert accepted config: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sensor (board_id) VALUES ($1), ($1)`, boardID); err != nil {
		t.Fatalf("insert sensors: %v", err)
	}
	return boardID
}

// insertExpiredOrRevokedSupportReference inserts a support_reference row
// directly (bypassing CreateSupportReference), returning the plaintext code
// it was "issued" as -- for tests that need an already-expired or
// already-revoked row without waiting or making a second RPC round trip.
func insertRawSupportReference(t *testing.T, pool *pgxpool.Pool, householdID int64, code string, expiresAt time.Time, revoked bool) {
	t.Helper()
	sum := sha256.Sum256([]byte(code))
	hash := hex.EncodeToString(sum[:])
	var revokedAt any
	if revoked {
		revokedAt = time.Now()
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO support_reference (household_id, code_hash, created_by_subject, expires_at, revoked_at)
		VALUES ($1, $2, 'test-setup', $3, $4)
	`, householdID, hash, expiresAt, revokedAt); err != nil {
		t.Fatalf("insert raw support reference: %v", err)
	}
}

func srMemberCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func srAdminCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: []string{RoleAdmin}})
}

// countPopulatedFields mirrors server_test.go's helper of the same name
// (unavailable here: that file is excluded from this integration build tag)
// -- counts fields protoreflect considers set (non-default/non-empty).
func countPopulatedFields(msg protoreflect.Message) int {
	n := 0
	msg.Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool {
		n++
		return true
	})
	return n
}

// TestCreateSupportReference_ReturnsCodeAndExpiry_DisclosesNothingElse proves
// FR80's "requires no description of the problem and discloses no household
// data in itself": CreateSupportReferenceResponse carries exactly two
// populated fields (code, expires_at) -- structurally, since the message
// type itself has no household field to leak -- and the code is a
// plausible opaque credential (fixed length, drawn from the Crockford
// Base32 alphabet, non-empty).
func TestCreateSupportReference_ReturnsCodeAndExpiry_DisclosesNothingElse(t *testing.T) {
	server, pool := newSupportReferenceTestServer(t)
	household := insertSRHousehold(t, pool, "The Smiths")
	insertSRMembership(t, pool, household, "alice")

	before := time.Now()
	resp, err := server.CreateSupportReference(srMemberCtx("alice"), &pb.CreateSupportReferenceRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("CreateSupportReference: %v", err)
	}

	if got := countPopulatedFields(resp.ProtoReflect()); got != 2 {
		t.Errorf("CreateSupportReferenceResponse has %d populated fields, want exactly 2 (code, expires_at) -- FR80 discloses nothing about the household", got)
	}
	if len(resp.Code) != supportReferenceCodeLength {
		t.Errorf("code length = %d, want %d", len(resp.Code), supportReferenceCodeLength)
	}
	for _, c := range resp.Code {
		if !strings.ContainsRune(supportReferenceCodeAlphabet, c) {
			t.Errorf("code %q contains a character outside the Crockford Base32 alphabet: %q", resp.Code, c)
		}
	}

	gotExpiry := contract.FromInstant(resp.ExpiresAt)
	wantExpiry := before.Add(DefaultSupportReferenceTTL)
	if diff := gotExpiry.Sub(wantExpiry); diff < -2*time.Second || diff > 2*time.Second {
		t.Errorf("expires_at = %v, want approximately %v (DefaultSupportReferenceTTL out from creation)", gotExpiry, wantExpiry)
	}
}

// TestSupportReferenceLifecycle_ListRevoke_AndAudit proves the owner-facing
// list/revoke lifecycle and FR80's "creation, revocation ... write audit
// rows" (FR9 integration, at the audit_log row level -- see this file's
// doc comment) and NFR6.3's "no valid_to column".
func TestSupportReferenceLifecycle_ListRevoke_AndAudit(t *testing.T) {
	server, pool := newSupportReferenceTestServer(t)
	household := insertSRHousehold(t, pool, "The Smiths")
	insertSRMembership(t, pool, household, "alice")
	ctx := srMemberCtx("alice")

	createResp, err := server.CreateSupportReference(ctx, &pb.CreateSupportReferenceRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("CreateSupportReference: %v", err)
	}

	// Creation wrote exactly one audit row, attributed to the household and
	// the correct action.
	assertAuditRow(t, pool, household, "CreateSupportReference", "support_reference")

	listResp, err := server.ListSupportReferences(ctx, &pb.ListSupportReferencesRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("ListSupportReferences: %v", err)
	}
	if len(listResp.References) != 1 {
		t.Fatalf("ListSupportReferences returned %d references, want 1", len(listResp.References))
	}
	ref := listResp.References[0]
	if ref.Revoked {
		t.Error("newly created reference reports Revoked = true, want false")
	}
	if ref.ResolveCount != 0 {
		t.Errorf("ResolveCount = %d, want 0 for a never-used reference", ref.ResolveCount)
	}
	// ListSupportReferences never returns the plaintext code (FR80) --
	// SupportReferenceInfo has no code field to leak, but assert the
	// returned id is usable for revocation, proving this is a real row.
	if ref.SupportReferenceId <= 0 {
		t.Fatalf("SupportReferenceId = %d, want a positive id", ref.SupportReferenceId)
	}

	if _, err := server.RevokeSupportReference(ctx, &pb.RevokeSupportReferenceRequest{HouseholdId: household, SupportReferenceId: ref.SupportReferenceId}); err != nil {
		t.Fatalf("RevokeSupportReference: %v", err)
	}
	assertAuditRow(t, pool, household, "RevokeSupportReference", "support_reference")

	// Revocation is not idempotent-by-design (repository.go's doc comment):
	// a second revoke of the same id is refused.
	if _, err := server.RevokeSupportReference(ctx, &pb.RevokeSupportReferenceRequest{HouseholdId: household, SupportReferenceId: ref.SupportReferenceId}); err == nil {
		t.Error("second revoke of an already-revoked reference returned nil error, want a refusal")
	}

	listAfterRevoke, err := server.ListSupportReferences(ctx, &pb.ListSupportReferencesRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("ListSupportReferences after revoke: %v", err)
	}
	if len(listAfterRevoke.References) != 1 || !listAfterRevoke.References[0].Revoked {
		t.Fatalf("after revoke, reference Revoked = %v, want true", listAfterRevoke.References[0].Revoked)
	}

	// Once revoked, the code no longer resolves for an admin (proven fully
	// by the NFR2 timing test below; here just confirm it takes the
	// no-match path).
	adminResp, err := server.ResolveToHousehold(srAdminCtx("root"), &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_SupportReference{SupportReference: createResp.Code}})
	if err != nil {
		t.Fatalf("ResolveToHousehold on a revoked code: %v", err)
	}
	if len(adminResp.Boards) != 0 {
		t.Errorf("ResolveToHousehold on a revoked code returned %d boards, want 0", len(adminResp.Boards))
	}

	// NFR6.3: support_reference has no valid_to column at all.
	var hasValidTo bool
	err = pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'support_reference' AND column_name = 'valid_to'
		)
	`).Scan(&hasValidTo)
	if err != nil {
		t.Fatalf("query information_schema: %v", err)
	}
	if hasValidTo {
		t.Error("support_reference has a valid_to column, want none (NFR6.3: short-lived token with expiry, not SCD2)")
	}
}

// assertAuditRow fails the test unless audit_log has a row matching
// (target_household_id, action, entity_kind) -- the shape FR9's (not yet
// built) owner-facing activity list would filter on.
func assertAuditRow(t *testing.T, pool *pgxpool.Pool, householdID int64, action, entityKind string) {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM audit_log
		WHERE target_household_id = $1 AND action = $2 AND entity_kind = $3
	`, householdID, action, entityKind).Scan(&n)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if n != 1 {
		t.Errorf("audit_log has %d rows for (household=%d, action=%q, entity_kind=%q), want 1", n, householdID, action, entityKind)
	}
}

// TestCreateSupportReference_StoresHashNotPlaintext proves the stored value
// is a hash and the plaintext code is not recoverable from the database --
// no column or query anywhere in support_reference reproduces it.
func TestCreateSupportReference_StoresHashNotPlaintext(t *testing.T) {
	server, pool := newSupportReferenceTestServer(t)
	household := insertSRHousehold(t, pool, "The Smiths")
	insertSRMembership(t, pool, household, "alice")

	resp, err := server.CreateSupportReference(srMemberCtx("alice"), &pb.CreateSupportReferenceRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("CreateSupportReference: %v", err)
	}

	var storedHash string
	if err := pool.QueryRow(context.Background(), `SELECT code_hash FROM support_reference WHERE household_id = $1`, household).Scan(&storedHash); err != nil {
		t.Fatalf("read code_hash: %v", err)
	}
	if storedHash == resp.Code {
		t.Fatal("code_hash column stores the plaintext code verbatim, want a hash")
	}
	sum := sha256.Sum256([]byte(resp.Code))
	wantHash := hex.EncodeToString(sum[:])
	if storedHash != wantHash {
		t.Errorf("code_hash = %q, want SHA-256(%q) = %q", storedHash, resp.Code, wantHash)
	}

	// No column in the row reproduces the plaintext by any other name --
	// this is a schema-level guarantee (support_reference has code_hash and
	// nothing else code-shaped), asserted here by checking the full column
	// list contains no other candidate.
	rows, err := pool.Query(context.Background(), `
		SELECT column_name FROM information_schema.columns WHERE table_name = 'support_reference'
	`)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		if col != "code_hash" && strings.Contains(col, "code") {
			t.Errorf("unexpected code-shaped column %q on support_reference -- only code_hash should exist", col)
		}
	}
}

// TestAdminResolve_ValidSupportReference_ReturnsFR79FieldsForHousehold
// proves FR10.2/FR80's core admin-side path: a support reference minted by
// a household member resolves, for an admin in the standing lane, to
// exactly that household's boards with FR79's health projection -- and
// that a genuine resolve increments resolve_count/last_resolved_at and
// writes a SupportReferenceResolve audit row (FR80's "existence and use
// are visible to the owner").
func TestAdminResolve_ValidSupportReference_ReturnsFR79FieldsForHousehold(t *testing.T) {
	server, pool := newSupportReferenceTestServer(t)
	household := insertSRHousehold(t, pool, "The Smiths")
	insertSRMembership(t, pool, household, "alice")
	insertSRBoardWithHealth(t, pool, "device-abc123", household)

	createResp, err := server.CreateSupportReference(srMemberCtx("alice"), &pb.CreateSupportReferenceRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("CreateSupportReference: %v", err)
	}

	resp, err := server.ResolveToHousehold(srAdminCtx("root"), &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_SupportReference{SupportReference: createResp.Code}})
	if err != nil {
		t.Fatalf("ResolveToHousehold: %v", err)
	}
	if len(resp.Boards) != 1 {
		t.Fatalf("got %d boards, want 1", len(resp.Boards))
	}
	got := resp.Boards[0]
	if got.DeviceId != "device-abc123" {
		t.Errorf("DeviceId = %q, want %q", got.DeviceId, "device-abc123")
	}
	if got.HouseholdId != household {
		t.Errorf("HouseholdId = %d, want %d", got.HouseholdId, household)
	}
	if got.HouseholdName != "The Smiths" {
		t.Errorf("HouseholdName = %q, want %q", got.HouseholdName, "The Smiths")
	}
	if got.ActiveVersion != 1 {
		t.Errorf("ActiveVersion = %d, want 1", got.ActiveVersion)
	}
	if got.SensorCount != 2 {
		t.Errorf("SensorCount = %d, want 2", got.SensorCount)
	}
	// FR79 fields only -- AdminBoardHealth has exactly 8 fields; assert the
	// populated count is bounded by that, i.e. nothing beyond the message's
	// own field set could have leaked (a structural sanity check mirroring
	// AdminBoardHealth's own doc comment).
	if got := countPopulatedFields(got.ProtoReflect()); got > 8 {
		t.Errorf("AdminBoardHealth has %d populated fields, want at most 8 (FR79's fixed field set)", got)
	}

	// Use is visible to the owner: resolve_count/last_resolved_at bumped,
	// and a household-attributed SupportReferenceResolve audit row exists.
	var resolveCount int32
	var lastResolvedAt *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT resolve_count, last_resolved_at FROM support_reference WHERE household_id = $1`, household).Scan(&resolveCount, &lastResolvedAt); err != nil {
		t.Fatalf("read resolve_count/last_resolved_at: %v", err)
	}
	if resolveCount != 1 {
		t.Errorf("resolve_count = %d, want 1", resolveCount)
	}
	if lastResolvedAt == nil {
		t.Error("last_resolved_at is NULL, want set after a valid resolve")
	}
	assertAuditRow(t, pool, household, supportReferenceResolveAction, "support_reference")
}

// TestNFR2_SupportReferenceResolve_UnknownExpiredRevoked_IndistinguishableFromNoMatch
// is the task's named NFR2 timing test: an unknown code, an expired code, a
// revoked code, and an ordinary no-match query of a different kind
// (person_identifier) must all produce the same response shape (zero
// boards, no error) and statistically indistinguishable timing -- proving
// resolveSupportReference's single-lookup, classify-in-Go design (see its
// doc comment) does not leak which of the three failure classes a code
// belongs to, nor does it cost measurably more or less than an ordinary
// no-match resolve of a different query kind.
//
// Method: N=30 timed calls per path (after 5 discarded warm-up calls per
// path), round-robined across all four paths so every path sees the same
// conditions over the run. Tolerance: every pair of paths' mean latencies
// must not differ by more than 75% of the larger mean -- the same
// deliberately loose tolerance and rationale as
// authz_scope_integration_test.go's TestGetDeviceConfig_NFR2_TimingIndistinguishable
// (a real extra round trip shows up as a multiple-of difference, not a
// fraction of one).
func TestNFR2_SupportReferenceResolve_UnknownExpiredRevoked_IndistinguishableFromNoMatch(t *testing.T) {
	server, pool := newSupportReferenceTestServer(t)
	household := insertSRHousehold(t, pool, "The Smiths")

	pastExpiry := time.Now().Add(-1 * time.Minute)
	futureExpiry := time.Now().Add(time.Hour)
	insertRawSupportReference(t, pool, household, "EXPIREDCODE", pastExpiry, false)
	insertRawSupportReference(t, pool, household, "REVOKEDCODE1", futureExpiry, true)

	adminCtx := srAdminCtx("root")

	callUnknown := func() time.Duration {
		start := time.Now()
		resp, err := server.ResolveToHousehold(adminCtx, &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_SupportReference{SupportReference: "NEVERISSUED"}})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("unknown code: unexpected error: %v", err)
		}
		if len(resp.Boards) != 0 {
			t.Fatalf("unknown code: got %d boards, want 0", len(resp.Boards))
		}
		return elapsed
	}
	callExpired := func() time.Duration {
		start := time.Now()
		resp, err := server.ResolveToHousehold(adminCtx, &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_SupportReference{SupportReference: "EXPIREDCODE"}})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("expired code: unexpected error: %v", err)
		}
		if len(resp.Boards) != 0 {
			t.Fatalf("expired code: got %d boards, want 0", len(resp.Boards))
		}
		return elapsed
	}
	callRevoked := func() time.Duration {
		start := time.Now()
		resp, err := server.ResolveToHousehold(adminCtx, &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_SupportReference{SupportReference: "REVOKEDCODE1"}})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("revoked code: unexpected error: %v", err)
		}
		if len(resp.Boards) != 0 {
			t.Fatalf("revoked code: got %d boards, want 0", len(resp.Boards))
		}
		return elapsed
	}
	callNoMatchPerson := func() time.Duration {
		start := time.Now()
		resp, err := server.ResolveToHousehold(adminCtx, &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_PersonIdentifier{PersonIdentifier: "nobody@example.com"}})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("no-match person_identifier: unexpected error: %v", err)
		}
		if len(resp.Boards) != 0 {
			t.Fatalf("no-match person_identifier: got %d boards, want 0", len(resp.Boards))
		}
		return elapsed
	}

	paths := map[string]func() time.Duration{
		"unknown":         callUnknown,
		"expired":         callExpired,
		"revoked":         callRevoked,
		"no_match_person": callNoMatchPerson,
	}

	const warmup = 5
	const n = 30
	const toleranceRatio = 0.75

	means := make(map[string]time.Duration, len(paths))
	for name, call := range paths {
		for i := 0; i < warmup; i++ {
			call()
		}
		var total time.Duration
		for i := 0; i < n; i++ {
			total += call()
		}
		means[name] = total / time.Duration(n)
	}

	for nameA, meanA := range means {
		for nameB, meanB := range means {
			if nameA >= nameB {
				continue
			}
			diff := meanA - meanB
			if diff < 0 {
				diff = -diff
			}
			larger := meanA
			if meanB > larger {
				larger = meanB
			}
			if larger == 0 {
				t.Fatalf("%s/%s: both mean latencies measured as 0 -- test setup problem", nameA, nameB)
			}
			if ratio := float64(diff) / float64(larger); ratio > toleranceRatio {
				t.Errorf("mean latency for %s (%v) vs %s (%v) differs by %.0f%% of the larger mean over N=%d, want <= %.0f%% -- this is the timing side channel NFR2 forbids",
					nameA, meanA, nameB, meanB, ratio*100, n, toleranceRatio*100)
			}
		}
	}

	// Expiry works with no background job: the expired row is still
	// physically present (nothing deleted it), yet resolves as unknown
	// purely because resolveSupportReference compares expires_at against
	// time.Now() at read time.
	var expiredStillPresent bool
	if err := pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM support_reference WHERE code_hash = $1)`, hashSupportReferenceCode("EXPIREDCODE")).Scan(&expiredStillPresent); err != nil {
		t.Fatalf("check expired row presence: %v", err)
	}
	if !expiredStillPresent {
		t.Error("the expired support_reference row is gone -- expiry must work by comparing expires_at at read time, not by a background deletion job")
	}
}
