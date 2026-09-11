package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
)

// GameConfigWorkshopLibraryRepository implements
// repository.GameConfigWorkshopLibraryRepository against the GC-level
// gameconfig_workshop_libraries table and the FR12 conflict tables (M6
// #2361, plan #2359). sgc_workshop_libraries (see ServerGameConfigRepository)
// remains authoritative for deploy-time resolution -- this repository is
// purely additive until the dependent retirement task cuts over (NFR1).
type GameConfigWorkshopLibraryRepository struct {
	db *pgxpool.Pool
}

func NewGameConfigWorkshopLibraryRepository(db *pgxpool.Pool) *GameConfigWorkshopLibraryRepository {
	return &GameConfigWorkshopLibraryRepository{db: db}
}

func (r *GameConfigWorkshopLibraryRepository) ListLibraries(ctx context.Context, configID int64) ([]*manman.WorkshopLibrary, error) {
	query := `
		SELECT wl.library_id, wl.game_id, wl.name, wl.description, wl.preset_id, wl.created_at, wl.updated_at
		FROM workshop_libraries wl
		INNER JOIN gameconfig_workshop_libraries gwl ON wl.library_id = gwl.library_id
		WHERE gwl.config_id = $1
		ORDER BY wl.library_id
	`

	rows, err := r.db.Query(ctx, query, configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var libraries []*manman.WorkshopLibrary
	for rows.Next() {
		lib := &manman.WorkshopLibrary{}
		if err := rows.Scan(
			&lib.LibraryID,
			&lib.GameID,
			&lib.Name,
			&lib.Description,
			&lib.PresetID,
			&lib.CreatedAt,
			&lib.UpdatedAt,
		); err != nil {
			return nil, err
		}
		libraries = append(libraries, lib)
	}

	return libraries, rows.Err()
}

func (r *GameConfigWorkshopLibraryRepository) ListAttachments(ctx context.Context, configID int64) ([]*manman.GameConfigWorkshopLibrary, error) {
	query := `
		SELECT config_id, library_id, preset_id, volume_id, installation_path_override, created_at
		FROM gameconfig_workshop_libraries
		WHERE config_id = $1
		ORDER BY library_id
	`

	rows, err := r.db.Query(ctx, query, configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attachments []*manman.GameConfigWorkshopLibrary
	for rows.Next() {
		attachment := &manman.GameConfigWorkshopLibrary{}
		if err := rows.Scan(
			&attachment.ConfigID,
			&attachment.LibraryID,
			&attachment.PresetID,
			&attachment.VolumeID,
			&attachment.InstallationPathOverride,
			&attachment.CreatedAt,
		); err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}

	return attachments, rows.Err()
}

func (r *GameConfigWorkshopLibraryRepository) AddLibrary(ctx context.Context, configID, libraryID int64, presetID, volumeID *int64, installationPathOverride *string) error {
	query := `
		INSERT INTO gameconfig_workshop_libraries (config_id, library_id, preset_id, volume_id, installation_path_override)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (config_id, library_id) DO UPDATE
		SET preset_id = EXCLUDED.preset_id,
		    volume_id = EXCLUDED.volume_id,
		    installation_path_override = EXCLUDED.installation_path_override
	`
	_, err := r.db.Exec(ctx, query, configID, libraryID, presetID, volumeID, installationPathOverride)
	return err
}

func (r *GameConfigWorkshopLibraryRepository) RemoveLibrary(ctx context.Context, configID, libraryID int64) error {
	query := `DELETE FROM gameconfig_workshop_libraries WHERE config_id = $1 AND library_id = $2`
	_, err := r.db.Exec(ctx, query, configID, libraryID)
	return err
}

// ListUnresolvedConflicts returns every unresolved conflict with its
// Candidates populated via one follow-up query keyed on the fetched
// conflict_ids (not a per-conflict loop) -- the FR12 resolution UI/RPC needs
// candidates on every entry it lists, and this keeps that from scaling
// query count with conflict count.
func (r *GameConfigWorkshopLibraryRepository) ListUnresolvedConflicts(ctx context.Context) ([]*manman.WorkshopLibraryMigrationConflict, error) {
	query := `
		SELECT conflict_id, config_id, detected_at, resolved_at, resolution, resolved_library_id
		FROM workshop_library_migration_conflicts
		WHERE resolved_at IS NULL
		ORDER BY conflict_id
	`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conflicts []*manman.WorkshopLibraryMigrationConflict
	conflictIDs := make([]int64, 0)
	for rows.Next() {
		c := &manman.WorkshopLibraryMigrationConflict{}
		if err := rows.Scan(
			&c.ConflictID,
			&c.ConfigID,
			&c.DetectedAt,
			&c.ResolvedAt,
			&c.Resolution,
			&c.ResolvedLibraryID,
		); err != nil {
			return nil, err
		}
		conflicts = append(conflicts, c)
		conflictIDs = append(conflictIDs, c.ConflictID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(conflicts) == 0 {
		return conflicts, nil
	}

	candidatesByConflict, err := r.listCandidatesByConflictIDs(ctx, conflictIDs)
	if err != nil {
		return nil, err
	}
	for _, c := range conflicts {
		c.Candidates = candidatesByConflict[c.ConflictID]
	}

	return conflicts, nil
}

// ListConflictCandidates returns conflictID's candidates regardless of
// resolved state (see interface doc comment).
func (r *GameConfigWorkshopLibraryRepository) ListConflictCandidates(ctx context.Context, conflictID int64) ([]*manman.WorkshopLibraryMigrationConflictCandidate, error) {
	byConflict, err := r.listCandidatesByConflictIDs(ctx, []int64{conflictID})
	if err != nil {
		return nil, err
	}
	return byConflict[conflictID], nil
}

func (r *GameConfigWorkshopLibraryRepository) listCandidatesByConflictIDs(ctx context.Context, conflictIDs []int64) (map[int64][]*manman.WorkshopLibraryMigrationConflictCandidate, error) {
	query := `
		SELECT conflict_id, library_id, sgc_id, preset_id, volume_id, installation_path_override
		FROM workshop_library_migration_conflict_candidates
		WHERE conflict_id = ANY($1)
		ORDER BY conflict_id, library_id, sgc_id
	`

	rows, err := r.db.Query(ctx, query, conflictIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byConflict := make(map[int64][]*manman.WorkshopLibraryMigrationConflictCandidate)
	for rows.Next() {
		cand := &manman.WorkshopLibraryMigrationConflictCandidate{}
		if err := rows.Scan(&cand.ConflictID, &cand.LibraryID, &cand.SGCID, &cand.PresetID, &cand.VolumeID, &cand.InstallationPathOverride); err != nil {
			return nil, err
		}
		byConflict[cand.ConflictID] = append(byConflict[cand.ConflictID], cand)
	}

	return byConflict, rows.Err()
}

// GetConflictForConfig returns configID's unresolved conflict, or (nil, nil)
// if it has none (including the case where a prior conflict for it has
// already been resolved).
func (r *GameConfigWorkshopLibraryRepository) GetConflictForConfig(ctx context.Context, configID int64) (*manman.WorkshopLibraryMigrationConflict, error) {
	query := `
		SELECT conflict_id, config_id, detected_at, resolved_at, resolution, resolved_library_id
		FROM workshop_library_migration_conflicts
		WHERE config_id = $1 AND resolved_at IS NULL
		ORDER BY conflict_id
		LIMIT 1
	`

	c := &manman.WorkshopLibraryMigrationConflict{}
	err := r.db.QueryRow(ctx, query, configID).Scan(
		&c.ConflictID,
		&c.ConfigID,
		&c.DetectedAt,
		&c.ResolvedAt,
		&c.Resolution,
		&c.ResolvedLibraryID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return c, nil
}

// ResolveConflict stamps resolved_at/resolution on conflictID and writes the
// resulting attachment set into gameconfig_workshop_libraries, all in one
// transaction. "union" writes one row per distinct library_id among the
// conflict's candidates (taking the override columns from that library_id's
// lowest sgc_id candidate as the representative variant); "override" writes
// only keepLibraryID. Resolving an already-resolved (or nonexistent)
// conflict is rejected -- the guarding UPDATE only touches rows where
// resolved_at IS NULL, and zero rows affected is treated as an error rather
// than silently re-applying the resolution.
func (r *GameConfigWorkshopLibraryRepository) ResolveConflict(ctx context.Context, conflictID int64, resolution string, keepLibraryID *int64) error {
	if resolution != "union" && resolution != "override" {
		return fmt.Errorf("invalid resolution %q: must be \"union\" or \"override\"", resolution)
	}
	if resolution == "override" && keepLibraryID == nil {
		return errors.New("override resolution requires keepLibraryID")
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	var configID int64
	err = tx.QueryRow(ctx, `
		UPDATE workshop_library_migration_conflicts
		SET resolved_at = NOW(), resolution = $2, resolved_library_id = $3
		WHERE conflict_id = $1 AND resolved_at IS NULL
		RETURNING config_id
	`, conflictID, resolution, keepLibraryID).Scan(&configID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = fmt.Errorf("conflict %d not found or already resolved", conflictID)
		return err
	}
	if err != nil {
		err = fmt.Errorf("failed to mark conflict %d resolved: %w", conflictID, err)
		return err
	}

	// candidate_repr: one representative attachment row per distinct
	// library_id among the conflict's candidates, sourced from that
	// library_id's lowest sgc_id (a stable, deterministic pick among
	// disagreeing override variants). preset_id/volume_id/
	// installation_path_override are read directly off
	// workshop_library_migration_conflict_candidates -- denormalized there at
	// conflict-detection time (migration 042) -- rather than joined from
	// sgc_workshop_libraries, which may already be dropped (043, NFR1) by the
	// time an unresolved conflict actually gets resolved (043's guard
	// explicitly permits that).
	_, err = tx.Exec(ctx, `
		WITH candidate_repr AS (
			SELECT DISTINCT ON (cc.library_id)
				cc.library_id, cc.preset_id, cc.volume_id, cc.installation_path_override
			FROM workshop_library_migration_conflict_candidates cc
			WHERE cc.conflict_id = $1
			ORDER BY cc.library_id, cc.sgc_id
		)
		INSERT INTO gameconfig_workshop_libraries (config_id, library_id, preset_id, volume_id, installation_path_override)
		SELECT $2, library_id, preset_id, volume_id, installation_path_override
		FROM candidate_repr
		WHERE ($3::bigint IS NULL OR library_id = $3)
		ON CONFLICT (config_id, library_id) DO NOTHING
	`, conflictID, configID, keepLibraryID)
	if err != nil {
		err = fmt.Errorf("failed to write resolved attachment set for conflict %d: %w", conflictID, err)
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		err = fmt.Errorf("failed to commit conflict %d resolution: %w", conflictID, err)
		return err
	}
	return nil
}
