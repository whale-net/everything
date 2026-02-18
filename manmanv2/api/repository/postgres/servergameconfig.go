package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type ServerGameConfigRepository struct {
	db *pgxpool.Pool
}

func NewServerGameConfigRepository(db *pgxpool.Pool) *ServerGameConfigRepository {
	return &ServerGameConfigRepository{db: db}
}

func (r *ServerGameConfigRepository) Create(ctx context.Context, sgc *manman.ServerGameConfig) (*manman.ServerGameConfig, error) {
	query := `
		INSERT INTO server_game_configs (server_id, game_config_id, port_bindings, status)
		VALUES ($1, $2, $3, $4)
		RETURNING sgc_id
	`

	err := r.db.QueryRow(ctx, query,
		sgc.ServerID,
		sgc.GameConfigID,
		sgc.PortBindings,
		sgc.Status,
	).Scan(&sgc.SGCID)
	if err != nil {
		return nil, err
	}

	return sgc, nil
}

func (r *ServerGameConfigRepository) Get(ctx context.Context, sgcID int64) (*manman.ServerGameConfig, error) {
	sgc := &manman.ServerGameConfig{}

	query := `
		SELECT sgc_id, server_id, game_config_id, port_bindings, status
		FROM server_game_configs
		WHERE sgc_id = $1
	`

	err := r.db.QueryRow(ctx, query, sgcID).Scan(
		&sgc.SGCID,
		&sgc.ServerID,
		&sgc.GameConfigID,
		&sgc.PortBindings,
		&sgc.Status,
	)
	if err != nil {
		return nil, err
	}

	return sgc, nil
}

func (r *ServerGameConfigRepository) List(ctx context.Context, serverID *int64, limit, offset int) ([]*manman.ServerGameConfig, error) {
	if limit <= 0 {
		limit = 50
	}

	var query string
	var args []interface{}

	if serverID != nil {
		query = `
			SELECT sgc_id, server_id, game_config_id, port_bindings, status
			FROM server_game_configs
			WHERE server_id = $1
			ORDER BY sgc_id
			LIMIT $2 OFFSET $3
		`
		args = []interface{}{*serverID, limit, offset}
	} else {
		query = `
			SELECT sgc_id, server_id, game_config_id, port_bindings, status
			FROM server_game_configs
			ORDER BY sgc_id
			LIMIT $1 OFFSET $2
		`
		args = []interface{}{limit, offset}
	}

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sgcs []*manman.ServerGameConfig
	for rows.Next() {
		sgc := &manman.ServerGameConfig{}
		err := rows.Scan(
			&sgc.SGCID,
			&sgc.ServerID,
			&sgc.GameConfigID,
			&sgc.PortBindings,
			&sgc.Status,
		)
		if err != nil {
			return nil, err
		}
		sgcs = append(sgcs, sgc)
	}

	return sgcs, rows.Err()
}

func (r *ServerGameConfigRepository) Update(ctx context.Context, sgc *manman.ServerGameConfig) error {
	query := `
		UPDATE server_game_configs
		SET port_bindings = $2, status = $3
		WHERE sgc_id = $1
	`

	_, err := r.db.Exec(ctx, query,
		sgc.SGCID,
		sgc.PortBindings,
		sgc.Status,
	)
	return err
}

func (r *ServerGameConfigRepository) Delete(ctx context.Context, sgcID int64) error {
	query := `DELETE FROM server_game_configs WHERE sgc_id = $1`
	_, err := r.db.Exec(ctx, query, sgcID)
	return err
}

func (r *ServerGameConfigRepository) AddLibrary(ctx context.Context, sgcID, libraryID int64) error {
	query := `INSERT INTO sgc_workshop_libraries (sgc_id, library_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`
	_, err := r.db.Exec(ctx, query, sgcID, libraryID)
	return err
}

func (r *ServerGameConfigRepository) ListLibraries(ctx context.Context, sgcID int64) ([]*manman.WorkshopLibrary, error) {
	query := `
		SELECT wl.library_id, wl.game_id, wl.name, wl.description, wl.created_at, wl.updated_at
		FROM workshop_libraries wl
		INNER JOIN sgc_workshop_libraries swl ON wl.library_id = swl.library_id
		WHERE swl.sgc_id = $1
		ORDER BY wl.library_id
	`

	rows, err := r.db.Query(ctx, query, sgcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var libraries []*manman.WorkshopLibrary
	for rows.Next() {
		lib := &manman.WorkshopLibrary{}
		err := rows.Scan(
			&lib.LibraryID,
			&lib.GameID,
			&lib.Name,
			&lib.Description,
			&lib.CreatedAt,
			&lib.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, lib)
	}

	return libraries, rows.Err()
}
