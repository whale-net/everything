package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
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
			file_size_bytes, installation_path, preset_id,
			is_collection, is_deprecated, collection_id, metadata, last_updated
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
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
		addon.PresetID,
		addon.IsCollection,
		addon.IsDeprecated,
		addon.CollectionID,
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
			   file_size_bytes, installation_path, preset_id,
			   is_collection, is_deprecated, collection_id, metadata, last_updated, created_at, updated_at
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
		&addon.PresetID,
		&addon.IsCollection,
		&addon.IsDeprecated,
		&addon.CollectionID,
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
			   file_size_bytes, installation_path, preset_id,
			   is_collection, is_deprecated, collection_id, metadata, last_updated, created_at, updated_at
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
		&addon.PresetID,
		&addon.IsCollection,
		&addon.IsDeprecated,
		&addon.CollectionID,
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

// GetByWorkshopIDAnyGame resolves workshopID to its addon (joined with its
// game's steam_app_id) without a known game_id -- see the interface doc
// comment (repository.go) for why the on-demand verify RPC (#2186) needs
// this rather than GetByWorkshopID. A Steam Workshop item id is globally
// unique to the app it belongs to, so at most one row is expected; this
// takes the lowest addon_id if more than one somehow matches, for
// deterministic behavior. Returns (nil, nil) on no match.
func (r *WorkshopAddonRepository) GetByWorkshopIDAnyGame(ctx context.Context, workshopID string) (*manman.WorkshopAddonWithGame, error) {
	query := `
		SELECT wa.addon_id, wa.game_id, wa.workshop_id, wa.platform_type, wa.name, wa.description,
			   wa.file_size_bytes, wa.installation_path, wa.preset_id,
			   wa.is_collection, wa.is_deprecated, wa.collection_id, wa.metadata, wa.last_updated,
			   wa.created_at, wa.updated_at, g.steam_app_id
		FROM workshop_addons wa
		INNER JOIN games g ON wa.game_id = g.game_id
		WHERE wa.workshop_id = $1
		ORDER BY wa.addon_id
		LIMIT 1
	`

	addon := &manman.WorkshopAddonWithGame{}
	err := r.db.QueryRow(ctx, query, workshopID).Scan(
		&addon.AddonID,
		&addon.GameID,
		&addon.WorkshopID,
		&addon.PlatformType,
		&addon.Name,
		&addon.Description,
		&addon.FileSizeBytes,
		&addon.InstallationPath,
		&addon.PresetID,
		&addon.IsCollection,
		&addon.IsDeprecated,
		&addon.CollectionID,
		&addon.Metadata,
		&addon.LastUpdated,
		&addon.CreatedAt,
		&addon.UpdatedAt,
		&addon.SteamAppID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return addon, nil
}

// ListByCollectionID returns all addon rows created as children of the given collection addon.
func (r *WorkshopAddonRepository) ListByCollectionID(ctx context.Context, collectionID int64) ([]*manman.WorkshopAddon, error) {
	query := `
		SELECT addon_id, game_id, workshop_id, platform_type, name, description,
			   file_size_bytes, installation_path, preset_id,
			   is_collection, is_deprecated, collection_id, metadata, last_updated, created_at, updated_at
		FROM workshop_addons
		WHERE collection_id = $1
		ORDER BY addon_id
	`

	rows, err := r.db.Query(ctx, query, collectionID)
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
			&addon.PresetID,
			&addon.IsCollection,
			&addon.IsDeprecated,
			&addon.CollectionID,
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

func (r *WorkshopAddonRepository) List(ctx context.Context, gameID *int64, includeDeprecated bool, limit, offset int) ([]*manman.WorkshopAddon, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT addon_id, game_id, workshop_id, platform_type, name, description,
			   file_size_bytes, installation_path, preset_id,
			   is_collection, is_deprecated, collection_id, metadata, last_updated, created_at, updated_at
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
			&addon.PresetID,
			&addon.IsCollection,
			&addon.IsDeprecated,
			&addon.CollectionID,
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
		    installation_path = $5, preset_id = $6,
		    is_collection = $7, is_deprecated = $8, collection_id = $9, metadata = $10,
		    last_updated = $11, updated_at = CURRENT_TIMESTAMP
		WHERE addon_id = $1
	`

	_, err := r.db.Exec(
		ctx, query,
		addon.AddonID,
		addon.Name,
		addon.Description,
		addon.FileSizeBytes,
		addon.InstallationPath,
		addon.PresetID,
		addon.IsCollection,
		addon.IsDeprecated,
		addon.CollectionID,
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
