// Package postgres implements the App Registry's repository interfaces
// against Postgres via pgx, following the manmanv2/api/repository/postgres
// pattern: one file per entity, domain structs (not proto types) in and out.
//
// NOTE ON TEST COVERAGE: this package compiles under `bazel build` and its
// SQL is exercised transitively by any manual/local run against a real
// Postgres instance, but there is no automated `bazel test` coverage against
// a live database in this change — see server/repository/fake/fake.go's
// doc comment for why, and the AR-2a report for the explicit breakdown of
// what runs in CI. The business logic this SQL implements (reconcile
// diffing, promotability derivation, idempotency, chart/image validation) is
// covered by handlers tests against repository/fake instead.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// dbtx is the subset of *pgxpool.Pool and pgx.Tx this package needs. Every
// per-entity repository is constructed with one, so the same query code runs
// unchanged whether it's part of a WithTx transaction or a bare read against
// the pool.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Registry is the pgx-backed repository.Registry.
type Registry struct {
	pool *pgxpool.Pool
	ex   dbtx
}

// NewRepository creates a Registry backed by the given connection pool.
func NewRepository(pool *pgxpool.Pool) *Registry {
	return &Registry{pool: pool, ex: pool}
}

func (r *Registry) Apps() repository.AppRepository                { return &appRepo{ex: r.ex} }
func (r *Registry) Builds() repository.BuildRepository            { return &buildRepo{ex: r.ex} }
func (r *Registry) Artifacts() repository.ArtifactRepository      { return &artifactRepo{ex: r.ex} }
func (r *Registry) Idempotency() repository.IdempotencyRepository { return &idempotencyRepo{ex: r.ex} }
func (r *Registry) Environments() repository.EnvironmentRepository {
	return &environmentRepo{ex: r.ex}
}

// WithTx runs fn inside a single Postgres transaction, committing iff fn
// returns nil and rolling back otherwise. This is the atomicity boundary
// ARCHITECTURE.md "Idempotency" depends on: the business write and the
// idempotency-key row land in the same commit.
func (r *Registry) WithTx(ctx context.Context, fn func(ctx context.Context, reg repository.Registry) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	txReg := &Registry{pool: r.pool, ex: tx}
	if err := fn(ctx, txReg); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}
