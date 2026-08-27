//go:build integration

package main

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/api/apierrors"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/leaflab/api/staleness"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// adminTestSchema is a self-contained minimal schema covering everything
// #1197's admin surface touches: household/membership (FR75), region/board/
// sensor/device_config (FR79's health derivation), elevation (FR10),
// support_reference (FR80), and audit_record (FR8). It intentionally omits
// tables and columns #1197 never reads (sensor_reading, plant, etc.) to
// avoid a TimescaleDB dependency this test binary doesn't otherwise need.
const adminTestSchema = `
CREATE TABLE household (
    household_id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE household_member (
    member_id     BIGSERIAL PRIMARY KEY,
    household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    principal_id  VARCHAR(255) NOT NULL,
    role          VARCHAR(64) NOT NULL,
    valid_from    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to      TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_household_member_active
    ON household_member(household_id, principal_id) WHERE valid_to IS NULL;

CREATE TABLE region (
    region_id        BIGSERIAL PRIMARY KEY,
    parent_region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
    name             VARCHAR(255) NOT NULL
);

CREATE VIEW v_region_path AS
WITH RECURSIVE path AS (
    SELECT r.region_id, r.parent_region_id, ARRAY[r.region_id]::BIGINT[] AS path_ids
    FROM region r
    WHERE r.parent_region_id IS NULL
    UNION ALL
    SELECT r.region_id, r.parent_region_id, p.path_ids || r.region_id
    FROM region r
    JOIN path p ON p.region_id = r.parent_region_id
)
SELECT region_id, parent_region_id, path_ids FROM path;

CREATE TABLE board (
    board_id          BIGSERIAL PRIMARY KEY,
    device_id         VARCHAR(64) NOT NULL UNIQUE,
    household_id      BIGINT REFERENCES household(household_id) ON DELETE RESTRICT,
    last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retired_at        TIMESTAMPTZ,
    retired_operation VARCHAR(64),
    retired_principal VARCHAR(255)
);

CREATE TABLE sensor (
    sensor_id BIGSERIAL PRIMARY KEY,
    board_id  BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
    region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT
);

CREATE TABLE device_config (
    config_id   BIGSERIAL PRIMARY KEY,
    board_id    BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
    version     BIGINT NOT NULL,
    config_json JSONB NOT NULL,
    accepted    BOOLEAN NOT NULL DEFAULT FALSE,
    pushed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (board_id, version)
);

CREATE TABLE elevation (
    elevation_id         BIGSERIAL PRIMARY KEY,
    admin_subject        VARCHAR(255) NOT NULL,
    target_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    reason               TEXT NOT NULL,
    entered_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at           TIMESTAMPTZ NOT NULL,
    renewed_from         BIGINT REFERENCES elevation(elevation_id) ON DELETE SET NULL
);

CREATE OR REPLACE FUNCTION elevation_append_only()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' OR TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'elevation is append-only: UPDATE and DELETE are not permitted';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER elevation_no_modify
BEFORE UPDATE OR DELETE ON elevation
FOR EACH ROW EXECUTE FUNCTION elevation_append_only();

CREATE TABLE support_reference (
    support_reference_id BIGSERIAL PRIMARY KEY,
    household_id          BIGINT NOT NULL REFERENCES household(household_id) ON DELETE CASCADE,
    code_hash             VARCHAR(64) NOT NULL,
    created_by            VARCHAR(255) NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at            TIMESTAMPTZ NOT NULL,
    revoked_at            TIMESTAMPTZ
);

CREATE INDEX idx_support_reference_code_hash ON support_reference(code_hash);

CREATE TABLE audit_record (
    audit_id             BIGSERIAL PRIMARY KEY,
    actor_subject        VARCHAR(255) NOT NULL,
    target_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    action               VARCHAR(64) NOT NULL,
    entity_type          VARCHAR(64) NOT NULL,
    entity_id            BIGINT NOT NULL,
    occurred_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason               TEXT,
    config_version       BIGINT,
    i2c_address          SMALLINT,
    mux_path             JSONB
);

CREATE OR REPLACE FUNCTION audit_record_append_only()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' OR TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit_record is append-only: UPDATE and DELETE are not permitted';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_record_no_modify
BEFORE UPDATE OR DELETE ON audit_record
FOR EACH ROW EXECUTE FUNCTION audit_record_append_only();
`

// ── test fixtures ────────────────────────────────────────────────────────

func adminCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: []string{"admin"}})
}

func memberCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: []string{}})
}

// newAdminTestServer builds a LeafLabAPIServer wired to pg with the given
// AdminConfig and a rate limiter using rps for the "support-reference"
// bucket (0 disables rate limiting entirely by not registering the bucket
// at all — Limiter.Allow fails open when a bucket is unregistered).
func newAdminTestServer(t *testing.T, pg *dbtest.Postgres, cfg AdminConfig, supportReferenceRPS int) (*LeafLabAPIServer, *Repository) {
	t.Helper()
	repo := NewRepository(pg.Pool)
	authz := NewAuthorizationPredicates(pg.Pool)

	registry := ratelimit.NewRegistry()
	if supportReferenceRPS > 0 {
		registry.Register(ratelimit.Bucket{Name: "support-reference", RequestsPerSecond: supportReferenceRPS})
	}
	limiter := ratelimit.NewLimiter(registry)

	server := NewLeafLabAPIServer(repo, nil, slog.New(slog.DiscardHandler), cfg, authz, limiter)
	return server, repo
}

func defaultTestAdminConfig() AdminConfig {
	return AdminConfig{
		ElevationDuration:        60 * time.Minute,
		Staleness:                staleness.NewConfig(),
		SupportReferenceDuration: 15 * time.Minute,
	}
}

func mustInsertHousehold(t *testing.T, ctx context.Context, pg *dbtest.Postgres, name string) int64 {
	t.Helper()
	var id int64
	err := pg.Pool.QueryRow(ctx, `INSERT INTO household (name) VALUES ($1) RETURNING household_id`, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert household %q: %v", name, err)
	}
	return id
}

func mustAddMember(t *testing.T, ctx context.Context, pg *dbtest.Postgres, householdID int64, principal string) {
	t.Helper()
	_, err := pg.Pool.Exec(ctx, `INSERT INTO household_member (household_id, principal_id, role) VALUES ($1, $2, 'owner')`, householdID, principal)
	if err != nil {
		t.Fatalf("add member %q to household %d: %v", principal, householdID, err)
	}
}

// mustInsertBoard inserts a board with the given last-seen age. If retired
// is true, retired_at is stamped (FR22.4).
func mustInsertBoard(t *testing.T, ctx context.Context, pg *dbtest.Postgres, deviceID string, householdID int64, lastSeenAgo time.Duration, retired bool) int64 {
	t.Helper()
	var id int64
	var retiredAt any
	if retired {
		retiredAt = time.Now()
	}
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id, household_id, last_seen_at, retired_at)
		VALUES ($1, $2, NOW() - $3::interval, $4)
		RETURNING board_id
	`, deviceID, householdID, fmt.Sprintf("%f seconds", lastSeenAgo.Seconds()), retiredAt).Scan(&id)
	if err != nil {
		t.Fatalf("insert board %q: %v", deviceID, err)
	}
	return id
}

func mustInsertSensor(t *testing.T, ctx context.Context, pg *dbtest.Postgres, boardID int64, regionID *int64) int64 {
	t.Helper()
	var id int64
	err := pg.Pool.QueryRow(ctx, `INSERT INTO sensor (board_id, region_id) VALUES ($1, $2) RETURNING sensor_id`, boardID, regionID).Scan(&id)
	if err != nil {
		t.Fatalf("insert sensor for board %d: %v", boardID, err)
	}
	return id
}

func mustInsertRegion(t *testing.T, ctx context.Context, pg *dbtest.Postgres, parentID *int64, name string) int64 {
	t.Helper()
	var id int64
	err := pg.Pool.QueryRow(ctx, `INSERT INTO region (parent_region_id, name) VALUES ($1, $2) RETURNING region_id`, parentID, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert region %q: %v", name, err)
	}
	return id
}

// mustInsertSupportReference bypasses CreateSupportReference to build
// fixtures the API cannot itself produce (already-expired, already-revoked)
// without waiting or a second RPC round trip.
func mustInsertSupportReference(t *testing.T, ctx context.Context, pg *dbtest.Postgres, householdID int64, code string, expiresAgo time.Duration, revoked bool) {
	t.Helper()
	var revokedAt any
	if revoked {
		revokedAt = time.Now()
	}
	_, err := pg.Pool.Exec(ctx, `
		INSERT INTO support_reference (household_id, code_hash, created_by, expires_at, revoked_at)
		VALUES ($1, $2, 'fixture', NOW() - $3::interval, $4)
	`, householdID, hashSupportCode(code), fmt.Sprintf("%f seconds", expiresAgo.Seconds()), revokedAt)
	if err != nil {
		t.Fatalf("insert support reference: %v", err)
	}
}

func auditCount(t *testing.T, ctx context.Context, pg *dbtest.Postgres, householdID int64, action string) int {
	t.Helper()
	var count int
	err := pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_record WHERE target_household_id = $1 AND action = $2
	`, householdID, action).Scan(&count)
	if err != nil {
		t.Fatalf("count audit_record: %v", err)
	}
	return count
}

func requireStatus(t *testing.T, err error, wantCode codes.Code, wantMessageKey string) *status.Status {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", wantCode)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != wantCode {
		t.Fatalf("code = %v, want %v (message: %s)", st.Code(), wantCode, st.Message())
	}
	if wantMessageKey != "" {
		detail := apierrors.ErrorDetailFromStatus(st)
		if detail == nil {
			t.Fatalf("expected ErrorDetail, got none")
		}
		if detail.MessageKey != wantMessageKey {
			t.Fatalf("MessageKey = %q, want %q", detail.MessageKey, wantMessageKey)
		}
	}
	return st
}

// ── FR10: elevation lifecycle ────────────────────────────────────────────

func TestEnterElevation_RequiresAdminRole(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")

	_, err := server.EnterElevation(memberCtx("bob@example.com"), &pb.EnterElevationRequest{TargetHouseholdId: hh, Reason: "helping bob"})
	requireStatus(t, err, codes.PermissionDenied, apierrors.AdminRoleRequired)
}

func TestEnterElevation_RequiresReason(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")

	_, err := server.EnterElevation(adminCtx("admin1"), &pb.EnterElevationRequest{TargetHouseholdId: hh, Reason: ""})
	requireStatus(t, err, codes.InvalidArgument, apierrors.ReasonRequired)
}

func TestEnterElevation_UnknownHouseholdNotFound(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)

	_, err := server.EnterElevation(adminCtx("admin1"), &pb.EnterElevationRequest{TargetHouseholdId: 99999, Reason: "investigating"})
	requireStatus(t, err, codes.NotFound, apierrors.HouseholdNotFound)
}

func TestEnterElevation_SuccessGrantsConfiguredDuration(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	cfg := defaultTestAdminConfig()
	cfg.ElevationDuration = 60 * time.Minute
	server, _ := newAdminTestServer(t, pg, cfg, 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")

	state, err := server.EnterElevation(adminCtx("admin1"), &pb.EnterElevationRequest{TargetHouseholdId: hh, Reason: "on-call ticket 42"})
	if err != nil {
		t.Fatalf("EnterElevation: %v", err)
	}
	if !state.Active {
		t.Fatalf("Active = false, want true")
	}
	if state.RemainingSeconds < 59*60 || state.RemainingSeconds > 60*60 {
		t.Errorf("RemainingSeconds = %d, want ~3600 (60m, A22 default)", state.RemainingSeconds)
	}
	if state.Reason != "on-call ticket 42" {
		t.Errorf("Reason = %q, want %q", state.Reason, "on-call ticket 42")
	}
}

func TestGetElevationState_NoElevationIsInactive(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")

	state, err := server.GetElevationState(adminCtx("admin1"), &pb.GetElevationStateRequest{TargetHouseholdId: hh})
	if err != nil {
		t.Fatalf("GetElevationState: %v", err)
	}
	if state.Active {
		t.Errorf("Active = true, want false (no elevation entered)")
	}
}

func TestRenewElevation_RequiresActiveElevation(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")

	_, err := server.RenewElevation(adminCtx("admin1"), &pb.RenewElevationRequest{TargetHouseholdId: hh, Reason: "still working"})
	requireStatus(t, err, codes.FailedPrecondition, apierrors.NoActiveElevation)
}

func TestRenewElevation_RejectsReusedReason(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	adminSubject := "admin1"

	if _, err := server.EnterElevation(adminCtx(adminSubject), &pb.EnterElevationRequest{TargetHouseholdId: hh, Reason: "ticket 1"}); err != nil {
		t.Fatalf("EnterElevation: %v", err)
	}

	_, err := server.RenewElevation(adminCtx(adminSubject), &pb.RenewElevationRequest{TargetHouseholdId: hh, Reason: "ticket 1"})
	requireStatus(t, err, codes.InvalidArgument, apierrors.ReasonNotRestated)
}

func TestRenewElevation_RestatedReasonExtendsAndLinksToOriginal(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	adminSubject := "admin1"

	first, err := server.EnterElevation(adminCtx(adminSubject), &pb.EnterElevationRequest{TargetHouseholdId: hh, Reason: "ticket 1"})
	if err != nil {
		t.Fatalf("EnterElevation: %v", err)
	}

	renewed, err := server.RenewElevation(adminCtx(adminSubject), &pb.RenewElevationRequest{TargetHouseholdId: hh, Reason: "ticket 1, still investigating"})
	if err != nil {
		t.Fatalf("RenewElevation: %v", err)
	}
	if !renewed.Active {
		t.Fatalf("Active = false after renewal, want true")
	}
	if renewed.Reason != "ticket 1, still investigating" {
		t.Errorf("Reason = %q, want restated reason", renewed.Reason)
	}
	// ExpiresAt is Unix-seconds resolution; a renewal issued within the same
	// wall-clock second as the original can legitimately compute an
	// identical value (both are NOW() + the same duration), so assert
	// non-regression rather than strict advancement.
	if renewed.ExpiresAt < first.ExpiresAt {
		t.Errorf("renewed ExpiresAt (%d) should not be earlier than original (%d)", renewed.ExpiresAt, first.ExpiresAt)
	}

	// Elevation is append-only (NFR6.3): renewal must insert a second row
	// linked via renewed_from, never mutate the first.
	var rowCount int
	if err := pg.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM elevation WHERE admin_subject = $1 AND target_household_id = $2`, adminSubject, hh).Scan(&rowCount); err != nil {
		t.Fatalf("count elevation rows: %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("elevation row count = %d, want 2 (append-only renewal)", rowCount)
	}

	var renewedFrom *int64
	if err := pg.Pool.QueryRow(ctx, `SELECT renewed_from FROM elevation WHERE reason = $1`, "ticket 1, still investigating").Scan(&renewedFrom); err != nil {
		t.Fatalf("query renewed_from: %v", err)
	}
	if renewedFrom == nil {
		t.Fatalf("renewed_from is NULL, want it to link back to the original elevation")
	}
}

func TestElevation_ExpiresAutomatically(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	cfg := defaultTestAdminConfig()
	cfg.ElevationDuration = 150 * time.Millisecond
	server, _ := newAdminTestServer(t, pg, cfg, 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	adminSubject := "admin1"

	if _, err := server.EnterElevation(adminCtx(adminSubject), &pb.EnterElevationRequest{TargetHouseholdId: hh, Reason: "quick check"}); err != nil {
		t.Fatalf("EnterElevation: %v", err)
	}

	state, err := server.GetElevationState(adminCtx(adminSubject), &pb.GetElevationStateRequest{TargetHouseholdId: hh})
	if err != nil {
		t.Fatalf("GetElevationState (before expiry): %v", err)
	}
	if !state.Active {
		t.Fatalf("Active = false immediately after EnterElevation, want true")
	}

	time.Sleep(300 * time.Millisecond)

	state, err = server.GetElevationState(adminCtx(adminSubject), &pb.GetElevationStateRequest{TargetHouseholdId: hh})
	if err != nil {
		t.Fatalf("GetElevationState (after expiry): %v", err)
	}
	if state.Active {
		t.Fatalf("Active = true after the elevation window elapsed, want false (automatic expiry)")
	}

	// A lapsed elevation is not "active to renew" either.
	_, err = server.RenewElevation(adminCtx(adminSubject), &pb.RenewElevationRequest{TargetHouseholdId: hh, Reason: "still here"})
	requireStatus(t, err, codes.FailedPrecondition, apierrors.NoActiveElevation)

	// FR10.3: a lapsed elevation no longer gates a write either.
	authz := NewAuthorizationPredicates(pg.Pool)
	if _, err := authz.RequireElevatedAdmin(adminCtx(adminSubject), hh); err == nil {
		t.Fatalf("RequireElevatedAdmin after expiry: expected error, got nil (a lapsed elevation must not gate a write)")
	}
}

// TestRequireElevatedAdmin_GatesWriteAndAuditStampsSubjectAndHousehold
// exercises FR10.3's write gate directly: RequireElevatedAdmin is the
// single predicate every future admin-write / non-standing-lane-read RPC
// calls before touching household data (no caller exists yet within
// #1197's own proto surface — the standing lane deliberately bypasses it).
// This proves the gate itself works and that the audit trail it enables
// carries both the admin subject and the target household, per FR10.1.
func TestRequireElevatedAdmin_GatesWriteAndAuditStampsSubjectAndHousehold(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, repo := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)
	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	adminSubject := "admin1"
	authz := NewAuthorizationPredicates(pg.Pool)

	// Without elevation: refused.
	if _, err := authz.RequireElevatedAdmin(adminCtx(adminSubject), hh); err == nil {
		t.Fatalf("RequireElevatedAdmin without elevation: expected error, got nil")
	}

	// Enter elevation, then the gate succeeds and identifies the admin.
	if _, err := server.EnterElevation(adminCtx(adminSubject), &pb.EnterElevationRequest{TargetHouseholdId: hh, Reason: "investigating billing issue"}); err != nil {
		t.Fatalf("EnterElevation: %v", err)
	}
	gatedSubject, err := authz.RequireElevatedAdmin(adminCtx(adminSubject), hh)
	if err != nil {
		t.Fatalf("RequireElevatedAdmin with active elevation: %v", err)
	}
	if gatedSubject != adminSubject {
		t.Fatalf("RequireElevatedAdmin returned subject %q, want %q", gatedSubject, adminSubject)
	}

	// A write gated this way stamps both admin subject and target household
	// on the resulting audit record (FR10.1).
	if err := repo.RecordAudit(ctx, gatedSubject, hh, "admin_write_example", "household", hh, "investigating billing issue"); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}
	var actorSubject string
	var targetHousehold int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT actor_subject, target_household_id FROM audit_record WHERE action = 'admin_write_example'
	`).Scan(&actorSubject, &targetHousehold)
	if err != nil {
		t.Fatalf("query audit record: %v", err)
	}
	if actorSubject != adminSubject {
		t.Errorf("audit actor_subject = %q, want %q", actorSubject, adminSubject)
	}
	if targetHousehold != hh {
		t.Errorf("audit target_household_id = %d, want %d", targetHousehold, hh)
	}

	// Elevation is scoped to the exact target household named at entry
	// (FR10.1): a different household is still refused.
	otherHousehold := mustInsertHousehold(t, ctx, pg, "hh-other")
	if _, err := authz.RequireElevatedAdmin(adminCtx(adminSubject), otherHousehold); err == nil {
		t.Fatalf("RequireElevatedAdmin against a non-elevated household: expected error, got nil")
	}
}

// ── FR10.2: Resolve — standing lane ──────────────────────────────────────

func TestResolve_RequiresAuthenticationAndAdminRole(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	_, err := server.Resolve(context.Background(), &pb.ResolveRequest{Target: &pb.ResolveRequest_Person{Person: "x"}})
	requireStatus(t, err, codes.Unauthenticated, "")

	_, err = server.Resolve(memberCtx("bob@example.com"), &pb.ResolveRequest{Target: &pb.ResolveRequest_Person{Person: "x"}})
	requireStatus(t, err, codes.PermissionDenied, apierrors.AdminRoleRequired)
}

func TestResolve_ByPerson_ReturnsHouseholdBoardHealth(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustAddMember(t, ctx, pg, hh, "alice@example.com")
	mustInsertBoard(t, ctx, pg, "board-1", hh, 1*time.Minute, false)

	resp, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_Person{Person: "alice@example.com"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resp.HouseholdId != hh {
		t.Errorf("HouseholdId = %d, want %d", resp.HouseholdId, hh)
	}
	if len(resp.Boards) != 1 || resp.Boards[0].DeviceId != "board-1" {
		t.Errorf("Boards = %+v, want one board-1", resp.Boards)
	}

	// Standing-lane reads are audited at query granularity (FR10.4).
	if got := auditCount(t, ctx, pg, hh, "resolve_person"); got != 1 {
		t.Errorf("audit rows for resolve_person = %d, want 1", got)
	}
}

func TestResolve_ByPerson_UnknownReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	_, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_Person{Person: "nobody@example.com"}})
	requireStatus(t, err, codes.NotFound, apierrors.ResolveNotFound)
}

func TestResolve_ByDeviceIDPrefix_UniqueMatchResolves(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustInsertBoard(t, ctx, pg, "leaflab-aaa111", hh, 1*time.Minute, false)

	resp, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_DeviceIdPrefix{DeviceIdPrefix: "leaflab-aaa"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resp.HouseholdId != hh {
		t.Errorf("HouseholdId = %d, want %d", resp.HouseholdId, hh)
	}
}

func TestResolve_ByDeviceIDPrefix_AmbiguousAcrossHouseholdsIsNotFound(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	hh1 := mustInsertHousehold(t, ctx, pg, "hh1")
	hh2 := mustInsertHousehold(t, ctx, pg, "hh2")
	mustInsertBoard(t, ctx, pg, "leaflab-shared1", hh1, 1*time.Minute, false)
	mustInsertBoard(t, ctx, pg, "leaflab-shared2", hh2, 1*time.Minute, false)

	_, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_DeviceIdPrefix{DeviceIdPrefix: "leaflab-shared"}})
	requireStatus(t, err, codes.NotFound, apierrors.ResolveNotFound)
}

func TestResolve_BySupportReference_ValidSucceeds(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustInsertSupportReference(t, ctx, pg, hh, "GOODCODE01", -1*time.Hour /* not yet expired: created "in the past" but expiry is future */, false)
	// mustInsertSupportReference computes expires_at = NOW() - expiresAgo; a
	// negative expiresAgo pushes expires_at into the future.

	resp, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_SupportReference{SupportReference: "GOODCODE01"}})
	if err != nil {
		t.Fatalf("Resolve valid support reference: %v", err)
	}
	if resp.HouseholdId != hh {
		t.Errorf("HouseholdId = %d, want %d", resp.HouseholdId, hh)
	}
}

// TestResolve_SupportReference_NFR2Indistinguishable verifies NFR2: an
// unknown, an expired, and a revoked support reference must be
// indistinguishable in status, response body, and timing.
func TestResolve_SupportReference_NFR2Indistinguishable(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100000) // generous: timing test, not rate-limit test

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustInsertSupportReference(t, ctx, pg, hh, "EXPIREDCOD", 1*time.Hour, false) // expired 1h ago
	mustInsertSupportReference(t, ctx, pg, hh, "REVOKEDCOD", -1*time.Hour, true) // not expired, but revoked

	cases := map[string]string{
		"unknown": "NOSUCHCODE",
		"expired": "EXPIREDCOD",
		"revoked": "REVOKEDCOD",
	}

	type result struct {
		st        *status.Status
		detail    string
		durations []time.Duration
	}
	results := make(map[string]*result, len(cases))
	for name := range cases {
		results[name] = &result{}
	}

	// Interleave calls across the three cases to avoid systemic timing bias
	// (e.g. cache warmup) favoring whichever ran first.
	const iterations = 25
	for i := 0; i < iterations; i++ {
		for _, name := range []string{"unknown", "expired", "revoked"} {
			code := cases[name]
			start := time.Now()
			_, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_SupportReference{SupportReference: code}})
			elapsed := time.Since(start)

			if err == nil {
				t.Fatalf("%s: expected a NotFound error (NFR2), got a successful resolve", name)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("%s: expected gRPC status error, got %v", name, err)
			}
			detail := apierrors.ErrorDetailFromStatus(st)
			if detail == nil {
				t.Fatalf("%s: expected an ErrorDetail on the status, got none (status: %v)", name, st)
			}
			results[name].st = st
			results[name].detail = detail.MessageKey
			results[name].durations = append(results[name].durations, elapsed)
		}
	}

	// Status and body must be byte-identical across all three (NFR2).
	unknown, expired, revoked := results["unknown"], results["expired"], results["revoked"]
	for _, pair := range []struct {
		name string
		r    *result
	}{{"expired", expired}, {"revoked", revoked}} {
		if pair.r.st.Code() != unknown.st.Code() {
			t.Errorf("%s code = %v, want %v (same as unknown)", pair.name, pair.r.st.Code(), unknown.st.Code())
		}
		if pair.r.st.Message() != unknown.st.Message() {
			t.Errorf("%s message = %q, want %q (same as unknown)", pair.name, pair.r.st.Message(), unknown.st.Message())
		}
		if pair.r.detail != unknown.detail {
			t.Errorf("%s message_key = %q, want %q (same as unknown)", pair.name, pair.r.detail, unknown.detail)
		}
	}
	if unknown.st.Code() != codes.NotFound || unknown.detail != apierrors.ResolveNotFound {
		t.Fatalf("unexpected baseline shape: code=%v detail=%q", unknown.st.Code(), unknown.detail)
	}

	// Timing must not leak which case occurred: compare mean latencies with
	// a generous tolerance (this runs against a real, possibly loaded, test
	// container — the point is to catch a gross short-circuit, e.g. an
	// early return that skips the DB round trip for one case, not to
	// enforce microsecond-level flatness).
	mean := func(ds []time.Duration) time.Duration {
		var total time.Duration
		for _, d := range ds {
			total += d
		}
		return total / time.Duration(len(ds))
	}
	means := map[string]time.Duration{
		"unknown": mean(unknown.durations),
		"expired": mean(expired.durations),
		"revoked": mean(revoked.durations),
	}
	var maxMean, minMean time.Duration
	for _, m := range means {
		if maxMean == 0 || m > maxMean {
			maxMean = m
		}
		if minMean == 0 || m < minMean {
			minMean = m
		}
	}
	// Guard against a near-zero minMean making the ratio meaningless.
	if minMean < 10*time.Microsecond {
		minMean = 10 * time.Microsecond
	}
	if ratio := float64(maxMean) / float64(minMean); ratio > 4.0 {
		t.Errorf("timing diverges across unknown/expired/revoked beyond tolerance: means=%v (ratio %.2fx)", means, ratio)
	}
}

func TestResolve_SupportReference_RateLimitedPerAdminPrincipal(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 1) // 1 request/sec burst

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustInsertSupportReference(t, ctx, pg, hh, "RATELIMCOD", -1*time.Hour, false)

	_, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_SupportReference{SupportReference: "RATELIMCOD"}})
	if err != nil {
		t.Fatalf("first Resolve call: expected success, got %v", err)
	}

	_, err = server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_SupportReference{SupportReference: "RATELIMCOD"}})
	requireStatus(t, err, codes.ResourceExhausted, "")
}

// ── FR79: ListFleetHealth ─────────────────────────────────────────────────

func TestListFleetHealth_RequiresAdminRole(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)

	_, err := server.ListFleetHealth(memberCtx("bob@example.com"), &pb.ListFleetHealthRequest{})
	requireStatus(t, err, codes.PermissionDenied, apierrors.AdminRoleRequired)
}

func TestListFleetHealth_ExcludesRetiredBoardsEvenWhenUnhealthy(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	// Both boards are equally stale (well past the 15m floor); one is retired.
	mustInsertBoard(t, ctx, pg, "board-active-stale", hh, 1*time.Hour, false)
	mustInsertBoard(t, ctx, pg, "board-retired-stale", hh, 1*time.Hour, true)

	resp, err := server.ListFleetHealth(adminCtx("admin1"), &pb.ListFleetHealthRequest{})
	if err != nil {
		t.Fatalf("ListFleetHealth: %v", err)
	}
	seen := map[string]bool{}
	for _, b := range resp.Boards {
		seen[b.DeviceId] = true
	}
	if !seen["board-active-stale"] {
		t.Errorf("expected active stale board in listing, got %v", resp.Boards)
	}
	if seen["board-retired-stale"] {
		t.Errorf("retired board must be excluded entirely (FR22.4), got %v", resp.Boards)
	}
	if resp.Page.TotalSize != 1 {
		t.Errorf("TotalSize = %d, want 1 (retired board excluded from count)", resp.Page.TotalSize)
	}

	// Also excluded under unhealthy_only, even though it's the "most
	// unhealthy" candidate — FR79: excluded from offline counts entirely.
	resp, err = server.ListFleetHealth(adminCtx("admin1"), &pb.ListFleetHealthRequest{UnhealthyOnly: true})
	if err != nil {
		t.Fatalf("ListFleetHealth unhealthy_only: %v", err)
	}
	for _, b := range resp.Boards {
		if b.DeviceId == "board-retired-stale" {
			t.Errorf("retired board appeared in unhealthy_only results: %+v", b)
		}
	}
}

func TestListFleetHealth_UnhealthyOnlyFiltersToNotReporting(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustInsertBoard(t, ctx, pg, "board-fresh", hh, 1*time.Minute, false)
	mustInsertBoard(t, ctx, pg, "board-stale", hh, 1*time.Hour, false)

	resp, err := server.ListFleetHealth(adminCtx("admin1"), &pb.ListFleetHealthRequest{UnhealthyOnly: true})
	if err != nil {
		t.Fatalf("ListFleetHealth: %v", err)
	}
	if len(resp.Boards) != 1 || resp.Boards[0].DeviceId != "board-stale" {
		t.Errorf("unhealthy_only boards = %+v, want only board-stale", resp.Boards)
	}
	for _, b := range resp.Boards {
		if b.Reporting {
			t.Errorf("board %q returned under unhealthy_only but Reporting=true", b.DeviceId)
		}
	}
}

func TestListFleetHealth_FiltersByHouseholdDeviceIDPrefixAndRegion(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)

	hh1 := mustInsertHousehold(t, ctx, pg, "hh1")
	hh2 := mustInsertHousehold(t, ctx, pg, "hh2")
	region1 := mustInsertRegion(t, ctx, pg, nil, "room-1")
	region2 := mustInsertRegion(t, ctx, pg, nil, "room-2")

	b1 := mustInsertBoard(t, ctx, pg, "alpha-1", hh1, 1*time.Minute, false)
	b2 := mustInsertBoard(t, ctx, pg, "beta-1", hh2, 1*time.Minute, false)
	mustInsertSensor(t, ctx, pg, b1, &region1)
	mustInsertSensor(t, ctx, pg, b2, &region2)

	// household_id filter
	resp, err := server.ListFleetHealth(adminCtx("admin1"), &pb.ListFleetHealthRequest{HouseholdId: hh1})
	if err != nil {
		t.Fatalf("ListFleetHealth household filter: %v", err)
	}
	if len(resp.Boards) != 1 || resp.Boards[0].DeviceId != "alpha-1" {
		t.Errorf("household_id=%d boards = %+v, want only alpha-1", hh1, resp.Boards)
	}

	// device_id_prefix filter
	resp, err = server.ListFleetHealth(adminCtx("admin1"), &pb.ListFleetHealthRequest{DeviceIdPrefix: "beta"})
	if err != nil {
		t.Fatalf("ListFleetHealth prefix filter: %v", err)
	}
	if len(resp.Boards) != 1 || resp.Boards[0].DeviceId != "beta-1" {
		t.Errorf("device_id_prefix=beta boards = %+v, want only beta-1", resp.Boards)
	}

	// region_id filter
	resp, err = server.ListFleetHealth(adminCtx("admin1"), &pb.ListFleetHealthRequest{RegionId: region2})
	if err != nil {
		t.Fatalf("ListFleetHealth region filter: %v", err)
	}
	if len(resp.Boards) != 1 || resp.Boards[0].DeviceId != "beta-1" {
		t.Errorf("region_id=%d boards = %+v, want only beta-1", region2, resp.Boards)
	}
}

func TestListFleetHealth_PaginationCoversAllBoardsWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	const total = 5
	for i := 0; i < total; i++ {
		mustInsertBoard(t, ctx, pg, fmt.Sprintf("board-%02d", i), hh, 1*time.Minute, false)
	}

	seen := map[string]bool{}
	var pageToken string
	pages := 0
	for {
		pages++
		if pages > total+1 {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
		resp, err := server.ListFleetHealth(adminCtx("admin1"), &pb.ListFleetHealthRequest{
			Page: &pb.PageRequest{PageSize: 2, PageToken: pageToken},
		})
		if err != nil {
			t.Fatalf("ListFleetHealth page %d: %v", pages, err)
		}
		for _, b := range resp.Boards {
			if seen[b.DeviceId] {
				t.Errorf("board %q returned more than once across pages", b.DeviceId)
			}
			seen[b.DeviceId] = true
		}
		if resp.Page.TotalSize != total {
			t.Errorf("TotalSize = %d, want %d", resp.Page.TotalSize, total)
		}
		if resp.Page.NextPageToken == "" {
			break
		}
		pageToken = resp.Page.NextPageToken
	}
	if len(seen) != total {
		t.Errorf("saw %d distinct boards across pages, want %d", len(seen), total)
	}
}

// TestListFleetHealth_AuditAtQueryGranularity verifies FR10.4: one audit
// record per distinct household actually returned, not one per board row.
func TestListFleetHealth_AuditAtQueryGranularity(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 0)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustInsertBoard(t, ctx, pg, "board-a", hh, 1*time.Minute, false)
	mustInsertBoard(t, ctx, pg, "board-b", hh, 1*time.Minute, false)

	resp, err := server.ListFleetHealth(adminCtx("admin1"), &pb.ListFleetHealthRequest{HouseholdId: hh})
	if err != nil {
		t.Fatalf("ListFleetHealth: %v", err)
	}
	if len(resp.Boards) != 2 {
		t.Fatalf("expected 2 boards returned, got %d", len(resp.Boards))
	}
	if got := auditCount(t, ctx, pg, hh, "list_fleet_health"); got != 1 {
		t.Errorf("audit rows for list_fleet_health against household with 2 returned boards = %d, want 1 (query granularity, not per-row)", got)
	}
}

// ── FR80: support references ────────────────────────────────────────────

func TestCreateSupportReference_MemberSucceedsAndIsAudited(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustAddMember(t, ctx, pg, hh, "alice@example.com")

	resp, err := server.CreateSupportReference(memberCtx("alice@example.com"), &pb.CreateSupportReferenceRequest{})
	if err != nil {
		t.Fatalf("CreateSupportReference: %v", err)
	}
	if resp.Reference == "" {
		t.Fatalf("Reference is empty, want a code")
	}
	if resp.ExpiresAt <= time.Now().Unix() {
		t.Errorf("ExpiresAt = %d, want a future Unix timestamp", resp.ExpiresAt)
	}

	// Resolves for an admin in the standing lane.
	resolveResp, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_SupportReference{SupportReference: resp.Reference}})
	if err != nil {
		t.Fatalf("Resolve on freshly created reference: %v", err)
	}
	if resolveResp.HouseholdId != hh {
		t.Errorf("resolved HouseholdId = %d, want %d", resolveResp.HouseholdId, hh)
	}

	// FR9: existence is visible in the household's own activity list.
	if got := auditCount(t, ctx, pg, hh, "create_support_reference"); got != 1 {
		t.Errorf("audit rows for create_support_reference = %d, want 1", got)
	}
}

func TestCreateSupportReference_PrincipalWithoutHouseholdIsRejected(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	_, err := server.CreateSupportReference(memberCtx("nobody@example.com"), &pb.CreateSupportReferenceRequest{})
	requireStatus(t, err, codes.PermissionDenied, apierrors.NoHousehold)
}

func TestRevokeSupportReference_MakesItUnresolvable(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustAddMember(t, ctx, pg, hh, "alice@example.com")

	created, err := server.CreateSupportReference(memberCtx("alice@example.com"), &pb.CreateSupportReferenceRequest{})
	if err != nil {
		t.Fatalf("CreateSupportReference: %v", err)
	}

	if _, err := server.RevokeSupportReference(memberCtx("alice@example.com"), &pb.RevokeSupportReferenceRequest{Reference: created.Reference}); err != nil {
		t.Fatalf("RevokeSupportReference: %v", err)
	}

	_, err = server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_SupportReference{SupportReference: created.Reference}})
	requireStatus(t, err, codes.NotFound, apierrors.ResolveNotFound)

	if got := auditCount(t, ctx, pg, hh, "revoke_support_reference"); got != 1 {
		t.Errorf("audit rows for revoke_support_reference = %d, want 1", got)
	}
}

func TestRevokeSupportReference_AlreadyRevokedIsNotFound(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	hh := mustInsertHousehold(t, ctx, pg, "hh1")
	mustAddMember(t, ctx, pg, hh, "alice@example.com")
	created, err := server.CreateSupportReference(memberCtx("alice@example.com"), &pb.CreateSupportReferenceRequest{})
	if err != nil {
		t.Fatalf("CreateSupportReference: %v", err)
	}
	if _, err := server.RevokeSupportReference(memberCtx("alice@example.com"), &pb.RevokeSupportReferenceRequest{Reference: created.Reference}); err != nil {
		t.Fatalf("first RevokeSupportReference: %v", err)
	}

	_, err = server.RevokeSupportReference(memberCtx("alice@example.com"), &pb.RevokeSupportReferenceRequest{Reference: created.Reference})
	requireStatus(t, err, codes.NotFound, apierrors.SupportReferenceNotFound)
}

// TestRevokeSupportReference_ScopedToCallersHousehold verifies revocation is
// owner-managed and scoped: a member of a different household cannot revoke
// another household's reference, even with the correct code.
func TestRevokeSupportReference_ScopedToCallersHousehold(t *testing.T) {
	ctx := context.Background()
	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminTestSchema})
	server, _ := newAdminTestServer(t, pg, defaultTestAdminConfig(), 100)

	hhA := mustInsertHousehold(t, ctx, pg, "hhA")
	mustAddMember(t, ctx, pg, hhA, "alice@example.com")
	hhB := mustInsertHousehold(t, ctx, pg, "hhB")
	mustAddMember(t, ctx, pg, hhB, "bob@example.com")

	created, err := server.CreateSupportReference(memberCtx("alice@example.com"), &pb.CreateSupportReferenceRequest{})
	if err != nil {
		t.Fatalf("CreateSupportReference: %v", err)
	}

	_, err = server.RevokeSupportReference(memberCtx("bob@example.com"), &pb.RevokeSupportReferenceRequest{Reference: created.Reference})
	requireStatus(t, err, codes.NotFound, apierrors.SupportReferenceNotFound)

	// Still resolvable — bob's attempt did not revoke alice's household's reference.
	resp, err := server.Resolve(adminCtx("admin1"), &pb.ResolveRequest{Target: &pb.ResolveRequest_SupportReference{SupportReference: created.Reference}})
	if err != nil {
		t.Fatalf("Resolve after cross-household revoke attempt: %v", err)
	}
	if resp.HouseholdId != hhA {
		t.Errorf("HouseholdId = %d, want %d (alice's household, unaffected by bob's attempt)", resp.HouseholdId, hhA)
	}
}
