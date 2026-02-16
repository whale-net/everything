package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type WorkshopInstallationRepository struct {
	db *pgxpool.Pool
}

func NewWorkshopInstallationRepository(db *pgxpool.Pool) *WorkshopInstallationRepository {
	return &WorkshopInstallationRepository{db: db}
}

func (r *WorkshopInstallationRepository) Create(ctx context.Context, installation *manman.WorkshopInstallation) (*manman.WorkshopInstallation, error) {
	query := `
		INSERT INTO workshop_installations (
			sgc_id, addon_id, status, installation_path, progress_percent,
			error_message, download_started_at, download_completed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING installation_id, created_at, updated_at
	`

	err := r.db.QueryRow(
		ctx, query,
		installation.SGCID,
		installation.AddonID,
		installation.Status,
		installation.InstallationPath,
		installation.ProgressPercent,
		installation.ErrorMessage,
		installation.DownloadStartedAt,
		installation.DownloadCompletedAt,
	).Scan(&installation.InstallationID, &installation.CreatedAt, &installation.UpdatedAt)

	if err != nil {
		return nil, err
	}

	return installation, nil
}

func (r *WorkshopInstallationRepository) Get(ctx context.Context, installationID int64) (*manman.WorkshopInstallation, error) {
	installation := &manman.WorkshopInstallation{}

	query := `
		SELECT installation_id, sgc_id, addon_id, status, installation_path,
			   progress_percent, error_message, download_started_at,
			   download_completed_at, created_at, updated_at
		FROM workshop_installations
		WHERE installation_id = $1
	`

	err := r.db.QueryRow(ctx, query, installationID).Scan(
		&installation.InstallationID,
		&installation.SGCID,
		&installation.AddonID,
		&installation.Status,
		&installation.InstallationPath,
		&installation.ProgressPercent,
		&installation.ErrorMessage,
		&installation.DownloadStartedAt,
		&installation.DownloadCompletedAt,
		&installation.CreatedAt,
		&installation.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return installation, nil
}

func (r *WorkshopInstallationRepository) GetBySGCAndAddon(ctx context.Context, sgcID, addonID int64) (*manman.WorkshopInstallation, error) {
	installation := &manman.WorkshopInstallation{}

	query := `
		SELECT installation_id, sgc_id, addon_id, status, installation_path,
			   progress_percent, error_message, download_started_at,
			   download_completed_at, created_at, updated_at
		FROM workshop_installations
		WHERE sgc_id = $1 AND addon_id = $2
	`

	err := r.db.QueryRow(ctx, query, sgcID, addonID).Scan(
		&installation.InstallationID,
		&installation.SGCID,
		&installation.AddonID,
		&installation.Status,
		&installation.InstallationPath,
		&installation.ProgressPercent,
		&installation.ErrorMessage,
		&installation.DownloadStartedAt,
		&installation.DownloadCompletedAt,
		&installation.CreatedAt,
		&installation.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return installation, nil
}

func (r *WorkshopInstallationRepository) ListBySGC(ctx context.Context, sgcID int64, limit, offset int) ([]*manman.WorkshopInstallation, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT installation_id, sgc_id, addon_id, status, installation_path,
			   progress_percent, error_message, download_started_at,
			   download_completed_at, created_at, updated_at
		FROM workshop_installations
		WHERE sgc_id = $1
		ORDER BY installation_id
		LIMIT $2 OFFSET $3
	`

	rows, err := r.db.Query(ctx, query, sgcID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var installations []*manman.WorkshopInstallation
	for rows.Next() {
		installation := &manman.WorkshopInstallation{}
		err := rows.Scan(
			&installation.InstallationID,
			&installation.SGCID,
			&installation.AddonID,
			&installation.Status,
			&installation.InstallationPath,
			&installation.ProgressPercent,
			&installation.ErrorMessage,
			&installation.DownloadStartedAt,
			&installation.DownloadCompletedAt,
			&installation.CreatedAt,
			&installation.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}

	return installations, rows.Err()
}

func (r *WorkshopInstallationRepository) ListByAddon(ctx context.Context, addonID int64, limit, offset int) ([]*manman.WorkshopInstallation, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT installation_id, sgc_id, addon_id, status, installation_path,
			   progress_percent, error_message, download_started_at,
			   download_completed_at, created_at, updated_at
		FROM workshop_installations
		WHERE addon_id = $1
		ORDER BY installation_id
		LIMIT $2 OFFSET $3
	`

	rows, err := r.db.Query(ctx, query, addonID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var installations []*manman.WorkshopInstallation
	for rows.Next() {
		installation := &manman.WorkshopInstallation{}
		err := rows.Scan(
			&installation.InstallationID,
			&installation.SGCID,
			&installation.AddonID,
			&installation.Status,
			&installation.InstallationPath,
			&installation.ProgressPercent,
			&installation.ErrorMessage,
			&installation.DownloadStartedAt,
			&installation.DownloadCompletedAt,
			&installation.CreatedAt,
			&installation.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}

	return installations, rows.Err()
}

func (r *WorkshopInstallationRepository) UpdateStatus(ctx context.Context, installationID int64, status string, errorMsg *string) error {
	query := `
		UPDATE workshop_installations
		SET status = $2, error_message = $3, updated_at = CURRENT_TIMESTAMP
		WHERE installation_id = $1
	`

	_, err := r.db.Exec(ctx, query, installationID, status, errorMsg)
	return err
}

func (r *WorkshopInstallationRepository) UpdateProgress(ctx context.Context, installationID int64, percent int) error {
	query := `
		UPDATE workshop_installations
		SET progress_percent = $2, updated_at = CURRENT_TIMESTAMP
		WHERE installation_id = $1
	`

	_, err := r.db.Exec(ctx, query, installationID, percent)
	return err
}

func (r *WorkshopInstallationRepository) Delete(ctx context.Context, installationID int64) error {
	query := `DELETE FROM workshop_installations WHERE installation_id = $1`
	_, err := r.db.Exec(ctx, query, installationID)
	return err
}
