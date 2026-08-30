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

// historyPointCap is the maximum number of plottable points GetSensorReadingHistory
// will return in a single response (FR9). Sent back on every response via
// point_cap so the UI never hardcodes this value.
const historyPointCap = 15000

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

// SensorExists reports whether a sensor with the given ID has ever been registered.
func (r *Repository) SensorExists(ctx context.Context, sensorID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM sensor WHERE sensor_id = $1)
	`, sensorID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check sensor %d exists: %w", sensorID, err)
	}
	return exists, nil
}

// ReadingPoint is a single (recorded_at, value) point from sensor_reading.
type ReadingPoint struct {
	RecordedAt time.Time
	Value      float64
}

// SensorReadingHistory is the result of a bounded, filtered range query
// against sensor_reading for one sensor (FR9, FR10).
type SensorReadingHistory struct {
	// Points, oldest-to-newest, with invalid readings already excluded and
	// the point cap already applied.
	Points []ReadingPoint
	// True when the range held more than historyPointCap plottable points.
	Capped bool
	// The range Points actually spans. Only meaningful when Capped is true.
	CoveredFrom time.Time
	CoveredTo   time.Time
	// Count of invalid (valid = FALSE) readings within [from, to), the full
	// selected range -- not narrowed to the covered (post-cap) range. This is
	// the definition the UI's copy must match: "N invalid readings in the
	// range you selected" rather than "...in the range shown".
	ExcludedInvalidCount uint32
}

// GetSensorReadingHistory queries sensor_reading directly (not the enriched
// view -- its per-row dimension joins are pure overhead on a 15,000-row
// result) for one sensor's raw readings in [from, to).
//
// The invalid-reading filter is applied first (in SQL, via `valid = TRUE`),
// then the most-recent-N cap is applied on top of the already-filtered rows,
// per FR9: the cap counts plottable points, not raw rows. Ordering by
// recorded_at DESC and capping at historyPointCap+1 lets a single query both
// fetch the most recent points and detect whether the range held more than
// the cap, without a second round trip; the extra row (if present) is
// dropped before reversing to ascending order for the response.
func (r *Repository) GetSensorReadingHistory(ctx context.Context, sensorID int64, from, to time.Time) (*SensorReadingHistory, error) {
	var invalidCount int64
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM sensor_reading
		WHERE sensor_id = $1
		  AND recorded_at >= $2
		  AND recorded_at < $3
		  AND valid = FALSE
	`, sensorID, from, to).Scan(&invalidCount)
	if err != nil {
		return nil, fmt.Errorf("count invalid readings for sensor %d: %w", sensorID, err)
	}

	rows, err := r.db.Query(ctx, `
		SELECT recorded_at, value
		FROM sensor_reading
		WHERE sensor_id = $1
		  AND recorded_at >= $2
		  AND recorded_at < $3
		  AND valid = TRUE
		ORDER BY recorded_at DESC
		LIMIT $4
	`, sensorID, from, to, historyPointCap+1)
	if err != nil {
		return nil, fmt.Errorf("query readings for sensor %d: %w", sensorID, err)
	}
	defer rows.Close()

	// Newest-first; reversed to ascending below once we know whether the
	// cap was hit.
	var desc []ReadingPoint
	for rows.Next() {
		var p ReadingPoint
		if err := rows.Scan(&p.RecordedAt, &p.Value); err != nil {
			return nil, fmt.Errorf("scan reading for sensor %d: %w", sensorID, err)
		}
		desc = append(desc, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate readings for sensor %d: %w", sensorID, err)
	}

	capped := len(desc) > historyPointCap
	if capped {
		desc = desc[:historyPointCap]
	}

	points := make([]ReadingPoint, len(desc))
	for i, p := range desc {
		points[len(desc)-1-i] = p
	}

	result := &SensorReadingHistory{
		Points:               points,
		Capped:               capped,
		ExcludedInvalidCount: uint32(invalidCount),
	}
	if capped {
		result.CoveredFrom = points[0].RecordedAt
		result.CoveredTo = points[len(points)-1].RecordedAt
	}
	return result, nil
}
