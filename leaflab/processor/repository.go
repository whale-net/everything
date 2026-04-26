package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository holds all DB write operations for the processor.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// UpsertBoard inserts a board row if it doesn't exist, or updates last_seen_at
// if it does. Returns the board_id.
func (r *Repository) UpsertBoard(ctx context.Context, deviceID string) (int64, error) {
	var boardID int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO board (device_id, registered_at, last_seen_at)
		VALUES ($1, NOW(), NOW())
		ON CONFLICT (device_id) DO UPDATE
			SET last_seen_at = NOW()
		RETURNING board_id
	`, deviceID).Scan(&boardID)
	if err != nil {
		return 0, fmt.Errorf("upsert board %q: %w", deviceID, err)
	}
	return boardID, nil
}

// UpsertSensorType inserts a sensor_type by name if it doesn't exist.
// Returns the sensor_type_id. The name should already be normalised
// (stripped of SENSOR_TYPE_ prefix, lowercased).
func (r *Repository) UpsertSensorType(ctx context.Context, name, unit string) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO sensor_type (name, default_unit)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE
			SET default_unit = sensor_type.default_unit
		RETURNING sensor_type_id
	`, name, unit).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert sensor_type %q: %w", name, err)
	}
	return id, nil
}

// HardwareAddress identifies a sensor by its physical wiring.
// MuxAddress and MuxChannel are only meaningful when MuxAddress > 0.
type HardwareAddress struct {
	I2CAddress uint32
	MuxAddress uint32
	MuxChannel uint32
}

// UpsertSensor upserts a sensor row.
// When hw is non-nil (hardware address known), it first tries to find an
// existing row by (board_id, i2c_address, mux_address, mux_channel) and
// updates its name/type/unit — this preserves identity across renames.
// Falls back to the name-based UNIQUE(board_id, name) upsert.
// Returns the sensor_id and current region_id (nil if unset).
func (r *Repository) UpsertSensor(ctx context.Context, boardID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (int64, *int64, error) {
	if hw != nil && hw.I2CAddress > 0 {
		// Nullable mux fields: only set when mux is in use.
		var muxAddr, muxCh *uint32
		if hw.MuxAddress > 0 {
			muxAddr = &hw.MuxAddress
			muxCh = &hw.MuxChannel
		}
		var sensorID int64
		var regionID *int64
		err := r.db.QueryRow(ctx, `
			UPDATE sensor
			SET name = $3, unit = $5
			WHERE board_id       = $1
			  AND sensor_type_id = $4
			  AND i2c_address    = $2
			  AND mux_address    IS NOT DISTINCT FROM $6
			  AND mux_channel    IS NOT DISTINCT FROM $7
			RETURNING sensor_id, region_id
		`, boardID, hw.I2CAddress, name, sensorTypeID, unit, muxAddr, muxCh).Scan(&sensorID, &regionID)
		if err == nil {
			return sensorID, regionID, nil // found by hardware address
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, fmt.Errorf("hw-address lookup for sensor %q on board %d: %w", name, boardID, err)
		}
		// ErrNoRows: no existing row for this hw address — fall through to name upsert.
	}

	// Name-based upsert; also persists hw address columns when provided.
	var i2cAddr, muxAddr, muxCh *uint32
	if hw != nil && hw.I2CAddress > 0 {
		i2cAddr = &hw.I2CAddress
		if hw.MuxAddress > 0 {
			muxAddr = &hw.MuxAddress
			muxCh = &hw.MuxChannel
		}
	}
	var sensorID int64
	var regionID *int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address, mux_address, mux_channel, registered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (board_id, name) DO UPDATE
			SET sensor_type_id = EXCLUDED.sensor_type_id,
			    unit            = EXCLUDED.unit,
			    i2c_address     = COALESCE(EXCLUDED.i2c_address, sensor.i2c_address),
			    mux_address     = COALESCE(EXCLUDED.mux_address, sensor.mux_address),
			    mux_channel     = COALESCE(EXCLUDED.mux_channel, sensor.mux_channel)
		RETURNING sensor_id, region_id
	`, boardID, sensorTypeID, name, unit, i2cAddr, muxAddr, muxCh).Scan(&sensorID, &regionID)
	if err != nil {
		return 0, nil, fmt.Errorf("upsert sensor %q on board %d: %w", name, boardID, err)
	}
	return sensorID, regionID, nil
}

// GetSensor returns the SensorInfo for a specific device+sensor name, or
// (zero, false) if not found. Used for cache-miss recovery.
func (r *Repository) GetSensor(ctx context.Context, deviceID, sensorName string) (SensorInfo, bool, error) {
	var info SensorInfo
	err := r.db.QueryRow(ctx, `
		SELECT s.sensor_id, s.region_id
		FROM sensor s
		JOIN board b ON b.board_id = s.board_id
		WHERE b.device_id = $1 AND s.name = $2
	`, deviceID, sensorName).Scan(&info.SensorID, &info.RegionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SensorInfo{}, false, nil
		}
		return SensorInfo{}, false, fmt.Errorf("get sensor %q/%q: %w", deviceID, sensorName, err)
	}
	return info, true, nil
}

// LoadSensorCache queries all boards and their sensors from the DB and
// returns them as a map of device_id → sensor_name → SensorInfo.
// Called once at startup to pre-warm the cache before any AMQP messages arrive.
func (r *Repository) LoadSensorCache(ctx context.Context) (map[string]map[string]SensorInfo, error) {
	rows, err := r.db.Query(ctx, `
		SELECT b.device_id, s.name, s.sensor_id, s.region_id
		FROM sensor s
		JOIN board b ON b.board_id = s.board_id
	`)
	if err != nil {
		return nil, fmt.Errorf("load sensor cache: %w", err)
	}
	defer rows.Close()

	out := make(map[string]map[string]SensorInfo)
	for rows.Next() {
		var deviceID, sensorName string
		var info SensorInfo
		if err := rows.Scan(&deviceID, &sensorName, &info.SensorID, &info.RegionID); err != nil {
			return nil, fmt.Errorf("scan sensor row: %w", err)
		}
		if out[deviceID] == nil {
			out[deviceID] = make(map[string]SensorInfo)
		}
		out[deviceID][sensorName] = info
	}
	return out, rows.Err()
}

// UpsertSensorHWHistory records a new wiring position for a sensor when it
// changes, closing the previous open row. No-op if wiring is unchanged.
func (r *Repository) UpsertSensorHWHistory(ctx context.Context, sensorID int64, hw *HardwareAddress) error {
	var muxAddr, muxCh *uint32
	if hw != nil && hw.MuxAddress > 0 {
		muxAddr = &hw.MuxAddress
		muxCh = &hw.MuxChannel
	}

	var curMuxAddr, curMuxCh *uint32
	err := r.db.QueryRow(ctx, `
		SELECT mux_address, mux_channel
		FROM sensor_hw_history
		WHERE sensor_id = $1 AND unassigned_at IS NULL
	`, sensorID).Scan(&curMuxAddr, &curMuxCh)

	noExisting := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !noExisting {
		return fmt.Errorf("get hw history for sensor %d: %w", sensorID, err)
	}

	unchanged := !noExisting &&
		nullableUint32Equal(curMuxAddr, muxAddr) &&
		nullableUint32Equal(curMuxCh, muxCh)
	if unchanged {
		return nil
	}

	if !noExisting {
		if _, err := r.db.Exec(ctx, `
			UPDATE sensor_hw_history SET unassigned_at = NOW()
			WHERE sensor_id = $1 AND unassigned_at IS NULL
		`, sensorID); err != nil {
			return fmt.Errorf("close hw history for sensor %d: %w", sensorID, err)
		}
	}

	if _, err := r.db.Exec(ctx, `
		INSERT INTO sensor_hw_history (sensor_id, mux_address, mux_channel)
		VALUES ($1, $2, $3)
	`, sensorID, muxAddr, muxCh); err != nil {
		return fmt.Errorf("insert hw history for sensor %d: %w", sensorID, err)
	}
	return nil
}

func nullableUint32Equal(a, b *uint32) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// InsertReading writes a sensor_reading row.
// uptimeS is the device uptime in seconds (proto uptime_ms divided by 1000).
func (r *Repository) InsertReading(ctx context.Context, sensorID int64, regionID *int64, value float64, valid bool, uptimeS uint32, recordedAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, region_id, value, valid, uptime_s, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sensorID, regionID, value, valid, uptimeS, recordedAt)
	if err != nil {
		return fmt.Errorf("insert reading for sensor %d: %w", sensorID, err)
	}
	return nil
}
