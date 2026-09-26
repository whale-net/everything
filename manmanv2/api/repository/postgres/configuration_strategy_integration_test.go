//go:build integration

// Real-Postgres coverage for ConfigurationStrategyRepository. Replays the
// real shipped migrations via //manmanv2/migrate/schema rather than
// hand-written DDL, so the strategy_type CHECK is exercised as shipped.
//
// The one behaviour worth singling out: ListByGame orders by apply_order
// first, not strategy_id -- strategies are applied in that order, so a
// listing that ignored it would deploy configuration in the wrong sequence.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:configuration_strategy_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	manman "github.com/whale-net/everything/manmanv2/models"
)

type strategyFixture struct {
	repo  *ConfigurationStrategyRepository
	pool  *pgxpool.Pool
	gameA int64
	gameB int64
}

func newStrategyHarness(t *testing.T) strategyFixture {
	t.Helper()
	pool := newMigratedPool(t)
	ctx := context.Background()

	games := NewGameRepository(pool)
	first, err := games.Create(ctx, &manman.Game{Name: "strategy-game-a", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("seed game a: %v", err)
	}
	second, err := games.Create(ctx, &manman.Game{Name: "strategy-game-b", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("seed game b: %v", err)
	}

	return strategyFixture{
		repo:  NewConfigurationStrategyRepository(pool),
		pool:  pool,
		gameA: first.GameID,
		gameB: second.GameID,
	}
}

func (f strategyFixture) seed(t *testing.T, gameID int64, name, strategyType string, applyOrder int) *manman.ConfigurationStrategy {
	t.Helper()
	s, err := f.repo.Create(context.Background(), &manman.ConfigurationStrategy{
		GameID:        gameID,
		Name:          name,
		Description:   strPtr(name + " description"),
		StrategyType:  strategyType,
		TargetPath:    strPtr("configs/" + name + ".json"),
		BaseTemplate:  strPtr(`{"base":true}`),
		RenderOptions: manman.JSONB{"engine": "gotemplate"},
		ApplyOrder:    applyOrder,
	})
	if err != nil {
		t.Fatalf("seed strategy %q: %v", name, err)
	}
	return s
}

// TestConfigurationStrategyRepository_CreateGetRoundTrip proves Create
// assigns a strategy_id and Get returns every stored field, including the
// JSONB render_options and the nullable target_path/base_template.
func TestConfigurationStrategyRepository_CreateGetRoundTrip(t *testing.T) {
	f := newStrategyHarness(t)
	ctx := context.Background()

	created, err := f.repo.Create(ctx, &manman.ConfigurationStrategy{
		GameID:        f.gameA,
		Name:          "server.properties",
		Description:   strPtr("main properties"),
		StrategyType:  "file_properties",
		TargetPath:    strPtr("server.properties"),
		BaseTemplate:  strPtr("motd=hello"),
		RenderOptions: manman.JSONB{"engine": "gotemplate", "indent": float64(2)},
		ApplyOrder:    10,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.StrategyID == 0 {
		t.Fatal("Create did not populate StrategyID")
	}

	got, err := f.repo.Get(ctx, created.StrategyID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GameID != f.gameA {
		t.Errorf("GameID = %d, want %d", got.GameID, f.gameA)
	}
	if got.Name != "server.properties" {
		t.Errorf("Name = %q, want %q", got.Name, "server.properties")
	}
	if got.Description == nil || *got.Description != "main properties" {
		t.Errorf("Description = %v, want %q", got.Description, "main properties")
	}
	if got.StrategyType != "file_properties" {
		t.Errorf("StrategyType = %q, want %q", got.StrategyType, "file_properties")
	}
	if got.TargetPath == nil || *got.TargetPath != "server.properties" {
		t.Errorf("TargetPath = %v, want %q", got.TargetPath, "server.properties")
	}
	if got.BaseTemplate == nil || *got.BaseTemplate != "motd=hello" {
		t.Errorf("BaseTemplate = %v, want %q", got.BaseTemplate, "motd=hello")
	}
	if got.RenderOptions["engine"] != "gotemplate" {
		t.Errorf("RenderOptions[engine] = %v, want %q", got.RenderOptions["engine"], "gotemplate")
	}
	if got.ApplyOrder != 10 {
		t.Errorf("ApplyOrder = %d, want 10", got.ApplyOrder)
	}
}

// TestConfigurationStrategyRepository_GetUnknownIDErrors proves Get on a
// missing strategy_id errors.
func TestConfigurationStrategyRepository_GetUnknownIDErrors(t *testing.T) {
	f := newStrategyHarness(t)

	if _, err := f.repo.Get(context.Background(), 999999); err == nil {
		t.Fatal("Get on an unknown strategy_id returned no error")
	}
}

// TestConfigurationStrategyRepository_ListByGameOrdersByApplyOrder proves
// ListByGame sorts by apply_order (not insertion order) and that another
// game's strategies never leak in.
func TestConfigurationStrategyRepository_ListByGameOrdersByApplyOrder(t *testing.T) {
	f := newStrategyHarness(t)
	ctx := context.Background()

	// Insert out of apply_order order to prove the ORDER BY, not insertion
	// order, is what determines the result.
	third := f.seed(t, f.gameA, "third", "file_properties", 30)
	first := f.seed(t, f.gameA, "first", "file_properties", 10)
	second := f.seed(t, f.gameA, "second", "file_properties", 20)
	f.seed(t, f.gameB, "other-game", "file_properties", 1)

	listed, err := f.repo.ListByGame(ctx, f.gameA)
	if err != nil {
		t.Fatalf("ListByGame: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("ListByGame returned %d rows, want 3", len(listed))
	}

	want := []int64{first.StrategyID, second.StrategyID, third.StrategyID}
	got := strategyIDs(listed)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListByGame order = %v, want %v (ordered by apply_order)", got, want)
		}
	}
	for _, s := range listed {
		if s.GameID != f.gameA {
			t.Errorf("strategy %d belongs to game %d, want %d", s.StrategyID, s.GameID, f.gameA)
		}
	}
}

// TestConfigurationStrategyRepository_UpdateBumpsUpdatedAt proves Update
// rewrites the mutable columns and advances updated_at, which its SET list
// touches explicitly via CURRENT_TIMESTAMP.
func TestConfigurationStrategyRepository_UpdateBumpsUpdatedAt(t *testing.T) {
	f := newStrategyHarness(t)
	ctx := context.Background()

	strategy := f.seed(t, f.gameA, "before", "file_properties", 5)
	// Read updated_at directly: Get does not select that column (see
	// TestConfigurationStrategyRepository_GetOmitsTimestamps), so the
	// repository's own round-trip can't observe the bump.
	var before time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT updated_at FROM configuration_strategies WHERE strategy_id = $1`, strategy.StrategyID,
	).Scan(&before); err != nil {
		t.Fatalf("read updated_at before Update: %v", err)
	}

	strategy.Name = "after"
	strategy.Description = strPtr("updated")
	strategy.StrategyType = "file_json"
	strategy.TargetPath = strPtr("after.json")
	strategy.BaseTemplate = strPtr(`{"after":true}`)
	strategy.RenderOptions = manman.JSONB{"engine": "jsonnet"}
	strategy.ApplyOrder = 99
	strategy.GameID = f.gameB // must be ignored -- game_id is not in the SET list

	if err := f.repo.Update(ctx, strategy); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var afterUpdatedAt time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT updated_at FROM configuration_strategies WHERE strategy_id = $1`, strategy.StrategyID,
	).Scan(&afterUpdatedAt); err != nil {
		t.Fatalf("read updated_at after Update: %v", err)
	}
	if !afterUpdatedAt.After(before) {
		t.Errorf("updated_at = %v, want after %v (Update sets CURRENT_TIMESTAMP)", afterUpdatedAt, before)
	}

	after, err := f.repo.Get(ctx, strategy.StrategyID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if after.GameID != f.gameA {
		t.Errorf("GameID = %d, want %d (Update must not re-parent)", after.GameID, f.gameA)
	}
	if after.Name != "after" || after.StrategyType != "file_json" {
		t.Errorf("Name/StrategyType = %q/%q, want %q/%q", after.Name, after.StrategyType, "after", "file_json")
	}
	if after.ApplyOrder != 99 {
		t.Errorf("ApplyOrder = %d, want 99", after.ApplyOrder)
	}
	if after.RenderOptions["engine"] != "jsonnet" {
		t.Errorf("RenderOptions[engine] = %v, want %q", after.RenderOptions["engine"], "jsonnet")
	}
}

// TestConfigurationStrategyRepository_GetOmitsTimestamps documents that Get's
// column list stops at apply_order, so the ConfigurationStrategy it returns
// always carries zero CreatedAt/UpdatedAt even though the table has both and
// Update maintains updated_at. Pinned so that either widening Get's SELECT or
// accepting this gap is a deliberate edit -- a caller reading
// strategy.UpdatedAt today gets the zero time, not a real one.
func TestConfigurationStrategyRepository_GetOmitsTimestamps(t *testing.T) {
	f := newStrategyHarness(t)
	ctx := context.Background()

	strategy := f.seed(t, f.gameA, "timestamps", "file_properties", 1)

	got, err := f.repo.Get(ctx, strategy.StrategyID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.CreatedAt.IsZero() || !got.UpdatedAt.IsZero() {
		t.Errorf("CreatedAt/UpdatedAt = %v/%v, want both zero (Get does not select them)",
			got.CreatedAt, got.UpdatedAt)
	}

	// The columns do exist and are populated -- it's the repository that
	// leaves them behind.
	var created, updated time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT created_at, updated_at FROM configuration_strategies WHERE strategy_id = $1`, strategy.StrategyID,
	).Scan(&created, &updated); err != nil {
		t.Fatalf("read timestamps directly: %v", err)
	}
	if created.IsZero() || updated.IsZero() {
		t.Errorf("stored created_at/updated_at = %v/%v, want both populated", created, updated)
	}
}

// TestConfigurationStrategyRepository_DeleteRemovesRow proves Delete is a
// hard delete and that a follow-up Get errors.
func TestConfigurationStrategyRepository_DeleteRemovesRow(t *testing.T) {
	f := newStrategyHarness(t)
	ctx := context.Background()

	strategy := f.seed(t, f.gameA, "doomed", "file_properties", 1)

	if err := f.repo.Delete(ctx, strategy.StrategyID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.repo.Get(ctx, strategy.StrategyID); err == nil {
		t.Fatal("Get succeeded after Delete, want an error")
	}
}

// TestConfigurationStrategyRepository_CreateUnknownGameFails proves the real
// migration's FK from configuration_strategies.game_id to games.game_id is
// enforced.
func TestConfigurationStrategyRepository_CreateUnknownGameFails(t *testing.T) {
	f := newStrategyHarness(t)

	_, err := f.repo.Create(context.Background(), &manman.ConfigurationStrategy{
		GameID:       999999,
		Name:         "orphan",
		StrategyType: "file_properties",
	})
	if err == nil {
		t.Fatal("Create with a nonexistent game_id succeeded, want an FK violation")
	}
}

func strategyIDs(strategies []*manman.ConfigurationStrategy) []int64 {
	ids := make([]int64, len(strategies))
	for i, s := range strategies {
		ids[i] = s.StrategyID
	}
	return ids
}
