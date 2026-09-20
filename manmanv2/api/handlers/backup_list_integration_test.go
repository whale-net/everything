//go:build integration

// Real-Postgres coverage for FR1/FR4/NFR4 (task #2809, plan #2777):
// BackupHandler.ListBackups must be a genuine fleet-wide query -- no filters
// spans more than one SGC -- filterable by SGC/volume/BackupConfig/status
// individually and in combination, server-side paginated with soft-deleted
// rows never surfacing on any page, and `items` must carry the same
// count/order as the legacy `backups` array plus populated display names.
// Real Postgres via //libs/go/dbtest, same precedent as
// backup_config_actions_integration_test.go -- no RabbitMQ needed since
// ListBackups never publishes a command.
//
// Run it explicitly (requires working Docker):
//
//	bazel test //manmanv2/api/handlers:backup_list_integration_test --test_output=all
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// backupListHandlerSchema is self-contained DDL covering every table
// BackupRepository.List joins across: games, game_configs, servers,
// server_game_configs, sessions, game_config_volumes, backup_configs and
// backups in their final (post-migration-045) shape.
const backupListHandlerSchema = `
	CREATE TABLE servers (
		server_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE,
		status VARCHAR(50) NOT NULL DEFAULT 'offline'
	);

	CREATE TABLE games (
		game_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE game_configs (
		config_id BIGSERIAL PRIMARY KEY,
		game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		name VARCHAR(255) NOT NULL,
		image VARCHAR(500) NOT NULL DEFAULT 'image:latest',
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
		status VARCHAR(50) NOT NULL DEFAULT 'pending'
	);

	CREATE TABLE game_config_volumes (
		volume_id BIGSERIAL PRIMARY KEY,
		config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
		name VARCHAR(100) NOT NULL,
		container_path TEXT NOT NULL DEFAULT '/data',
		UNIQUE(config_id, name)
	);

	CREATE TABLE backup_configs (
		backup_config_id BIGSERIAL PRIMARY KEY,
		volume_id BIGINT NOT NULL REFERENCES game_config_volumes(volume_id) ON DELETE CASCADE,
		cadence_minutes INT NOT NULL CHECK (cadence_minutes > 0),
		backup_path TEXT NOT NULL DEFAULT 'saves',
		enabled BOOLEAN NOT NULL DEFAULT true
	);

	CREATE TABLE backups (
		backup_id BIGSERIAL PRIMARY KEY,
		session_id BIGINT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
		server_game_config_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
		backup_config_id BIGINT REFERENCES backup_configs(backup_config_id) ON DELETE SET NULL,
		volume_id BIGINT REFERENCES game_config_volumes(volume_id) ON DELETE SET NULL,
		s3_url TEXT,
		size_bytes BIGINT,
		status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
		error_message TEXT,
		description TEXT,
		trigger_source TEXT NOT NULL DEFAULT 'unknown' CHECK (trigger_source IN ('scheduled', 'manual', 'unknown')),
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		deleted_at TIMESTAMP
	);

	CREATE INDEX idx_backups_volume_id ON backups(volume_id);
`

func newBackupListHandlerTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: backupListHandlerSchema})
	return db.Pool
}

func newBackupListHandler(pool *pgxpool.Pool) *BackupHandler {
	backupRepo := postgres.NewBackupRepository(pool)
	return NewBackupHandler(backupRepo, nil, nil)
}

// seededSGC is a fully-seeded deployment (server + game + game config +
// server_game_config) plus the session every backup row must reference.
type seededSGC struct {
	sgcID     int64
	sessionID int64
	configID  int64
}

func seedBackupListSGC(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) seededSGC {
	t.Helper()
	var serverID, gameID, configID, sgcID, sessionID int64
	if err := pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, "server-"+label).Scan(&serverID); err != nil {
		t.Fatalf("seed server %q: %v", label, err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO games (name) VALUES ($1) RETURNING game_id`, "game-"+label).Scan(&gameID); err != nil {
		t.Fatalf("seed game %q: %v", label, err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO game_configs (game_id, name) VALUES ($1, $2) RETURNING config_id`, gameID, "config-"+label).Scan(&configID); err != nil {
		t.Fatalf("seed game_config %q: %v", label, err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO server_game_configs (server_id, game_config_id) VALUES ($1, $2) RETURNING sgc_id`, serverID, configID).Scan(&sgcID); err != nil {
		t.Fatalf("seed server_game_config %q: %v", label, err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO sessions (sgc_id) VALUES ($1) RETURNING session_id`, sgcID).Scan(&sessionID); err != nil {
		t.Fatalf("seed session %q: %v", label, err)
	}
	return seededSGC{sgcID: sgcID, sessionID: sessionID, configID: configID}
}

func seedBackupListVolume(t *testing.T, ctx context.Context, pool *pgxpool.Pool, configID int64, name string) int64 {
	t.Helper()
	var volumeID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO game_config_volumes (config_id, name) VALUES ($1, $2) RETURNING volume_id
	`, configID, name).Scan(&volumeID); err != nil {
		t.Fatalf("seed volume %q: %v", name, err)
	}
	return volumeID
}

func seedBackupListConfig(t *testing.T, ctx context.Context, pool *pgxpool.Pool, volumeID int64) int64 {
	t.Helper()
	var backupConfigID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO backup_configs (volume_id, cadence_minutes) VALUES ($1, 60) RETURNING backup_config_id
	`, volumeID).Scan(&backupConfigID); err != nil {
		t.Fatalf("seed backup_config: %v", err)
	}
	return backupConfigID
}

type backupSeed struct {
	sgc            seededSGC
	volumeID       *int64
	backupConfigID *int64
	status         string
	createdAt      time.Time
}

func insertBackupListRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, s backupSeed) int64 {
	t.Helper()
	var backupID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO backups (session_id, server_game_config_id, backup_config_id, volume_id, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING backup_id
	`, s.sgc.sessionID, s.sgc.sgcID, s.backupConfigID, s.volumeID, s.status, s.createdAt).Scan(&backupID); err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	return backupID
}

// TestListBackups_NoFiltersSpansMultipleSGCs is the FR1/NFR4 green case:
// with no filters at all, ListBackups returns rows across more than one
// SGC, proving the query is fleet-wide rather than scoped to a single
// deployment.
func TestListBackups_NoFiltersSpansMultipleSGCs(t *testing.T) {
	ctx := context.Background()
	pool := newBackupListHandlerTestDB(t)
	h := newBackupListHandler(pool)

	sgcA := seedBackupListSGC(t, ctx, pool, "a")
	sgcB := seedBackupListSGC(t, ctx, pool, "b")
	now := time.Now().UTC().Truncate(time.Second)
	insertBackupListRow(t, ctx, pool, backupSeed{sgc: sgcA, status: manman.BackupStatusCompleted, createdAt: now})
	insertBackupListRow(t, ctx, pool, backupSeed{sgc: sgcB, status: manman.BackupStatusCompleted, createdAt: now.Add(time.Second)})

	resp, err := h.ListBackups(ctx, &pb.ListBackupsRequest{})
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(resp.Backups) != 2 {
		t.Fatalf("expected 2 backups fleet-wide, got %d", len(resp.Backups))
	}
	sgcSeen := map[int64]bool{resp.Backups[0].ServerGameConfigId: true, resp.Backups[1].ServerGameConfigId: true}
	if !sgcSeen[sgcA.sgcID] || !sgcSeen[sgcB.sgcID] {
		t.Fatalf("expected rows from both sgc %d and %d, got %+v", sgcA.sgcID, sgcB.sgcID, sgcSeen)
	}
}

// TestListBackups_FiltersNarrowResults is the FR4 green case: each filter
// dimension (SGC, volume, BackupConfig, status), alone and combined,
// narrows the fleet-wide result set to exactly the matching rows.
func TestListBackups_FiltersNarrowResults(t *testing.T) {
	ctx := context.Background()
	pool := newBackupListHandlerTestDB(t)
	h := newBackupListHandler(pool)

	sgcA := seedBackupListSGC(t, ctx, pool, "a")
	sgcB := seedBackupListSGC(t, ctx, pool, "b")
	volA := seedBackupListVolume(t, ctx, pool, sgcA.configID, "vol-a")
	volB := seedBackupListVolume(t, ctx, pool, sgcB.configID, "vol-b")
	cfgA := seedBackupListConfig(t, ctx, pool, volA)
	cfgB := seedBackupListConfig(t, ctx, pool, volB)
	now := time.Now().UTC().Truncate(time.Second)

	b1 := insertBackupListRow(t, ctx, pool, backupSeed{sgc: sgcA, volumeID: &volA, backupConfigID: &cfgA, status: manman.BackupStatusCompleted, createdAt: now})
	b2 := insertBackupListRow(t, ctx, pool, backupSeed{sgc: sgcA, volumeID: &volB, backupConfigID: &cfgB, status: manman.BackupStatusFailed, createdAt: now.Add(time.Second)})
	b3 := insertBackupListRow(t, ctx, pool, backupSeed{sgc: sgcB, volumeID: &volA, backupConfigID: &cfgA, status: manman.BackupStatusCompleted, createdAt: now.Add(2 * time.Second)})
	b4 := insertBackupListRow(t, ctx, pool, backupSeed{sgc: sgcB, volumeID: &volB, backupConfigID: &cfgB, status: manman.BackupStatusFailed, createdAt: now.Add(3 * time.Second)})

	assertBackupIDs := func(t *testing.T, req *pb.ListBackupsRequest, want []int64) {
		t.Helper()
		resp, err := h.ListBackups(ctx, req)
		if err != nil {
			t.Fatalf("ListBackups(%+v): %v", req, err)
		}
		got := make(map[int64]bool, len(resp.Backups))
		for _, b := range resp.Backups {
			got[b.BackupId] = true
		}
		if len(got) != len(want) {
			t.Fatalf("ListBackups(%+v): got %d rows, want %d (%v)", req, len(got), len(want), want)
		}
		for _, id := range want {
			if !got[id] {
				t.Errorf("ListBackups(%+v): expected backup_id %d in result, got %v", req, id, got)
			}
		}
	}

	assertBackupIDs(t, &pb.ListBackupsRequest{ServerGameConfigId: sgcA.sgcID}, []int64{b1, b2})
	assertBackupIDs(t, &pb.ListBackupsRequest{VolumeId: volA}, []int64{b1, b3})
	assertBackupIDs(t, &pb.ListBackupsRequest{BackupConfigId: cfgB}, []int64{b2, b4})
	assertBackupIDs(t, &pb.ListBackupsRequest{Status: manman.BackupStatusCompleted}, []int64{b1, b3})
	assertBackupIDs(t, &pb.ListBackupsRequest{ServerGameConfigId: sgcA.sgcID, Status: manman.BackupStatusCompleted}, []int64{b1})
}

// TestListBackups_RejectsUnknownStatus proves an unrecognized status filter
// is rejected with InvalidArgument rather than silently matching nothing
// (or everything).
func TestListBackups_RejectsUnknownStatus(t *testing.T) {
	ctx := context.Background()
	pool := newBackupListHandlerTestDB(t)
	h := newBackupListHandler(pool)

	_, err := h.ListBackups(ctx, &pb.ListBackupsRequest{Status: "not-a-real-status"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for an unknown status, got %v", err)
	}
}

// TestListBackups_PaginationAndSoftDelete is the pagination + soft-delete
// green case: with N+1 matching rows and page_size = N, the first page
// returns N rows plus a non-empty next_page_token; following it returns the
// remainder and an empty token; a soft-deleted row is never returned on
// either page.
func TestListBackups_PaginationAndSoftDelete(t *testing.T) {
	ctx := context.Background()
	pool := newBackupListHandlerTestDB(t)
	h := newBackupListHandler(pool)

	sgc := seedBackupListSGC(t, ctx, pool, "a")
	now := time.Now().UTC().Truncate(time.Second)

	const n = 3
	var ids []int64
	for i := 0; i < n+1; i++ {
		ids = append(ids, insertBackupListRow(t, ctx, pool, backupSeed{
			sgc: sgc, status: manman.BackupStatusCompleted, createdAt: now.Add(time.Duration(i) * time.Second),
		}))
	}
	// A soft-deleted row, newer than everything else -- must never appear.
	deletedID := insertBackupListRow(t, ctx, pool, backupSeed{
		sgc: sgc, status: manman.BackupStatusCompleted, createdAt: now.Add(time.Duration(n+1) * time.Second),
	})
	if _, err := pool.Exec(ctx, `UPDATE backups SET deleted_at = NOW() WHERE backup_id = $1`, deletedID); err != nil {
		t.Fatalf("soft-delete backup: %v", err)
	}

	page1, err := h.ListBackups(ctx, &pb.ListBackupsRequest{ServerGameConfigId: sgc.sgcID, PageSize: n})
	if err != nil {
		t.Fatalf("ListBackups page 1: %v", err)
	}
	if len(page1.Backups) != n {
		t.Fatalf("page 1: expected %d rows, got %d", n, len(page1.Backups))
	}
	if page1.NextPageToken == "" {
		t.Fatalf("page 1: expected a non-empty next_page_token")
	}

	page2, err := h.ListBackups(ctx, &pb.ListBackupsRequest{ServerGameConfigId: sgc.sgcID, PageSize: n, PageToken: page1.NextPageToken})
	if err != nil {
		t.Fatalf("ListBackups page 2: %v", err)
	}
	if len(page2.Backups) != 1 {
		t.Fatalf("page 2: expected 1 remaining row, got %d", len(page2.Backups))
	}
	if page2.NextPageToken != "" {
		t.Fatalf("page 2: expected an empty next_page_token, got %q", page2.NextPageToken)
	}

	seen := map[int64]bool{}
	for _, b := range append(page1.Backups, page2.Backups...) {
		seen[b.BackupId] = true
	}
	if len(seen) != n+1 {
		t.Fatalf("expected exactly %d distinct non-deleted rows across both pages, got %d", n+1, len(seen))
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("expected backup_id %d to appear across the two pages", id)
		}
	}
	if seen[deletedID] {
		t.Errorf("soft-deleted backup_id %d must never appear in a page", deletedID)
	}
}

// TestListBackups_ItemsMatchBackupsCountAndOrderWithDisplayNames proves
// `items` carries the same count and order as the legacy `backups` array,
// with each item's display names populated from the joined
// SGC/GameConfig/volume/server rows.
func TestListBackups_ItemsMatchBackupsCountAndOrderWithDisplayNames(t *testing.T) {
	ctx := context.Background()
	pool := newBackupListHandlerTestDB(t)
	h := newBackupListHandler(pool)

	sgcA := seedBackupListSGC(t, ctx, pool, "alpha")
	sgcB := seedBackupListSGC(t, ctx, pool, "beta")
	volA := seedBackupListVolume(t, ctx, pool, sgcA.configID, "vol-alpha")
	now := time.Now().UTC().Truncate(time.Second)

	insertBackupListRow(t, ctx, pool, backupSeed{sgc: sgcA, volumeID: &volA, status: manman.BackupStatusCompleted, createdAt: now})
	insertBackupListRow(t, ctx, pool, backupSeed{sgc: sgcB, status: manman.BackupStatusCompleted, createdAt: now.Add(time.Second)})

	resp, err := h.ListBackups(ctx, &pb.ListBackupsRequest{})
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(resp.Items) != len(resp.Backups) {
		t.Fatalf("items count %d does not match backups count %d", len(resp.Items), len(resp.Backups))
	}
	for i, item := range resp.Items {
		if item.Backup.BackupId != resp.Backups[i].BackupId {
			t.Fatalf("position %d: items[].backup.backup_id=%d does not match backups[]=%d", i, item.Backup.BackupId, resp.Backups[i].BackupId)
		}
	}
	// Newest first: sgcB's row (created a second later) comes before sgcA's.
	if resp.Items[0].ServerGameConfigName != "server-beta / config-beta" {
		t.Errorf("expected first item's server_game_config_name %q, got %q", "server-beta / config-beta", resp.Items[0].ServerGameConfigName)
	}
	if resp.Items[0].GameConfigName != "config-beta" {
		t.Errorf("expected first item's game_config_name %q, got %q", "config-beta", resp.Items[0].GameConfigName)
	}
	if resp.Items[0].ServerName != "server-beta" {
		t.Errorf("expected first item's server_name %q, got %q", "server-beta", resp.Items[0].ServerName)
	}
	if resp.Items[1].VolumeName != "vol-alpha" {
		t.Errorf("expected second item's volume_name %q, got %q", "vol-alpha", resp.Items[1].VolumeName)
	}
	if resp.Items[0].VolumeName != "" {
		t.Errorf("expected first item's volume_name empty (no volume_id set), got %q", resp.Items[0].VolumeName)
	}
}
