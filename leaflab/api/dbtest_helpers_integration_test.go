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
	"github.com/whale-net/everything/libs/go/dbtest"
)

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

// newTestServer starts a real Postgres container, applies testSchema, and
// returns a LeafLabAPIServer backed by a real Repository plus the raw pool
// for fixture setup / assertions. publisher is nil: every RPC exercised by
// these files either never reaches the publish step (PushDeviceConfig is
// tested only via its pre-write validation refusal) or never touches it at
// all (ListBoards).
func newTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: testSchema})
	repo := NewRepository(db.Pool)
	return NewLeafLabAPIServer(repo, nil, nil, discardLogger()), db.Pool
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
