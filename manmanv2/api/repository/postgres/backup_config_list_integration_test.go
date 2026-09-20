//go:build integration

// Real-Postgres coverage for FR9/FR10 (task #2810, plan #2777):
// BackupConfigRepository.List must answer fleet-wide (no filter), narrow
// correctly on each of volume/GameConfig/enabled, keep the existing
// per-volume call byte-identical to the pre-widening behavior, join in
// display names for a list row without a follow-up RPC, exclude
// soft-deleted rows, and paginate deterministically. Same hand-written,
// build-tagged precedent as the other integration targets in this package.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:backup_config_list_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/manmanv2/api/repository"
)

// backupConfigListSchema is self-contained DDL for the tables
// BackupConfigRepository.List joins across: games -> game_configs ->
// game_config_volumes -> backup_configs.
const backupConfigListSchema = `
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

	CREATE TABLE game_config_volumes (
		volume_id BIGSERIAL PRIMARY KEY,
		config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
		name VARCHAR(100) NOT NULL,
		container_path TEXT NOT NULL,
		host_subpath TEXT
	);

	CREATE TABLE backup_configs (
		backup_config_id BIGSERIAL PRIMARY KEY,
		volume_id BIGINT NOT NULL REFERENCES game_config_volumes(volume_id) ON DELETE CASCADE,
		cadence_minutes INT NOT NULL CHECK (cadence_minutes > 0),
		backup_path TEXT NOT NULL,
		enabled BOOLEAN NOT NULL DEFAULT true,
		last_backup_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		deleted_at TIMESTAMP
	);
`

func mustQueryRow(t *testing.T, pool *pgxpool.Pool, query string, dest []any, args ...any) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), query, args...).Scan(dest...); err != nil {
		t.Fatalf("seed query %q: %v", query, err)
	}
}

func newBackupConfigListRepo(t *testing.T) (*BackupConfigRepository, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: backupConfigListSchema})
	return &BackupConfigRepository{db: db.Pool}, db.Pool
}

// seededFleet is two games, each with one GameConfig and one volume, so
// tests can prove "fleet-wide" spans more than one GameConfig/volume and
// exercise the game_config_id / volume_id filters independently.
type seededFleet struct {
	gameConfigA, gameConfigB int64
	volumeA, volumeB         int64
}

func seedBackupConfigFleet(t *testing.T, pool *pgxpool.Pool) seededFleet {
	t.Helper()

	var gameA, gameB int64
	mustQueryRow(t, pool, `INSERT INTO games (name) VALUES ('Game A') RETURNING game_id`, []any{&gameA})
	mustQueryRow(t, pool, `INSERT INTO games (name) VALUES ('Game B') RETURNING game_id`, []any{&gameB})

	var f seededFleet
	mustQueryRow(t, pool, `INSERT INTO game_configs (game_id, name, image) VALUES ($1, 'Config A', 'img-a') RETURNING config_id`, []any{&f.gameConfigA}, gameA)
	mustQueryRow(t, pool, `INSERT INTO game_configs (game_id, name, image) VALUES ($1, 'Config B', 'img-b') RETURNING config_id`, []any{&f.gameConfigB}, gameB)

	mustQueryRow(t, pool, `INSERT INTO game_config_volumes (config_id, name, container_path) VALUES ($1, 'saves-a', '/data') RETURNING volume_id`, []any{&f.volumeA}, f.gameConfigA)
	mustQueryRow(t, pool, `INSERT INTO game_config_volumes (config_id, name, container_path) VALUES ($1, 'saves-b', '/data') RETURNING volume_id`, []any{&f.volumeB}, f.gameConfigB)

	return f
}

func seedBackupConfig(t *testing.T, pool *pgxpool.Pool, volumeID int64, enabled bool) int64 {
	t.Helper()
	var id int64
	mustQueryRow(t, pool, `
		INSERT INTO backup_configs (volume_id, cadence_minutes, backup_path, enabled)
		VALUES ($1, 30, 'saves', $2) RETURNING backup_config_id`,
		[]any{&id}, volumeID, enabled)
	return id
}

// TestBackupConfigList_NoFilterIsFleetWide proves FR9: no volume_id returns
// BackupConfigs spanning more than one GameConfig/volume, with display
// context populated so a row can be rendered without a follow-up RPC.
func TestBackupConfigList_NoFilterIsFleetWide(t *testing.T) {
	repo, pool := newBackupConfigListRepo(t)
	f := seedBackupConfigFleet(t, pool)
	idA := seedBackupConfig(t, pool, f.volumeA, true)
	idB := seedBackupConfig(t, pool, f.volumeB, true)

	rows, err := repo.List(context.Background(), repository.BackupConfigListFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows fleet-wide, got %d", len(rows))
	}

	byID := map[int64]*repository.BackupConfigListRow{}
	for _, r := range rows {
		byID[r.Config.BackupConfigID] = r
	}
	rowA, rowB := byID[idA], byID[idB]
	if rowA == nil || rowB == nil {
		t.Fatalf("expected rows for both seeded configs, got %+v", rows)
	}
	if rowA.GameConfigID != f.gameConfigA || rowA.GameConfigName != "Config A" || rowA.GameName != "Game A" || rowA.VolumeName != "saves-a" {
		t.Fatalf("row A missing/incorrect display context: %+v", rowA)
	}
	if rowB.GameConfigID != f.gameConfigB || rowB.GameConfigName != "Config B" || rowB.GameName != "Game B" || rowB.VolumeName != "saves-b" {
		t.Fatalf("row B missing/incorrect display context: %+v", rowB)
	}
}

// TestBackupConfigList_VolumeIDFilterMatchesLegacyBehavior guards the
// Config Editor's Volumes tab: filtering by volume_id alone must return
// exactly (and only) that volume's configs, same as the old per-volume List.
func TestBackupConfigList_VolumeIDFilterMatchesLegacyBehavior(t *testing.T) {
	repo, pool := newBackupConfigListRepo(t)
	f := seedBackupConfigFleet(t, pool)
	idA := seedBackupConfig(t, pool, f.volumeA, true)
	seedBackupConfig(t, pool, f.volumeB, true)

	volA := f.volumeA
	rows, err := repo.List(context.Background(), repository.BackupConfigListFilter{VolumeID: &volA}, 50, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Config.BackupConfigID != idA {
		t.Fatalf("expected exactly volume A's config, got %+v", rows)
	}
}

// TestBackupConfigList_GameConfigIDFilterNarrows proves FR10's GameConfig
// filter narrows to only that GameConfig's configs.
func TestBackupConfigList_GameConfigIDFilterNarrows(t *testing.T) {
	repo, pool := newBackupConfigListRepo(t)
	f := seedBackupConfigFleet(t, pool)
	seedBackupConfig(t, pool, f.volumeA, true)
	idB := seedBackupConfig(t, pool, f.volumeB, true)

	gcB := f.gameConfigB
	rows, err := repo.List(context.Background(), repository.BackupConfigListFilter{GameConfigID: &gcB}, 50, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Config.BackupConfigID != idB {
		t.Fatalf("expected exactly GameConfig B's config, got %+v", rows)
	}
}

// TestBackupConfigList_EnabledFilterDistinguishesFalseFromUnset is the FR10
// red/green case: bc.enabled = $3 must apply when Enabled is non-nil, and
// enabled=false must return only disabled configs (never conflated with
// the unset/no-filter case).
func TestBackupConfigList_EnabledFilterDistinguishesFalseFromUnset(t *testing.T) {
	repo, pool := newBackupConfigListRepo(t)
	f := seedBackupConfigFleet(t, pool)
	enabledID := seedBackupConfig(t, pool, f.volumeA, true)
	disabledID := seedBackupConfig(t, pool, f.volumeB, false)

	// Unset: both returned.
	rows, err := repo.List(context.Background(), repository.BackupConfigListFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("List (unset): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both configs with enabled unset, got %d", len(rows))
	}

	// enabled = false: only the disabled one.
	no := false
	rows, err = repo.List(context.Background(), repository.BackupConfigListFilter{Enabled: &no}, 50, 0)
	if err != nil {
		t.Fatalf("List (enabled=false): %v", err)
	}
	if len(rows) != 1 || rows[0].Config.BackupConfigID != disabledID {
		t.Fatalf("expected only the disabled config, got %+v", rows)
	}

	// enabled = true: only the enabled one.
	yes := true
	rows, err = repo.List(context.Background(), repository.BackupConfigListFilter{Enabled: &yes}, 50, 0)
	if err != nil {
		t.Fatalf("List (enabled=true): %v", err)
	}
	if len(rows) != 1 || rows[0].Config.BackupConfigID != enabledID {
		t.Fatalf("expected only the enabled config, got %+v", rows)
	}
}

// TestBackupConfigList_ExcludesSoftDeleted proves a soft-deleted config
// never appears, filtered or not.
func TestBackupConfigList_ExcludesSoftDeleted(t *testing.T) {
	repo, pool := newBackupConfigListRepo(t)
	f := seedBackupConfigFleet(t, pool)
	liveID := seedBackupConfig(t, pool, f.volumeA, true)
	deletedID := seedBackupConfig(t, pool, f.volumeA, true)

	if _, err := pool.Exec(context.Background(),
		`UPDATE backup_configs SET deleted_at = NOW() WHERE backup_config_id = $1`, deletedID); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	rows, err := repo.List(context.Background(), repository.BackupConfigListFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Config.BackupConfigID != liveID {
		t.Fatalf("expected only the live config, got %+v", rows)
	}
}

// TestBackupConfigList_PaginationIsDeterministic proves ORDER BY
// backup_config_id + LIMIT/OFFSET pages consistently with no gaps/overlaps.
func TestBackupConfigList_PaginationIsDeterministic(t *testing.T) {
	repo, pool := newBackupConfigListRepo(t)
	f := seedBackupConfigFleet(t, pool)
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, seedBackupConfig(t, pool, f.volumeA, true))
	}

	page1, err := repo.List(context.Background(), repository.BackupConfigListFilter{}, 2, 0)
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	page2, err := repo.List(context.Background(), repository.BackupConfigListFilter{}, 2, 2)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	page3, err := repo.List(context.Background(), repository.BackupConfigListFilter{}, 2, 4)
	if err != nil {
		t.Fatalf("List page3 (last, partial): %v", err)
	}

	if len(page1) != 2 || len(page2) != 2 || len(page3) != 1 {
		t.Fatalf("unexpected page sizes: %d, %d, %d", len(page1), len(page2), len(page3))
	}
	var got []int64
	for _, p := range [][]*repository.BackupConfigListRow{page1, page2, page3} {
		for _, r := range p {
			got = append(got, r.Config.BackupConfigID)
		}
	}
	if len(got) != len(ids) {
		t.Fatalf("pagination lost/duplicated rows: want %v, got %v", ids, got)
	}
	for i, id := range ids {
		if got[i] != id {
			t.Fatalf("pagination order mismatch at %d: want %d, got %d (full: %v vs %v)", i, id, got[i], ids, got)
		}
	}
}
