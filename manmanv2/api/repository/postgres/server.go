package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
)

type ServerRepository struct {
	db *pgxpool.Pool
}

func NewServerRepository(db *pgxpool.Pool) *ServerRepository {
	return &ServerRepository{db: db}
}

// serverSelectColumns is the column list shared by every SELECT below,
// including drain_state/drain_requested_at (#2360, manmanv2 M6).
const serverSelectColumns = `server_id, name, status, last_seen, is_default, host_public_address, drain_state, drain_requested_at`

// scanServer scans a row produced by a query selecting serverSelectColumns.
func scanServer(row pgx.Row) (*manman.Server, error) {
	server := &manman.Server{}
	err := row.Scan(
		&server.ServerID,
		&server.Name,
		&server.Status,
		&server.LastSeen,
		&server.IsDefault,
		&server.HostPublicAddress,
		&server.DrainState,
		&server.DrainRequestedAt,
	)
	if err != nil {
		return nil, err
	}
	return server, nil
}

func (r *ServerRepository) Create(ctx context.Context, name string) (*manman.Server, error) {
	server := &manman.Server{
		Name:       name,
		Status:     manman.ServerStatusOffline,
		IsDefault:  false, // Will be set after checking if first server
		DrainState: manman.ServerDrainStateSchedulable,
	}

	// Check if this will be the first server
	var serverCount int64
	countQuery := `SELECT COUNT(*) FROM servers`
	if err := r.db.QueryRow(ctx, countQuery).Scan(&serverCount); err != nil {
		return nil, err
	}

	// If no servers exist, make this the default
	if serverCount == 0 {
		server.IsDefault = true
	}

	query := `
		INSERT INTO servers (name, status, is_default)
		VALUES ($1, $2, $3)
		RETURNING server_id, created_at, updated_at, host_public_address
	`

	var createdAt, updatedAt time.Time
	err := r.db.QueryRow(ctx, query, server.Name, server.Status, server.IsDefault).Scan(
		&server.ServerID,
		&createdAt,
		&updatedAt,
		&server.HostPublicAddress,
	)
	if err != nil {
		return nil, err
	}

	return server, nil
}

func (r *ServerRepository) Get(ctx context.Context, serverID int64) (*manman.Server, error) {
	query := `
		SELECT ` + serverSelectColumns + `
		FROM servers
		WHERE server_id = $1
	`

	return scanServer(r.db.QueryRow(ctx, query, serverID))
}

func (r *ServerRepository) GetByName(ctx context.Context, name string) (*manman.Server, error) {
	query := `
		SELECT ` + serverSelectColumns + `
		FROM servers
		WHERE name = $1
	`

	return scanServer(r.db.QueryRow(ctx, query, name))
}

func (r *ServerRepository) List(ctx context.Context, limit, offset int) ([]*manman.Server, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT ` + serverSelectColumns + `
		FROM servers
		ORDER BY server_id
		LIMIT $1 OFFSET $2
	`

	rows, err := r.db.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*manman.Server
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}

	return servers, rows.Err()
}

func (r *ServerRepository) Update(ctx context.Context, server *manman.Server) error {
	query := `
		UPDATE servers
		SET name = $2, status = $3, is_default = $4, host_public_address = $5
		WHERE server_id = $1
	`

	_, err := r.db.Exec(ctx, query, server.ServerID, server.Name, server.Status, server.IsDefault, server.HostPublicAddress)
	return err
}

func (r *ServerRepository) Delete(ctx context.Context, serverID int64) error {
	query := `DELETE FROM servers WHERE server_id = $1`
	_, err := r.db.Exec(ctx, query, serverID)
	return err
}

func (r *ServerRepository) UpdateStatusAndLastSeen(ctx context.Context, serverID int64, status string, lastSeen time.Time) error {
	query := `
		UPDATE servers
		SET status = $2, last_seen = $3
		WHERE server_id = $1
		RETURNING server_id
	`

	var returnedID int64
	err := r.db.QueryRow(ctx, query, serverID, status, lastSeen).Scan(&returnedID)
	return err
}

func (r *ServerRepository) UpdateLastSeen(ctx context.Context, serverID int64, lastSeen time.Time) error {
	query := `
		UPDATE servers
		SET last_seen = $2
		WHERE server_id = $1
		RETURNING server_id
	`

	var returnedID int64
	err := r.db.QueryRow(ctx, query, serverID, lastSeen).Scan(&returnedID)
	return err
}

func (r *ServerRepository) ListStaleServers(ctx context.Context, thresholdSeconds int) ([]*manman.Server, error) {
	query := `
		SELECT ` + serverSelectColumns + `
		FROM servers
		WHERE status = $1
		  AND last_seen < NOW() - INTERVAL '1 second' * $2
		ORDER BY server_id
	`

	rows, err := r.db.Query(ctx, query, manman.ServerStatusOnline, thresholdSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*manman.Server
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}

	return servers, rows.Err()
}

// SetDrainState transitions a server's drain state (#2360, manmanv2 M6,
// C29 groundwork). Inert here -- callers (DrainServer/UndrainServer
// handlers) are the only writers; no cordon enforcement or eviction
// happens as a side effect of this write.
func (r *ServerRepository) SetDrainState(ctx context.Context, serverID int64, state string, requestedAt *time.Time) error {
	query := `
		UPDATE servers
		SET drain_state = $2, drain_requested_at = $3
		WHERE server_id = $1
		RETURNING server_id
	`

	var returnedID int64
	return r.db.QueryRow(ctx, query, serverID, state, requestedAt).Scan(&returnedID)
}

// ListByDrainState lists servers currently in the given drain state.
// Used by the dependent eviction task (#2360, manmanv2 M6).
func (r *ServerRepository) ListByDrainState(ctx context.Context, state string) ([]*manman.Server, error) {
	query := `
		SELECT ` + serverSelectColumns + `
		FROM servers
		WHERE drain_state = $1
		ORDER BY server_id
	`

	rows, err := r.db.Query(ctx, query, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*manman.Server
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}

	return servers, rows.Err()
}

func (r *ServerRepository) MarkServersOffline(ctx context.Context, serverIDs []int64) error {
	if len(serverIDs) == 0 {
		return nil
	}

	query := `
		UPDATE servers
		SET status = $1
		WHERE server_id = ANY($2)
	`

	_, err := r.db.Exec(ctx, query, manman.ServerStatusOffline, serverIDs)
	return err
}

// GetDefaultServer returns the default server
func (r *ServerRepository) GetDefaultServer(ctx context.Context) (*manman.Server, error) {
	query := `
		SELECT ` + serverSelectColumns + `
		FROM servers
		WHERE is_default = TRUE
		LIMIT 1
	`

	return scanServer(r.db.QueryRow(ctx, query))
}

// SetDefaultServer clears all default flags and sets the specified server as default
func (r *ServerRepository) SetDefaultServer(ctx context.Context, serverID int64) error {
	// Use a transaction to ensure atomicity
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Clear all defaults
	clearQuery := `UPDATE servers SET is_default = FALSE WHERE is_default = TRUE`
	if _, err := tx.Exec(ctx, clearQuery); err != nil {
		return err
	}

	// Set new default
	setQuery := `UPDATE servers SET is_default = TRUE WHERE server_id = $1`
	if _, err := tx.Exec(ctx, setQuery, serverID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
