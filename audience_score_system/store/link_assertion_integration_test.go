//go:build integration

// Real-Postgres coverage for LinkAssertionStore (migration 021, issue
// #2597, FR3/NFR1) -- the single-use consumption ledger that makes an FR2
// link assertion usable exactly once. See store_integration_test.go's
// package doc for why this file only builds under the "integration" build
// tag.
package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ── LinkAssertionStore (FR3, NFR1) ──────────────────────────────────────────

func TestLinkAssertionStore_Consume_FirstUseSucceeds(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	err := s.LinkAssertions().Consume(ctx, "jti-first-use", time.Now().Add(time.Hour))
	require.NoError(t, err)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM link_assertion_consumption WHERE jti = $1`, "jti-first-use",
	).Scan(&count))
	assert.Equal(t, 1, count, "a successful Consume must insert exactly one row")
}

// TestLinkAssertionStore_Consume_ReplayIsRejected is the replay-rejection
// half of NFR1: a second Consume of the same jti must fail with
// ErrAssertionAlreadyConsumed and must not insert a second row.
//
// Red/green per this task's Testing note: temporarily making Consume
// swallow the unique violation and return nil (rather than translating it
// to ErrAssertionAlreadyConsumed) makes this go red.
func TestLinkAssertionStore_Consume_ReplayIsRejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const jti = "jti-replayed"

	require.NoError(t, s.LinkAssertions().Consume(ctx, jti, time.Now().Add(time.Hour)))

	err := s.LinkAssertions().Consume(ctx, jti, time.Now().Add(time.Hour))
	assert.ErrorIs(t, err, store.ErrAssertionAlreadyConsumed, "a replayed jti must be rejected as already consumed")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM link_assertion_consumption WHERE jti = $1`, jti,
	).Scan(&count))
	assert.Equal(t, 1, count, "a rejected replay must not insert a second row for the same jti")
}

// TestLinkAssertionStore_Consume_ConcurrentSameJTI_ExactlyOneSucceeds proves
// single-use holds under real concurrent load: detection is the PRIMARY
// KEY's unique-violation on INSERT (never a SELECT-then-INSERT check, which
// would race), so exactly one of many concurrent Consume calls for the same
// jti must succeed and every other must come back
// ErrAssertionAlreadyConsumed -- no panic, and no raw pgx error leaking out.
func TestLinkAssertionStore_Consume_ConcurrentSameJTI_ExactlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	const (
		jti     = "jti-concurrent"
		workers = 20
	)

	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.LinkAssertions().Consume(ctx, jti, time.Now().Add(time.Hour))
		}(i)
	}
	wg.Wait()

	successCount := 0
	for i, err := range errs {
		if err == nil {
			successCount++
			continue
		}
		assert.ErrorIs(t, err, store.ErrAssertionAlreadyConsumed, "goroutine %d: every losing Consume must report already-consumed, not some other error", i)
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent Consume of the same jti must succeed")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM link_assertion_consumption WHERE jti = $1`, jti,
	).Scan(&count))
	assert.Equal(t, 1, count, "the winning Consume must leave exactly one row behind")
}

func TestLinkAssertionStore_Consume_DistinctJTIsAreIndependent(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	require.NoError(t, s.LinkAssertions().Consume(ctx, "jti-a", time.Now().Add(time.Hour)))
	err := s.LinkAssertions().Consume(ctx, "jti-b", time.Now().Add(time.Hour))

	assert.NoError(t, err, "consuming one jti must never affect the availability of a distinct jti")
}

func TestLinkAssertionStore_ReapExpired_DeletesOnlyRowsPastExpiry(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	now := time.Now()

	require.NoError(t, s.LinkAssertions().Consume(ctx, "jti-expired", now.Add(time.Hour)))
	require.NoError(t, s.LinkAssertions().Consume(ctx, "jti-live", now.Add(time.Hour)))

	// Age one row past expiry directly via SQL -- simulating time passing
	// without racing Consume's own clock.
	_, err := db.Pool.Exec(ctx, `UPDATE link_assertion_consumption SET expires_at = $1 WHERE jti = $2`,
		now.Add(-time.Minute), "jti-expired")
	require.NoError(t, err)

	n, err := s.LinkAssertions().ReapExpired(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "ReapExpired must report exactly one row deleted")

	var remaining []string
	rows, err := db.Pool.Query(ctx, `SELECT jti FROM link_assertion_consumption`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var jti string
		require.NoError(t, rows.Scan(&jti))
		remaining = append(remaining, jti)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"jti-live"}, remaining, "only the row past expires_at must be deleted")
}
