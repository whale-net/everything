//go:build integration

// Real-Postgres coverage for ConfigurationPatchRepository, replaying the real
// shipped migrations via //manmanv2/migrate/schema rather than hand-written
// DDL.
//
// The behaviour worth knowing: migration 033 dropped the
// one-patch-per-(strategy, level, entity) unique constraint, so a single
// entity legitimately carries many patches, and every read path orders them
// by (patch_order, patch_id). GetByStrategyAndEntity is therefore "the first
// one", not "the one" -- getting that wrong silently applies the wrong
// configuration.
//
// entity_id is a bare BIGINT with no FK (it is polymorphic: a config_id,
// sgc_id, or session_id depending on patch_level), so these tests use real
// ids from the matching table where the level calls for it.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:configuration_patch_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	manman "github.com/whale-net/everything/manmanv2/models"
)

type patchFixture struct {
	repo          *ConfigurationPatchRepository
	pool          *pgxpool.Pool
	strategyA     int64
	strategyB     int64
	gameConfigID  int64
	otherConfigID int64
}

func newPatchHarness(t *testing.T) patchFixture {
	t.Helper()
	pool := newMigratedPool(t)
	ctx := context.Background()

	game, err := NewGameRepository(pool).Create(ctx,
		&manman.Game{Name: "patch-game", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("seed game: %v", err)
	}

	// configuration_patches.strategy_id is a real FK, so the strategies
	// must exist. Two of them, so the (strategy, level, entity) scoping can
	// be told apart from a missing filter.
	strategies := NewConfigurationStrategyRepository(pool)
	strategyA, err := strategies.Create(ctx, &manman.ConfigurationStrategy{
		GameID:       game.GameID,
		Name:         "patch-strategy-a",
		StrategyType: "file_properties",
	})
	if err != nil {
		t.Fatalf("seed strategy a: %v", err)
	}
	strategyB, err := strategies.Create(ctx, &manman.ConfigurationStrategy{
		GameID:       game.GameID,
		Name:         "patch-strategy-b",
		StrategyType: "file_properties",
	})
	if err != nil {
		t.Fatalf("seed strategy b: %v", err)
	}

	configs := NewGameConfigRepository(pool)
	gameConfigID, err := configs.Create(ctx, &manman.GameConfig{
		GameID: game.GameID, Name: "patch-config-a", Image: "example:latest",
	})
	if err != nil {
		t.Fatalf("seed game config a: %v", err)
	}
	otherConfigID, err := configs.Create(ctx, &manman.GameConfig{
		GameID: game.GameID, Name: "patch-config-b", Image: "example:latest",
	})
	if err != nil {
		t.Fatalf("seed game config b: %v", err)
	}

	return patchFixture{
		repo:          NewConfigurationPatchRepository(pool),
		pool:          pool,
		strategyA:     strategyA.StrategyID,
		strategyB:     strategyB.StrategyID,
		gameConfigID:  gameConfigID.ConfigID,
		otherConfigID: otherConfigID.ConfigID,
	}
}

func (f patchFixture) seed(t *testing.T, strategyID int64, level string, entityID int64, content string, order int) *manman.ConfigurationPatch {
	t.Helper()
	p, err := f.repo.Create(context.Background(), &manman.ConfigurationPatch{
		StrategyID:   strategyID,
		PatchLevel:   level,
		EntityID:     entityID,
		PatchContent: strPtr(content),
		PatchFormat:  "json_merge_patch",
		PatchOrder:   order,
	})
	if err != nil {
		t.Fatalf("seed patch (strategy=%d level=%s entity=%d): %v", strategyID, level, entityID, err)
	}
	return p
}

// TestConfigurationPatchRepository_CreateGetRoundTrip proves Create assigns a
// patch_id and both timestamps, and Get returns every stored field including
// the nullable volume_id and path_override.
func TestConfigurationPatchRepository_CreateGetRoundTrip(t *testing.T) {
	f := newPatchHarness(t)
	ctx := context.Background()

	volume, err := NewGameConfigVolumeRepository(f.pool).Create(ctx, &manman.GameConfigVolume{
		ConfigID:      f.gameConfigID,
		Name:          "patched-volume",
		ContainerPath: "/data/patched",
		VolumeType:    "bind",
	})
	if err != nil {
		t.Fatalf("seed volume: %v", err)
	}

	created, err := f.repo.Create(ctx, &manman.ConfigurationPatch{
		StrategyID:   f.strategyA,
		PatchLevel:   "game_config",
		EntityID:     f.gameConfigID,
		PatchContent: strPtr(`{"motd":"hello"}`),
		PatchFormat:  "json_merge_patch",
		VolumeID:     &volume.VolumeID,
		PathOverride: strPtr("override/path"),
		PatchOrder:   7,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.PatchID == 0 {
		t.Fatal("Create did not populate PatchID")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Errorf("Create returned zero timestamps: created_at=%v updated_at=%v", created.CreatedAt, created.UpdatedAt)
	}

	got, err := f.repo.Get(ctx, created.PatchID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.StrategyID != f.strategyA {
		t.Errorf("StrategyID = %d, want %d", got.StrategyID, f.strategyA)
	}
	if got.PatchLevel != "game_config" {
		t.Errorf("PatchLevel = %q, want %q", got.PatchLevel, "game_config")
	}
	if got.EntityID != f.gameConfigID {
		t.Errorf("EntityID = %d, want %d", got.EntityID, f.gameConfigID)
	}
	if got.PatchContent == nil || *got.PatchContent != `{"motd":"hello"}` {
		t.Errorf("PatchContent = %v, want %q", got.PatchContent, `{"motd":"hello"}`)
	}
	if got.PatchFormat != "json_merge_patch" {
		t.Errorf("PatchFormat = %q, want %q", got.PatchFormat, "json_merge_patch")
	}
	if got.VolumeID == nil || *got.VolumeID != volume.VolumeID {
		t.Errorf("VolumeID = %v, want %d", got.VolumeID, volume.VolumeID)
	}
	if got.PathOverride == nil || *got.PathOverride != "override/path" {
		t.Errorf("PathOverride = %v, want %q", got.PathOverride, "override/path")
	}
	if got.PatchOrder != 7 {
		t.Errorf("PatchOrder = %d, want 7", got.PatchOrder)
	}
}

// TestConfigurationPatchRepository_GetUnknownIDErrors proves Get on a missing
// patch_id errors.
func TestConfigurationPatchRepository_GetUnknownIDErrors(t *testing.T) {
	f := newPatchHarness(t)

	if _, err := f.repo.Get(context.Background(), 999999); err == nil {
		t.Fatal("Get on an unknown patch_id returned no error")
	}
}

// TestConfigurationPatchRepository_MultiplePatchesPerEntityAllowed proves
// migration 033's dropped unique constraint: a second patch on the same
// (strategy, level, entity) is accepted, not rejected.
func TestConfigurationPatchRepository_MultiplePatchesPerEntityAllowed(t *testing.T) {
	f := newPatchHarness(t)

	first := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "one", 1)
	second := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "two", 2)

	if first.PatchID == second.PatchID {
		t.Fatal("the two patches share a patch_id")
	}

	all, err := f.repo.ListByStrategyAndEntity(context.Background(), f.strategyA, "game_config", f.gameConfigID)
	if err != nil {
		t.Fatalf("ListByStrategyAndEntity: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("returned %d patches, want 2 (one-per-entity must no longer be enforced)", len(all))
	}
}

// TestConfigurationPatchRepository_ListByStrategyAndEntityOrdersByPatchOrder
// proves the read path sorts by (patch_order, patch_id) -- and that
// GetByStrategyAndEntity returns the first of them, not an arbitrary one.
func TestConfigurationPatchRepository_ListByStrategyAndEntityOrdersByPatchOrder(t *testing.T) {
	f := newPatchHarness(t)
	ctx := context.Background()

	// Insert out of order, and give the middle one the lowest patch_order.
	third := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "third", 30)
	first := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "first", 10)
	second := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "second", 20)

	listed, err := f.repo.ListByStrategyAndEntity(ctx, f.strategyA, "game_config", f.gameConfigID)
	if err != nil {
		t.Fatalf("ListByStrategyAndEntity: %v", err)
	}
	got := patchIDs(listed)
	want := []int64{first.PatchID, second.PatchID, third.PatchID}
	if len(got) != len(want) {
		t.Fatalf("returned %d patches (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (patch_order ASC)", got, want)
		}
	}

	// GetByStrategyAndEntity is LIMIT 1 over that same ordering, so it must
	// return the lowest patch_order.
	one, err := f.repo.GetByStrategyAndEntity(ctx, f.strategyA, "game_config", f.gameConfigID)
	if err != nil {
		t.Fatalf("GetByStrategyAndEntity: %v", err)
	}
	if one.PatchID != first.PatchID {
		t.Errorf("GetByStrategyAndEntity returned patch %d, want %d (lowest patch_order)", one.PatchID, first.PatchID)
	}
}

// TestConfigurationPatchRepository_ScopingDoesNotLeak proves every reader is
// scoped by the full (strategy, patch_level, entity_id) triple: a matching
// entity under a different strategy, level, or entity id must not appear.
func TestConfigurationPatchRepository_ScopingDoesNotLeak(t *testing.T) {
	f := newPatchHarness(t)
	ctx := context.Background()

	mine := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "mine", 1)
	// Same level and entity, different strategy.
	f.seed(t, f.strategyB, "game_config", f.gameConfigID, "other-strategy", 1)
	// Same strategy and entity, different level.
	f.seed(t, f.strategyA, "server_game_config", f.gameConfigID, "other-level", 1)
	// Same strategy and level, different entity.
	f.seed(t, f.strategyA, "game_config", f.otherConfigID, "other-entity", 1)

	listed, err := f.repo.ListByStrategyAndEntity(ctx, f.strategyA, "game_config", f.gameConfigID)
	if err != nil {
		t.Fatalf("ListByStrategyAndEntity: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("returned %d patches, want 1: %v", len(listed), patchContents(listed))
	}
	if listed[0].PatchID != mine.PatchID {
		t.Errorf("returned patch %d, want %d", listed[0].PatchID, mine.PatchID)
	}

	one, err := f.repo.GetByStrategyAndEntity(ctx, f.strategyA, "game_config", f.gameConfigID)
	if err != nil {
		t.Fatalf("GetByStrategyAndEntity: %v", err)
	}
	if one.PatchID != mine.PatchID {
		t.Errorf("GetByStrategyAndEntity returned patch %d, want %d", one.PatchID, mine.PatchID)
	}
}

// TestConfigurationPatchRepository_ListOptionalFilters proves each of List's
// three nilable arguments filters independently, and that all-nil returns
// everything.
func TestConfigurationPatchRepository_ListOptionalFilters(t *testing.T) {
	f := newPatchHarness(t)
	ctx := context.Background()

	a := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "a", 1)
	b := f.seed(t, f.strategyB, "game_config", f.gameConfigID, "b", 2)
	c := f.seed(t, f.strategyA, "server_game_config", f.gameConfigID, "c", 3)
	f.seed(t, f.strategyA, "game_config", f.otherConfigID, "d", 4)

	t.Run("no filters returns all", func(t *testing.T) {
		all, err := f.repo.List(ctx, nil, nil, nil)
		if err != nil {
			t.Fatalf("List(nil,nil,nil): %v", err)
		}
		if len(all) != 4 {
			t.Errorf("returned %d patches, want 4", len(all))
		}
	})

	t.Run("strategyID filters", func(t *testing.T) {
		byStrategy, err := f.repo.List(ctx, &f.strategyA, nil, nil)
		if err != nil {
			t.Fatalf("List(strategyA): %v", err)
		}
		if len(byStrategy) != 3 {
			t.Fatalf("returned %d patches, want 3", len(byStrategy))
		}
		for _, p := range byStrategy {
			if p.StrategyID != f.strategyA {
				t.Errorf("patch %d has strategy %d, want %d", p.PatchID, p.StrategyID, f.strategyA)
			}
		}
	})

	t.Run("patchLevel filters", func(t *testing.T) {
		level := "server_game_config"
		byLevel, err := f.repo.List(ctx, nil, &level, nil)
		if err != nil {
			t.Fatalf("List(level): %v", err)
		}
		if len(byLevel) != 1 {
			t.Fatalf("returned %d patches, want 1", len(byLevel))
		}
		if byLevel[0].PatchID != c.PatchID {
			t.Errorf("returned patch %d, want %d", byLevel[0].PatchID, c.PatchID)
		}
	})

	t.Run("entityID filters", func(t *testing.T) {
		byEntity, err := f.repo.List(ctx, nil, nil, &f.gameConfigID)
		if err != nil {
			t.Fatalf("List(entity): %v", err)
		}
		if len(byEntity) != 3 {
			t.Fatalf("returned %d patches, want 3", len(byEntity))
		}
		for _, p := range byEntity {
			if p.EntityID != f.gameConfigID {
				t.Errorf("patch %d has entity %d, want %d", p.PatchID, p.EntityID, f.gameConfigID)
			}
		}
	})

	t.Run("combined filters intersect", func(t *testing.T) {
		level := "game_config"
		both, err := f.repo.List(ctx, &f.strategyA, &level, &f.gameConfigID)
		if err != nil {
			t.Fatalf("List(all three): %v", err)
		}
		if len(both) != 1 {
			t.Fatalf("returned %d patches, want 1", len(both))
		}
		if both[0].PatchID != a.PatchID {
			t.Errorf("returned patch %d, want %d", both[0].PatchID, a.PatchID)
		}
	})

	_ = b
}

// TestConfigurationPatchRepository_UpdateBumpsUpdatedAtAndKeepsScope proves
// Update rewrites only the mutable columns, advances updated_at, and leaves
// strategy_id/patch_level/entity_id -- the scope -- untouched.
func TestConfigurationPatchRepository_UpdateBumpsUpdatedAtAndKeepsScope(t *testing.T) {
	f := newPatchHarness(t)
	ctx := context.Background()

	patch := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "before", 1)
	before, err := f.repo.Get(ctx, patch.PatchID)
	if err != nil {
		t.Fatalf("Get before Update: %v", err)
	}

	volume, err := NewGameConfigVolumeRepository(f.pool).Create(ctx, &manman.GameConfigVolume{
		ConfigID:      f.gameConfigID,
		Name:          "update-volume",
		ContainerPath: "/data/update",
		VolumeType:    "bind",
	})
	if err != nil {
		t.Fatalf("seed volume: %v", err)
	}

	patch.PatchContent = strPtr("after")
	patch.PatchFormat = "yaml_merge"
	patch.VolumeID = &volume.VolumeID
	patch.PathOverride = strPtr("new/path")
	patch.PatchOrder = 42
	// Scope fields -- all three must be ignored by Update.
	patch.StrategyID = f.strategyB
	patch.PatchLevel = "session"
	patch.EntityID = f.otherConfigID

	if err := f.repo.Update(ctx, patch); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := f.repo.Get(ctx, patch.PatchID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if after.StrategyID != f.strategyA {
		t.Errorf("StrategyID = %d, want %d (Update must not re-scope)", after.StrategyID, f.strategyA)
	}
	if after.PatchLevel != "game_config" {
		t.Errorf("PatchLevel = %q, want %q (Update must not re-scope)", after.PatchLevel, "game_config")
	}
	if after.EntityID != f.gameConfigID {
		t.Errorf("EntityID = %d, want %d (Update must not re-scope)", after.EntityID, f.gameConfigID)
	}
	if after.PatchContent == nil || *after.PatchContent != "after" {
		t.Errorf("PatchContent = %v, want %q", after.PatchContent, "after")
	}
	if after.PatchFormat != "yaml_merge" {
		t.Errorf("PatchFormat = %q, want %q", after.PatchFormat, "yaml_merge")
	}
	if after.VolumeID == nil || *after.VolumeID != volume.VolumeID {
		t.Errorf("VolumeID = %v, want %d", after.VolumeID, volume.VolumeID)
	}
	if after.PathOverride == nil || *after.PathOverride != "new/path" {
		t.Errorf("PathOverride = %v, want %q", after.PathOverride, "new/path")
	}
	if after.PatchOrder != 42 {
		t.Errorf("PatchOrder = %d, want 42", after.PatchOrder)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want after %v (Update sets CURRENT_TIMESTAMP)", after.UpdatedAt, before.UpdatedAt)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("CreatedAt = %v, want unchanged %v", after.CreatedAt, before.CreatedAt)
	}
}

// TestConfigurationPatchRepository_DeleteRemovesRow proves Delete is a hard
// delete, and that a strategy's patches cascade away with their strategy.
func TestConfigurationPatchRepository_DeleteRemovesRow(t *testing.T) {
	f := newPatchHarness(t)
	ctx := context.Background()

	patch := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "doomed", 1)

	if err := f.repo.Delete(ctx, patch.PatchID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.repo.Get(ctx, patch.PatchID); err == nil {
		t.Fatal("Get succeeded after Delete, want an error")
	}
}

// TestConfigurationPatchRepository_DeleteCascadesFromStrategy proves the
// strategy_id FK is ON DELETE CASCADE, so destroying a strategy takes its
// patches with it rather than orphaning them.
func TestConfigurationPatchRepository_DeleteCascadesFromStrategy(t *testing.T) {
	f := newPatchHarness(t)
	ctx := context.Background()

	patch := f.seed(t, f.strategyA, "game_config", f.gameConfigID, "cascade", 1)

	if err := NewConfigurationStrategyRepository(f.pool).Delete(ctx, f.strategyA); err != nil {
		t.Fatalf("delete strategy: %v", err)
	}

	if _, err := f.repo.Get(ctx, patch.PatchID); err == nil {
		t.Error("the patch survived its strategy's deletion, want ON DELETE CASCADE")
	}
}

// TestConfigurationPatchRepository_CreateUnknownStrategyFails proves the FK
// from configuration_patches.strategy_id is enforced.
func TestConfigurationPatchRepository_CreateUnknownStrategyFails(t *testing.T) {
	f := newPatchHarness(t)

	_, err := f.repo.Create(context.Background(), &manman.ConfigurationPatch{
		StrategyID:   999999,
		PatchLevel:   "game_config",
		EntityID:     f.gameConfigID,
		PatchContent: strPtr("orphan"),
	})
	if err == nil {
		t.Fatal("Create with a nonexistent strategy_id succeeded, want an FK violation")
	}
}

// TestConfigurationPatchRepository_CreateInvalidLevelFails proves the real
// migration's patch_level CHECK -- game_config, server_game_config, session --
// is enforced.
func TestConfigurationPatchRepository_CreateInvalidLevelFails(t *testing.T) {
	f := newPatchHarness(t)

	_, err := f.repo.Create(context.Background(), &manman.ConfigurationPatch{
		StrategyID:   f.strategyA,
		PatchLevel:   "not_a_real_level",
		EntityID:     f.gameConfigID,
		PatchContent: strPtr("nope"),
	})
	if err == nil {
		t.Fatal("Create with an invalid patch_level succeeded, want a CHECK violation")
	}
}

var _ = time.Time{}

func patchIDs(patches []*manman.ConfigurationPatch) []int64 {
	ids := make([]int64, len(patches))
	for i, p := range patches {
		ids[i] = p.PatchID
	}
	return ids
}

func patchContents(patches []*manman.ConfigurationPatch) []string {
	out := make([]string, len(patches))
	for i, p := range patches {
		if p.PatchContent == nil {
			out[i] = "<nil>"
			continue
		}
		out[i] = *p.PatchContent
	}
	return out
}
