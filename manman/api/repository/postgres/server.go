package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manman"
)

type ServerRepository struct {
	db *pgxpool.Pool
}

func NewServerRepository(db *pgxpool.Pool) *ServerRepository {
	return &ServerRepository{db: db}
}

func (r *ServerRepository) Create(ctx context.Context, name string) (*manman.Server, error) {
	server := &manman.Server{
		Name:   name,
		Status: manman.ServerStatusOffline,
	}

	query := `
		INSERT INTO servers (name, status)
		VALUES ($1, $2)
		RETURNING server_id, created_at, updated_at
	`

	var createdAt, updatedAt time.Time
	err := r.db.QueryRow(ctx, query, server.Name, server.Status).Scan(
		&server.ServerID,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}

	return server, nil
}

func (r *ServerRepository) Get(ctx context.Context, serverID int64) (*manman.Server, error) {
	server := &manman.Server{}

	query := `
		SELECT server_id, name, status, last_seen
		FROM servers
		WHERE server_id = $1
	`

	err := r.db.QueryRow(ctx, query, serverID).Scan(
		&server.ServerID,
		&server.Name,
		&server.Status,
		&server.LastSeen,
	)
	if err != nil {
		return nil, err
	}

	return server, nil
}

func (r *ServerRepository) List(ctx context.Context, limit, offset int) ([]*manman.Server, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT server_id, name, status, last_seen
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
		server := &manman.Server{}
		err := rows.Scan(
			&server.ServerID,
			&server.Name,
			&server.Status,
			&server.LastSeen,
		)
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
		SET name = $2, status = $3
		WHERE server_id = $1
	`

	_, err := r.db.Exec(ctx, query, server.ServerID, server.Name, server.Status)
	return err
}

func (r *ServerRepository) Delete(ctx context.Context, serverID int64) error {
	query := `DELETE FROM servers WHERE server_id = $1`
	_, err := r.db.Exec(ctx, query, serverID)
	return err
}
