package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type ConfigurationPatchRepository struct {
	db *pgxpool.Pool
}

func NewConfigurationPatchRepository(db *pgxpool.Pool) *ConfigurationPatchRepository {
	return &ConfigurationPatchRepository{db: db}
}

func (r *ConfigurationPatchRepository) Create(ctx context.Context, patch *manman.ConfigurationPatch) (*manman.ConfigurationPatch, error) {
	query := `
		INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format, volume_id, path_override)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING patch_id, created_at, updated_at
	`

	err := r.db.QueryRow(ctx, query,
		patch.StrategyID,
		patch.PatchLevel,
		patch.EntityID,
		patch.PatchContent,
		patch.PatchFormat,
		patch.VolumeID,
		patch.PathOverride,
	).Scan(&patch.PatchID, &patch.CreatedAt, &patch.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return patch, nil
}

func (r *ConfigurationPatchRepository) Get(ctx context.Context, patchID int64) (*manman.ConfigurationPatch, error) {
	patch := &manman.ConfigurationPatch{}

	query := `
		SELECT patch_id, strategy_id, patch_level, entity_id, patch_content, patch_format, volume_id, path_override, created_at, updated_at
		FROM configuration_patches
		WHERE patch_id = $1
	`

	err := r.db.QueryRow(ctx, query, patchID).Scan(
		&patch.PatchID,
		&patch.StrategyID,
		&patch.PatchLevel,
		&patch.EntityID,
		&patch.PatchContent,
		&patch.PatchFormat,
		&patch.VolumeID,
		&patch.PathOverride,
		&patch.CreatedAt,
		&patch.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return patch, nil
}

func (r *ConfigurationPatchRepository) GetByStrategyAndEntity(ctx context.Context, strategyID int64, patchLevel string, entityID int64) (*manman.ConfigurationPatch, error) {
	patch := &manman.ConfigurationPatch{}

	query := `
		SELECT patch_id, strategy_id, patch_level, entity_id, patch_content, patch_format, volume_id, path_override, created_at, updated_at
		FROM configuration_patches
		WHERE strategy_id = $1 AND patch_level = $2 AND entity_id = $3
	`

	err := r.db.QueryRow(ctx, query, strategyID, patchLevel, entityID).Scan(
		&patch.PatchID,
		&patch.StrategyID,
		&patch.PatchLevel,
		&patch.EntityID,
		&patch.PatchContent,
		&patch.PatchFormat,
		&patch.VolumeID,
		&patch.PathOverride,
		&patch.CreatedAt,
		&patch.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return patch, nil
}

func (r *ConfigurationPatchRepository) List(ctx context.Context, strategyID *int64, patchLevel *string, entityID *int64) ([]*manman.ConfigurationPatch, error) {
	query := `
		SELECT patch_id, strategy_id, patch_level, entity_id, patch_content, patch_format, volume_id, path_override, created_at, updated_at
		FROM configuration_patches
		WHERE ($1::bigint IS NULL OR strategy_id = $1)
		  AND ($2::varchar IS NULL OR patch_level = $2)
		  AND ($3::bigint IS NULL OR entity_id = $3)
		ORDER BY patch_id
	`

	rows, err := r.db.Query(ctx, query, strategyID, patchLevel, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var patches []*manman.ConfigurationPatch
	for rows.Next() {
		patch := &manman.ConfigurationPatch{}
		err := rows.Scan(
			&patch.PatchID,
			&patch.StrategyID,
			&patch.PatchLevel,
			&patch.EntityID,
			&patch.PatchContent,
			&patch.PatchFormat,
			&patch.VolumeID,
			&patch.PathOverride,
			&patch.CreatedAt,
			&patch.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		patches = append(patches, patch)
	}

	return patches, rows.Err()
}

func (r *ConfigurationPatchRepository) Update(ctx context.Context, patch *manman.ConfigurationPatch) error {
	query := `
		UPDATE configuration_patches
		SET patch_content = $2, patch_format = $3, volume_id = $4, path_override = $5, updated_at = CURRENT_TIMESTAMP
		WHERE patch_id = $1
	`

	_, err := r.db.Exec(ctx, query,
		patch.PatchID,
		patch.PatchContent,
		patch.PatchFormat,
		patch.VolumeID,
		patch.PathOverride,
	)
	return err
}

func (r *ConfigurationPatchRepository) Delete(ctx context.Context, patchID int64) error {
	query := `DELETE FROM configuration_patches WHERE patch_id = $1`
	_, err := r.db.Exec(ctx, query, patchID)
	return err
}
