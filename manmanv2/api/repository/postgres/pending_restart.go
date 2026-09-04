package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
)

// PendingRestartRepository is the postgres-backed implementation of
// repository.PendingRestartRepository. See repository.go for the interface
// contract; the atomicity requirements on ClaimForSession and ExpireStalled
// (single UPDATE ... RETURNING, never SELECT-then-UPDATE) are load-bearing
// for NFR10 and are implemented in this task's Implementation phase.
type PendingRestartRepository struct {
	db *pgxpool.Pool
}

func NewPendingRestartRepository(db *pgxpool.Pool) *PendingRestartRepository {
	return &PendingRestartRepository{db: db}
}

func (r *PendingRestartRepository) Create(ctx context.Context, sgcID, gatingSessionID int64, stallDeadline time.Time) (*manman.PendingRestart, error) {
	return nil, fmt.Errorf("PendingRestartRepository.Create: not implemented")
}

func (r *PendingRestartRepository) ClaimForSession(ctx context.Context, gatingSessionID int64) (*manman.PendingRestart, error) {
	return nil, fmt.Errorf("PendingRestartRepository.ClaimForSession: not implemented")
}

func (r *PendingRestartRepository) MarkStarted(ctx context.Context, pendingRestartID, startedSessionID int64) error {
	return fmt.Errorf("PendingRestartRepository.MarkStarted: not implemented")
}

func (r *PendingRestartRepository) MarkFailed(ctx context.Context, pendingRestartID int64, reason string) error {
	return fmt.Errorf("PendingRestartRepository.MarkFailed: not implemented")
}

func (r *PendingRestartRepository) ExpireStalled(ctx context.Context, now time.Time) ([]*manman.PendingRestart, error) {
	return nil, fmt.Errorf("PendingRestartRepository.ExpireStalled: not implemented")
}

func (r *PendingRestartRepository) GetLatestBySGCIDs(ctx context.Context, sgcIDs []int64) (map[int64]*manman.PendingRestart, error) {
	return nil, fmt.Errorf("PendingRestartRepository.GetLatestBySGCIDs: not implemented")
}
