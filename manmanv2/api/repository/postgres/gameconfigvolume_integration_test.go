//go:build integration

// Real-Postgres coverage for GameConfigVolumeRepository -- the volume mounts
// attached to a game config. Replays the real shipped migrations via
// //manmanv2/migrate/schema rather than hand-written DDL, so the volume_type
// CHECK constraint in particular is exercised as actually shipped.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:gameconfigvolume_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	manman "github.com/whale-net/everything/manmanv2/models"
)

type volumeFixture struct {
	repo             *GameConfigVolumeRepository
	pool             *pgxpool.Pool
	configA, configB int64
}

func newVolumeHarness(t *testing.T) volumeFixture {
	t.Helper()
	pool := newMigratedPool(t)
	ctx := context.Background()

	game, err := NewGameRepository(pool).Create(ctx,
		&manman.Game{Name: "volume-game", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("seed game: %v", err)
	}

	configs := NewGameConfigRepository(pool)
	// Two configs so ListByGameConfig's filter is meaningful.
	var configA, configB int64
	for i, name := range []string{"vol-config-a", "vol-config-b"} {
		cfg, err := configs.Create(ctx, &manman.GameConfig{
			GameID: game.GameID,
			Name:   name,
			Image:  "example:latest",
		})
		if err != nil {
			t.Fatalf("seed config %d: %v", i, err)
		}
		if i == 0 {
			configA = cfg.ConfigID
		} else {
			configB = cfg.ConfigID
		}
	}

	return volumeFixture{
		repo:    NewGameConfigVolumeRepository(pool),
		pool:    pool,
		configA: configA,
		configB: configB,
	}
}

func (f volumeFixture) seed(t *testing.T, configID int64, name, volumeType string) *manman.GameConfigVolume {
	t.Helper()
	v, err := f.repo.Create(context.Background(), &manman.GameConfigVolume{
		ConfigID:      configID,
		Name:          name,
		Description:   strPtr(name + " description"),
		ContainerPath: "/data/" + name,
		HostSubpath:   strPtr("host/" + name),
		ReadOnly:      false,
		VolumeType:    volumeType,
	})
	if err != nil {
		t.Fatalf("seed volume %q: %v", name, err)
	}
	return v
}

// TestGameConfigVolumeRepository_CreateGetRoundTrip proves Create assigns a
// volume_id and a server-side created_at, and that Get returns every stored
// field including the nullable description and host_subpath.
func TestGameConfigVolumeRepository_CreateGetRoundTrip(t *testing.T) {
	f := newVolumeHarness(t)
	ctx := context.Background()

	created, err := f.repo.Create(ctx, &manman.GameConfigVolume{
		ConfigID:      f.configA,
		Name:          "world",
		Description:   strPtr("persistent world data"),
		ContainerPath: "/data/world",
		HostSubpath:   strPtr("instances/default/world"),
		ReadOnly:      false,
		VolumeType:    "bind",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.VolumeID == 0 {
		t.Fatal("Create did not populate VolumeID")
	}
	if created.CreatedAt.IsZero() {
		t.Error("Create did not populate CreatedAt from the RETURNING clause")
	}

	got, err := f.repo.Get(ctx, created.VolumeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ConfigID != f.configA {
		t.Errorf("ConfigID = %d, want %d", got.ConfigID, f.configA)
	}
	if got.Name != "world" {
		t.Errorf("Name = %q, want %q", got.Name, "world")
	}
	if got.Description == nil || *got.Description != "persistent world data" {
		t.Errorf("Description = %v, want %q", got.Description, "persistent world data")
	}
	if got.ContainerPath != "/data/world" {
		t.Errorf("ContainerPath = %q, want %q", got.ContainerPath, "/data/world")
	}
	if got.HostSubpath == nil || *got.HostSubpath != "instances/default/world" {
		t.Errorf("HostSubpath = %v, want %q", got.HostSubpath, "instances/default/world")
	}
	if got.ReadOnly {
		t.Error("ReadOnly = true, want false")
	}
	if got.VolumeType != "bind" {
		t.Errorf("VolumeType = %q, want %q", got.VolumeType, "bind")
	}
}

// TestGameConfigVolumeRepository_GetUnknownIDErrors proves Get on a missing
// volume_id errors.
func TestGameConfigVolumeRepository_GetUnknownIDErrors(t *testing.T) {
	f := newVolumeHarness(t)

	if _, err := f.repo.Get(context.Background(), 999999); err == nil {
		t.Fatal("Get on an unknown volume_id returned no error")
	}
}

// TestGameConfigVolumeRepository_ListByGameConfigFilters proves the filter is
// applied and ordered by volume_id, and that another config's volumes never
// leak in.
func TestGameConfigVolumeRepository_ListByGameConfigFilters(t *testing.T) {
	f := newVolumeHarness(t)
	ctx := context.Background()

	first := f.seed(t, f.configA, "a-world", "bind")
	second := f.seed(t, f.configA, "a-config", "bind")
	f.seed(t, f.configB, "b-world", "bind")

	listed, err := f.repo.ListByGameConfig(ctx, f.configA)
	if err != nil {
		t.Fatalf("ListByGameConfig: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("ListByGameConfig returned %d rows, want 2", len(listed))
	}
	if listed[0].VolumeID != first.VolumeID || listed[1].VolumeID != second.VolumeID {
		t.Errorf("ListByGameConfig = %v, want [%d %d] (ordered by volume_id)",
			volumeIDs(listed), first.VolumeID, second.VolumeID)
	}
	for _, v := range listed {
		if v.ConfigID != f.configA {
			t.Errorf("volume %d belongs to config %d, want %d", v.VolumeID, v.ConfigID, f.configA)
		}
	}

	empty, err := f.repo.ListByGameConfig(ctx, 999999)
	if err != nil {
		t.Fatalf("ListByGameConfig(unknown): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListByGameConfig(unknown) returned %d rows, want 0", len(empty))
	}
}

// TestGameConfigVolumeRepository_UpdateMutatesColumns proves Update rewrites
// the mutable columns but preserves created_at, which its SET list omits.
func TestGameConfigVolumeRepository_UpdateMutatesColumns(t *testing.T) {
	f := newVolumeHarness(t)
	ctx := context.Background()

	vol := f.seed(t, f.configA, "before", "bind")
	originalCreatedAt := vol.CreatedAt

	vol.Name = "after"
	vol.Description = strPtr("updated")
	vol.ContainerPath = "/data/after"
	vol.HostSubpath = strPtr("host/after")
	vol.ReadOnly = true
	vol.VolumeType = "named"
	vol.ConfigID = f.configB // must be ignored

	if err := f.repo.Update(ctx, vol); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := f.repo.Get(ctx, vol.VolumeID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.ConfigID != f.configA {
		t.Errorf("ConfigID = %d, want %d (Update must not re-parent)", got.ConfigID, f.configA)
	}
	if got.Name != "after" || got.ContainerPath != "/data/after" {
		t.Errorf("Name/ContainerPath = %q/%q, want %q/%q", got.Name, got.ContainerPath, "after", "/data/after")
	}
	if !got.ReadOnly {
		t.Error("ReadOnly = false, want true")
	}
	if got.VolumeType != "named" {
		t.Errorf("VolumeType = %q, want %q", got.VolumeType, "named")
	}
	if !got.CreatedAt.Equal(originalCreatedAt) {
		t.Errorf("CreatedAt = %v, want unchanged %v", got.CreatedAt, originalCreatedAt)
	}
}

// TestGameConfigVolumeRepository_DeleteRemovesRow proves Delete is a hard
// delete and that a follow-up Get errors.
func TestGameConfigVolumeRepository_DeleteRemovesRow(t *testing.T) {
	f := newVolumeHarness(t)
	ctx := context.Background()

	vol := f.seed(t, f.configA, "doomed", "bind")

	if err := f.repo.Delete(ctx, vol.VolumeID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.repo.Get(ctx, vol.VolumeID); err == nil {
		t.Fatal("Get succeeded after Delete, want an error")
	}
}

// TestGameConfigVolumeRepository_CreateUnknownConfigFails proves the real
// migration's FK from game_config_volumes.config_id to game_configs.config_id
// is enforced.
func TestGameConfigVolumeRepository_CreateUnknownConfigFails(t *testing.T) {
	f := newVolumeHarness(t)

	_, err := f.repo.Create(context.Background(), &manman.GameConfigVolume{
		ConfigID:      999999,
		Name:          "orphan",
		ContainerPath: "/data/orphan",
		VolumeType:    "bind",
	})
	if err == nil {
		t.Fatal("Create with a nonexistent config_id succeeded, want an FK violation")
	}
}

func volumeIDs(volumes []*manman.GameConfigVolume) []int64 {
	ids := make([]int64, len(volumes))
	for i, v := range volumes {
		ids[i] = v.VolumeID
	}
	return ids
}
