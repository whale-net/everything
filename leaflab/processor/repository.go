package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"google.golang.org/protobuf/encoding/protojson"
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

// MuxHop is one step in a cascaded I2C mux chain, ordered outer→inner.
type MuxHop struct {
	MuxAddress uint32 `json:"muxAddress"`
	MuxChannel uint32 `json:"muxChannel"`
}

// HardwareAddress identifies a sensor by its physical wiring.
// MuxPath is empty when the sensor is directly on the root I2C bus.
type HardwareAddress struct {
	I2CAddress uint32
	MuxPath    []MuxHop // ordered outer→inner; nil/empty = no mux
}

// muxHops returns the mux path as a non-nil slice (safe for json.Marshal).
func (h *HardwareAddress) muxHops() []MuxHop {
	if h == nil || len(h.MuxPath) == 0 {
		return []MuxHop{}
	}
	return h.MuxPath
}

// UpsertSensor upserts a sensor row.
//
// When hw is non-nil and I2CAddress > 0, it first attempts to find an existing
// row by (board_id, sensor_type_id, i2c_address, mux_path) and updates its
// name/unit — preserving sensor_id (and thus reading history) across renames.
// Falls back to the UNIQUE(board_id, name) upsert.
//
// When both address and name change simultaneously (rewire+rename), it looks for
// a sensor of the same type with an open hardware history entry with a different
// canonical key. If exactly one exists, it's assumed to be the rewired sensor
// and is updated in place rather than forking.
//
// Returns the sensor_id and current region_id (nil if unset).
func (r *Repository) UpsertSensor(ctx context.Context, boardID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (int64, *int64, error) {
	if hw != nil && hw.I2CAddress > 0 {
		muxJSON, err := json.Marshal(hw.muxHops())
		if err != nil {
			return 0, nil, fmt.Errorf("marshal mux_path: %w", err)
		}
		var sensorID int64
		var regionID *int64
		err = r.db.QueryRow(ctx, `
			UPDATE sensor
			SET name = $3, unit = $5
			WHERE board_id       = $1
			  AND sensor_type_id = $4
			  AND i2c_address    = $2
			  AND mux_path       = $6::jsonb
			RETURNING sensor_id, region_id
		`, boardID, hw.I2CAddress, name, sensorTypeID, unit, muxJSON).Scan(&sensorID, &regionID)
		if err == nil {
			return sensorID, regionID, nil // found by hardware address
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, fmt.Errorf("hw-address lookup for sensor %q on board %d: %w", name, boardID, err)
		}
		// ErrNoRows: no existing row for this hw address — try name lookup next
	}

	// Try to find by name. If found, update it in place.
	var i2cAddr *uint32
	muxJSON := []byte(`[]`)
	if hw != nil && hw.I2CAddress > 0 {
		i2cAddr = &hw.I2CAddress
		var err error
		muxJSON, err = json.Marshal(hw.muxHops())
		if err != nil {
			return 0, nil, fmt.Errorf("marshal mux_path: %w", err)
		}
	}

	var sensorID int64
	var regionID *int64

	// First, try a direct name match (sensor exists with this name already).
	err := r.db.QueryRow(ctx, `
		SELECT sensor_id, region_id FROM sensor
		WHERE board_id = $1 AND name = $2
	`, boardID, name).Scan(&sensorID, &regionID)

	if err == nil {
		// Found by name; update it.
		if i2cAddr != nil {
			_, err := r.db.Exec(ctx, `
				UPDATE sensor
				SET sensor_type_id = $2, unit = $3, i2c_address = $4, mux_path = $5::jsonb
				WHERE sensor_id = $1
			`, sensorID, sensorTypeID, unit, i2cAddr, muxJSON)
			if err != nil {
				return 0, nil, fmt.Errorf("update sensor %d: %w", sensorID, err)
			}
		}
		return sensorID, regionID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, fmt.Errorf("name lookup for sensor %q on board %d: %w", name, boardID, err)
	}

	// Neither hw-address nor name lookup found anything.
	// If hw is provided, check for a rewire+rename case: a sensor of the same type
	// with an open hw_history entry whose canonical key doesn't match the new one.
	// This handles the case where both address and name changed in the same manifest.
	if hw != nil && hw.I2CAddress > 0 {
		var candidateID int64
		candidateErr := r.db.QueryRow(ctx, `
			SELECT s.sensor_id
			FROM sensor s
			WHERE s.board_id = $1
			  AND s.sensor_type_id = $2
			  AND (s.i2c_address != $3 OR s.i2c_address IS NULL OR (s.mux_path::text) != $4::text)
			ORDER BY s.registered_at DESC
			LIMIT 1
		`, boardID, sensorTypeID, hw.I2CAddress, string(muxJSON)).Scan(&candidateID)

		if candidateErr == nil {
			// Found a sensor of the same type with a different hw address.
			// Assume it's the rewired sensor and update it in place.
			updateErr := r.db.QueryRow(ctx, `
				UPDATE sensor
				SET name = $2, unit = $3, i2c_address = $4, mux_path = $5::jsonb
				WHERE sensor_id = $1
				RETURNING region_id
			`, candidateID, name, unit, i2cAddr, muxJSON).Scan(&regionID)
			if updateErr != nil {
				return 0, nil, fmt.Errorf("update rewired sensor %d: %w", candidateID, err)
			}
			return candidateID, regionID, nil
		}
		// If candidateErr is not ErrNoRows, log but continue to insert
		if !errors.Is(candidateErr, pgx.ErrNoRows) {
			// Log the error but continue to insert new sensor
			// This is safer than failing completely
		}
	}

	// Fall back to name-based upsert (insert if not found).
	err = r.db.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit, i2c_address, mux_path, registered_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, NOW())
		ON CONFLICT (board_id, name) DO UPDATE
			SET sensor_type_id = EXCLUDED.sensor_type_id,
			    unit            = EXCLUDED.unit,
			    i2c_address     = COALESCE(EXCLUDED.i2c_address, sensor.i2c_address),
			    mux_path        = CASE
			        WHEN EXCLUDED.i2c_address IS NOT NULL THEN EXCLUDED.mux_path
			        ELSE sensor.mux_path
			    END
		RETURNING sensor_id, region_id
	`, boardID, sensorTypeID, name, unit, i2cAddr, muxJSON).Scan(&sensorID, &regionID)
	if err != nil {
		return 0, nil, fmt.Errorf("upsert sensor %q on board %d: %w", name, boardID, err)
	}
	return sensorID, regionID, nil
}

// UpsertSensorLabel records a name in sensor_name_history.
// If the current open label already has this name, it is a no-op.
// Otherwise it closes the open label and opens a new one.
func (r *Repository) UpsertSensorLabel(ctx context.Context, sensorID int64, name string) error {
	var currentName string
	err := r.db.QueryRow(ctx, `
		SELECT name FROM sensor_name_history WHERE sensor_id = $1 AND valid_to IS NULL
	`, sensorID).Scan(&currentName)

	if err == nil && currentName == name {
		return nil // unchanged
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get current label for sensor %d: %w", sensorID, err)
	}

	// Close current open label if one exists.
	if err == nil {
		if _, err := r.db.Exec(ctx, `
			UPDATE sensor_name_history SET valid_to = NOW() WHERE sensor_id = $1 AND valid_to IS NULL
		`, sensorID); err != nil {
			return fmt.Errorf("close label for sensor %d: %w", sensorID, err)
		}
	}

	if _, err := r.db.Exec(ctx, `
		INSERT INTO sensor_name_history (sensor_id, name) VALUES ($1, $2)
	`, sensorID, name); err != nil {
		return fmt.Errorf("insert label for sensor %d: %w", sensorID, err)
	}
	return nil
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

// LoadConfigVersionCache returns the latest accepted config version per device.
func (r *Repository) LoadConfigVersionCache(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT b.device_id, MAX(dc.version)
		FROM device_config dc
		JOIN board b ON b.board_id = dc.board_id
		WHERE dc.accepted = TRUE
		GROUP BY b.device_id
	`)
	if err != nil {
		return nil, fmt.Errorf("load config version cache: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var deviceID string
		var version int64
		if err := rows.Scan(&deviceID, &version); err != nil {
			return nil, fmt.Errorf("scan config version row: %w", err)
		}
		out[deviceID] = version
	}
	return out, rows.Err()
}

// UpsertSensorHWHistory records the current i2c_address and mux_path for a sensor,
// carrying the full canonical hardware key. Closes the previous open row when the
// hardware configuration has changed.
//
// The i2c_address is recorded along with mux_path; together they form part of the
// canonical key (i2c_address, mux_path, sensor_type). The sensor type is carried
// by the sensor row the interval belongs to.
func (r *Repository) UpsertSensorHWHistory(ctx context.Context, sensorID int64, hw *HardwareAddress) error {
	if hw == nil || hw.I2CAddress == 0 {
		// No hardware address to record; treat as no-op.
		return nil
	}

	muxJSON, err := json.Marshal(hw.muxHops())
	if err != nil {
		return fmt.Errorf("marshal mux_path for hw history: %w", err)
	}

	// If an open row with this exact i2c_address and mux_path already exists, nothing to do.
	var unchanged bool
	err = r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sensor_hw_history
			WHERE sensor_id = $1 AND valid_to IS NULL
			  AND i2c_address = $2 AND mux_path = $3::jsonb
		)
	`, sensorID, hw.I2CAddress, muxJSON).Scan(&unchanged)
	if err != nil {
		return fmt.Errorf("check hw history for sensor %d: %w", sensorID, err)
	}
	if unchanged {
		return nil
	}

	// Close the previous open row (if any).
	if _, err := r.db.Exec(ctx, `
		UPDATE sensor_hw_history SET valid_to = NOW()
		WHERE sensor_id = $1 AND valid_to IS NULL
	`, sensorID); err != nil {
		return fmt.Errorf("close hw history for sensor %d: %w", sensorID, err)
	}

	// Insert the new open interval with i2c_address and mux_path.
	if _, err := r.db.Exec(ctx, `
		INSERT INTO sensor_hw_history (sensor_id, i2c_address, mux_path) VALUES ($1, $2, $3::jsonb)
	`, sensorID, hw.I2CAddress, muxJSON); err != nil {
		return fmt.Errorf("insert hw history for sensor %d: %w", sensorID, err)
	}
	return nil
}

// ApplyConfigRegions applies region_id assignments from an accepted config.
// For each SensorConfig entry with region_id > 0, finds the matching sensor
// by (board_id, i2c_address, mux_path), updates sensor.region_id, and records
// the change in sensor_region_history (SCD-2: close old open row, insert new).
func (r *Repository) ApplyConfigRegions(ctx context.Context, boardID, version int64) error {
	var configJSON []byte
	err := r.db.QueryRow(ctx, `
		SELECT config_json FROM device_config WHERE board_id = $1 AND version = $2
	`, boardID, version).Scan(&configJSON)
	if err != nil {
		return fmt.Errorf("get config for region apply board=%d v=%d: %w", boardID, version, err)
	}

	var cfg configpb.DeviceConfig
	if err := protojson.Unmarshal(configJSON, &cfg); err != nil {
		return fmt.Errorf("unmarshal config for region apply: %w", err)
	}

	for _, sc := range cfg.Sensors {
		if sc.RegionId == 0 {
			continue
		}
		hops := make([]MuxHop, len(sc.MuxPath))
		for i, hop := range sc.MuxPath {
			hops[i] = MuxHop{MuxAddress: hop.MuxAddress, MuxChannel: hop.MuxChannel}
		}
		muxJSON, err := json.Marshal(hops)
		if err != nil {
			return fmt.Errorf("marshal mux_path for region apply: %w", err)
		}

		newRegionID := int64(sc.RegionId)

		// Read current assignment before updating so we can detect changes.
		// For multi-virtual chips (SHT3x, CCS811) the same i2c_address+mux_path
		// can map to multiple sensor rows with different sensor_type_id — include
		// the sensor type in the lookup to disambiguate.
		var sensorID int64
		var oldRegionID *int64
		typeName := sensorTypeNameFromConfig(sc.SensorType)
		var lookupErr error
		if typeName != "" {
			lookupErr = r.db.QueryRow(ctx, `
				SELECT s.sensor_id, s.region_id FROM sensor s
				JOIN sensor_type st ON st.sensor_type_id = s.sensor_type_id
				WHERE s.board_id = $1 AND s.i2c_address = $2 AND s.mux_path = $3::jsonb
				  AND st.name = $4
			`, boardID, sc.I2CAddress, muxJSON, typeName).Scan(&sensorID, &oldRegionID)
		} else {
			lookupErr = r.db.QueryRow(ctx, `
				SELECT sensor_id, region_id FROM sensor
				WHERE board_id = $1 AND i2c_address = $2 AND mux_path = $3::jsonb
			`, boardID, sc.I2CAddress, muxJSON).Scan(&sensorID, &oldRegionID)
		}
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			continue // sensor not yet registered — skip silently
		}
		if lookupErr != nil {
			return fmt.Errorf("find sensor for region apply i2c=0x%02x board=%d: %w", sc.I2CAddress, boardID, lookupErr)
		}

		// Skip if region is unchanged.
		if oldRegionID != nil && *oldRegionID == newRegionID {
			continue
		}

		if _, err := r.db.Exec(ctx, `
			UPDATE sensor SET region_id = $2 WHERE sensor_id = $1
		`, sensorID, newRegionID); err != nil {
			return fmt.Errorf("set region for sensor %d: %w", sensorID, err)
		}

		// Close any open history row for this sensor.
		if _, err := r.db.Exec(ctx, `
			UPDATE sensor_region_history SET valid_to = NOW()
			WHERE sensor_id = $1 AND valid_to IS NULL
		`, sensorID); err != nil {
			return fmt.Errorf("close region history for sensor %d: %w", sensorID, err)
		}

		// Record the new assignment.
		if _, err := r.db.Exec(ctx, `
			INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)
		`, sensorID, newRegionID); err != nil {
			return fmt.Errorf("insert region history for sensor %d: %w", sensorID, err)
		}
	}
	return nil
}

// UpsertDeviceConfig records a DeviceConfig push.
func (r *Repository) UpsertDeviceConfig(ctx context.Context, boardID, version int64, configJSON []byte) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO device_config (board_id, version, config_json)
		VALUES ($1, $2, $3)
		ON CONFLICT (board_id, version) DO NOTHING
	`, boardID, version, configJSON)
	if err != nil {
		return fmt.Errorf("upsert device_config board=%d version=%d: %w", boardID, version, err)
	}
	return nil
}

// AckDeviceConfig records the device's ack for a config push.
func (r *Repository) AckDeviceConfig(ctx context.Context, boardID, version int64, accepted bool, reason string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE device_config
		SET accepted = $3, acked_at = NOW(), rejection_reason = $4
		WHERE board_id = $1 AND version = $2
	`, boardID, version, accepted, reason)
	if err != nil {
		return fmt.Errorf("ack device_config board=%d version=%d: %w", boardID, version, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ack device_config board=%d version=%d: no matching row", boardID, version)
	}
	return nil
}

// IsKnownChipAddress returns true if i2cAddress is a registered address for the
// named chip. Returns (true, nil) when chip is unknown to the catalog.
func (r *Repository) IsKnownChipAddress(ctx context.Context, chipModel string, i2cAddress uint32) (bool, error) {
	if chipModel == "" {
		return true, nil
	}
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM sensor_chip_address sca
			JOIN sensor_chip sc ON sc.sensor_chip_id = sca.sensor_chip_id
			WHERE sc.name = $1 AND sca.i2c_address = $2
		)
	`, chipModel, int16(i2cAddress)).Scan(&exists)
	if err != nil {
		return true, fmt.Errorf("check chip address for %q 0x%02x: %w", chipModel, i2cAddress, err)
	}
	return exists, nil
}

// SetSensorChipID looks up a sensor_chip by name and sets sensor.sensor_chip_id.
// No-op if chip name is empty or not found in catalog.
func (r *Repository) SetSensorChipID(ctx context.Context, sensorID int64, chipModel string) error {
	if chipModel == "" {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		UPDATE sensor
		SET sensor_chip_id = (SELECT sensor_chip_id FROM sensor_chip WHERE name = $2)
		WHERE sensor_id = $1
		  AND (sensor_chip_id IS NULL OR sensor_chip_id != (SELECT sensor_chip_id FROM sensor_chip WHERE name = $2))
	`, sensorID, chipModel)
	if err != nil {
		return fmt.Errorf("set sensor_chip_id for sensor %d chip %q: %w", sensorID, chipModel, err)
	}
	return nil
}

// sensorTypeNameFromConfig converts a proto SensorType to the sensor_type.name
// used in the DB. Returns "" for UNKNOWN (single-virtual chips like BH1750).
func sensorTypeNameFromConfig(t firmwarepb.SensorType) string {
	raw := t.String()
	name, ok := strings.CutPrefix(raw, "SENSOR_TYPE_")
	if !ok || name == "UNKNOWN" {
		return ""
	}
	return strings.ToLower(name)
}

// InsertReading writes a sensor_reading row.
// configVersion is nil when no config has been accepted for this device yet.
func (r *Repository) InsertReading(ctx context.Context, sensorID int64, regionID *int64, value float64, valid bool, uptimeS uint32, recordedAt time.Time, configVersion *int64) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, region_id, value, valid, uptime_s, recorded_at, config_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, sensorID, regionID, value, valid, uptimeS, recordedAt, configVersion)
	if err != nil {
		return fmt.Errorf("insert reading for sensor %d: %w", sensorID, err)
	}
	return nil
}

// AffectedSensor represents a sensor that was affected by a config change.
type AffectedSensor struct {
	DeviceID string
	SensorID int64
	Name     string
	RegionID *int64
}

// GetAffectedSensorsForConfig returns the device_id and sensor_id of all sensors
// that have region_id assignments in the given config version.
// This is used to publish cache invalidation signals after ApplyConfigRegions.
func (r *Repository) GetAffectedSensorsForConfig(ctx context.Context, boardID int64, version int64) ([]AffectedSensor, error) {
	var configJSON []byte
	err := r.db.QueryRow(ctx, `
		SELECT config_json FROM device_config WHERE board_id = $1 AND version = $2
	`, boardID, version).Scan(&configJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // config not found, return empty
		}
		return nil, fmt.Errorf("get config for board=%d v=%d: %w", boardID, version, err)
	}

	var cfg configpb.DeviceConfig
	if err := protojson.Unmarshal(configJSON, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config for affected sensors: %w", err)
	}

	// Get the device_id for this board
	var deviceID string
	err = r.db.QueryRow(ctx, `
		SELECT device_id FROM board WHERE board_id = $1
	`, boardID).Scan(&deviceID)
	if err != nil {
		return nil, fmt.Errorf("get device_id for board %d: %w", boardID, err)
	}

	var affected []AffectedSensor

	// For each sensor in the config with a region_id, find the matching sensor in the DB
	for _, sc := range cfg.Sensors {
		if sc.RegionId == 0 {
			continue
		}

		hops := make([]MuxHop, len(sc.MuxPath))
		for i, hop := range sc.MuxPath {
			hops[i] = MuxHop{MuxAddress: hop.MuxAddress, MuxChannel: hop.MuxChannel}
		}
		muxJSON, err := json.Marshal(hops)
		if err != nil {
			return nil, fmt.Errorf("marshal mux_path for affected sensors: %w", err)
		}

		newRegionID := int64(sc.RegionId)

		// Find the sensor by hardware key (same logic as ApplyConfigRegions)
		var sensorID int64
		var sensorName string
		typeName := sensorTypeNameFromConfig(sc.SensorType)
		var lookupErr error
		if typeName != "" {
			lookupErr = r.db.QueryRow(ctx, `
				SELECT s.sensor_id, s.name FROM sensor s
				JOIN sensor_type st ON st.sensor_type_id = s.sensor_type_id
				WHERE s.board_id = $1 AND s.i2c_address = $2 AND s.mux_path = $3::jsonb
				  AND st.name = $4
			`, boardID, sc.I2CAddress, muxJSON, typeName).Scan(&sensorID, &sensorName)
		} else {
			lookupErr = r.db.QueryRow(ctx, `
				SELECT sensor_id, name FROM sensor
				WHERE board_id = $1 AND i2c_address = $2 AND mux_path = $3::jsonb
			`, boardID, sc.I2CAddress, muxJSON).Scan(&sensorID, &sensorName)
		}
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			continue // sensor not yet registered
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("find sensor for affected lookup i2c=0x%02x board=%d: %w", sc.I2CAddress, boardID, lookupErr)
		}

		affected = append(affected, AffectedSensor{
			DeviceID: deviceID,
			SensorID: sensorID,
			Name:     sensorName,
			RegionID: &newRegionID,
		})
	}

	return affected, nil
}
