//go:build integration

// Shared fixtures for this package's real-Postgres integration tests (see
// response_contract_integration_test.go and
// repository_board_lifecycle_integration_test.go). Kept in one file so the
// schema and helpers aren't duplicated across test files that both need a
// board/device_config database.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// authedCtx returns a context carrying grpcauth.Claims for a fixed test
// subject, exactly as the auth interceptor chain would inject them for a
// real authenticated caller (see grpcauth.ContextWithClaims). Every RPC
// call in this package's integration tests must use this instead of a bare
// context.Background(): scopeForCaller (server.go) fails closed -- an
// empty, permits-nothing Scope -- on a context with no Claims, regardless
// of what authzSvc would otherwise grant (see stubAuthz above).
func authedCtx() context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "test-caller"})
}

// testSchema is self-contained hand-written DDL -- it deliberately does not
// depend on leaflab/migrate's migrations so these tests stay hermetic (see
// dbtest's own doc comment on Options.Schema). retired_at/idx_board_active
// mirror migration 015_ownership's board retirement columns/index (FR22.1,
// FR22.4, FR22.5) so RetireBoard/GetBoardByID/ListBoards can be exercised
// against real SQL without pulling in the full ownership schema those RPCs
// don't touch.
//
// audit_log mirrors migration 016_audit_log's column set (schema only --
// no household table exists here, so target_household_id carries no FK,
// and the append-only trigger/REVOKE aren't reproduced; those are a
// migration-fidelity concern for a test that runs the real migration file,
// not this hermetic schema). It exists here so RetireBoard and
// InsertDeviceConfigNextVersion -- both of which now write an audit_log
// row in the same transaction as their write -- have somewhere to write
// it.
const testSchema = `
	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
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

// testAuditEntry returns a minimal valid audit.Entry for tests exercising a
// write path that now requires one (RetireBoard,
// InsertDeviceConfigNextVersion) but aren't themselves testing audit
// content -- action/entity_kind are arbitrary but non-empty (both columns
// are NOT NULL).
func testAuditEntry() audit.Entry {
	return audit.Entry{
		ActorSubject: "test-actor",
		ActorKind:    audit.ActorKindHuman,
		Action:       "TestAction",
		EntityKind:   "test-entity",
	}
}

// discardLogger is a *slog.Logger that throws away everything it's given --
// these tests assert on returned errors and DB state, not log output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// allScope is a test-only authz.Scope that matches every row -- these
// files exercise FR59/FR61/FR64's response contract and FR22's board
// retirement, not FR5's household scoping (leaflab/api/authz carries its
// own coverage for that once Testing lands), so a Scope that never
// filters keeps this fixture hermetic without pulling in a
// household/household_membership schema these tests don't otherwise need.
type allScope struct{}

func (allScope) Permits(ref authz.EntityRef, res authz.Resolution) bool { return true }
func (allScope) Filter(argStart int) (string, []any)                    { return "TRUE", nil }

// stubAuthz is a test-only authzResolver that grants allScope to every
// principal, regardless of subject -- see allScope's doc comment.
// ResolveBoardByDeviceID is unused by these files' tests today (none of
// them exercise PushDeviceConfig/GetDeviceConfig's board-scoped path);
// it panics if that ever changes without updating this fixture.
type stubAuthz struct{}

func (stubAuthz) ScopeForPrincipal(ctx context.Context, principalSubject string) (authz.Scope, error) {
	return allScope{}, nil
}

func (stubAuthz) ResolveBoardByDeviceID(ctx context.Context, deviceID string) (authz.EntityRef, authz.Resolution, error) {
	panic("not used by this package's integration tests")
}

// newTestServer starts a real Postgres container, applies testSchema, and
// returns a LeafLabAPIServer backed by a real Repository plus the raw pool
// for fixture setup / assertions. publisher is nil: every RPC exercised by
// these files either never reaches the publish step (PushDeviceConfig is
// tested only via its pre-write validation refusal) or never touches it at
// all (ListBoards). authzSvc is stubAuthz, not a real authz.PGResolver:
// these tests assert on the response contract and retirement behavior, not
// on FR5 scoping, and don't want to carry a household/household_membership
// fixture just to make ListBoards return non-empty. Callers must still
// present grpcauth.Claims on ctx (see response_contract_integration_test.go)
// -- scopeForCaller fails closed on an unauthenticated context regardless
// of what authzSvc would return.
func newTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: testSchema})
	repo := NewRepository(db.Pool)
	return NewLeafLabAPIServer(repo, stubAuthz{}, nil, nil, discardLogger()), db.Pool
}

// newTestRepository starts a real Postgres container, applies testSchema,
// and returns a *Repository directly -- for tests exercising repository.go
// methods that have no corresponding RPC surface exercised elsewhere
// (RetireBoard, GetBoardByID).
func newTestRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: testSchema})
	return NewRepository(db.Pool), db.Pool
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}

func insertBoard(t *testing.T, pool *pgxpool.Pool, deviceID string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).Scan(&id)
	if err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}
