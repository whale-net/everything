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

func (r *ServerCapabilityRepository) Upsert(ctx context.Context, cap *manman.ServerCapability) error {
	query := `
		INSERT INTO server_capabilities (
			server_id, total_memory_mb, available_memory_mb,
			cpu_cores, available_cpu_millicores, docker_version, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (server_id) DO UPDATE SET
			total_memory_mb = EXCLUDED.total_memory_mb,
			available_memory_mb = EXCLUDED.available_memory_mb,
			cpu_cores = EXCLUDED.cpu_cores,
			available_cpu_millicores = EXCLUDED.available_cpu_millicores,
			docker_version = EXCLUDED.docker_version,
			updated_at = EXCLUDED.updated_at
	`

	now := time.Now()
	_, err := r.db.Exec(ctx, query,
		cap.ServerID,
		cap.TotalMemoryMB,
		cap.AvailableMemoryMB,
		cap.CPUCores,
		cap.AvailableCPUMillicores,
		cap.DockerVersion,
		now,
	)
	return err
}

func (r *ServerCapabilityRepository) Get(ctx context.Context, serverID int64) (*manman.ServerCapability, error) {
	cap := &manman.ServerCapability{}

	query := `
		SELECT server_id, total_memory_mb, available_memory_mb,
		       cpu_cores, available_cpu_millicores, docker_version, updated_at
		FROM server_capabilities
		WHERE server_id = $1
	`

	err := r.db.QueryRow(ctx, query, serverID).Scan(
		&cap.ServerID,
		&cap.TotalMemoryMB,
		&cap.AvailableMemoryMB,
		&cap.CPUCores,
		&cap.AvailableCPUMillicores,
		&cap.DockerVersion,
		&cap.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return cap, nil
}
