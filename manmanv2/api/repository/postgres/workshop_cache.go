package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
)

// WorkshopCacheRepository is the postgres-backed implementation of
// repository.WorkshopCacheRepository. See repository.go for the interface
// contract; the concurrent-upsert convergence guarantee (NFR4) is
// implemented in this task's Implementation phase via
// INSERT ... ON CONFLICT (cache_key) DO UPDATE ... RETURNING *.
type WorkshopCacheRepository struct {
	db *pgxpool.Pool
}

func NewWorkshopCacheRepository(pool *pgxpool.Pool) *WorkshopCacheRepository {
	return &WorkshopCacheRepository{db: pool}
}

func (r *WorkshopCacheRepository) GetCacheEntryByKey(ctx context.Context, cacheKey string) (*manman.WorkshopCacheEntry, error) {
	return nil, fmt.Errorf("WorkshopCacheRepository.GetCacheEntryByKey: not implemented")
}

func (r *WorkshopCacheRepository) UpsertCacheEntry(ctx context.Context, entry *manman.WorkshopCacheEntry) (*manman.WorkshopCacheEntry, error) {
	return nil, fmt.Errorf("WorkshopCacheRepository.UpsertCacheEntry: not implemented")
}

func (r *WorkshopCacheRepository) ListCacheEntriesForWorkshopID(ctx context.Context, workshopID string) ([]*manman.WorkshopCacheEntry, error) {
	return nil, fmt.Errorf("WorkshopCacheRepository.ListCacheEntriesForWorkshopID: not implemented")
}

func (r *WorkshopCacheRepository) GetCacheEntry(ctx context.Context, cacheEntryID int64) (*manman.WorkshopCacheEntry, error) {
	return nil, fmt.Errorf("WorkshopCacheRepository.GetCacheEntry: not implemented")
}

func (r *WorkshopCacheRepository) TouchCacheEntryVerified(ctx context.Context, cacheEntryID int64, verifiedAt time.Time) error {
	return fmt.Errorf("WorkshopCacheRepository.TouchCacheEntryVerified: not implemented")
}

func (r *WorkshopCacheRepository) DeleteCacheEntry(ctx context.Context, cacheEntryID int64) error {
	return fmt.Errorf("WorkshopCacheRepository.DeleteCacheEntry: not implemented")
}

func (r *WorkshopCacheRepository) UpsertHostPresence(ctx context.Context, cacheEntryID, serverID int64) error {
	return fmt.Errorf("WorkshopCacheRepository.UpsertHostPresence: not implemented")
}

func (r *WorkshopCacheRepository) ListHostPresence(ctx context.Context, cacheEntryID int64) ([]*manman.WorkshopCacheHostPresence, error) {
	return nil, fmt.Errorf("WorkshopCacheRepository.ListHostPresence: not implemented")
}
