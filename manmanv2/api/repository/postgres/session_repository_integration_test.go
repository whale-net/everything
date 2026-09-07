//go:build integration

// This file only builds under the "integration" build tag, same as
// pending_restart_integration_test.go's precedent for this package -- see
// //libs/go/dbtest's README for how to run it. It exercises the actual
// DB-enforced compare-and-swap semantics UpdateSessionEndIfStatus's
// "WHERE status = $2" clause relies on (#2061): whether the row's current
// status still matches the caller's stale snapshot, decided atomically by
// Postgres itself, not by application code re-reading the row -- something
// an in-memory fake can't verify because it wouldn't be exercising the same
// race the real UPDATE...RETURNING closes.
//
// Schema here is hand-written, self-contained DDL mirroring exactly the
// pieces of manmanv2/migrate/migrations/001_initial_schema.up.sql that a
// sessions row's FKs require (servers, games, game_configs,
// server_game_configs, sessions), per dbtest's README ("Options.Schema
// should be self-contained DDL -- do not depend on another package's
// migrations").
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const sessionRepositorySchema = `
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
		started_at TIMESTAMP,
		ended_at TIMESTAMP,
		exit_code INTEGER,
		status VARCHAR(50) NOT NULL DEFAULT 'pending',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
`

func newSessionRepositoryTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: sessionRepositorySchema})
	return db.Pool
}

// seedSessionRow creates a server + game + game_config + server_game_config
// chain and a session row at the given status, returning the session_id.
func seedSessionRow(t *testing.T, pool *pgxpool.Pool, label, status string) int64 {
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
	var sessionID int64
	if err := pool.QueryRow(ctx, `INSERT INTO sessions (sgc_id, status) VALUES ($1, $2) RETURNING session_id`, sgcID, status).Scan(&sessionID); err != nil {
		t.Fatalf("seed session %s: %v", label, err)
	}
	return sessionID
}

// TestUpdateSessionEndIfStatus_MatchingStatusWrites proves the CAS write
// path: when expectedStatus matches the row's current status, the write
// commits and (true, nil) is returned.
func TestUpdateSessionEndIfStatus_MatchingStatusWrites(t *testing.T) {
	pool := newSessionRepositoryTestDB(t)
	ctx := context.Background()
	repo := NewSessionRepository(pool)

	sessionID := seedSessionRow(t, pool, "match", "stopping")

	now := time.Now()
	exitCode := 0
	updated, err := repo.UpdateSessionEndIfStatus(ctx, sessionID, "stopping", "lost", now, &exitCode)
	if err != nil {
		t.Fatalf("UpdateSessionEndIfStatus: %v", err)
	}
	if !updated {
		t.Fatal("expected updated=true when expectedStatus matches the row's current status")
	}

	got, err := repo.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "lost" {
		t.Fatalf("expected status 'lost' after CAS write, got %q", got.Status)
	}
}

// TestUpdateSessionEndIfStatus_MismatchedStatusSkipsWrite proves the CAS
// miss path: when expectedStatus does not match the row's current status
// (e.g. a real transition committed first), the write is skipped, (false,
// nil) is returned, and the row is left untouched.
func TestUpdateSessionEndIfStatus_MismatchedStatusSkipsWrite(t *testing.T) {
	pool := newSessionRepositoryTestDB(t)
	ctx := context.Background()
	repo := NewSessionRepository(pool)

	// Row is actually 'stopped' (a real terminal transition already landed),
	// but the caller's stale snapshot still says 'stopping'.
	sessionID := seedSessionRow(t, pool, "mismatch", "stopped")

	now := time.Now()
	updated, err := repo.UpdateSessionEndIfStatus(ctx, sessionID, "stopping", "lost", now, nil)
	if err != nil {
		t.Fatalf("UpdateSessionEndIfStatus should not error on a CAS miss: %v", err)
	}
	if updated {
		t.Fatal("expected updated=false when expectedStatus does not match the row's current status")
	}

	got, err := repo.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "stopped" {
		t.Fatalf("expected the real 'stopped' transition to survive the CAS miss, got %q", got.Status)
	}
	if got.EndedAt != nil {
		t.Fatalf("expected ended_at to remain untouched by the skipped write, got %v", got.EndedAt)
	}
}

// TestUpdateSessionEndIfStatus_NonexistentSessionReturnsFalseNoError proves
// a nonexistent sessionID is treated the same as a CAS miss: (false, nil),
// not an error.
func TestUpdateSessionEndIfStatus_NonexistentSessionReturnsFalseNoError(t *testing.T) {
	pool := newSessionRepositoryTestDB(t)
	ctx := context.Background()
	repo := NewSessionRepository(pool)

	updated, err := repo.UpdateSessionEndIfStatus(ctx, 999999, "stopping", "lost", time.Now(), nil)
	if err != nil {
		t.Fatalf("expected no error for a nonexistent session, got: %v", err)
	}
	if updated {
		t.Fatal("expected updated=false for a nonexistent session")
	}
}
