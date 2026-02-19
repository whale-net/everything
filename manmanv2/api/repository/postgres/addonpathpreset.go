package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	manman "github.com/whale-net/everything/manmanv2"
)

type AddonPathPresetRepository struct {
	db *pgxpool.Pool
}

func NewAddonPathPresetRepository(db *pgxpool.Pool) *AddonPathPresetRepository {
	return &AddonPathPresetRepository{db: db}
}

func (r *AddonPathPresetRepository) Create(ctx context.Context, preset *manman.GameAddonPathPreset) (*manman.GameAddonPathPreset, error) {
	query := `
		INSERT INTO game_addon_path_presets (game_id, name, description, volume_id, installation_path)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING preset_id, created_at
	`

	err := r.db.QueryRow(ctx, query,
		preset.GameID,
		preset.Name,
		preset.Description,
		preset.VolumeID,
		preset.InstallationPath,
	).Scan(&preset.PresetID, &preset.CreatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to create addon path preset: %w", err)
	}

	return preset, nil
}

func (r *AddonPathPresetRepository) Get(ctx context.Context, presetID int64) (*manman.GameAddonPathPreset, error) {
	query := `
		SELECT preset_id, game_id, name, description, volume_id, installation_path, created_at
		FROM game_addon_path_presets
		WHERE preset_id = $1
	`

	preset := &manman.GameAddonPathPreset{}
	err := r.db.QueryRow(ctx, query, presetID).Scan(
		&preset.PresetID,
		&preset.GameID,
		&preset.Name,
		&preset.Description,
		&preset.VolumeID,
		&preset.InstallationPath,
		&preset.CreatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get addon path preset: %w", err)
	}

	return preset, nil
}

func (r *AddonPathPresetRepository) ListByGame(ctx context.Context, gameID int64) ([]*manman.GameAddonPathPreset, error) {
	query := `
		SELECT preset_id, game_id, name, description, volume_id, installation_path, created_at
		FROM game_addon_path_presets
		WHERE game_id = $1
		ORDER BY name
	`

	rows, err := r.db.Query(ctx, query, gameID)
	if err != nil {
		return nil, fmt.Errorf("failed to list addon path presets: %w", err)
	}
	defer rows.Close()

	var presets []*manman.GameAddonPathPreset
	for rows.Next() {
		preset := &manman.GameAddonPathPreset{}
		err := rows.Scan(
			&preset.PresetID,
			&preset.GameID,
			&preset.Name,
			&preset.Description,
			&preset.VolumeID,
			&preset.InstallationPath,
			&preset.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan preset: %w", err)
		}
		presets = append(presets, preset)
	}

	return presets, nil
}

func (r *AddonPathPresetRepository) Update(ctx context.Context, preset *manman.GameAddonPathPreset) error {
	query := `
		UPDATE game_addon_path_presets
		SET name = $2, description = $3, volume_id = $4, installation_path = $5
		WHERE preset_id = $1
	`

	result, err := r.db.Exec(ctx, query,
		preset.PresetID,
		preset.Name,
		preset.Description,
		preset.VolumeID,
		preset.InstallationPath,
	)

	if err != nil {
		return fmt.Errorf("failed to update addon path preset: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("preset not found")
	}

	return nil
}

func (r *AddonPathPresetRepository) Delete(ctx context.Context, presetID int64) error {
	query := `DELETE FROM game_addon_path_presets WHERE preset_id = $1`

	result, err := r.db.Exec(ctx, query, presetID)
	if err != nil {
		return fmt.Errorf("failed to delete addon path preset: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("preset not found")
	}

	return nil
}
