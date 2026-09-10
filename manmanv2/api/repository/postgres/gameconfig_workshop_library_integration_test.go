//go:build integration

// Real-Postgres coverage for GameConfigWorkshopLibraryRepository (#2361,
// plan #2359): the ON CONFLICT DO UPDATE upsert AddLibrary relies on, the
// scoped list/read paths, and -- the piece an in-memory fake can't prove --
// ResolveConflict's one-transaction "stamp resolved_at + write the
// resulting attachment set" behavior for both "union" and "override", and
// that resolving an already-resolved conflict is rejected rather than
// silently reapplied or silently ignored.
//
// Schema here is hand-written, self-contained DDL mirroring exactly the
// pieces of manmanv2/migrate/migrations/042_gameconfig_workshop_libraries.up.sql
// this test needs, per dbtest's README ("Options.Schema should be
// self-contained DDL -- do not depend on another package's migrations").
// sgc_workshop_libraries.sgc_id and workshop_library_migration_conflict_candidates.sgc_id
// deliberately carry no FK here, matching the real migration (SGC scope is
// being retired, not a first-class parent this layer depends on).
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	manman "github.com/whale-net/everything/manmanv2/models"
)

const gameConfigWorkshopLibrarySchema = `
	CREATE TABLE games (
		game_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL
	);

	CREATE TABLE game_configs (
		config_id BIGSERIAL PRIMARY KEY,
		game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		name VARCHAR(255) NOT NULL,
		image TEXT NOT NULL
	);

	CREATE TABLE workshop_libraries (
		library_id BIGSERIAL PRIMARY KEY,
		game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		name VARCHAR(255) NOT NULL,
		description TEXT,
		preset_id BIGINT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	CREATE TABLE sgc_workshop_libraries (
		sgc_id BIGINT NOT NULL,
		library_id BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
		preset_id BIGINT,
		volume_id BIGINT,
		installation_path_override TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (sgc_id, library_id)
	);

	CREATE TABLE gameconfig_workshop_libraries (
		config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
		library_id BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
		preset_id BIGINT,
		volume_id BIGINT,
		installation_path_override TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (config_id, library_id)
	);

	CREATE TABLE workshop_library_migration_conflicts (
		conflict_id BIGSERIAL PRIMARY KEY,
		config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
		detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		resolved_at TIMESTAMPTZ,
		resolution TEXT CHECK (resolution IN ('union', 'override')),
		resolved_library_id BIGINT REFERENCES workshop_libraries(library_id) ON DELETE SET NULL
	);

	CREATE TABLE workshop_library_migration_conflict_candidates (
		conflict_id BIGINT NOT NULL REFERENCES workshop_library_migration_conflicts(conflict_id) ON DELETE CASCADE,
		library_id BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
		sgc_id BIGINT NOT NULL,
		PRIMARY KEY (conflict_id, library_id, sgc_id)
	);
`

func newGameConfigWorkshopLibraryTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: gameConfigWorkshopLibrarySchema})
	return db.Pool
}

// seedGCWLGameAndConfig seeds one game and one game_config, returning
// (gameID, configID).
func seedGCWLGameAndConfig(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) (int64, int64) {
	t.Helper()
	var gameID int64
	if err := pool.QueryRow(ctx, `INSERT INTO games (name) VALUES ($1) RETURNING game_id`, name+"-game").Scan(&gameID); err != nil {
		t.Fatalf("seed game: %v", err)
	}
	var configID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO game_configs (game_id, name, image) VALUES ($1, $2, 'image') RETURNING config_id`, gameID, name+"-config",
	).Scan(&configID); err != nil {
		t.Fatalf("seed game_config: %v", err)
	}
	return gameID, configID
}

func seedGCWLLibrary(ctx context.Context, t *testing.T, pool *pgxpool.Pool, gameID int64, name string) int64 {
	t.Helper()
	var libraryID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO workshop_libraries (game_id, name) VALUES ($1, $2) RETURNING library_id`, gameID, name,
	).Scan(&libraryID); err != nil {
		t.Fatalf("seed library %s: %v", name, err)
	}
	return libraryID
}

func seedGCWLConflict(ctx context.Context, t *testing.T, pool *pgxpool.Pool, configID int64) int64 {
	t.Helper()
	var conflictID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO workshop_library_migration_conflicts (config_id) VALUES ($1) RETURNING conflict_id`, configID,
	).Scan(&conflictID); err != nil {
		t.Fatalf("seed conflict for config %d: %v", configID, err)
	}
	return conflictID
}

func seedGCWLCandidate(ctx context.Context, t *testing.T, pool *pgxpool.Pool, conflictID, libraryID, sgcID int64, presetID, volumeID *int64, installationPathOverride *string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workshop_library_migration_conflict_candidates (conflict_id, library_id, sgc_id) VALUES ($1, $2, $3)`,
		conflictID, libraryID, sgcID,
	); err != nil {
		t.Fatalf("seed candidate (conflict=%d, library=%d, sgc=%d): %v", conflictID, libraryID, sgcID, err)
	}
	// ResolveConflict's candidate_repr join reads the actual attachment row
	// from sgc_workshop_libraries -- a real deployment always has one for
	// every candidate (the backfill only records candidates it read from
	// there), so tests must seed it too.
	if _, err := pool.Exec(ctx,
		`INSERT INTO sgc_workshop_libraries (sgc_id, library_id, preset_id, volume_id, installation_path_override)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (sgc_id, library_id) DO NOTHING`,
		sgcID, libraryID, presetID, volumeID, installationPathOverride,
	); err != nil {
		t.Fatalf("seed sgc_workshop_libraries for candidate (sgc=%d, library=%d): %v", sgcID, libraryID, err)
	}
}

func TestGameConfigWorkshopLibrary_AddLibrary_UpsertsOverridesOnConflict(t *testing.T) {
	pool := newGameConfigWorkshopLibraryTestDB(t)
	ctx := context.Background()
	repo := NewGameConfigWorkshopLibraryRepository(pool)

	_, configID := seedGCWLGameAndConfig(ctx, t, pool, "add-upsert")
	gameID, _ := seedGCWLGameAndConfig(ctx, t, pool, "add-upsert-lib")
	libraryID := seedGCWLLibrary(ctx, t, pool, gameID, "lib")

	presetA := int64(1)
	if err := repo.AddLibrary(ctx, configID, libraryID, &presetA, nil, nil); err != nil {
		t.Fatalf("first AddLibrary: %v", err)
	}

	overrideB := "/opt/b"
	if err := repo.AddLibrary(ctx, configID, libraryID, nil, nil, &overrideB); err != nil {
		t.Fatalf("second AddLibrary (upsert): %v", err)
	}

	attachments, err := repo.ListAttachments(ctx, configID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected exactly one attachment after upsert, got %d", len(attachments))
	}
	if attachments[0].PresetID != nil {
		t.Fatalf("expected the second AddLibrary's nil preset_id to overwrite the first, got %v", *attachments[0].PresetID)
	}
	if attachments[0].InstallationPathOverride == nil || *attachments[0].InstallationPathOverride != "/opt/b" {
		t.Fatalf("expected installation_path_override to be updated to /opt/b, got %v", attachments[0].InstallationPathOverride)
	}
}

func TestGameConfigWorkshopLibrary_ListAttachmentsAndListLibraries_ScopedToConfig(t *testing.T) {
	pool := newGameConfigWorkshopLibraryTestDB(t)
	ctx := context.Background()
	repo := NewGameConfigWorkshopLibraryRepository(pool)

	gameID, configA := seedGCWLGameAndConfig(ctx, t, pool, "scope-a")
	_, configB := seedGCWLGameAndConfig(ctx, t, pool, "scope-b")
	libA := seedGCWLLibrary(ctx, t, pool, gameID, "lib-a")
	libB := seedGCWLLibrary(ctx, t, pool, gameID, "lib-b")

	if err := repo.AddLibrary(ctx, configA, libA, nil, nil, nil); err != nil {
		t.Fatalf("AddLibrary configA/libA: %v", err)
	}
	if err := repo.AddLibrary(ctx, configB, libB, nil, nil, nil); err != nil {
		t.Fatalf("AddLibrary configB/libB: %v", err)
	}

	attachments, err := repo.ListAttachments(ctx, configA)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(attachments) != 1 || attachments[0].LibraryID != libA {
		t.Fatalf("expected ListAttachments(configA) to return exactly libA, got %+v", attachments)
	}

	libraries, err := repo.ListLibraries(ctx, configA)
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libraries) != 1 || libraries[0].LibraryID != libA {
		t.Fatalf("expected ListLibraries(configA) to return exactly libA, got %+v", libraries)
	}
}

func TestGameConfigWorkshopLibrary_RemoveLibrary_DeletesOnlyThatPair(t *testing.T) {
	pool := newGameConfigWorkshopLibraryTestDB(t)
	ctx := context.Background()
	repo := NewGameConfigWorkshopLibraryRepository(pool)

	gameID, configID := seedGCWLGameAndConfig(ctx, t, pool, "remove")
	libA := seedGCWLLibrary(ctx, t, pool, gameID, "lib-a")
	libB := seedGCWLLibrary(ctx, t, pool, gameID, "lib-b")
	if err := repo.AddLibrary(ctx, configID, libA, nil, nil, nil); err != nil {
		t.Fatalf("AddLibrary libA: %v", err)
	}
	if err := repo.AddLibrary(ctx, configID, libB, nil, nil, nil); err != nil {
		t.Fatalf("AddLibrary libB: %v", err)
	}

	if err := repo.RemoveLibrary(ctx, configID, libA); err != nil {
		t.Fatalf("RemoveLibrary: %v", err)
	}

	attachments, err := repo.ListAttachments(ctx, configID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(attachments) != 1 || attachments[0].LibraryID != libB {
		t.Fatalf("expected only libB to remain, got %+v", attachments)
	}
}

func TestGameConfigWorkshopLibrary_ListUnresolvedConflicts_ExcludesResolved(t *testing.T) {
	pool := newGameConfigWorkshopLibraryTestDB(t)
	ctx := context.Background()
	repo := NewGameConfigWorkshopLibraryRepository(pool)

	_, unresolvedConfig := seedGCWLGameAndConfig(ctx, t, pool, "unresolved")
	_, resolvedConfig := seedGCWLGameAndConfig(ctx, t, pool, "resolved")
	unresolvedConflict := seedGCWLConflict(ctx, t, pool, unresolvedConfig)
	resolvedConflict := seedGCWLConflict(ctx, t, pool, resolvedConfig)
	if _, err := pool.Exec(ctx, `UPDATE workshop_library_migration_conflicts SET resolved_at = NOW(), resolution = 'union' WHERE conflict_id = $1`, resolvedConflict); err != nil {
		t.Fatalf("mark resolved: %v", err)
	}

	unresolved, err := repo.ListUnresolvedConflicts(ctx)
	if err != nil {
		t.Fatalf("ListUnresolvedConflicts: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0].ConflictID != unresolvedConflict {
		t.Fatalf("expected exactly the unresolved conflict %d, got %+v", unresolvedConflict, unresolved)
	}
}

func TestGameConfigWorkshopLibrary_GetConflictForConfig_NilWhenNoneOrResolved(t *testing.T) {
	pool := newGameConfigWorkshopLibraryTestDB(t)
	ctx := context.Background()
	repo := NewGameConfigWorkshopLibraryRepository(pool)

	_, noConflictConfig := seedGCWLGameAndConfig(ctx, t, pool, "no-conflict")
	got, err := repo.GetConflictForConfig(ctx, noConflictConfig)
	if err != nil {
		t.Fatalf("GetConflictForConfig (no conflict): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for a config with no conflict, got %+v", got)
	}

	_, resolvedConfig := seedGCWLGameAndConfig(ctx, t, pool, "already-resolved")
	resolvedConflict := seedGCWLConflict(ctx, t, pool, resolvedConfig)
	if _, err := pool.Exec(ctx, `UPDATE workshop_library_migration_conflicts SET resolved_at = NOW(), resolution = 'union' WHERE conflict_id = $1`, resolvedConflict); err != nil {
		t.Fatalf("mark resolved: %v", err)
	}
	got, err = repo.GetConflictForConfig(ctx, resolvedConfig)
	if err != nil {
		t.Fatalf("GetConflictForConfig (resolved): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for a config whose only conflict is already resolved, got %+v", got)
	}

	_, unresolvedConfig := seedGCWLGameAndConfig(ctx, t, pool, "still-open")
	unresolvedConflict := seedGCWLConflict(ctx, t, pool, unresolvedConfig)
	got, err = repo.GetConflictForConfig(ctx, unresolvedConfig)
	if err != nil {
		t.Fatalf("GetConflictForConfig (unresolved): %v", err)
	}
	if got == nil || got.ConflictID != unresolvedConflict {
		t.Fatalf("expected the unresolved conflict %d, got %+v", unresolvedConflict, got)
	}
}

// TestGameConfigWorkshopLibrary_ResolveConflict_UnionWritesOneRepresentativeRowPerLibrary
// proves the "union" path: every distinct library_id among the conflict's
// candidates lands at GC level, taking the lowest-sgc_id candidate's
// overrides as the representative variant, and resolved_at/resolution are
// stamped (resolved_library_id left nil since more than one library wins).
func TestGameConfigWorkshopLibrary_ResolveConflict_UnionWritesOneRepresentativeRowPerLibrary(t *testing.T) {
	pool := newGameConfigWorkshopLibraryTestDB(t)
	ctx := context.Background()
	repo := NewGameConfigWorkshopLibraryRepository(pool)

	gameID, configID := seedGCWLGameAndConfig(ctx, t, pool, "resolve-union")
	libA := seedGCWLLibrary(ctx, t, pool, gameID, "lib-a")
	libB := seedGCWLLibrary(ctx, t, pool, gameID, "lib-b")
	conflictID := seedGCWLConflict(ctx, t, pool, configID)

	overrideLow := "/opt/low-sgc-wins"
	overrideHigh := "/opt/high-sgc-loses"
	// libA has two disagreeing variants (sgc 1 and sgc 2); the lowest
	// sgc_id (1) must win as the representative.
	seedGCWLCandidate(ctx, t, pool, conflictID, libA, 1, nil, nil, &overrideLow)
	seedGCWLCandidate(ctx, t, pool, conflictID, libA, 2, nil, nil, &overrideHigh)
	seedGCWLCandidate(ctx, t, pool, conflictID, libB, 3, nil, nil, nil)

	if err := repo.ResolveConflict(ctx, conflictID, "union", nil); err != nil {
		t.Fatalf("ResolveConflict union: %v", err)
	}

	attachments, err := repo.ListAttachments(ctx, configID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(attachments) != 2 {
		t.Fatalf("expected 2 GC-level rows (libA, libB) after union resolution, got %d: %+v", len(attachments), attachments)
	}
	byLibrary := map[int64]*manman.GameConfigWorkshopLibrary{}
	for _, a := range attachments {
		byLibrary[a.LibraryID] = a
	}
	if a, ok := byLibrary[libA]; !ok || a.InstallationPathOverride == nil || *a.InstallationPathOverride != overrideLow {
		t.Fatalf("expected libA's representative variant to be the lowest sgc_id's override %q, got %+v", overrideLow, byLibrary[libA])
	}
	if _, ok := byLibrary[libB]; !ok {
		t.Fatalf("expected libB to be present after union resolution")
	}

	var resolvedAt, resolution any
	var resolvedLibraryID *int64
	if err := pool.QueryRow(ctx, `SELECT resolved_at, resolution, resolved_library_id FROM workshop_library_migration_conflicts WHERE conflict_id = $1`, conflictID).
		Scan(&resolvedAt, &resolution, &resolvedLibraryID); err != nil {
		t.Fatalf("query resolved conflict: %v", err)
	}
	if resolvedAt == nil {
		t.Fatal("expected resolved_at to be stamped")
	}
	if resolution != "union" {
		t.Fatalf("expected resolution = union, got %v", resolution)
	}
	if resolvedLibraryID != nil {
		t.Fatalf("expected resolved_library_id to stay nil for a union resolution, got %v", *resolvedLibraryID)
	}
}

// TestGameConfigWorkshopLibrary_ResolveConflict_OverrideWritesOnlyKeptLibrary
// proves the "override" path: only keepLibraryID lands at GC level, the
// other candidate library is discarded, and resolved_library_id records the
// winner.
func TestGameConfigWorkshopLibrary_ResolveConflict_OverrideWritesOnlyKeptLibrary(t *testing.T) {
	pool := newGameConfigWorkshopLibraryTestDB(t)
	ctx := context.Background()
	repo := NewGameConfigWorkshopLibraryRepository(pool)

	gameID, configID := seedGCWLGameAndConfig(ctx, t, pool, "resolve-override")
	libKeep := seedGCWLLibrary(ctx, t, pool, gameID, "lib-keep")
	libDiscard := seedGCWLLibrary(ctx, t, pool, gameID, "lib-discard")
	conflictID := seedGCWLConflict(ctx, t, pool, configID)
	seedGCWLCandidate(ctx, t, pool, conflictID, libKeep, 10, nil, nil, nil)
	seedGCWLCandidate(ctx, t, pool, conflictID, libDiscard, 20, nil, nil, nil)

	if err := repo.ResolveConflict(ctx, conflictID, "override", &libKeep); err != nil {
		t.Fatalf("ResolveConflict override: %v", err)
	}

	attachments, err := repo.ListAttachments(ctx, configID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(attachments) != 1 || attachments[0].LibraryID != libKeep {
		t.Fatalf("expected only libKeep to be attached after override resolution, got %+v", attachments)
	}

	var resolvedLibraryID *int64
	if err := pool.QueryRow(ctx, `SELECT resolved_library_id FROM workshop_library_migration_conflicts WHERE conflict_id = $1`, conflictID).Scan(&resolvedLibraryID); err != nil {
		t.Fatalf("query resolved_library_id: %v", err)
	}
	if resolvedLibraryID == nil || *resolvedLibraryID != libKeep {
		t.Fatalf("expected resolved_library_id = %d, got %v", libKeep, resolvedLibraryID)
	}
}

// TestGameConfigWorkshopLibrary_ResolveConflict_TwiceIsRejected proves
// resolving an already-resolved conflict is rejected (not silently
// reapplied) -- the guarding UPDATE ... WHERE resolved_at IS NULL affects
// zero rows on the second call, and that is surfaced as an error.
func TestGameConfigWorkshopLibrary_ResolveConflict_TwiceIsRejected(t *testing.T) {
	pool := newGameConfigWorkshopLibraryTestDB(t)
	ctx := context.Background()
	repo := NewGameConfigWorkshopLibraryRepository(pool)

	gameID, configID := seedGCWLGameAndConfig(ctx, t, pool, "resolve-twice")
	lib := seedGCWLLibrary(ctx, t, pool, gameID, "lib")
	conflictID := seedGCWLConflict(ctx, t, pool, configID)
	seedGCWLCandidate(ctx, t, pool, conflictID, lib, 1, nil, nil, nil)

	if err := repo.ResolveConflict(ctx, conflictID, "union", nil); err != nil {
		t.Fatalf("first ResolveConflict: %v", err)
	}
	if err := repo.ResolveConflict(ctx, conflictID, "override", &lib); err == nil {
		t.Fatal("expected a second ResolveConflict call on the same conflict to be rejected, it succeeded")
	}

	// The first resolution's outcome must be untouched by the rejected
	// second call.
	attachments, err := repo.ListAttachments(ctx, configID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(attachments) != 1 || attachments[0].LibraryID != lib {
		t.Fatalf("expected the first resolution's attachment to remain untouched, got %+v", attachments)
	}
}
