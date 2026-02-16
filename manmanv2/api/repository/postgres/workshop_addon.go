package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type WorkshopAddonRepository struct {
	db *pgxpool.Pool
}

func NewWorkshopAddonRepository(db *pgxpool.Pool) *WorkshopAddonRepository {
	return &WorkshopAddonRepository{db: db}
}

func (r *WorkshopAddonRepository) Create(ctx context.Context, addon *manman.WorkshopAddon) (*manman.WorkshopAddon, error) {
	query := `
		INSERT INTO workshop_addons (
			game_id, workshop_id, platform_type, name, description,
			file_size_bytes, installation_path, is_collection, is_deprecated,
			metadata, last_updated
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING addon_id, created_at, updated_at
	`

	err := r.db.QueryRow(
		ctx, query,
		addon.GameID,
		addon.WorkshopID,
		addon.PlatformType,
		addon.Name,
		addon.Description,
		addon.FileSizeBytes,
		addon.InstallationPath,
		addon.IsCollection,
		addon.IsDeprecated,
		addon.Metadata,
		addon.LastUpdated,
	).Scan(&addon.AddonID, &addon.CreatedAt, &addon.UpdatedAt)

	if err != nil {
		return nil, err
	}

	return addon, nil
}

func (r *WorkshopAddonRepository) Get(ctx context.Context, addonID int64) (*manman.WorkshopAddon, error) {
	addon := &manman.WorkshopAddon{}

	query := `
		SELECT addon_id, game_id, workshop_id, platform_type, name, description,
			   file_size_bytes, installation_path, is_collection, is_deprecated,
			   metadata, last_updated, created_at, updated_at
		FROM workshop_addons
		WHERE addon_id = $1
	`

	err := r.db.QueryRow(ctx, query, addonID).Scan(
		&addon.AddonID,
		&addon.GameID,
		&addon.WorkshopID,
		&addon.PlatformType,
		&addon.Name,
		&addon.Description,
		&addon.FileSizeBytes,
		&addon.InstallationPath,
		&addon.IsCollection,
		&addon.IsDeprecated,
		&addon.Metadata,
		&addon.LastUpdated,
		&addon.CreatedAt,
		&addon.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return addon, nil
}

func (r *WorkshopAddonRepository) GetByWorkshopID(ctx context.Context, gameID int64, workshopID string, platformType string) (*manman.WorkshopAddon, error) {
	addon := &manman.WorkshopAddon{}

	query := `
		SELECT addon_id, game_id, workshop_id, platform_type, name, description,
			   file_size_bytes, installation_path, is_collection, is_deprecated,
			   metadata, last_updated, created_at, updated_at
		FROM workshop_addons
		WHERE game_id = $1 AND workshop_id = $2 AND platform_type = $3
	`

	err := r.db.QueryRow(ctx, query, gameID, workshopID, platformType).Scan(
		&addon.AddonID,
		&addon.GameID,
		&addon.WorkshopID,
		&addon.PlatformType,
		&addon.Name,
		&addon.Description,
		&addon.FileSizeBytes,
		&addon.InstallationPath,
		&addon.IsCollection,
		&addon.IsDeprecated,
		&addon.Metadata,
		&addon.LastUpdated,
		&addon.CreatedAt,
		&addon.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return addon, nil
}

func (r *WorkshopAddonRepository) List(ctx context.Context, gameID *int64, includeDeprecated bool, limit, offset int) ([]*manman.WorkshopAddon, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT addon_id, game_id, workshop_id, platform_type, name, description,
			   file_size_bytes, installation_path, is_collection, is_deprecated,
			   metadata, last_updated, created_at, updated_at
		FROM workshop_addons
		WHERE ($1::bigint IS NULL OR game_id = $1)
		  AND ($2 = true OR is_deprecated = false)
		ORDER BY addon_id
		LIMIT $3 OFFSET $4
	`

	rows, err := r.db.Query(ctx, query, gameID, includeDeprecated, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var addons []*manman.WorkshopAddon
	for rows.Next() {
		addon := &manman.WorkshopAddon{}
		err := rows.Scan(
			&addon.AddonID,
			&addon.GameID,
			&addon.WorkshopID,
			&addon.PlatformType,
			&addon.Name,
			&addon.Description,
			&addon.FileSizeBytes,
			&addon.InstallationPath,
			&addon.IsCollection,
			&addon.IsDeprecated,
			&addon.Metadata,
			&addon.LastUpdated,
			&addon.CreatedAt,
			&addon.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		addons = append(addons, addon)
	}

	return addons, rows.Err()
}

func (r *WorkshopAddonRepository) Update(ctx context.Context, addon *manman.WorkshopAddon) error {
	query := `
		UPDATE workshop_addons
		SET name = $2, description = $3, file_size_bytes = $4,
		    installation_path = $5, is_collection = $6, is_deprecated = $7,
		    metadata = $8, last_updated = $9, updated_at = CURRENT_TIMESTAMP
		WHERE addon_id = $1
	`

	_, err := r.db.Exec(
		ctx, query,
		addon.AddonID,
		addon.Name,
		addon.Description,
		addon.FileSizeBytes,
		addon.InstallationPath,
		addon.IsCollection,
		addon.IsDeprecated,
		addon.Metadata,
		addon.LastUpdated,
	)
	return err
}

func (r *WorkshopAddonRepository) Delete(ctx context.Context, addonID int64) error {
	query := `DELETE FROM workshop_addons WHERE addon_id = $1`
	_, err := r.db.Exec(ctx, query, addonID)
	return err
}
