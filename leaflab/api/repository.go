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
// Reads from v_board_last_reading rather than joining board/sensor/
// sensor_reading directly: sensor_reading is a TimescaleDB hypertable with no
// upper bound on size, and a plain MAX(sr.recorded_at) GROUP BY join scans
// every reading ever recorded on every call. The view resolves each sensor's
// latest reading via a LATERAL ORDER BY ... LIMIT 1, which
// idx_sensor_reading_sensor_id(sensor_id, recorded_at DESC) answers in O(1)
// per sensor instead of O(total readings).
//
// Deliberately does not read board.last_seen_at or sensor.last_seen_at (see
// #1497): neither is bumped by readings, so neither is a liveness signal.
// Deliberately does not filter on sensor_reading.valid: this answers "is data
// arriving", not "is the data good".
func (r *Repository) ListBoardsWithState(ctx context.Context) ([]BoardWithReadingRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT board_id, device_id, last_reading_at
		FROM v_board_last_reading
		ORDER BY board_id
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

// GetBoardIdentity returns a board's device_id, or pgx.ErrNoRows (unwrapped,
// so callers can errors.Is against it directly) when board_id is unknown.
func (r *Repository) GetBoardIdentity(ctx context.Context, boardID int64) (string, error) {
	var deviceID string
	err := r.db.QueryRow(ctx, `SELECT device_id FROM board WHERE board_id = $1`, boardID).Scan(&deviceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		return "", fmt.Errorf("get board identity for %d: %w", boardID, err)
	}
	return deviceID, nil
}

// ListSensorDetailsForBoard returns every sensor recorded for a board (FR6 —
// no filtering by recency, region, or config membership; a sensor that
// stopped reporting years ago still appears), each with its most recent
// reading when it has one.
//
// Sensor identity comes from v_sensor_current (leaflab/migrate/migrations/
// 012_views.up.sql), which already resolves the sensor_name_history SCD2
// join to the current open row and joins sensor_type — re-deriving that by
// hand here would duplicate logic that already exists. Readings come
// directly from sensor_reading, not the heavier v_sensor_reading_enriched,
// whose device_config/region joins are dead weight here (no region or
// location is shown in M1).
//
// This is one query for the board's sensors plus a LATERAL "most recent
// reading" join per sensor row — not one query per sensor — since boards
// carry on the order of tens of sensors and sensor_reading is a
// TimescaleDB hypertable.
//
// LatestValue/LatestRecordedAt/LatestValid are nil together, only when the
// sensor has never reported a reading. Deliberately does not filter or
// branch on `valid`: the latest reading's valid flag is carried straight
// through as a display property (FR7), not used to hide the reading or to
// derive reporting state (that's reportingState, applied by the caller to
// LatestRecordedAt).
func (r *Repository) ListSensorDetailsForBoard(ctx context.Context, boardID int64) ([]SensorDetailRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT
			sc.sensor_id,
			COALESCE(sc.sensor_name, ''),
			sc.sensor_unit,
			sc.sensor_type_name,
			lr.value,
			lr.recorded_at,
			lr.valid
		FROM v_sensor_current sc
		LEFT JOIN LATERAL (
			SELECT sr.value, sr.recorded_at, sr.valid
			FROM sensor_reading sr
			WHERE sr.sensor_id = sc.sensor_id
			ORDER BY sr.recorded_at DESC
			LIMIT 1
		) lr ON TRUE
		WHERE sc.board_id = $1
		ORDER BY sc.sensor_id
	`, boardID)
	if err != nil {
		return nil, fmt.Errorf("list sensor details for board %d: %w", boardID, err)
	}
	defer rows.Close()

	var sensors []SensorDetailRow
	for rows.Next() {
		var s SensorDetailRow
		if err := rows.Scan(
			&s.SensorID,
			&s.SensorName,
			&s.Unit,
			&s.SensorTypeName,
			&s.LatestValue,
			&s.LatestRecordedAt,
			&s.LatestValid,
		); err != nil {
			return nil, fmt.Errorf("scan sensor detail: %w", err)
		}
		sensors = append(sensors, s)
	}
	return sensors, rows.Err()
}

// SensorDetailRow is one sensor plus its most recent reading, if it has
// ever reported one. LatestValue/LatestRecordedAt/LatestValid are nil
// together exactly when the sensor has no readings at all.
type SensorDetailRow struct {
	SensorID       int64
	SensorName     string
	Unit           string
	SensorTypeName string

	LatestValue      *float64
	LatestRecordedAt *time.Time
	LatestValid      *bool
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

// -- M2 ownership/authorization repository methods --------------------------

// GetLeafLabUserIDBySub resolves an OIDC subject claim to a leaflab_user_id.
// Returns (0, false, nil) when no leaflab_user row exists for the subject --
// leaflab-api never creates one (LB1, hard constraint); provisioning stays
// leaflab-ui's exclusive responsibility via upsertLeafLabUser
// (leaflab/ui/handlers_auth.go).
func (r *Repository) GetLeafLabUserIDBySub(ctx context.Context, oidcSub string) (int64, bool, error) {
	var id int64
	err := r.db.QueryRow(ctx, `
		SELECT leaflab_user_id FROM leaflab_user WHERE oidc_sub = $1
	`, oidcSub).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get leaflab_user by sub: %w", err)
	}
	return id, true, nil
}

// GetCurrentBoardOwner returns the leaflab_user_id of a board's current
// owner. owned=false means board_owner_history has no open (valid_to IS
// NULL) row for this board -- the board is unowned, never expressed as a
// NULL owner on an open row (see migration 013_ownership.up.sql). Must read
// via the valid_to IS NULL predicate backed by
// idx_board_owner_history_current.
func (r *Repository) GetCurrentBoardOwner(ctx context.Context, boardID int64) (int64, bool, error) {
	var ownerID int64
	err := r.db.QueryRow(ctx, `
		SELECT leaflab_user_id
		FROM board_owner_history
		WHERE board_id = $1 AND valid_to IS NULL
	`, boardID).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get current owner for board %d: %w", boardID, err)
	}
	return ownerID, true, nil
}

// GetBoardIDForSensor resolves a sensor_id to its owning board_id. ok=false
// means no sensor with that ID exists.
func (r *Repository) GetBoardIDForSensor(ctx context.Context, sensorID int64) (int64, bool, error) {
	var boardID int64
	err := r.db.QueryRow(ctx, `
		SELECT board_id FROM sensor WHERE sensor_id = $1
	`, sensorID).Scan(&boardID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get board for sensor %d: %w", sensorID, err)
	}
	return boardID, true, nil
}

// GetBoardIDForDeviceID resolves a device_id to its board_id without
// creating a row -- unlike the old GetOrCreateBoard, an unknown device_id
// must surface as codes.NotFound to the caller (FR7), not silently register
// a new board. ok=false means no board with that device_id exists.
func (r *Repository) GetBoardIDForDeviceID(ctx context.Context, deviceID string) (int64, bool, error) {
	var boardID int64
	err := r.db.QueryRow(ctx, `
		SELECT board_id FROM board WHERE device_id = $1
	`, deviceID).Scan(&boardID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get board for device %s: %w", deviceID, err)
	}
	return boardID, true, nil
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
