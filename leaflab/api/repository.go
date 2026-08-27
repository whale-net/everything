package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
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

// auditedWrite runs fn inside a single transaction: fn performs the write
// and returns the audit.Entry to record for it (built inside fn so it can
// carry values -- e.g. an assigned version number -- that are only known
// once the write has run). The audit row is inserted via the same
// transaction via audit.PostgresAuditor, and the whole thing commits only
// if both the write and the audit insert succeed.
//
// This is FR8/NFR6.2's "a rolled-back write leaves no audit row; a
// committed write always has exactly one": if fn returns an error (e.g.
// RetireBoard's ErrBoardAlreadyRetired), the transaction is rolled back
// before any audit row is written, and that error is returned unwrapped so
// callers can still match it with errors.Is.
func (r *Repository) auditedWrite(ctx context.Context, fn func(tx pgx.Tx) (audit.Entry, error)) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- no-op once committed; only matters on the early-return paths above

	entry, err := fn(tx)
	if err != nil {
		return err
	}
	if err := audit.NewPostgresAuditor(tx).Record(ctx, entry); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
//
// entry is the audit record for this push (FR8.2 names config pushes as a
// write whose acting principal must be recorded); auditedWrite inserts it
// in the same transaction as the device_config row, with entry.EntityID
// filled in with the assigned version once it's known. A write that fails
// (including every retry outcome other than success) leaves neither row.
func (r *Repository) InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte, entry audit.Entry) (int64, error) {
	var version int64
	err := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		for {
			err := tx.QueryRow(ctx, `
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
				break
			}
			if errors.Is(err, pgx.ErrNoRows) {
				// another writer claimed this version; retry with the new MAX
				continue
			}
			return audit.Entry{}, fmt.Errorf("insert device_config for board %d: %w", boardID, err)
		}
		versionStr := strconv.FormatInt(version, 10)
		entry.EntityID = &versionStr
		return entry, nil
	})
	if err != nil {
		return 0, err
	}
	return version, nil
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
//
// scope is FR5.1's household scoping, applied via scope.Filter() **inside**
// this query (its WHERE clause) rather than as a Go-side post-filter on the
// returned rows -- FR5.2's "aggregates/listings apply Scope.Filter() inside
// the query" applies equally to a plain listing. argStart is chosen so the
// scope's placeholders never collide with the keyset/limit params already
// bound above them ($1/$2 when hasAfter, $1 otherwise).
func (r *Repository) ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32, scope authz.Scope) ([]BoardRow, error) {
	var sqlQuery string
	var args []any
	if hasAfter {
		filter, filterArgs := scope.Filter(3)
		sqlQuery = fmt.Sprintf(`
			SELECT board_id, device_id, last_seen_at
			FROM board
			WHERE board_id > $1
			  AND retired_at IS NULL
			  AND (%s)
			ORDER BY board_id
			LIMIT $2
		`, filter)
		args = append([]any{afterBoardID, limit}, filterArgs...)
	} else {
		filter, filterArgs := scope.Filter(2)
		sqlQuery = fmt.Sprintf(`
			SELECT board_id, device_id, last_seen_at
			FROM board
			WHERE retired_at IS NULL
			  AND (%s)
			ORDER BY board_id
			LIMIT $1
		`, filter)
		args = append([]any{limit}, filterArgs...)
	}

	rows, err := r.db.Query(ctx, sqlQuery, args...)
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
// entry is the audit record for this retirement (FR8.2 names it among the
// writes whose acting principal must be recorded); auditedWrite inserts it
// in the same transaction as the retired_at UPDATE. Neither the UPDATE nor
// the audit row commits when the board doesn't exist or is already
// retired -- fn returns before entry ever reaches
// audit.PostgresAuditor.Record.
func (r *Repository) RetireBoard(ctx context.Context, boardID int64, entry audit.Entry) error {
	return r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		var id int64
		err := tx.QueryRow(ctx, `
			UPDATE board
			SET retired_at = NOW()
			WHERE board_id = $1
			  AND retired_at IS NULL
			RETURNING board_id
		`, boardID).Scan(&id)
		if err == nil {
			return entry, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return audit.Entry{}, fmt.Errorf("retire board %d: %w", boardID, err)
		}

		// No row updated -- distinguish "doesn't exist" from "already
		// retired" so callers get the accurate failure class.
		var exists bool
		if checkErr := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM board WHERE board_id = $1)
		`, boardID).Scan(&exists); checkErr != nil {
			return audit.Entry{}, fmt.Errorf("retire board %d: check existence: %w", boardID, checkErr)
		}
		if !exists {
			return audit.Entry{}, ErrBoardNotFound
		}
		return audit.Entry{}, ErrBoardAlreadyRetired
	})
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
