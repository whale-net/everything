package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manman"
)

type ServerCapabilityRepository struct {
	db *pgxpool.Pool
}

func NewServerCapabilityRepository(db *pgxpool.Pool) *ServerCapabilityRepository {
	return &ServerCapabilityRepository{db: db}
}

func (r *ServerCapabilityRepository) Insert(ctx context.Context, cap *manman.ServerCapability) error {
	query := `
		INSERT INTO server_capabilities (
			server_id, total_memory_mb, available_memory_mb,
			cpu_cores, available_cpu_millicores, docker_version, recorded_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING capability_id, recorded_at
	`

	now := time.Now()
	err := r.db.QueryRow(ctx, query,
		cap.ServerID,
		cap.TotalMemoryMB,
		cap.AvailableMemoryMB,
		cap.CPUCores,
		cap.AvailableCPUMillicores,
		cap.DockerVersion,
		now,
	).Scan(&cap.CapabilityID, &cap.RecordedAt)
	return err
}

func (r *ServerCapabilityRepository) Get(ctx context.Context, serverID int64) (*manman.ServerCapability, error) {
	cap := &manman.ServerCapability{}

	query := `
		SELECT capability_id, server_id, total_memory_mb, available_memory_mb,
		       cpu_cores, available_cpu_millicores, docker_version, recorded_at
		FROM server_capabilities
		WHERE server_id = $1
		ORDER BY recorded_at DESC
		LIMIT 1
	`

	err := r.db.QueryRow(ctx, query, serverID).Scan(
		&cap.CapabilityID,
		&cap.ServerID,
		&cap.TotalMemoryMB,
		&cap.AvailableMemoryMB,
		&cap.CPUCores,
		&cap.AvailableCPUMillicores,
		&cap.DockerVersion,
		&cap.RecordedAt,
	)
	if err != nil {
		return nil, err
	}

	return cap, nil
}
