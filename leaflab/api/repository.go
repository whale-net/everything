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

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
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

// ListBoards returns all known boards.
func (r *Repository) ListBoards(ctx context.Context) ([]BoardRow, error) {
	rows, err := r.db.Query(ctx, `SELECT board_id, device_id FROM board ORDER BY board_id`)
	if err != nil {
		return nil, fmt.Errorf("list boards: %w", err)
	}
	defer rows.Close()

	var boards []BoardRow
	for rows.Next() {
		var b BoardRow
		if err := rows.Scan(&b.BoardID, &b.DeviceID); err != nil {
			return nil, fmt.Errorf("scan board: %w", err)
		}
		boards = append(boards, b)
	}
	return boards, rows.Err()
}

type BoardRow struct {
	BoardID  int64
	DeviceID string
}

// ListBoardsWithState returns every board (FR4 — no owner filtering, no
// ownership table read at all) along with the most recent recorded_at across
// all of its sensors' readings. LastReadingAt is nil when the board has no
// readings at all — including boards with zero sensors.
//
// This is a single aggregate query over board/sensor/sensor_reading rather
// than one query per board: sensor_reading is a TimescaleDB hypertable
// partitioned on recorded_at, so per-board MAX() lookups in a loop would be
// both slow and non-atomic across boards.
//
// Deliberately does not read board.last_seen_at or sensor.last_seen_at (see
// #1497): neither is bumped by readings, so neither is a liveness signal.
// Deliberately does not filter on sensor_reading.valid: this answers "is data
// arriving", not "is the data good".
func (r *Repository) ListBoardsWithState(ctx context.Context) ([]BoardWithReadingRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT b.board_id, b.device_id, MAX(sr.recorded_at) AS last_reading_at
		FROM board b
		LEFT JOIN sensor s ON s.board_id = b.board_id
		LEFT JOIN sensor_reading sr ON sr.sensor_id = s.sensor_id
		GROUP BY b.board_id, b.device_id
		ORDER BY b.board_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list boards with state: %w", err)
	}
	defer rows.Close()

	var boards []BoardWithReadingRow
	for rows.Next() {
		var b BoardWithReadingRow
		if err := rows.Scan(&b.BoardID, &b.DeviceID, &b.LastReadingAt); err != nil {
			return nil, fmt.Errorf("scan board with state: %w", err)
		}
		boards = append(boards, b)
	}
	return boards, rows.Err()
}

// BoardWithReadingRow is one board plus the max recorded_at across all of its
// sensors' readings. LastReadingAt is nil when the board has no readings.
type BoardWithReadingRow struct {
	BoardID       int64
	DeviceID      string
	LastReadingAt *time.Time
}
