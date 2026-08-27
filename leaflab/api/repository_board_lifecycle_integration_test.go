//go:build integration

// Real-Postgres integration coverage for the FR22.1/FR22.4/FR22.5 board
// retirement operation added by migration 015_ownership: RetireBoard sets
// retired_at and distinguishes "doesn't exist" from "already retired" so
// callers get the accurate failure class; ListBoards excludes a retired
// board from its default listing while GetBoardByID keeps it readable by
// explicit id. See //libs/go/dbtest's README for how to run integration
// tests like this one; fixtures (testSchema, newTestRepository, insertBoard)
// live in dbtest_helpers_integration_test.go.
package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRetireBoard_SetsRetiredAtAndDistinguishesNotFoundFromAlreadyRetired
// proves RetireBoard's three-way outcome end-to-end against real SQL: a
// first call on an existing board succeeds and actually persists retired_at
// (not just returns nil), a second call on the same board is refused as
// "already retired" rather than silently succeeding a second time
// (retirement is not idempotent-by-design), and a call naming a board_id
// that was never inserted is refused as "not found" -- a distinct failure
// class from "already retired".
func TestRetireBoard_SetsRetiredAtAndDistinguishesNotFoundFromAlreadyRetired(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "device-retire-me")

	var retiredAtBefore *time.Time
	if err := pool.QueryRow(ctx, `SELECT retired_at FROM board WHERE board_id = $1`, boardID).Scan(&retiredAtBefore); err != nil {
		t.Fatalf("test setup: read retired_at before retirement: %v", err)
	}
	if retiredAtBefore != nil {
		t.Fatalf("test setup: board %d already has retired_at set: %v", boardID, *retiredAtBefore)
	}

	if err := repo.RetireBoard(ctx, boardID, testAuditEntry()); err != nil {
		t.Fatalf("first RetireBoard call: %v", err)
	}

	var retiredAtAfter *time.Time
	if err := pool.QueryRow(ctx, `SELECT retired_at FROM board WHERE board_id = $1`, boardID).Scan(&retiredAtAfter); err != nil {
		t.Fatalf("read retired_at after retirement: %v", err)
	}
	if retiredAtAfter == nil {
		t.Fatal("retired_at is still NULL after RetireBoard succeeded -- the operation must actually persist")
	}

	// Second call on the same board: refused, distinct from not-found.
	err := repo.RetireBoard(ctx, boardID, testAuditEntry())
	if err == nil {
		t.Fatal("second RetireBoard call on an already-retired board returned nil error, want ErrBoardAlreadyRetired")
	}
	if !errors.Is(err, ErrBoardAlreadyRetired) {
		t.Errorf("second RetireBoard call error = %v, want ErrBoardAlreadyRetired", err)
	}
	if errors.Is(err, ErrBoardNotFound) {
		t.Error("second RetireBoard call error satisfies ErrBoardNotFound -- already-retired must be a distinct class from not-found")
	}

	// A board_id that was never inserted: refused as not-found, distinct
	// from already-retired.
	const neverInsertedBoardID = int64(999999)
	err = repo.RetireBoard(ctx, neverInsertedBoardID, testAuditEntry())
	if err == nil {
		t.Fatal("RetireBoard on a nonexistent board_id returned nil error, want ErrBoardNotFound")
	}
	if !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("RetireBoard on a nonexistent board_id error = %v, want ErrBoardNotFound", err)
	}
	if errors.Is(err, ErrBoardAlreadyRetired) {
		t.Error("RetireBoard on a nonexistent board_id error satisfies ErrBoardAlreadyRetired -- not-found must be a distinct class from already-retired")
	}
}

// TestListBoards_ExcludesRetiredBoards proves the FR22.1/FR22.4/FR22.5
// "excluded from default listings" guard holds against real SQL
// (idx_board_active), not just by reading the query: a retired board is
// absent from ListBoards' result while a non-retired sibling still appears.
func TestListBoards_ExcludesRetiredBoards(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	activeID := insertBoard(t, pool, "device-active")
	retiredID := insertBoard(t, pool, "device-retired")

	if err := repo.RetireBoard(ctx, retiredID, testAuditEntry()); err != nil {
		t.Fatalf("test setup: retire board %d: %v", retiredID, err)
	}

	rows, err := repo.ListBoards(ctx, 0, false, 10)
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}

	var sawActive, sawRetired bool
	for _, r := range rows {
		switch r.BoardID {
		case activeID:
			sawActive = true
		case retiredID:
			sawRetired = true
		}
	}
	if !sawActive {
		t.Errorf("ListBoards did not return the non-retired board %d", activeID)
	}
	if sawRetired {
		t.Errorf("ListBoards returned the retired board %d, want it excluded from the default listing", retiredID)
	}
}

// TestGetBoardByID_RetiredBoardStillReadableByExplicitID proves the other
// half of FR22.1/FR22.4/FR22.5's guard: a retired board is excluded from
// ListBoards (above) but remains fully resolvable by explicit id, with its
// retired_at populated so a caller can distinguish it from an active board.
// A board_id that names no row still returns ErrBoardNotFound.
func TestGetBoardByID_RetiredBoardStillReadableByExplicitID(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "device-explicit-id")
	if err := repo.RetireBoard(ctx, boardID, testAuditEntry()); err != nil {
		t.Fatalf("test setup: retire board %d: %v", boardID, err)
	}

	got, err := repo.GetBoardByID(ctx, boardID)
	if err != nil {
		t.Fatalf("GetBoardByID on a retired board: %v", err)
	}
	if got.RetiredAt == nil {
		t.Error("GetBoardByID on a retired board returned nil RetiredAt, want it populated")
	}
	if got.DeviceID != "device-explicit-id" {
		t.Errorf("DeviceID = %q, want %q", got.DeviceID, "device-explicit-id")
	}

	const neverInsertedBoardID = int64(999999)
	_, err = repo.GetBoardByID(ctx, neverInsertedBoardID)
	if !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("GetBoardByID on a nonexistent board_id error = %v, want ErrBoardNotFound", err)
	}
}
