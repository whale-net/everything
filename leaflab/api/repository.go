package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"google.golang.org/protobuf/encoding/protojson"
)

// ErrNoHousehold is returned by the household-resolution helpers below when
// a board/region/plant resolves to no household -- the FR1.1 "unclaimed
// board" exception aside, this should never occur post-backfill (FR70.1).
var ErrNoHousehold = errors.New("row resolves to no household")

// ErrBoardNotFound is returned by board lookups/operations when board_id
// names no row.
var ErrBoardNotFound = errors.New("board not found")

// ErrBoardAlreadyRetired is returned by RetireBoard when the board is
// already retired -- retirement is not idempotent-by-design (FR22.1 names
// the operation; calling it twice is a caller error, not a no-op).
var ErrBoardAlreadyRetired = errors.New("board already retired")

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// Ping reports whether the database is reachable, for FR63's health probe.
// It carries no result beyond error/no-error -- GetHealth translates that
// into HEALTH_DEGRADED/HEALTH_UP and nothing more specific reaches a
// caller.
func (r *Repository) Ping(ctx context.Context) error {
	return r.db.Ping(ctx)
}

// GetOrCreateBoard returns the board_id for the given device_id, creating a row if needed.
func (r *Repository) GetOrCreateBoard(ctx context.Context, deviceID string) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO board (device_id, registered_at, last_seen_at)
		VALUES ($1, NOW(), NOW())
		ON CONFLICT (device_id) DO UPDATE SET last_seen_at = NOW()
		RETURNING board_id
	`, deviceID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get/create board %s: %w", deviceID, err)
	}
	return id, nil
}

// InsertDeviceConfigNextVersion assigns the next version for the board and
// inserts the pending config row. ON CONFLICT DO NOTHING retries when two
// concurrent writers compute the same MAX(version)+1; the unique constraint on
// (board_id, version) guarantees only one wins per version number.
func (r *Repository) InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte) (int64, error) {
	for {
		var version int64
		err := r.db.QueryRow(ctx, `
			WITH next AS (
				SELECT COALESCE(MAX(version), 0) + 1 AS v
				FROM device_config
				WHERE board_id = $1
			)
			INSERT INTO device_config (board_id, version, config_json)
			SELECT $1, next.v, $2 FROM next
			ON CONFLICT (board_id, version) DO NOTHING
			RETURNING version
		`, boardID, configJSON).Scan(&version)
		if err == nil {
			return version, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// another writer claimed this version; retry with the new MAX
			continue
		}
		return 0, fmt.Errorf("insert device_config for board %d: %w", boardID, err)
	}
}

// GetLatestAcceptedConfig returns the highest-version accepted config for a board.
// Returns nil, nil if no accepted config exists.
func (r *Repository) GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error) {
	var jsonBytes []byte
	err := r.db.QueryRow(ctx, `
		SELECT dc.config_json
		FROM device_config dc
		JOIN board b ON b.board_id = dc.board_id
		WHERE b.device_id = $1
		  AND dc.accepted = TRUE
		ORDER BY dc.version DESC
		LIMIT 1
	`, deviceID).Scan(&jsonBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get latest config for %s: %w", deviceID, err)
	}

	var cfg configpb.DeviceConfig
	if err := protojson.Unmarshal(jsonBytes, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal stored config for %s: %w", deviceID, err)
	}
	return &cfg, nil
}

// ListBoards returns up to limit boards ordered by board_id, keyset-paginated
// on (board_id) per FR61: afterBoardID/hasAfter is the last board_id of the
// previous page (from contract.DecodeBoardCursor), not an offset, so
// pagination stays correct while boards are inserted mid-scan. Callers
// typically request limit+1 rows so a next page can be detected without a
// separate COUNT query.
//
// This is the default listing FR22.1/FR22.4/FR22.5 guard against: a retired
// board (retired_at IS NOT NULL) is excluded here, backed by idx_board_active
// from migration 015. A retired board remains resolvable by explicit id and
// through its history/readings paths -- those lookups don't go through
// ListBoards and are unaffected.
func (r *Repository) ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32) ([]BoardRow, error) {
	var rows pgx.Rows
	var err error
	if hasAfter {
		rows, err = r.db.Query(ctx, `
			SELECT board_id, device_id, last_seen_at
			FROM board
			WHERE board_id > $1
			  AND retired_at IS NULL
			ORDER BY board_id
			LIMIT $2
		`, afterBoardID, limit)
	} else {
		rows, err = r.db.Query(ctx, `
			SELECT board_id, device_id, last_seen_at
			FROM board
			WHERE retired_at IS NULL
			ORDER BY board_id
			LIMIT $1
		`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list boards: %w", err)
	}
	defer rows.Close()

	var boards []BoardRow
	for rows.Next() {
		var b BoardRow
		if err := rows.Scan(&b.BoardID, &b.DeviceID, &b.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan board: %w", err)
		}
		boards = append(boards, b)
	}
	return boards, rows.Err()
}

type BoardRow struct {
	BoardID    int64
	DeviceID   string
	LastSeenAt time.Time
	RetiredAt  *time.Time
}

// GetBoardByID returns a board by its numeric id regardless of retired
// state -- the FR22.1/FR22.4/FR22.5 "remains readable by explicit id" half
// of the retired-board guard. ListBoards is the half that excludes it from
// default listings; this is the explicit-id escape hatch.
func (r *Repository) GetBoardByID(ctx context.Context, boardID int64) (BoardRow, error) {
	var b BoardRow
	err := r.db.QueryRow(ctx, `
		SELECT board_id, device_id, last_seen_at, retired_at
		FROM board
		WHERE board_id = $1
	`, boardID).Scan(&b.BoardID, &b.DeviceID, &b.LastSeenAt, &b.RetiredAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BoardRow{}, ErrBoardNotFound
		}
		return BoardRow{}, fmt.Errorf("get board %d: %w", boardID, err)
	}
	return b, nil
}

// RetireBoard is the named operation FR22.1/FR22.4/FR22.5 requires: it sets
// board.retired_at, which removes the board from ListBoards's default
// listing (idx_board_active) and from FR79's offline counts / FR62's
// household-wide classification (both read board population elsewhere),
// while leaving the row, its history and its readings fully resolvable.
//
// It does not record the acting principal -- FR8's append-only audit log is
// #1338, a sibling task; wiring this operation's audit emission is that
// task's job, the same way #1296 tracks it for PushDeviceConfig.
func (r *Repository) RetireBoard(ctx context.Context, boardID int64) error {
	var id int64
	err := r.db.QueryRow(ctx, `
		UPDATE board
		SET retired_at = NOW()
		WHERE board_id = $1
		  AND retired_at IS NULL
		RETURNING board_id
	`, boardID).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("retire board %d: %w", boardID, err)
	}

	// No row updated -- distinguish "doesn't exist" from "already retired" so
	// callers get the accurate failure class.
	var exists bool
	if checkErr := r.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM board WHERE board_id = $1)
	`, boardID).Scan(&exists); checkErr != nil {
		return fmt.Errorf("retire board %d: check existence: %w", boardID, checkErr)
	}
	if !exists {
		return ErrBoardNotFound
	}
	return ErrBoardAlreadyRetired
}

// ── Ownership (FR1.1, FR70.1, NFR6.1) ────────────────────────────────────────
//
// household_id is carried directly on board and plant, and on the region
// tree root only (descendants inherit -- see migration 015's
// enforce_region_household_root trigger). board_ownership additionally
// tracks board re-ownership history in SCD2 shape; the helpers below read
// through it rather than board.household_id so that "current household of a
// board" and the value-at-T variant share one source of truth.

// CurrentHouseholdForBoard returns the household a board currently resolves
// to. ErrNoHousehold covers both "never claimed" (FR1.1's exception) and any
// row that should be unreachable post-backfill.
func (r *Repository) CurrentHouseholdForBoard(ctx context.Context, boardID int64) (int64, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id
		FROM board_ownership
		WHERE board_id = $1
		  AND valid_to IS NULL
	`, boardID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNoHousehold
		}
		return 0, fmt.Errorf("current household for board %d: %w", boardID, err)
	}
	return householdID, nil
}

// HouseholdForBoardAtTime resolves the household that owned a board at time
// t, per AGENTS.md's value-at-time-T predicate over board_ownership.
func (r *Repository) HouseholdForBoardAtTime(ctx context.Context, boardID int64, t time.Time) (int64, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id
		FROM board_ownership
		WHERE board_id = $1
		  AND valid_from <= $2
		  AND (valid_to IS NULL OR valid_to > $2)
	`, boardID, t).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNoHousehold
		}
		return 0, fmt.Errorf("household for board %d at %s: %w", boardID, t, err)
	}
	return householdID, nil
}

// CurrentHouseholdForRegion resolves the household a region currently
// belongs to, walking up to the tree root if the given region is a
// descendant (only the root carries household_id).
func (r *Repository) CurrentHouseholdForRegion(ctx context.Context, regionID int64) (int64, error) {
	var householdID *int64
	err := r.db.QueryRow(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT region_id, parent_region_id, household_id
			FROM region
			WHERE region_id = $1

			UNION ALL

			SELECT r.region_id, r.parent_region_id, r.household_id
			FROM region r
			JOIN ancestors a ON r.region_id = a.parent_region_id
		)
		SELECT household_id
		FROM ancestors
		WHERE parent_region_id IS NULL
	`, regionID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNoHousehold
		}
		return 0, fmt.Errorf("current household for region %d: %w", regionID, err)
	}
	if householdID == nil {
		return 0, ErrNoHousehold
	}
	return *householdID, nil
}

// CurrentHouseholdForPlant returns the household a plant currently resolves
// to. plant.household_id is carried directly (not inherited via region).
func (r *Repository) CurrentHouseholdForPlant(ctx context.Context, plantID int64) (int64, error) {
	var householdID *int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id
		FROM plant
		WHERE plant_id = $1
	`, plantID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNoHousehold
		}
		return 0, fmt.Errorf("current household for plant %d: %w", plantID, err)
	}
	if householdID == nil {
		return 0, ErrNoHousehold
	}
	return *householdID, nil
}
