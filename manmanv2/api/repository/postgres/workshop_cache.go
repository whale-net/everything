package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
)

// WorkshopCacheRepository is the postgres-backed implementation of
// repository.WorkshopCacheRepository. See repository.go for the interface
// contract; the concurrent-upsert convergence guarantee (NFR4) is
// implemented here via INSERT ... ON CONFLICT (cache_key) DO UPDATE ...
// RETURNING *, so two racing writers for the same (workshop_id,
// content_version) always converge on the same row instead of erroring.
type WorkshopCacheRepository struct {
	db *pgxpool.Pool
}

func NewWorkshopCacheRepository(pool *pgxpool.Pool) *WorkshopCacheRepository {
	return &WorkshopCacheRepository{db: pool}
}

const workshopCacheEntryColumns = `
	cache_entry_id, workshop_id, content_version, cache_key, s3_key,
	size_bytes, last_verified_at, created_at
`

func scanWorkshopCacheEntry(row pgx.Row) (*manman.WorkshopCacheEntry, error) {
	e := &manman.WorkshopCacheEntry{}
	err := row.Scan(
		&e.CacheEntryID,
		&e.WorkshopID,
		&e.ContentVersion,
		&e.CacheKey,
		&e.S3Key,
		&e.SizeBytes,
		&e.LastVerifiedAt,
		&e.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (r *WorkshopCacheRepository) GetCacheEntryByKey(ctx context.Context, cacheKey string) (*manman.WorkshopCacheEntry, error) {
	query := `
		SELECT ` + workshopCacheEntryColumns + `
		FROM workshop_cache_entries
		WHERE cache_key = $1
	`
	e, err := scanWorkshopCacheEntry(r.db.QueryRow(ctx, query, cacheKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

// UpsertCacheEntry is an idempotent insert-or-return-existing on cache_key
// (NFR4 foundation): a concurrent second writer racing to cache the same
// (workshop_id, content_version) gets the existing row back -- with its
// size_bytes preserved unless the incoming value supplies one -- instead of
// a unique-violation error.
func (r *WorkshopCacheRepository) UpsertCacheEntry(ctx context.Context, entry *manman.WorkshopCacheEntry) (*manman.WorkshopCacheEntry, error) {
	query := `
		INSERT INTO workshop_cache_entries (workshop_id, content_version, cache_key, s3_key, size_bytes)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (cache_key) DO UPDATE
			SET size_bytes = COALESCE(EXCLUDED.size_bytes, workshop_cache_entries.size_bytes)
		RETURNING ` + workshopCacheEntryColumns

	return scanWorkshopCacheEntry(r.db.QueryRow(
		ctx, query,
		entry.WorkshopID,
		entry.ContentVersion,
		entry.CacheKey,
		entry.S3Key,
		entry.SizeBytes,
	))
}

// ListCacheEntriesForWorkshopID returns every cache entry for workshopID,
// newest-first by created_at (FR10: an Admin scanning an addon's cache wants
// its most recent content version at the top). cache_entry_id is a
// BIGSERIAL and so is monotonic with insertion order too, but created_at is
// the documented, semantically-meaningful sort key -- order by it directly
// rather than relying on that correlation.
func (r *WorkshopCacheRepository) ListCacheEntriesForWorkshopID(ctx context.Context, workshopID string) ([]*manman.WorkshopCacheEntry, error) {
	query := `
		SELECT ` + workshopCacheEntryColumns + `
		FROM workshop_cache_entries
		WHERE workshop_id = $1
		ORDER BY created_at DESC, cache_entry_id DESC
	`
	rows, err := r.db.Query(ctx, query, workshopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*manman.WorkshopCacheEntry
	for rows.Next() {
		e, err := scanWorkshopCacheEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (r *WorkshopCacheRepository) GetCacheEntry(ctx context.Context, cacheEntryID int64) (*manman.WorkshopCacheEntry, error) {
	query := `
		SELECT ` + workshopCacheEntryColumns + `
		FROM workshop_cache_entries
		WHERE cache_entry_id = $1
	`
	e, err := scanWorkshopCacheEntry(r.db.QueryRow(ctx, query, cacheEntryID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *WorkshopCacheRepository) TouchCacheEntryVerified(ctx context.Context, cacheEntryID int64, verifiedAt time.Time) error {
	query := `
		UPDATE workshop_cache_entries
		SET last_verified_at = $2
		WHERE cache_entry_id = $1
	`
	_, err := r.db.Exec(ctx, query, cacheEntryID, verifiedAt)
	return err
}

// DeleteCacheEntry is the explicit Admin eviction path (FR12) -- there is no
// automatic garbage collection of superseded entries in this layer. The
// ON DELETE CASCADE on workshop_cache_host_presence (migration 040) removes
// any presence rows for this entry along with it.
func (r *WorkshopCacheRepository) DeleteCacheEntry(ctx context.Context, cacheEntryID int64) error {
	_, err := r.db.Exec(ctx, `DELETE FROM workshop_cache_entries WHERE cache_entry_id = $1`, cacheEntryID)
	return err
}

func (r *WorkshopCacheRepository) UpsertHostPresence(ctx context.Context, cacheEntryID, serverID int64) error {
	query := `
		INSERT INTO workshop_cache_host_presence (cache_entry_id, server_id)
		VALUES ($1, $2)
		ON CONFLICT (cache_entry_id, server_id) DO UPDATE
			SET last_seen_at = now()
	`
	_, err := r.db.Exec(ctx, query, cacheEntryID, serverID)
	return err
}

func (r *WorkshopCacheRepository) ListHostPresence(ctx context.Context, cacheEntryID int64) ([]*manman.WorkshopCacheHostPresence, error) {
	query := `
		SELECT cache_entry_id, server_id, first_seen_at, last_seen_at
		FROM workshop_cache_host_presence
		WHERE cache_entry_id = $1
		ORDER BY server_id
	`
	rows, err := r.db.Query(ctx, query, cacheEntryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var presence []*manman.WorkshopCacheHostPresence
	for rows.Next() {
		p := &manman.WorkshopCacheHostPresence{}
		if err := rows.Scan(&p.CacheEntryID, &p.ServerID, &p.FirstSeenAt, &p.LastSeenAt); err != nil {
			return nil, err
		}
		presence = append(presence, p)
	}
	return presence, rows.Err()
}

// ListHostPresenceForCacheEntryIDs is the FR10 fleet-visibility read path:
// one query (ANY($1) over cacheEntryIDs, joined with servers for a display
// name) regardless of how many entries are being rendered -- the query count
// must not scale with entry count. An empty input returns an empty map with
// no query at all.
func (r *WorkshopCacheRepository) ListHostPresenceForCacheEntryIDs(ctx context.Context, cacheEntryIDs []int64) (map[int64][]*manman.WorkshopCacheHostPresenceWithServer, error) {
	result := make(map[int64][]*manman.WorkshopCacheHostPresenceWithServer)
	if len(cacheEntryIDs) == 0 {
		return result, nil
	}

	query := `
		SELECT p.cache_entry_id, p.server_id, s.name, p.first_seen_at, p.last_seen_at
		FROM workshop_cache_host_presence p
		JOIN servers s ON s.server_id = p.server_id
		WHERE p.cache_entry_id = ANY($1)
		ORDER BY p.cache_entry_id, s.name
	`
	rows, err := r.db.Query(ctx, query, cacheEntryIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		p := &manman.WorkshopCacheHostPresenceWithServer{}
		if err := rows.Scan(&p.CacheEntryID, &p.ServerID, &p.ServerName, &p.FirstSeenAt, &p.LastSeenAt); err != nil {
			return nil, err
		}
		result[p.CacheEntryID] = append(result[p.CacheEntryID], p)
	}
	return result, rows.Err()
}
