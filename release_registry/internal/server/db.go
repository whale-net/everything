// Package server provides the gRPC RegistryService implementation backed by Postgres.
package server

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/db"
)

type Server struct {
	pool *pgxpool.Pool
}

// NewServer creates a new Server. Pass nil pool for no DB dependency in stubs.
func NewServer(ctx context.Context, cfg db.Config) (*Server, error) {
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool}, nil
}
