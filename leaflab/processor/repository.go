package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
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

// HardwareAddress identifies a sensor by its physical wiring, using
// hwkey's canonical (FR18) address and mux-path types so this package never
// re-derives address or mux comparison rules on its own.
// MuxPath is empty when the sensor is directly on the root I2C bus.
type HardwareAddress struct {
	I2CAddress hwkey.AddressOpt
	MuxPath    hwkey.MuxPath // ordered outer→inner; nil/empty = no mux
}

// hasKnownAddress reports whether hw carries a real, addressable I2C address
// -- neither absent (no hardware address recorded) nor the legacy "unknown
// address" sentinel (an explicit 0, see hwkey.AddressOpt).
func (h *HardwareAddress) hasKnownAddress() bool {
	return h != nil && !h.I2CAddress.IsAbsent() && !h.I2CAddress.IsUnknownSentinel()
}

// UpsertSensor upserts a sensor row, applying FR16's three-case resolution
// order so a rewire (address/mux change) or a rename (name change) never
// mints a second sensor row for the same physical sensor:
//
//  1. Match on (board_id, canonical hardware key) -- when hw carries a
//     known address, hwkey.Key.SQLPredicate matches idx_sensor_hw_address
//     exactly. The "rewire-with-rename" case: name may have changed too,
//     but the hardware key already identifies the row, so its name is
//     updated in place and sensor_id (and everything keyed on it --
//     readings, name history, region history) stays attached.
//  2. Match on (board_id, name) -- the UNIQUE(board_id, name) upsert
//     fallback. The "rename-stable rewire" case: the hardware key changed
//     (or was never known) but the name still identifies the row, so its
//     hardware key is updated in place.
//  3. Neither matches: this INSERTs a genuinely new sensor row. On the
//     device manifest path (this method's only caller) that's correct --
//     NFR9 means the device can never be refused. It's the two cases
//     above, not this one, that FR16.3 depends on: a manifest entry that
//     changes address *and* name in the same message matches neither 1 nor
//     2 individually. handleManifest resolves that case by elimination
//     before calling UpsertSensor -- see LoadBoardSensorIdentities and
//     RewireAndRenameSensor -- so this method's own fallback-to-INSERT
//     here is only ever reached for entries handleManifest has already
//     determined really are new.
//
// Returns the sensor_id and current region_id (nil if unset).
func (r *Repository) UpsertSensor(ctx context.Context, boardID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (int64, *int64, error) {
	if hw.hasKnownAddress() {
		key := hwkey.Key{I2CAddress: hw.I2CAddress, MuxPath: hw.MuxPath, SensorTypeID: hwkey.SensorTypeID(sensorTypeID)}
		pred, predArgs := key.SQLPredicate(1)
		args := append([]any{boardID}, predArgs...)
		args = append(args, name, unit)

		var sensorID int64
		var regionID *int64
		query := fmt.Sprintf(`
			UPDATE sensor
			SET name = $%d, unit = $%d
			WHERE board_id = $1 AND %s
			RETURNING sensor_id, region_id
		`, len(args)-1, len(args), pred)
		err := r.db.QueryRow(ctx, query, args...).Scan(&sensorID, &regionID)
		if err == nil {
			return sensorID, regionID, nil // found by hardware address
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, fmt.Errorf("hw-address lookup for sensor %q on board %d: %w", name, boardID, err)
		}
		// ErrNoRows: no existing row for this hw address — fall through to name upsert.
	}

	// Name-based upsert; persists i2c_address and mux_path when provided.
	var i2cAddr *int32
	muxJSON := []byte(`[]`)
	if hw.hasKnownAddress() {
		v, _ := hw.I2CAddress.Value()
		addr := int32(v)
		i2cAddr = &addr
		muxJSON = []byte(hw.MuxPath.SQLText())
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

// BoardSensorIdentity is one existing sensor's identity snapshot -- its
// sensor_id, current name, sensor_type_id and current hardware key -- as
// recorded in the sensor table before a manifest is applied. handleManifest
// loads all of a board's sensors this way up front so it can resolve FR16.3
// (a manifest entry that changes both hardware key and name in the same
// message) by elimination: an incoming entry is paired with the one
// existing identity that no entry in the same manifest claims by hardware
// key or by name, rather than being silently INSERTed as a new row.
type BoardSensorIdentity struct {
	SensorID     int64
	Name         string
	SensorTypeID int64
	HW           *HardwareAddress // nil when the row has no known address
}

// LoadBoardSensorIdentities returns the current identity snapshot for every
// sensor on a board, for FR16.3's elimination-based resolution (see
// BoardSensorIdentity and UpsertSensor's doc comment).
func (r *Repository) LoadBoardSensorIdentities(ctx context.Context, boardID int64) ([]BoardSensorIdentity, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sensor_id, name, sensor_type_id, i2c_address, mux_path::text
		FROM sensor
		WHERE board_id = $1
		ORDER BY sensor_id
	`, boardID)
	if err != nil {
		return nil, fmt.Errorf("load board sensor identities for board %d: %w", boardID, err)
	}
	defer rows.Close()

	var out []BoardSensorIdentity
	for rows.Next() {
		var bsi BoardSensorIdentity
		var i2cAddr *int32
		var muxText string
		if err := rows.Scan(&bsi.SensorID, &bsi.Name, &bsi.SensorTypeID, &i2cAddr, &muxText); err != nil {
			return nil, fmt.Errorf("scan board sensor identity for board %d: %w", boardID, err)
		}
		if i2cAddr != nil {
			var muxPath hwkey.MuxPath
			// muxText is mux_path::text in hwkey's own canonical encoding
			// (SQLText's doc comment) -- MuxPath.UnmarshalJSON is its exact
			// inverse, so this round-trips without this package re-deriving
			// mux_path parsing rules of its own.
			if err := muxPath.UnmarshalJSON([]byte(muxText)); err != nil {
				return nil, fmt.Errorf("parse mux_path for sensor %d: %w", bsi.SensorID, err)
			}
			bsi.HW = &HardwareAddress{I2CAddress: hwkey.Address(uint16(*i2cAddr)), MuxPath: muxPath}
		}
		out = append(out, bsi)
	}
	return out, rows.Err()
}

// RewireAndRenameSensor updates an existing sensor row's name, unit,
// sensor_type and hardware key in place by primary key, bypassing both the
// hardware-key lookup and the (board_id, name) upsert that UpsertSensor
// tries first -- FR16.3's simultaneous-rewire-and-rename case matches
// neither, by definition, so the caller (handleManifest) has already
// identified sensorID by elimination (see LoadBoardSensorIdentities) and
// just needs the row updated without another identity lookup that would
// fail. sensor_id, and everything keyed on it, is unchanged by construction
// -- this is an UPDATE, never an INSERT. Returns the sensor's current
// region_id (nil if unset), mirroring UpsertSensor's return shape so the
// caller can populate its sensor cache identically regardless of which of
// the two methods resolved the entry.
func (r *Repository) RewireAndRenameSensor(ctx context.Context, sensorID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (*int64, error) {
	var i2cAddr *int32
	muxJSON := []byte(`[]`)
	if hw.hasKnownAddress() {
		v, _ := hw.I2CAddress.Value()
		addr := int32(v)
		i2cAddr = &addr
		muxJSON = []byte(hw.MuxPath.SQLText())
	}
	var regionID *int64
	err := r.db.QueryRow(ctx, `
		UPDATE sensor
		SET name = $2, unit = $3, sensor_type_id = $4, i2c_address = $5, mux_path = $6::jsonb
		WHERE sensor_id = $1
		RETURNING region_id
	`, sensorID, name, unit, sensorTypeID, i2cAddr, muxJSON).Scan(&regionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("rewire and rename sensor %d: no matching row", sensorID)
	}
	if err != nil {
		return nil, fmt.Errorf("rewire and rename sensor %d: %w", sensorID, err)
	}
	return regionID, nil
}

// resolveManifestIdentities implements FR16.3's elimination step for a
// manifest. For each of the parallel entries described by sensorTypeIDs,
// hws and names (index-aligned with the manifest's own sensor list), it
// marks every existing identity that entry matches by hardware key (FR16
// case 1) or by name (FR16 case 2) as "claimed" -- exactly the two checks
// UpsertSensor's own SQL performs, duplicated here in Go because the
// elimination step needs to see every entry's claims at once, not one
// entry's claim in isolation the way a single UPDATE statement does.
//
// An entry that claims nothing is left "unresolved"; an existing identity
// nothing claims is left "unclaimed". When there is exactly one of each,
// of the same sensor_type, they're paired: that is FR16.3's case -- a
// manifest entry whose address *and* name both changed in the same
// message, so it matches neither of UpsertSensor's own lookups
// individually, yet by elimination there is only one physical sensor it
// could be. Anything more ambiguous (zero or multiple unresolved entries,
// zero or multiple unclaimed identities, or a sensor_type mismatch) is
// left unresolved here and falls through to UpsertSensor's normal
// resolution, i.e. case 3 -- a genuinely new row -- rather than guessing.
//
// Returns manifest-index → sensor_id for every entry resolved this way.
// handleManifest passes each into RewireAndRenameSensor instead of
// UpsertSensor.
func resolveManifestIdentities(existing []BoardSensorIdentity, sensorTypeIDs []int64, hws []*HardwareAddress, names []string) map[int]int64 {
	entryMatches := func(bsi BoardSensorIdentity, sensorTypeID int64, hw *HardwareAddress, name string) bool {
		if bsi.Name == name {
			return true
		}
		if hw.hasKnownAddress() && bsi.HW != nil && bsi.SensorTypeID == sensorTypeID {
			key := hwkey.Key{I2CAddress: hw.I2CAddress, MuxPath: hw.MuxPath, SensorTypeID: hwkey.SensorTypeID(sensorTypeID)}
			exKey := hwkey.Key{I2CAddress: bsi.HW.I2CAddress, MuxPath: bsi.HW.MuxPath, SensorTypeID: hwkey.SensorTypeID(bsi.SensorTypeID)}
			if key.Equal(exKey) {
				return true
			}
		}
		return false
	}

	claimed := make(map[int64]bool, len(existing))
	var unresolved []int
	for i := range hws {
		claimedThis := false
		for _, bsi := range existing {
			if entryMatches(bsi, sensorTypeIDs[i], hws[i], names[i]) {
				claimed[bsi.SensorID] = true
				claimedThis = true
			}
		}
		if !claimedThis {
			unresolved = append(unresolved, i)
		}
	}

	var unclaimed []BoardSensorIdentity
	for _, bsi := range existing {
		if !claimed[bsi.SensorID] {
			unclaimed = append(unclaimed, bsi)
		}
	}

	out := make(map[int]int64)
	if len(unresolved) == 1 && len(unclaimed) == 1 && unclaimed[0].SensorTypeID == sensorTypeIDs[unresolved[0]] {
		out[unresolved[0]] = unclaimed[0].SensorID
	}
	return out
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

// UpsertSensorHWHistory records the current (i2c_address, mux_path) for a
// sensor -- the ChipKey half of the FR18 canonical hardware key, the
// sensor_type component being carried by the sensor row this interval
// belongs to (FR16.1) -- using hwkey.MuxPath's canonical text form (FR18.1)
// so the stored value matches what idx_sensor_hw_address's
// (mux_path::text) expression -- and any later hwkey.Key.SQLPredicate
// lookup -- expects.
// Closes the previous open row when either the address or the path has
// changed; before this, the address wasn't recorded here at all, so an
// address-only change (mux_path unchanged) was silently dropped.
func (r *Repository) UpsertSensorHWHistory(ctx context.Context, sensorID int64, hw *HardwareAddress) error {
	var muxText string
	var i2cAddr *int32
	if hw != nil {
		muxText = hw.MuxPath.SQLText()
		if v, ok := hw.I2CAddress.Value(); ok {
			addr := int32(v)
			i2cAddr = &addr
		}
	} else {
		muxText = hwkey.MuxPath(nil).SQLText()
	}

	// If an open row with this exact address+path already exists, nothing
	// to do. i2c_address is compared with IS NOT DISTINCT FROM since NULL
	// (absent) must compare equal to NULL, not "unknown"/no-match the way
	// plain `=` would.
	var unchanged bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sensor_hw_history
			WHERE sensor_id = $1 AND valid_to IS NULL AND mux_path::text = $2
			  AND i2c_address IS NOT DISTINCT FROM $3
		)
	`, sensorID, muxText, i2cAddr).Scan(&unchanged)
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
		INSERT INTO sensor_hw_history (sensor_id, mux_path, i2c_address) VALUES ($1, $2::jsonb, $3)
	`, sensorID, muxText, i2cAddr); err != nil {
		return fmt.Errorf("insert hw history for sensor %d: %w", sensorID, err)
	}
	return nil
}

// RegionChange describes one sensor whose region_id ApplyConfigRegions
// changed, for FR73's cross-process cache invalidation: the caller
// (handler.go's handleConfigAck) publishes one invalidation event per
// entry after this method's per-sensor writes have committed (see
// leaflab/invalidation.Publisher.Publish's doc comment on publishing only
// after commit), so leaflab/processor's own SensorCache -- and every other
// process's -- is told about the change regardless of which process
// applied it.
type RegionChange struct {
	SensorID   int64
	SensorName string
	RegionID   int64
}

// ApplyConfigRegions applies region_id assignments from an accepted config.
// For each SensorConfig entry with region_id > 0, finds the matching sensor
// by (board_id, i2c_address, mux_path), updates sensor.region_id, and records
// the change in sensor_region_history (SCD-2: close old open row, insert new).
// Returns every sensor whose region_id actually changed.
func (r *Repository) ApplyConfigRegions(ctx context.Context, boardID, version int64) ([]RegionChange, error) {
	var configJSON []byte
	err := r.db.QueryRow(ctx, `
		SELECT config_json FROM device_config WHERE board_id = $1 AND version = $2
	`, boardID, version).Scan(&configJSON)
	if err != nil {
		return nil, fmt.Errorf("get config for region apply board=%d v=%d: %w", boardID, version, err)
	}

	var cfg configpb.DeviceConfig
	if err := protojson.Unmarshal(configJSON, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config for region apply: %w", err)
	}

	var changes []RegionChange
	for _, sc := range cfg.Sensors {
		if sc.RegionId == 0 {
			continue
		}
		muxPath := make(hwkey.MuxPath, len(sc.MuxPath))
		for i, hop := range sc.MuxPath {
			muxPath[i] = hwkey.MuxHop{MuxAddress: hop.MuxAddress, MuxChannel: hop.MuxChannel}
		}
		muxText := muxPath.SQLText()

		newRegionID := int64(sc.RegionId)

		// Read current assignment before updating so we can detect changes.
		// For multi-virtual chips (SHT3x, CCS811) the same i2c_address+mux_path
		// can map to multiple sensor rows with different sensor_type_id — include
		// the sensor type in the lookup to disambiguate. mux_path is compared via
		// its canonical text form (FR18.1), matching idx_sensor_hw_address's
		// (mux_path::text) expression rather than jsonb value equality.
		var sensorID int64
		var sensorName string
		var oldRegionID *int64
		typeName := sensorTypeNameFromConfig(sc.SensorType)
		var lookupErr error
		if typeName != "" {
			lookupErr = r.db.QueryRow(ctx, `
				SELECT s.sensor_id, s.name, s.region_id FROM sensor s
				JOIN sensor_type st ON st.sensor_type_id = s.sensor_type_id
				WHERE s.board_id = $1 AND s.i2c_address = $2 AND s.mux_path::text = $3
				  AND st.name = $4
			`, boardID, sc.I2CAddress, muxText, typeName).Scan(&sensorID, &sensorName, &oldRegionID)
		} else {
			lookupErr = r.db.QueryRow(ctx, `
				SELECT sensor_id, name, region_id FROM sensor
				WHERE board_id = $1 AND i2c_address = $2 AND mux_path::text = $3
			`, boardID, sc.I2CAddress, muxText).Scan(&sensorID, &sensorName, &oldRegionID)
		}
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			continue // sensor not yet registered — skip silently
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("find sensor for region apply i2c=0x%02x board=%d: %w", sc.I2CAddress, boardID, lookupErr)
		}

		// Skip if region is unchanged.
		if oldRegionID != nil && *oldRegionID == newRegionID {
			continue
		}

		if _, err := r.db.Exec(ctx, `
			UPDATE sensor SET region_id = $2 WHERE sensor_id = $1
		`, sensorID, newRegionID); err != nil {
			return nil, fmt.Errorf("set region for sensor %d: %w", sensorID, err)
		}

		// Close any open history row for this sensor.
		if _, err := r.db.Exec(ctx, `
			UPDATE sensor_region_history SET valid_to = NOW()
			WHERE sensor_id = $1 AND valid_to IS NULL
		`, sensorID); err != nil {
			return nil, fmt.Errorf("close region history for sensor %d: %w", sensorID, err)
		}

		// Record the new assignment.
		if _, err := r.db.Exec(ctx, `
			INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)
		`, sensorID, newRegionID); err != nil {
			return nil, fmt.Errorf("insert region history for sensor %d: %w", sensorID, err)
		}

		// This sensor's region_id has committed by this point (each
		// statement above auto-commits individually; ApplyConfigRegions
		// does not wrap the loop in an explicit transaction) -- safe to
		// report as a change the caller can now publish (see
		// RegionChange's doc comment on publish-after-commit).
		changes = append(changes, RegionChange{SensorID: sensorID, SensorName: sensorName, RegionID: newRegionID})
	}
	return changes, nil
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
