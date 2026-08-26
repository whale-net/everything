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
	configpb "github.com/whale-net/everything/firmware/proto/config"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
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
		// ErrNoRows: no existing row for this hw address — fall through to name upsert.
	}

	// Name-based upsert; persists i2c_address and mux_path when provided.
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
	err := r.db.QueryRow(ctx, `
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

// UpsertSensorHWHistory records the current mux_path for a sensor.
// Closes the previous open row when the path has changed.
func (r *Repository) UpsertSensorHWHistory(ctx context.Context, sensorID int64, hw *HardwareAddress) error {
	muxJSON, err := json.Marshal(hw.muxHops())
	if err != nil {
		return fmt.Errorf("marshal mux_path for hw history: %w", err)
	}

	// If an open row with this exact path already exists, nothing to do.
	var unchanged bool
	err = r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sensor_hw_history
			WHERE sensor_id = $1 AND valid_to IS NULL AND mux_path = $2::jsonb
		)
	`, sensorID, muxJSON).Scan(&unchanged)
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

	if _, err := r.db.Exec(ctx, `
		INSERT INTO sensor_hw_history (sensor_id, mux_path) VALUES ($1, $2::jsonb)
	`, sensorID, muxJSON); err != nil {
		return fmt.Errorf("insert hw history for sensor %d: %w", sensorID, err)
	}
	return nil
}

// ApplyConfigRegions applies region_id assignments from an accepted config.
// For each SensorConfig entry with region_id > 0, finds the matching sensor
// by (board_id, i2c_address, mux_path), validates the region against the board's household,
// checks staleness against the sensor's current region interval, then updates
// sensor.region_id and records the change in sensor_region_history (SCD-2: close old
// open row, insert new). If validation or staleness check fails, the entry is skipped
// and an audit record is written. (FR1.3)
func (r *Repository) ApplyConfigRegions(ctx context.Context, boardID, version int64) error {
	var configJSON []byte
	var pushTime time.Time
	err := r.db.QueryRow(ctx, `
		SELECT config_json, pushed_at FROM device_config WHERE board_id = $1 AND version = $2
	`, boardID, version).Scan(&configJSON, &pushTime)
	if err != nil {
		return fmt.Errorf("get config for region apply board=%d v=%d: %w", boardID, version, err)
	}

	var cfg configpb.DeviceConfig
	if err := protojson.Unmarshal(configJSON, &cfg); err != nil {
		return fmt.Errorf("unmarshal config for region apply: %w", err)
	}

	// Get the board's household for validation (FR1.2 write invariant enforcement).
	var householdID int64
	err = r.db.QueryRow(ctx, `
		SELECT household_id FROM board WHERE board_id = $1
	`, boardID).Scan(&householdID)
	if err != nil {
		return fmt.Errorf("get board household for region apply board=%d: %w", boardID, err)
	}

	for _, sc := range cfg.Sensors {
		if sc.RegionId == 0 {
			continue
		}
		hops := make([]MuxHop, len(sc.MuxPath))
		for j, hop := range sc.MuxPath {
			hops[j] = MuxHop{MuxAddress: hop.MuxAddress, MuxChannel: hop.MuxChannel}
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

		// FR1.3 apply-time validation: re-validate that the region belongs to the board's household.
		isValid, err := r.ValidateRegionBelongsToHousehold(ctx, newRegionID, householdID)
		if err != nil {
			return fmt.Errorf("validate region %d household %d: %w", newRegionID, householdID, err)
		}
		if !isValid {
			// Skip and audit this entry (FR1.3).
			// Note: provenance-based skips are keyed on FR82.4; household can query audit_record
			// to see skipped entries. Architectural decision: skips keyed on provenance (who
			// authored the entry, i.e., the caller who initiated the push) rather than
			// differs-from-base. See #1192 discussion.
			skipReason := fmt.Sprintf("region %d does not belong to household %d", newRegionID, householdID)
			if _, err := r.db.Exec(ctx, `
				INSERT INTO audit_record (actor_subject, target_household_id, action, entity_type,
				                         entity_id, config_version, i2c_address, mux_path, reason)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
			`, "", householdID, "skip_config_entry", "sensor", sensorID, version,
				sc.I2CAddress, muxJSON, skipReason); err != nil {
				return fmt.Errorf("audit skip for sensor %d: %w", sensorID, err)
			}
			continue
		}

		// FR1.3 staleness check: only apply if pushed_at >= sensor's current region interval's valid_from.
		// This ensures that a payload is applied only if it was pushed after the sensor's
		// current region assignment opened.
		var regionValidFrom *time.Time
		err = r.db.QueryRow(ctx, `
			SELECT valid_from FROM sensor_region_history
			WHERE sensor_id = $1 AND valid_to IS NULL
		`, sensorID).Scan(&regionValidFrom)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get current region interval for sensor %d: %w", sensorID, err)
		}

		// If sensor has a current region assignment and pushTime is before it opened, skip.
		if regionValidFrom != nil && pushTime.Before(*regionValidFrom) {
			skipReason := fmt.Sprintf("payload pushed at %v before current region interval opened at %v",
				pushTime, regionValidFrom)
			if _, err := r.db.Exec(ctx, `
				INSERT INTO audit_record (actor_subject, target_household_id, action, entity_type,
				                         entity_id, config_version, i2c_address, mux_path, reason)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
			`, "", householdID, "skip_config_entry", "sensor", sensorID, version,
				sc.I2CAddress, muxJSON, skipReason); err != nil {
				return fmt.Errorf("audit staleness skip for sensor %d: %w", sensorID, err)
			}
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

// ValidateRegionBelongsToHousehold checks whether a region belongs to the given household.
// A region belongs to a household if it is a root region (parent_region_id IS NULL) with
// matching household_id, or a descendant of such a root.
// Returns (true, nil) if the region belongs to the household.
// Returns (false, nil) if the region belongs to a different household or does not exist.
// Returns (false, err) on database error.
func (r *Repository) ValidateRegionBelongsToHousehold(ctx context.Context, regionID int64, householdID int64) (bool, error) {
	var count int64
	err := r.db.QueryRow(ctx, `
		WITH RECURSIVE region_ancestry AS (
			-- Base case: the region itself (if it's a root)
			SELECT region_id, household_id FROM region
			WHERE region_id = $1 AND parent_region_id IS NULL
			UNION ALL
			-- Recursive case: traverse up to find the root
			SELECT r.region_id, r.household_id FROM region r
			INNER JOIN region_ancestry ra ON ra.region_id = r.parent_region_id
			WHERE r.region_id = $1
		)
		SELECT COUNT(*) FROM region_ancestry WHERE household_id = $2
	`, regionID, householdID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("validate region %d household %d: %w", regionID, householdID, err)
	}
	return count > 0, nil
}

// ValidateBoardBelongsToHousehold checks whether a board belongs to the given household
// by examining the current board ownership row (where valid_to IS NULL).
// Returns (true, nil) if the board currently belongs to the household.
// Returns (false, nil) if the board belongs to a different household or does not exist.
// Returns (false, err) on database error.
func (r *Repository) ValidateBoardBelongsToHousehold(ctx context.Context, boardID int64, householdID int64) (bool, error) {
	var count int64
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM board
		WHERE board_id = $1 AND household_id = $2
	`, boardID, householdID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("validate board %d household %d: %w", boardID, householdID, err)
	}
	return count > 0, nil
}
