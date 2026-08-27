package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/hwkey"
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

// RegionApplySkipRow is one audit_log row leaflab/processor's
// ApplyConfigRegions wrote for a config entry it skipped instead of
// applying (FR1.3) -- see GetRegionApplySkips.
type RegionApplySkipRow struct {
	SensorID   int64
	Reason     string
	OccurredAt time.Time
}

// GetRegionApplySkips returns the audit_log rows leaflab/processor's
// ApplyConfigRegions wrote for deviceID's board (audit.ActionApplyConfigRegionSkip),
// most recent first -- FR1.3's caller-visible skip surface, read back
// through GetDeviceConfig (server.go). Joins entity_id (the skipped
// sensor's id, stored as text) to sensor.board_id rather than matching on
// audit_log.actor_subject's "board:<id>" text, so this stays correct even
// if that formatting ever changes; entity_kind is narrowed to "sensor" so
// a numeric entity_id from an unrelated action can never collide with a
// sensor_id by coincidence.
//
// Provenance (FR82.4) is Phase 4 -- until it lands this returns every skip
// recorded for the board, not only ones the calling principal authored
// (see ApplyConfigRegions' doc comment in leaflab/processor/repository.go).
func (r *Repository) GetRegionApplySkips(ctx context.Context, deviceID string) ([]RegionApplySkipRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT al.entity_id, al.reason, al.occurred_at
		FROM audit_log al
		JOIN sensor s ON s.sensor_id = al.entity_id::bigint
		JOIN board b ON b.board_id = s.board_id
		WHERE al.action = $1
		  AND al.entity_kind = $2
		  AND b.device_id = $3
		ORDER BY al.occurred_at DESC
	`, audit.ActionApplyConfigRegionSkip, audit.EntityKindSensor, deviceID)
	if err != nil {
		return nil, fmt.Errorf("get region apply skips for %s: %w", deviceID, err)
	}
	defer rows.Close()

	var skips []RegionApplySkipRow
	for rows.Next() {
		var entityID string
		var skip RegionApplySkipRow
		if err := rows.Scan(&entityID, &skip.Reason, &skip.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan region apply skip row for %s: %w", deviceID, err)
		}
		sensorID, err := strconv.ParseInt(entityID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse sensor_id %q from audit_log for %s: %w", entityID, deviceID, err)
		}
		skip.SensorID = sensorID
		skips = append(skips, skip)
	}
	return skips, rows.Err()
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
