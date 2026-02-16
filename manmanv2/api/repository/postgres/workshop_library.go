package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type WorkshopLibraryRepository struct {
	db *pgxpool.Pool
}

func NewWorkshopLibraryRepository(db *pgxpool.Pool) *WorkshopLibraryRepository {
	return &WorkshopLibraryRepository{db: db}
}

// Create creates a new workshop library
func (r *WorkshopLibraryRepository) Create(ctx context.Context, library *manman.WorkshopLibrary) (*manman.WorkshopLibrary, error) {
	query := `
		INSERT INTO workshop_libraries (game_id, name, description)
		VALUES ($1, $2, $3)
		RETURNING library_id, created_at, updated_at
	`

	err := r.db.QueryRow(
		ctx, query,
		library.GameID,
		library.Name,
		library.Description,
	).Scan(&library.LibraryID, &library.CreatedAt, &library.UpdatedAt)

	if err != nil {
		return nil, err
	}

	return library, nil
}

// Get retrieves a workshop library by ID
func (r *WorkshopLibraryRepository) Get(ctx context.Context, libraryID int64) (*manman.WorkshopLibrary, error) {
	library := &manman.WorkshopLibrary{}

	query := `
		SELECT library_id, game_id, name, description, created_at, updated_at
		FROM workshop_libraries
		WHERE library_id = $1
	`

	err := r.db.QueryRow(ctx, query, libraryID).Scan(
		&library.LibraryID,
		&library.GameID,
		&library.Name,
		&library.Description,
		&library.CreatedAt,
		&library.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return library, nil
}

// List retrieves workshop libraries with optional game filtering
func (r *WorkshopLibraryRepository) List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.WorkshopLibrary, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT library_id, game_id, name, description, created_at, updated_at
		FROM workshop_libraries
		WHERE ($1::bigint IS NULL OR game_id = $1)
		ORDER BY library_id
		LIMIT $2 OFFSET $3
	`

	rows, err := r.db.Query(ctx, query, gameID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var libraries []*manman.WorkshopLibrary
	for rows.Next() {
		library := &manman.WorkshopLibrary{}
		err := rows.Scan(
			&library.LibraryID,
			&library.GameID,
			&library.Name,
			&library.Description,
			&library.CreatedAt,
			&library.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, library)
	}

	return libraries, rows.Err()
}

// Update updates a workshop library
func (r *WorkshopLibraryRepository) Update(ctx context.Context, library *manman.WorkshopLibrary) error {
	query := `
		UPDATE workshop_libraries
		SET name = $2, description = $3, updated_at = CURRENT_TIMESTAMP
		WHERE library_id = $1
	`

	_, err := r.db.Exec(
		ctx, query,
		library.LibraryID,
		library.Name,
		library.Description,
	)
	return err
}

// Delete deletes a workshop library
func (r *WorkshopLibraryRepository) Delete(ctx context.Context, libraryID int64) error {
	query := `DELETE FROM workshop_libraries WHERE library_id = $1`
	_, err := r.db.Exec(ctx, query, libraryID)
	return err
}

// AddAddon adds an addon to a library
func (r *WorkshopLibraryRepository) AddAddon(ctx context.Context, libraryID, addonID int64, displayOrder int) error {
	query := `
		INSERT INTO workshop_library_addons (library_id, addon_id, display_order)
		VALUES ($1, $2, $3)
		ON CONFLICT (library_id, addon_id) DO UPDATE
		SET display_order = EXCLUDED.display_order
	`

	_, err := r.db.Exec(ctx, query, libraryID, addonID, displayOrder)
	return err
}

// RemoveAddon removes an addon from a library
func (r *WorkshopLibraryRepository) RemoveAddon(ctx context.Context, libraryID, addonID int64) error {
	query := `
		DELETE FROM workshop_library_addons
		WHERE library_id = $1 AND addon_id = $2
	`

	_, err := r.db.Exec(ctx, query, libraryID, addonID)
	return err
}

// ListAddons retrieves all addons in a library
func (r *WorkshopLibraryRepository) ListAddons(ctx context.Context, libraryID int64) ([]*manman.WorkshopAddon, error) {
	query := `
		SELECT wa.addon_id, wa.game_id, wa.workshop_id, wa.platform_type, wa.name, wa.description,
		       wa.file_size_bytes, wa.installation_path, wa.is_collection, wa.is_deprecated,
		       wa.metadata, wa.last_updated, wa.created_at, wa.updated_at
		FROM workshop_addons wa
		INNER JOIN workshop_library_addons wla ON wa.addon_id = wla.addon_id
		WHERE wla.library_id = $1
		ORDER BY wla.display_order, wa.addon_id
	`

	rows, err := r.db.Query(ctx, query, libraryID)
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

// AddReference adds a reference from parent library to child library
func (r *WorkshopLibraryRepository) AddReference(ctx context.Context, parentLibraryID, childLibraryID int64) error {
	// First check for circular reference
	hasCircular, err := r.DetectCircularReference(ctx, parentLibraryID, childLibraryID)
	if err != nil {
		return fmt.Errorf("failed to check for circular reference: %w", err)
	}
	if hasCircular {
		return fmt.Errorf("circular reference detected: adding reference would create a cycle")
	}

	query := `
		INSERT INTO workshop_library_references (parent_library_id, child_library_id)
		VALUES ($1, $2)
		ON CONFLICT (parent_library_id, child_library_id) DO NOTHING
	`

	_, err = r.db.Exec(ctx, query, parentLibraryID, childLibraryID)
	return err
}

// RemoveReference removes a reference from parent library to child library
func (r *WorkshopLibraryRepository) RemoveReference(ctx context.Context, parentLibraryID, childLibraryID int64) error {
	query := `
		DELETE FROM workshop_library_references
		WHERE parent_library_id = $1 AND child_library_id = $2
	`

	_, err := r.db.Exec(ctx, query, parentLibraryID, childLibraryID)
	return err
}

// ListReferences retrieves all child libraries referenced by a parent library
func (r *WorkshopLibraryRepository) ListReferences(ctx context.Context, libraryID int64) ([]*manman.WorkshopLibrary, error) {
	query := `
		SELECT wl.library_id, wl.game_id, wl.name, wl.description, wl.created_at, wl.updated_at
		FROM workshop_libraries wl
		INNER JOIN workshop_library_references wlr ON wl.library_id = wlr.child_library_id
		WHERE wlr.parent_library_id = $1
		ORDER BY wl.library_id
	`

	rows, err := r.db.Query(ctx, query, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var libraries []*manman.WorkshopLibrary
	for rows.Next() {
		library := &manman.WorkshopLibrary{}
		err := rows.Scan(
			&library.LibraryID,
			&library.GameID,
			&library.Name,
			&library.Description,
			&library.CreatedAt,
			&library.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, library)
	}

	return libraries, rows.Err()
}

// DetectCircularReference checks if adding a reference would create a circular dependency
// It uses a recursive CTE to traverse the reference graph
func (r *WorkshopLibraryRepository) DetectCircularReference(ctx context.Context, parentLibraryID, childLibraryID int64) (bool, error) {
	// If parent and child are the same, it's a self-reference (circular)
	if parentLibraryID == childLibraryID {
		return true, nil
	}

	// Use recursive CTE to check if childLibraryID already references parentLibraryID
	// (directly or transitively). If it does, adding parentLibraryID -> childLibraryID
	// would create a cycle.
	query := `
		WITH RECURSIVE reference_chain AS (
			-- Base case: start from the child library
			SELECT child_library_id AS library_id
			FROM workshop_library_references
			WHERE parent_library_id = $1
			
			UNION
			
			-- Recursive case: follow references from current libraries
			SELECT wlr.child_library_id
			FROM workshop_library_references wlr
			INNER JOIN reference_chain rc ON wlr.parent_library_id = rc.library_id
		)
		SELECT EXISTS(
			SELECT 1 FROM reference_chain WHERE library_id = $2
		)
	`

	var hasCircular bool
	err := r.db.QueryRow(ctx, query, childLibraryID, parentLibraryID).Scan(&hasCircular)
	if err != nil {
		return false, err
	}

	return hasCircular, nil
}
