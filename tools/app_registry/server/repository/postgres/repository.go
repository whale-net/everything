// Package postgres implements the App Registry's repository interfaces
// against Postgres via pgx. Empty in AR-1 — see ../repository.go.
package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// NewRepository creates a Repository backed by the given connection pool.
func NewRepository(pool *pgxpool.Pool) *repository.Repository {
	_ = pool // wired into per-entity repositories starting AR-2
	return &repository.Repository{}
}
