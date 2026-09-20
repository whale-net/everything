//go:build integration

// Real-Postgres coverage for backups.trigger_source (M7 FR2/FR3 backing,
// plan #2777, task #2808): a Backup created through BackupRepository.Create
// with TriggerSource = "manual" round-trips through Get and List with that
// value intact, an unset TriggerSource defaults to "unknown" on write (the
// FR2 backfill rule applied to the write path too), and the CHECK
// constraint rejects any other value. Same precedent as
// action_soft_delete_integration_test.go: build-tagged `integration`,
// self-contained DDL, run explicitly with Docker.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:backup_trigger_source_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// backupTriggerSourceSchema is self-contained DDL for the backups table in
// its post-044 shape (M7 FR2) plus the minimal parent chain a backups row
// requires.
const backupTriggerSourceSchema = `
	CREATE TABLE games (
		game_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE game_configs (
		config_id BIGSERIAL PRIMARY KEY,
		game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		name VARCHAR(255) NOT NULL,
		image VARCHAR(500) NOT NULL
	);

	CREATE TABLE servers (
		server_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE server_game_configs (
		sgc_id BIGSERIAL PRIMARY KEY,
		server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
		game_config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE
	);

	CREATE TABLE sessions (
		session_id BIGSERIAL PRIMARY KEY,
		sgc_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE
	);

	CREATE TABLE backups (
		backup_id BIGSERIAL PRIMARY KEY,
		session_id BIGINT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
		server_game_config_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
		backup_config_id BIGINT,
		volume_id BIGINT,
		s3_url TEXT,
		size_bytes BIGINT,
		status TEXT NOT NULL DEFAULT 'pending'
			CHECK (status IN ('pending', 'running', 'completed', 'failed')),
		error_message TEXT,
		description TEXT,
		trigger_source TEXT NOT NULL DEFAULT 'unknown'
			CHECK (trigger_source IN ('scheduled', 'manual', 'unknown')),
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		deleted_at TIMESTAMP
	);
`

// newBackupTriggerSourceRepo brings up an isolated database with
// backupTriggerSourceSchema applied and returns a BackupRepository plus the
// raw pool for fixture seeding/assertions.
func newBackupTriggerSourceRepo(t *testing.T) (*BackupRepository, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	if _, err := db.Pool.Exec(ctx, backupTriggerSourceSchema); err != nil {
		t.Fatalf("apply backupTriggerSourceSchema: %v", err)
	}
	return &BackupRepository{db: db.Pool}, db.Pool
}

// seedSessionFixture walks the parent chain a backups row requires and
// returns the resulting session_id and sgc_id.
func seedSessionFixture(t *testing.T, pool *pgxpool.Pool, name string) (sessionID, sgcID int64) {
	t.Helper()
	ctx := context.Background()

	var gameID, configID, serverID int64
	seed := func(q string, dest *int64, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, q, args...).Scan(dest); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	seed(`INSERT INTO games (name) VALUES ($1) RETURNING game_id`, &gameID, name+"-game")
	seed(`INSERT INTO game_configs (game_id, name, image) VALUES ($1, $2, 'image') RETURNING config_id`, &configID, gameID, name+"-config")
	seed(`INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, &serverID, name+"-server")
	seed(`INSERT INTO server_game_configs (server_id, game_config_id) VALUES ($1, $2) RETURNING sgc_id`, &sgcID, serverID, configID)
	seed(`INSERT INTO sessions (sgc_id) VALUES ($1) RETURNING session_id`, &sessionID, sgcID)
	return sessionID, sgcID
}

// TestBackupRepository_TriggerSourceRoundTrips proves a Backup created with
// TriggerSource = "manual" reads back as "manual" through both Get and
// List -- the issue's Testing item covering the repository round-trip.
func TestBackupRepository_TriggerSourceRoundTrips(t *testing.T) {
	repo, pool := newBackupTriggerSourceRepo(t)
	sessionID, sgcID := seedSessionFixture(t, pool, "trigger-roundtrip")

	created, err := repo.Create(context.Background(), &manman.Backup{
		SessionID:          sessionID,
		ServerGameConfigID: sgcID,
		Status:             manman.BackupStatusPending,
		TriggerSource:      manman.BackupTriggerSourceManual,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(context.Background(), created.BackupID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TriggerSource != manman.BackupTriggerSourceManual {
		t.Fatalf("Get: expected TriggerSource = %q, got %q", manman.BackupTriggerSourceManual, got.TriggerSource)
	}

	listed, err := repo.List(context.Background(), &sgcID, nil, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 listed backup, got %d", len(listed))
	}
	if listed[0].TriggerSource != manman.BackupTriggerSourceManual {
		t.Fatalf("List: expected TriggerSource = %q, got %q", manman.BackupTriggerSourceManual, listed[0].TriggerSource)
	}
}

// TestBackupRepository_CreateDefaultsUnsetTriggerSourceToUnknown proves the
// write path applies the same "never guess manual/scheduled" rule the
// migration's DEFAULT clause applies to pre-existing history: a Backup
// created without an explicit TriggerSource lands on "unknown".
func TestBackupRepository_CreateDefaultsUnsetTriggerSourceToUnknown(t *testing.T) {
	repo, pool := newBackupTriggerSourceRepo(t)
	sessionID, sgcID := seedSessionFixture(t, pool, "trigger-default")

	created, err := repo.Create(context.Background(), &manman.Backup{
		SessionID:          sessionID,
		ServerGameConfigID: sgcID,
		Status:             manman.BackupStatusPending,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.TriggerSource != manman.BackupTriggerSourceUnknown {
		t.Fatalf("expected unset TriggerSource to default to %q, got %q", manman.BackupTriggerSourceUnknown, created.TriggerSource)
	}

	got, err := repo.Get(context.Background(), created.BackupID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TriggerSource != manman.BackupTriggerSourceUnknown {
		t.Fatalf("Get: expected TriggerSource = %q, got %q", manman.BackupTriggerSourceUnknown, got.TriggerSource)
	}
}

// TestBackupRepository_TriggerSourceRejectsInvalidValue proves the CHECK
// constraint backing FR2 rejects any value outside scheduled/manual/unknown
// even when written directly (not just through the repository, which never
// produces an invalid value itself).
func TestBackupRepository_TriggerSourceRejectsInvalidValue(t *testing.T) {
	_, pool := newBackupTriggerSourceRepo(t)
	sessionID, sgcID := seedSessionFixture(t, pool, "trigger-invalid")

	if _, err := pool.Exec(context.Background(), `
		INSERT INTO backups (session_id, server_game_config_id, trigger_source) VALUES ($1, $2, 'bogus')
	`, sessionID, sgcID); err == nil {
		t.Fatal("expected an invalid trigger_source value to be rejected by the CHECK constraint, it succeeded")
	}
}
