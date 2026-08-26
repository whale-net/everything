package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/leaflab/api/pagetoken"
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

// ListBoards returns a page of boards using keyset pagination on (recorded_at DESC, board_id).
// recorded_at is the Unix timestamp (seconds since epoch) of last_seen_at.
// pageToken can be nil for the first page, or a decoded token from a previous response.
// Returns the boards, a next page token (nil if this is the last page), and any error.
func (r *Repository) ListBoards(ctx context.Context, pageSize int32, pageToken *pagetoken.Token) ([]BoardRow, *pagetoken.Token, error) {
	// Clamp page size to maximum; enforce a default if not specified
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}

	// Build the keyset pagination query.
	// We want to fetch boards ordered by (recorded_at DESC, board_id ASC).
	// If we have a token, we start after the last board's (recorded_at, board_id).
	var query string
	var args []interface{}

	if pageToken == nil || (pageToken.LastRecordedAt == 0 && pageToken.LastBoardID == 0) {
		// First page: fetch the first pageSize rows, plus one extra to detect more pages
		query = `
			SELECT board_id, device_id, EXTRACT(EPOCH FROM last_seen_at)::bigint AS recorded_at
			FROM board
			ORDER BY last_seen_at DESC, board_id ASC
			LIMIT $1
		`
		args = []interface{}{pageSize + 1}
	} else {
		// Subsequent page: fetch rows after the token's position.
		// The condition is: (recorded_at, board_id) < (token.recorded_at, token.board_id)
		// in DESC order, which means (recorded_at < token.recorded_at) OR (recorded_at = token.recorded_at AND board_id < token.board_id)
		query = `
			SELECT board_id, device_id, EXTRACT(EPOCH FROM last_seen_at)::bigint AS recorded_at
			FROM board
			WHERE (EXTRACT(EPOCH FROM last_seen_at)::bigint < $1)
			   OR (EXTRACT(EPOCH FROM last_seen_at)::bigint = $1 AND board_id < $2)
			ORDER BY last_seen_at DESC, board_id ASC
			LIMIT $3
		`
		args = []interface{}{pageToken.LastRecordedAt, pageToken.LastBoardID, pageSize + 1}
	}

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list boards: %w", err)
	}
	defer rows.Close()

	var boards []BoardRow
	for rows.Next() {
		var b BoardRow
		if err := rows.Scan(&b.BoardID, &b.DeviceID, &b.RecordedAt); err != nil {
			return nil, nil, fmt.Errorf("scan board: %w", err)
		}
		boards = append(boards, b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("rows error: %w", err)
	}

	// Check if there are more pages
	var nextToken *pagetoken.Token
	if len(boards) > int(pageSize) {
		// We have more pages; truncate to pageSize and create next token
		nextToken = &pagetoken.Token{
			LastRecordedAt: boards[pageSize-1].RecordedAt,
			LastBoardID:    boards[pageSize-1].BoardID,
		}
		boards = boards[:pageSize]
	}

	return boards, nextToken, nil
}

// GetTotalBoardCount returns the total number of boards.
func (r *Repository) GetTotalBoardCount(ctx context.Context) (int32, error) {
	var count int32
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM board`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("get board count: %w", err)
	}
	return count, nil
}

type BoardRow struct {
	BoardID    int64
	DeviceID   string
	RecordedAt int64 // Unix timestamp in seconds
}

const (
	DefaultPageSize = 50
	MaxPageSize     = 1000
)

// GetSensorTimelines retrieves the three aligned timelines (name, hardware, region)
// for a sensor identified by sensor_id. Returns all historical entries in order.
// A sensor dropped from desired state keeps all three timelines.
func (r *Repository) GetSensorTimelines(ctx context.Context, sensorID int64) (*SensorTimelinesResult, error) {
	result := &SensorTimelinesResult{
		SensorID:        sensorID,
		NameTimeline:    []*SensorNameEntry{},
		HardwareTimeline: []*SensorHardwareEntry{},
		RegionTimeline:  []*SensorRegionEntry{},
	}

	// Fetch name timeline
	rows, err := r.db.Query(ctx, `
		SELECT name, EXTRACT(EPOCH FROM valid_from)::int64, COALESCE(EXTRACT(EPOCH FROM valid_to)::int64, 0)
		FROM sensor_name_history
		WHERE sensor_id = $1
		ORDER BY valid_from ASC
	`, sensorID)
	if err != nil {
		return nil, fmt.Errorf("query sensor_name_history: %w", err)
	}
	for rows.Next() {
		var name string
		var validFrom, validTo int64
		if err := rows.Scan(&name, &validFrom, &validTo); err != nil {
			return nil, fmt.Errorf("scan sensor_name_history: %w", err)
		}
		result.NameTimeline = append(result.NameTimeline, &SensorNameEntry{
			Name:      name,
			ValidFrom: validFrom,
			ValidTo:   validTo,
		})
	}
	rows.Close()

	// Fetch hardware timeline (carrying full canonical key)
	rows, err = r.db.Query(ctx, `
		SELECT i2c_address, mux_path, EXTRACT(EPOCH FROM valid_from)::int64, COALESCE(EXTRACT(EPOCH FROM valid_to)::int64, 0)
		FROM sensor_hw_history
		WHERE sensor_id = $1
		ORDER BY valid_from ASC
	`, sensorID)
	if err != nil {
		return nil, fmt.Errorf("query sensor_hw_history: %w", err)
	}
	for rows.Next() {
		var i2cAddr *uint32
		var muxPathJSON string
		var validFrom, validTo int64
		if err := rows.Scan(&i2cAddr, &muxPathJSON, &validFrom, &validTo); err != nil {
			return nil, fmt.Errorf("scan sensor_hw_history: %w", err)
		}
		// i2cAddr is NULL for closed intervals pre-migration 013
		i2cAddrVal := uint32(0)
		if i2cAddr != nil {
			i2cAddrVal = *i2cAddr
		}
		result.HardwareTimeline = append(result.HardwareTimeline, &SensorHardwareEntry{
			I2CAddress: i2cAddrVal,
			MuxPath:    muxPathJSON,
			ValidFrom:  validFrom,
			ValidTo:    validTo,
		})
	}
	rows.Close()

	// Fetch region timeline
	rows, err = r.db.Query(ctx, `
		SELECT region_id, COALESCE((SELECT name FROM region WHERE region_id = srh.region_id), ''), 
		       EXTRACT(EPOCH FROM valid_from)::int64, COALESCE(EXTRACT(EPOCH FROM valid_to)::int64, 0)
		FROM sensor_region_history srh
		WHERE sensor_id = $1
		ORDER BY valid_from ASC
	`, sensorID)
	if err != nil {
		return nil, fmt.Errorf("query sensor_region_history: %w", err)
	}
	for rows.Next() {
		var regionID int64
		var regionName string
		var validFrom, validTo int64
		if err := rows.Scan(&regionID, &regionName, &validFrom, &validTo); err != nil {
			return nil, fmt.Errorf("scan sensor_region_history: %w", err)
		}
		result.RegionTimeline = append(result.RegionTimeline, &SensorRegionEntry{
			RegionID:   regionID,
			RegionName: regionName,
			ValidFrom:  validFrom,
			ValidTo:    validTo,
		})
	}
	rows.Close()

	return result, nil
}

type SensorTimelinesResult struct {
	SensorID         int64
	NameTimeline     []*SensorNameEntry
	HardwareTimeline []*SensorHardwareEntry
	RegionTimeline   []*SensorRegionEntry
}

type SensorNameEntry struct {
	Name      string
	ValidFrom int64
	ValidTo   int64
}

type SensorHardwareEntry struct {
	I2CAddress uint32
	MuxPath    string // JSONB as string
	ValidFrom  int64
	ValidTo    int64
}

type SensorRegionEntry struct {
	RegionID   int64
	RegionName string
	ValidFrom  int64
	ValidTo    int64
}

