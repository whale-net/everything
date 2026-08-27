//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest/README.md for how to run it.
//
// These tests exercise exactly what leaflab/api/contract's pure unit tests
// (failure_test.go, paging_test.go) cannot: real repository/server behavior
// against a real Postgres board/device_config schema -- a refusal writing
// nothing, keyset pagination staying correct while rows are inserted
// mid-scan, and a mutated/foreign page token being rejected end-to-end
// through LeafLabAPIServer.ListBoards, not just contract.DecodeCursor in
// isolation. Schema is self-contained hand-written DDL (see dbtest's own
// doc comment on Options.Schema) covering only the two tables this phase's
// RPCs touch (board, device_config) -- it deliberately does not depend on
// leaflab/migrate's migrations so this test stays hermetic.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const testSchema = `
	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);
`

// discardLogger is a *slog.Logger that throws away everything it's given --
// these tests assert on returned errors and DB state, not log output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestServer starts a real Postgres container, applies testSchema, and
// returns a LeafLabAPIServer backed by a real Repository plus the raw pool
// for fixture setup / assertions. publisher is nil: every RPC exercised by
// this file either never reaches the publish step (PushDeviceConfig is
// tested only via its pre-write validation refusal) or never touches it at
// all (ListBoards).
func newTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: testSchema})
	repo := NewRepository(db.Pool)
	return NewLeafLabAPIServer(repo, nil, discardLogger()), db.Pool
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}

func insertBoard(t *testing.T, pool *pgxpool.Pool, deviceID string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).Scan(&id)
	if err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

// TestPushDeviceConfig_RefusalWritesNothing covers FR59.3's
// refuse-before-anything-is-written contract as it's actually exercised in
// this phase: PushDeviceConfig validates device_id and returns a structured
// failure *before* touching the repository at all. This proves that in
// practice, not just by reading the code -- if validateDeviceID's check
// ever moved after a write, this test would catch it.
func TestPushDeviceConfig_RefusalWritesNothing(t *testing.T) {
	server, pool := newTestServer(t)
	ctx := context.Background()

	_, err := server.PushDeviceConfig(ctx, &pb.PushDeviceConfigRequest{
		DeviceId: "not a valid device id!!",
	})
	if err == nil {
		t.Fatal("PushDeviceConfig with an invalid device_id returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureInvalidArgument)
	}

	if got := countRows(t, pool, "board"); got != 0 {
		t.Errorf("board rows after refused push = %d, want 0 (refusal must write nothing)", got)
	}
	if got := countRows(t, pool, "device_config"); got != 0 {
		t.Errorf("device_config rows after refused push = %d, want 0 (refusal must write nothing)", got)
	}
}

// TestListBoards_PageTokenTampered_Rejected proves the opaque-token
// contract holds end-to-end through the RPC, not just at
// contract.DecodeCursor in isolation: a mutated (bit-flipped) real token,
// and an entirely foreign hand-written token, are both rejected with a
// structured invalid_argument failure rather than being reinterpreted as a
// scan position.
func TestListBoards_PageTokenTampered_Rejected(t *testing.T) {
	server, pool := newTestServer(t)
	ctx := context.Background()
	insertBoard(t, pool, "device-a")

	// A real token, then mutated -- e.g. an attacker or client bug flips a
	// byte trying to walk to an arbitrary board_id.
	realToken := contract.EncodeBoardCursor(1)
	rawTokenBytes, err := base64.RawURLEncoding.DecodeString(realToken)
	if err != nil {
		t.Fatalf("test setup: decode real token: %v", err)
	}
	mutated := append([]byte(nil), rawTokenBytes...)
	mutated[len(mutated)-1] ^= 0xFF
	mutatedToken := base64.RawURLEncoding.EncodeToString(mutated)

	// A foreign, hand-written token that never came from EncodeCursor at
	// all -- the shape a caller might guess if the encoding were treated as
	// a transparent offset.
	foreignToken := base64.RawURLEncoding.EncodeToString([]byte("offset=0"))

	for name, token := range map[string]string{
		"mutated real token": mutatedToken,
		"foreign token":      foreignToken,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := server.ListBoards(ctx, &pb.ListBoardsRequest{
				Page: &pb.PageRequest{PageToken: token},
			})
			if err == nil {
				t.Fatalf("ListBoards with %s returned nil error, want a rejection", name)
			}
			detail, ok := contract.FromError(err)
			if !ok {
				t.Fatalf("error %v carries no Failure detail", err)
			}
			if detail.Class != string(contract.FailureInvalidArgument) {
				t.Errorf("Class = %q, want %q", detail.Class, contract.FailureInvalidArgument)
			}
			if detail.Field != "page_token" {
				t.Errorf("Field = %q, want %q", detail.Field, "page_token")
			}
		})
	}
}

// TestListBoards_PageSizeAboveCap_ClampedNotRejected inserts more boards
// than contract.PageCap, requests a page_size far above the cap, and
// asserts the RPC succeeds (not a rejection) while returning at most
// PageCap boards with a next_page_token signalling more remain -- proving
// the clamp is actually enforced server-side on the full RPC path, not just
// in contract.ClampPageSize's unit test.
func TestListBoards_PageSizeAboveCap_ClampedNotRejected(t *testing.T) {
	server, pool := newTestServer(t)
	ctx := context.Background()

	total := int(contract.PageCap) + 5
	for i := 0; i < total; i++ {
		insertBoard(t, pool, fmt.Sprintf("device-%03d", i))
	}

	resp, err := server.ListBoards(ctx, &pb.ListBoardsRequest{
		Page: &pb.PageRequest{PageSize: contract.PageCap * 1000},
	})
	if err != nil {
		t.Fatalf("ListBoards with an over-cap page_size returned an error, want clamping: %v", err)
	}
	if len(resp.Boards) != int(contract.PageCap) {
		t.Errorf("len(Boards) = %d, want PageCap %d", len(resp.Boards), contract.PageCap)
	}
	if resp.Page.GetNextPageToken() == "" {
		t.Error("NextPageToken is empty, want a token since more boards remain beyond the clamped page")
	}
}

// TestListBoards_KeysetPagination_NoDuplicatesNoSkips_UnderConcurrentInserts
// proves FR61's core guarantee: a token obtained from page N still resumes
// correctly at page N+1 -- no duplicated rows, no skipped rows -- even when
// new boards are inserted between the two calls. It also checks that every
// BoardInfo carries a populated Instant (last_seen_at) and every response
// carries server_now, per FR64.
func TestListBoards_KeysetPagination_NoDuplicatesNoSkips_UnderConcurrentInserts(t *testing.T) {
	server, pool := newTestServer(t)
	ctx := context.Background()

	// Seed 5 boards up front (board_id 1..5).
	for i := 0; i < 5; i++ {
		insertBoard(t, pool, fmt.Sprintf("seed-%d", i))
	}

	const pageSize = 2
	seen := map[int64]bool{}
	var order []int64
	insertedMidScan := false

	page, err := server.ListBoards(ctx, &pb.ListBoardsRequest{Page: &pb.PageRequest{PageSize: pageSize}})
	if err != nil {
		t.Fatalf("first ListBoards call: %v", err)
	}

	for pageNum := 1; ; pageNum++ {
		if len(page.Boards) == 0 && pageNum > 1 {
			t.Fatalf("page %d returned zero boards before NextPageToken went empty", pageNum)
		}
		for _, b := range page.Boards {
			if seen[b.BoardId] {
				t.Fatalf("board_id %d returned more than once across pages (duplicate under concurrent insert)", b.BoardId)
			}
			seen[b.BoardId] = true
			order = append(order, b.BoardId)

			if b.LastSeenAt == nil || b.LastSeenAt.UnixMillis == 0 {
				t.Errorf("board_id %d: LastSeenAt missing/zero, want a populated Instant (FR64)", b.BoardId)
			}
		}
		if page.ServerNow == nil || page.ServerNow.UnixMillis == 0 {
			t.Errorf("page %d: ServerNow missing/zero, want a populated Instant (FR64)", pageNum)
		}

		// Simulate a concurrent writer inserting a new board between this
		// page fetch and the next one -- exactly once, after the first
		// page, so we can prove it neither duplicates nor is silently
		// skipped by later pages.
		if pageNum == 1 && !insertedMidScan {
			insertBoard(t, pool, "concurrent-insert-1")
			insertBoard(t, pool, "concurrent-insert-2")
			insertedMidScan = true
		}

		nextToken := page.Page.GetNextPageToken()
		if nextToken == "" {
			break
		}
		page, err = server.ListBoards(ctx, &pb.ListBoardsRequest{
			Page: &pb.PageRequest{PageToken: nextToken, PageSize: pageSize},
		})
		if err != nil {
			t.Fatalf("ListBoards page %d: %v", pageNum+1, err)
		}
		if pageNum > 10 {
			t.Fatal("pagination did not terminate after 10 pages")
		}
	}

	// 5 seeded + 2 inserted mid-scan = 7 total; every one must appear
	// exactly once, in ascending board_id order (no skips, no duplicates,
	// no reordering).
	if len(order) != 7 {
		t.Fatalf("collected %d boards across all pages, want 7 (5 seeded + 2 inserted mid-scan): %v", len(order), order)
	}
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Fatalf("boards not strictly ascending at index %d: %v", i, order)
		}
	}

	var dbCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM board`).Scan(&dbCount); err != nil {
		t.Fatalf("count boards: %v", err)
	}
	if dbCount != 7 {
		t.Fatalf("test setup: expected 7 boards in DB, found %d", dbCount)
	}
}
