package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type GameConfigVolumeRepository struct {
	db *pgxpool.Pool
}

func NewGameConfigVolumeRepository(db *pgxpool.Pool) *GameConfigVolumeRepository {
	return &GameConfigVolumeRepository{db: db}
}

func (r *GameConfigVolumeRepository) Create(ctx context.Context, volume *manman.GameConfigVolume) (*manman.GameConfigVolume, error) {
	query := `
		INSERT INTO game_config_volumes (config_id, name, description, container_path, host_subpath, read_only)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING volume_id, created_at
	`

	err := r.db.QueryRow(ctx, query,
		volume.ConfigID,
		volume.Name,
		volume.Description,
		volume.ContainerPath,
		volume.HostSubpath,
		volume.ReadOnly,
	).Scan(&volume.VolumeID, &volume.CreatedAt)
	if err != nil {
		return nil, err
	}

	return volume, nil
}

func (r *GameConfigVolumeRepository) Get(ctx context.Context, volumeID int64) (*manman.GameConfigVolume, error) {
	volume := &manman.GameConfigVolume{}

	query := `
		SELECT volume_id, config_id, name, description, container_path, host_subpath, read_only, created_at
		FROM game_config_volumes
		WHERE volume_id = $1
	`

	err := r.db.QueryRow(ctx, query, volumeID).Scan(
		&volume.VolumeID,
		&volume.ConfigID,
		&volume.Name,
		&volume.Description,
		&volume.ContainerPath,
		&volume.HostSubpath,
		&volume.ReadOnly,
		&volume.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	return volume, nil
}

func (r *GameConfigVolumeRepository) ListByGameConfig(ctx context.Context, configID int64) ([]*manman.GameConfigVolume, error) {
	query := `
		SELECT volume_id, config_id, name, description, container_path, host_subpath, read_only, created_at
		FROM game_config_volumes
		WHERE config_id = $1
		ORDER BY volume_id
	`

	rows, err := r.db.Query(ctx, query, configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var volumes []*manman.GameConfigVolume
	for rows.Next() {
		volume := &manman.GameConfigVolume{}
		err := rows.Scan(
			&volume.VolumeID,
			&volume.ConfigID,
			&volume.Name,
			&volume.Description,
			&volume.ContainerPath,
			&volume.HostSubpath,
			&volume.ReadOnly,
			&volume.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, volume)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return volumes, nil
}

func (r *GameConfigVolumeRepository) Update(ctx context.Context, volume *manman.GameConfigVolume) error {
	query := `
		UPDATE game_config_volumes
		SET name = $2, description = $3, container_path = $4, host_subpath = $5, read_only = $6
		WHERE volume_id = $1
	`

	_, err := r.db.Exec(ctx, query,
		volume.VolumeID,
		volume.Name,
		volume.Description,
		volume.ContainerPath,
		volume.HostSubpath,
		volume.ReadOnly,
	)

	return err
}

func (r *GameConfigVolumeRepository) Delete(ctx context.Context, volumeID int64) error {
	query := `DELETE FROM game_config_volumes WHERE volume_id = $1`
	_, err := r.db.Exec(ctx, query, volumeID)
	return err
}
