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

// ListBoards returns up to limit boards ordered by board_id, keyset-paginated
// on (board_id) per FR61: afterBoardID/hasAfter is the last board_id of the
// previous page (from contract.DecodeBoardCursor), not an offset, so
// pagination stays correct while boards are inserted mid-scan. Callers
// typically request limit+1 rows so a next page can be detected without a
// separate COUNT query.
func (r *Repository) ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32) ([]BoardRow, error) {
	var rows pgx.Rows
	var err error
	if hasAfter {
		rows, err = r.db.Query(ctx, `
			SELECT board_id, device_id, last_seen_at
			FROM board
			WHERE board_id > $1
			ORDER BY board_id
			LIMIT $2
		`, afterBoardID, limit)
	} else {
		rows, err = r.db.Query(ctx, `
			SELECT board_id, device_id, last_seen_at
			FROM board
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
}

// --- FR16/FR17 sensor identity resolution -----------------------------
//
// leaflab/api and leaflab/processor are separate Go binaries with no shared
// library layer between them beyond leaflab/hwkey (canonical key
// comparison, FR18) -- so HardwareAddress and the sensor_type name helper
// below intentionally mirror leaflab/processor/repository.go's types of
// the same name/shape rather than importing them.
//
// These are the API's read-only half of FR16's three-case resolution
// order (see leaflab/processor/repository.go's UpsertSensor doc comment
// for the full three cases): case 1 (FindSensorIDByHWKey) and case 2
// (FindSensorIDByName). Both are pure reads -- nothing is written -- so
// server.go can use them to decide, before anything is written, whether an
// incoming PushDeviceConfig or RewireSensor entry continues an existing
// sensor's identity (cases 1/2) or would establish a new one (case 3,
// FR17's refusal trigger). The actual case-3 refusal decision, FR16.4's
// swap detection, and RewireSensor's write path are Implementation-phase
// business logic, not scaffolded here.

// HardwareAddress identifies a sensor by its physical wiring: hwkey's
// canonical (FR18) address and mux-path types. MuxPath is empty when the
// sensor is directly on the root I2C bus.
type HardwareAddress struct {
	I2CAddress hwkey.AddressOpt
	MuxPath    hwkey.MuxPath
}

// hasKnownAddress reports whether hw carries a real, addressable I2C
// address -- neither absent nor the legacy "unknown address" sentinel (an
// explicit 0, see hwkey.AddressOpt).
func (h *HardwareAddress) hasKnownAddress() bool {
	return h != nil && !h.I2CAddress.IsAbsent() && !h.I2CAddress.IsUnknownSentinel()
}

// HardwareAddressFromSensorConfig builds a HardwareAddress from a
// firmware.SensorConfig entry (PushDeviceConfig/RewireSensor's wire
// shape), using hwkey for every field so this package never re-derives
// FR18's address/mux canonicalisation rules. i2c_address == 0 is treated
// as "no address" (config.proto's implicit sentinel, matching
// leaflab/processor's manifest handling).
func HardwareAddressFromSensorConfig(muxPath []*configpb.MuxHop, i2cAddress uint32) *HardwareAddress {
	addr := hwkey.Address(uint16(i2cAddress))
	if addr.IsUnknownSentinel() {
		return nil
	}
	hops := make(hwkey.MuxPath, len(muxPath))
	for i, hop := range muxPath {
		hops[i] = hwkey.MuxHop{MuxAddress: hop.GetMuxAddress(), MuxChannel: hop.GetMuxChannel()}
	}
	return &HardwareAddress{I2CAddress: addr, MuxPath: hops}
}

// sensorTypeNameFromConfig converts a proto SensorType to the
// sensor_type.name used in the DB. Mirrors
// leaflab/processor/repository.go's function of the same name exactly.
// Returns "" for UNKNOWN (single-virtual chips like BH1750).
func sensorTypeNameFromConfig(t firmwarepb.SensorType) string {
	raw := t.String()
	name, ok := strings.CutPrefix(raw, "SENSOR_TYPE_")
	if !ok || name == "UNKNOWN" {
		return ""
	}
	return strings.ToLower(name)
}

// FindSensorIDByHWKey resolves an existing sensor on boardID whose current
// (sensor_type, i2c_address, mux_path) equals hw -- FR16 case 1,
// read-only. typeName is resolved to a sensor_type_id first, also
// read-only: an unknown type name means no sensor of that type can exist
// yet, so this returns (0, false, nil) rather than upserting one into
// existence the way leaflab/processor's UpsertSensorType would.
func (r *Repository) FindSensorIDByHWKey(ctx context.Context, boardID int64, typeName string, hw *HardwareAddress) (int64, bool, error) {
	if !hw.hasKnownAddress() {
		return 0, false, nil
	}

	sensorTypeID, ok, err := r.resolveSensorTypeID(ctx, typeName)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}

	key := hwkey.Key{I2CAddress: hw.I2CAddress, MuxPath: hw.MuxPath, SensorTypeID: hwkey.SensorTypeID(sensorTypeID)}
	pred, predArgs := key.SQLPredicate(1)
	args := append([]any{boardID}, predArgs...)

	var sensorID int64
	query := fmt.Sprintf(`SELECT sensor_id FROM sensor WHERE board_id = $1 AND %s`, pred)
	err = r.db.QueryRow(ctx, query, args...).Scan(&sensorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find sensor by hw key on board %d: %w", boardID, err)
	}
	return sensorID, true, nil
}

// FindSensorIDByName resolves an existing sensor on boardID by its current
// name -- FR16 case 2, read-only.
func (r *Repository) FindSensorIDByName(ctx context.Context, boardID int64, name string) (int64, bool, error) {
	var sensorID int64
	err := r.db.QueryRow(ctx, `SELECT sensor_id FROM sensor WHERE board_id = $1 AND name = $2`, boardID, name).Scan(&sensorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find sensor by name %q on board %d: %w", name, boardID, err)
	}
	return sensorID, true, nil
}

// resolveSensorTypeID looks up sensor_type_id for typeName, read-only.
// Returns (0, false, nil) when no sensor_type with that name exists yet --
// shared by FindSensorIDByHWKey and checkPushConfigIdentity's FR16.4/FR17
// resolution, both of which need this exact "unknown type name means no
// sensor of that type can exist yet" behaviour.
func (r *Repository) resolveSensorTypeID(ctx context.Context, typeName string) (int64, bool, error) {
	var sensorTypeID int64
	err := r.db.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type WHERE name = $1`, typeName).Scan(&sensorTypeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find sensor_type_id for %q: %w", typeName, err)
	}
	return sensorTypeID, true, nil
}

// BoardSensorIdentity is one existing sensor's identity snapshot on the API
// side -- mirrors leaflab/processor/repository.go's type of the same
// name/shape (see this file's package doc comment on why it isn't shared
// as a single type across the two binaries). Used by
// checkPushConfigIdentity's FR16.4 swap detection and FR17 case-3 refusal
// decision, which need every existing identity on the board at once, not
// one entry's candidate match in isolation the way
// FindSensorIDByHWKey/FindSensorIDByName do.
type BoardSensorIdentity struct {
	SensorID     int64
	Name         string
	SensorTypeID int64
	HW           *HardwareAddress // nil when the row has no known address
}

// LoadBoardSensorIdentities returns the current identity snapshot for every
// sensor on a board, read-only. See BoardSensorIdentity.
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

// RewireSensorHW updates sensorID's hardware key in place and records a
// sensor_hw_history interval, atomically -- FR16's rewire path, applied
// through the explicit RewireSensor RPC. Unlike
// leaflab/processor/repository.go's RewireAndRenameSensor +
// UpsertSensorHWHistory pair (called sequentially, not transactionally,
// since the device manifest path already tolerates a partial write being
// corrected by the device's next manifest), this wraps both writes in one
// transaction: RewireSensor is a one-shot operator action with no retry
// signal of its own, so the sensor_hw_history interval must never be
// written without the sensor row's own hardware key changing to match, or
// vice versa. Callers are expected to have already resolved sensorID via
// FindSensorIDByName (FR16 case 2 -- name is RewireSensor's stable
// anchor); this method does not re-resolve identity.
func (r *Repository) RewireSensorHW(ctx context.Context, sensorID int64, hw *HardwareAddress) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin rewire tx for sensor %d: %w", sensorID, err)
	}
	defer tx.Rollback(ctx) // no-op once Commit has succeeded

	var i2cAddr *int32
	muxJSON := []byte(`[]`)
	muxText := hwkey.MuxPath(nil).SQLText()
	if hw.hasKnownAddress() {
		v, _ := hw.I2CAddress.Value()
		addr := int32(v)
		i2cAddr = &addr
		muxJSON = []byte(hw.MuxPath.SQLText())
		muxText = hw.MuxPath.SQLText()
	}

	tag, err := tx.Exec(ctx, `
		UPDATE sensor SET i2c_address = $2, mux_path = $3::jsonb WHERE sensor_id = $1
	`, sensorID, i2cAddr, muxJSON)
	if err != nil {
		return fmt.Errorf("rewire sensor %d: %w", sensorID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("rewire sensor %d: no matching row", sensorID)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sensor_hw_history SET valid_to = NOW()
		WHERE sensor_id = $1 AND valid_to IS NULL
	`, sensorID); err != nil {
		return fmt.Errorf("close hw history for sensor %d: %w", sensorID, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sensor_hw_history (sensor_id, mux_path, i2c_address) VALUES ($1, $2::jsonb, $3)
	`, sensorID, muxText, i2cAddr); err != nil {
		return fmt.Errorf("insert hw history for sensor %d: %w", sensorID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit rewire tx for sensor %d: %w", sensorID, err)
	}
	return nil
}
