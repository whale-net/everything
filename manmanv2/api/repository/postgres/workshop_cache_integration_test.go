//go:build integration

// This file only builds under the "integration" build tag, same as
// session_repository_integration_test.go's precedent for this package --
// see //libs/go/dbtest's README for how to run it. It exercises the actual
// DB-enforced convergence guarantee NFR4 depends on: two writers racing to
// cache the same (workshop_id, content_version) must both succeed and land
// on the exact same row, via UpsertCacheEntry's
// INSERT ... ON CONFLICT (cache_key) DO UPDATE ... RETURNING -- something an
// in-memory fake can't verify because it wouldn't be exercising the same
// unique-constraint race Postgres itself resolves.
//
// Schema here is hand-written, self-contained DDL mirroring exactly the
// pieces of manmanv2/migrate/migrations/040_workshop_cache_entries.up.sql
// this test needs (workshop_cache_entries, plus servers for the presence
// FK), per dbtest's README ("Options.Schema should be self-contained DDL --
// do not depend on another package's migrations").
package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	manman "github.com/whale-net/everything/manmanv2/models"
)

const workshopCacheSchema = `
	CREATE TABLE servers (
		server_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE workshop_cache_entries (
		cache_entry_id BIGSERIAL PRIMARY KEY,
		workshop_id TEXT NOT NULL,
		content_version TEXT NOT NULL,
		cache_key TEXT NOT NULL,
		s3_key TEXT NOT NULL,
		size_bytes BIGINT,
		last_verified_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT workshop_cache_entries_cache_key_key UNIQUE (cache_key)
	);

	CREATE TABLE workshop_cache_host_presence (
		cache_entry_id BIGINT NOT NULL REFERENCES workshop_cache_entries(cache_entry_id) ON DELETE CASCADE,
		server_id BIGINT NOT NULL REFERENCES servers(server_id),
		first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (cache_entry_id, server_id)
	);
`

func newWorkshopCacheTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: workshopCacheSchema})
	return db.Pool
}

// TestUpsertCacheEntry_DuplicateCallIsIdempotent proves the sequential half
// of NFR4: calling UpsertCacheEntry twice for the same cache_key returns
// the same cache_entry_id both times, with no error on the second call.
func TestUpsertCacheEntry_DuplicateCallIsIdempotent(t *testing.T) {
	pool := newWorkshopCacheTestDB(t)
	ctx := context.Background()
	repo := NewWorkshopCacheRepository(pool)

	entry := &manman.WorkshopCacheEntry{
		WorkshopID:     "123456789",
		ContentVersion: "20240102",
		CacheKey:       "ws/123456789/20240102",
		S3Key:          "workshop-cache/123456789/20240102.tar",
	}

	first, err := repo.UpsertCacheEntry(ctx, entry)
	if err != nil {
		t.Fatalf("first UpsertCacheEntry: %v", err)
	}
	if first.CacheEntryID == 0 {
		t.Fatal("expected a non-zero cache_entry_id from the first upsert")
	}

	second, err := repo.UpsertCacheEntry(ctx, entry)
	if err != nil {
		t.Fatalf("second UpsertCacheEntry (duplicate cache_key) must not error, got: %v", err)
	}
	if second.CacheEntryID != first.CacheEntryID {
		t.Fatalf("expected the duplicate upsert to return the same cache_entry_id, got %d and %d", first.CacheEntryID, second.CacheEntryID)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM workshop_cache_entries WHERE cache_key = $1`, entry.CacheKey).Scan(&count); err != nil {
		t.Fatalf("count rows for cache_key: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row for cache_key %q after duplicate upsert, got %d", entry.CacheKey, count)
	}
}

// TestUpsertCacheEntry_PreservesExistingSizeBytesWhenIncomingIsNil proves
// the ON CONFLICT DO UPDATE clause's COALESCE: a second writer that doesn't
// yet know the object size (nil SizeBytes) does not clobber a first
// writer's already-recorded size.
func TestUpsertCacheEntry_PreservesExistingSizeBytesWhenIncomingIsNil(t *testing.T) {
	pool := newWorkshopCacheTestDB(t)
	ctx := context.Background()
	repo := NewWorkshopCacheRepository(pool)

	size := int64(4096)
	first, err := repo.UpsertCacheEntry(ctx, &manman.WorkshopCacheEntry{
		WorkshopID:     "111",
		ContentVersion: "1",
		CacheKey:       "ws/111/1",
		S3Key:          "workshop-cache/111/1.tar",
		SizeBytes:      &size,
	})
	if err != nil {
		t.Fatalf("first UpsertCacheEntry: %v", err)
	}
	if first.SizeBytes == nil || *first.SizeBytes != size {
		t.Fatalf("expected first upsert to record size_bytes=%d, got %v", size, first.SizeBytes)
	}

	second, err := repo.UpsertCacheEntry(ctx, &manman.WorkshopCacheEntry{
		WorkshopID:     "111",
		ContentVersion: "1",
		CacheKey:       "ws/111/1",
		S3Key:          "workshop-cache/111/1.tar",
		SizeBytes:      nil,
	})
	if err != nil {
		t.Fatalf("second UpsertCacheEntry: %v", err)
	}
	if second.CacheEntryID != first.CacheEntryID {
		t.Fatalf("expected the same cache_entry_id, got %d and %d", first.CacheEntryID, second.CacheEntryID)
	}
	if second.SizeBytes == nil || *second.SizeBytes != size {
		t.Fatalf("expected size_bytes to remain %d when a later upsert supplies no size, got %v", size, second.SizeBytes)
	}
}

// TestUpsertCacheEntry_ConcurrentSameKeyConverges is NFR4's actual race:
// many goroutines call UpsertCacheEntry concurrently for the identical
// (workshop_id, content_version)/cache_key -- the shape two hosts racing to
// cache the same content would produce. Every call must succeed (no
// unique-violation error surfacing to the caller) and all must converge on
// exactly one cache_entry_id / one row, proving the DB-level
// ON CONFLICT ... DO UPDATE ... RETURNING resolves the race, not
// application-level locking that an in-memory fake could fake its way
// around.
func TestUpsertCacheEntry_ConcurrentSameKeyConverges(t *testing.T) {
	pool := newWorkshopCacheTestDB(t)
	ctx := context.Background()
	repo := NewWorkshopCacheRepository(pool)

	const cacheKey = "ws/555/20240102"
	const numWriters = 12

	var wg sync.WaitGroup
	results := make([]*manman.WorkshopCacheEntry, numWriters)
	errs := make([]error, numWriters)

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.UpsertCacheEntry(ctx, &manman.WorkshopCacheEntry{
				WorkshopID:     "555",
				ContentVersion: "20240102",
				CacheKey:       cacheKey,
				S3Key:          "workshop-cache/555/20240102.tar",
			})
		}(i)
	}
	wg.Wait()

	var firstID int64
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: UpsertCacheEntry must not error on a concurrent same-key race, got: %v", i, err)
		}
		if results[i] == nil || results[i].CacheEntryID == 0 {
			t.Fatalf("writer %d: expected a non-zero cache_entry_id", i)
		}
		if i == 0 {
			firstID = results[i].CacheEntryID
			continue
		}
		if results[i].CacheEntryID != firstID {
			t.Fatalf("expected all concurrent writers to converge on cache_entry_id %d, writer %d got %d", firstID, i, results[i].CacheEntryID)
		}
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM workshop_cache_entries WHERE cache_key = $1`, cacheKey).Scan(&count); err != nil {
		t.Fatalf("count rows for cache_key: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row for cache_key %q after %d concurrent upserts, got %d", cacheKey, numWriters, count)
	}
}

// TestUpsertHostPresence_ConcurrentSameHostConverges is the same race for
// host presence: the same server racing to record its own presence for a
// cache entry must not error, and must leave exactly one presence row
// behind (ON CONFLICT (cache_entry_id, server_id) DO UPDATE).
func TestUpsertHostPresence_ConcurrentSameHostConverges(t *testing.T) {
	pool := newWorkshopCacheTestDB(t)
	ctx := context.Background()
	repo := NewWorkshopCacheRepository(pool)

	entry, err := repo.UpsertCacheEntry(ctx, &manman.WorkshopCacheEntry{
		WorkshopID:     "777",
		ContentVersion: "1",
		CacheKey:       "ws/777/1",
		S3Key:          "workshop-cache/777/1.tar",
	})
	if err != nil {
		t.Fatalf("UpsertCacheEntry: %v", err)
	}

	var serverID int64
	if err := pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('presence-host') RETURNING server_id`).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	const numWriters = 8
	var wg sync.WaitGroup
	errs := make([]error, numWriters)
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = repo.UpsertHostPresence(ctx, entry.CacheEntryID, serverID)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: UpsertHostPresence must not error on a concurrent same-host race, got: %v", i, err)
		}
	}

	presence, err := repo.ListHostPresence(ctx, entry.CacheEntryID)
	if err != nil {
		t.Fatalf("ListHostPresence: %v", err)
	}
	if len(presence) != 1 {
		t.Fatalf("expected exactly one presence row after %d concurrent same-host upserts, got %d", numWriters, len(presence))
	}
}
