package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/pagetoken"
	pb "github.com/whale-net/everything/leaflab/api/proto"
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

// ── Household resolution accessors ───────────────────────────────────────────
// Per FR1.1: every entity (board, region, plant, sensor, reading) resolves to
// exactly one household. These accessors provide the resolution paths.

// GetBoardHousehold resolves a board to its owning household_id.
// Returns pgx.ErrNoRows if board not found or not yet claimed.
func (r *Repository) GetBoardHousehold(ctx context.Context, boardID int64) (int64, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id FROM board
		WHERE board_id = $1
	`, boardID).Scan(&householdID)
	if err != nil {
		return 0, fmt.Errorf("get board %d household: %w", boardID, err)
	}
	return householdID, nil
}

// GetRegionHousehold resolves a region to its owning household_id.
// For non-root regions, traverses the parent tree to find the root's household_id.
// Per FR1.1: region trees carry household_id only on root; descendants inherit.
func (r *Repository) GetRegionHousehold(ctx context.Context, regionID int64) (int64, error) {
	var householdID int64
	// Use a recursive CTE to find the root region (parent_region_id IS NULL)
	// and return its household_id.
	err := r.db.QueryRow(ctx, `
		WITH RECURSIVE region_path AS (
			-- Base case: the given region
			SELECT region_id, parent_region_id, household_id
			FROM region
			WHERE region_id = $1
			
			UNION ALL
			
			-- Recursive case: traverse up to parent
			SELECT r.region_id, r.parent_region_id, r.household_id
			FROM region r
			JOIN region_path rp ON r.region_id = rp.parent_region_id
		)
		-- Return the household_id of the root (where parent_region_id IS NULL)
		SELECT household_id
		FROM region_path
		WHERE parent_region_id IS NULL
		LIMIT 1
	`, regionID).Scan(&householdID)
	if err != nil {
		return 0, fmt.Errorf("get region %d household: %w", regionID, err)
	}

	return householdID, nil
}

// GetPlantHousehold resolves a plant to its owning household_id.
func (r *Repository) GetPlantHousehold(ctx context.Context, plantID int64) (int64, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id FROM plant
		WHERE plant_id = $1
	`, plantID).Scan(&householdID)
	if err != nil {
		return 0, fmt.Errorf("get plant %d household: %w", plantID, err)
	}
	return householdID, nil
}

// GetSensorHousehold resolves a sensor to its owning household_id through the board.
// Per FR1.1: sensors inherit household through board.
func (r *Repository) GetSensorHousehold(ctx context.Context, sensorID int64) (int64, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT b.household_id FROM sensor s
		JOIN board b ON b.board_id = s.board_id
		WHERE s.sensor_id = $1
	`, sensorID).Scan(&householdID)
	if err != nil {
		return 0, fmt.Errorf("get sensor %d household: %w", sensorID, err)
	}
	return householdID, nil
}

// ── SCD2 write helpers (close-and-open pattern) ───────────────────────────────

// UpdateHouseholdMember closes the current membership record and opens a new one.
// Per SCD2 pattern: UPDATE valid_to, then INSERT new row.
// Returns the new member_id if successful.
func (r *Repository) UpdateHouseholdMember(ctx context.Context, householdID int64, principalID string, newRole string) (int64, error) {
	// Close the current membership record.
	_, err := r.db.Exec(ctx, `
		UPDATE household_member
		SET valid_to = NOW()
		WHERE household_id = $1 AND principal_id = $2 AND valid_to IS NULL
	`, householdID, principalID)
	if err != nil {
		return 0, fmt.Errorf("close household_member for household %d principal %s: %w", householdID, principalID, err)
	}

	// Insert new membership record with updated role.
	var memberID int64
	err = r.db.QueryRow(ctx, `
		INSERT INTO household_member (household_id, principal_id, role, valid_from)
		VALUES ($1, $2, $3, NOW())
		RETURNING member_id
	`, householdID, principalID, newRole).Scan(&memberID)
	if err != nil {
		return 0, fmt.Errorf("insert new household_member for household %d principal %s: %w", householdID, principalID, err)
	}

	return memberID, nil
}

// AddHouseholdMember adds a new principal to a household.
// Per SCD2 pattern: INSERT new row (initial membership).
func (r *Repository) AddHouseholdMember(ctx context.Context, householdID int64, principalID string, role string) (int64, error) {
	var memberID int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO household_member (household_id, principal_id, role, valid_from)
		VALUES ($1, $2, $3, NOW())
		RETURNING member_id
	`, householdID, principalID, role).Scan(&memberID)
	if err != nil {
		return 0, fmt.Errorf("add household_member to household %d principal %s: %w", householdID, principalID, err)
	}

	return memberID, nil
}

// RemoveHouseholdMember closes the current membership record (soft-delete via SCD2).
func (r *Repository) RemoveHouseholdMember(ctx context.Context, householdID int64, principalID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE household_member
		SET valid_to = NOW()
		WHERE household_id = $1 AND principal_id = $2 AND valid_to IS NULL
	`, householdID, principalID)
	if err != nil {
		return fmt.Errorf("remove household_member from household %d principal %s: %w", householdID, principalID, err)
	}

	return nil
}

// TransferBoardOwnership transfers a board to a new household (close-and-open on board_ownership).
// Per SCD2 pattern: UPDATE valid_to on current owner, then INSERT new ownership row.
// Also updates board.household_id to the new household (O(1) current-value access).
func (r *Repository) TransferBoardOwnership(ctx context.Context, boardID int64, newHouseholdID int64) error {
	// Use a transaction to ensure atomicity of board_ownership update and board.household_id update.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transfer board %d ownership: %w", boardID, err)
	}
	defer tx.Rollback(ctx)

	// Close the current ownership record.
	_, err = tx.Exec(ctx, `
		UPDATE board_ownership
		SET valid_to = NOW()
		WHERE board_id = $1 AND valid_to IS NULL
	`, boardID)
	if err != nil {
		return fmt.Errorf("close board_ownership for board %d: %w", boardID, err)
	}

	// Insert new ownership record.
	_, err = tx.Exec(ctx, `
		INSERT INTO board_ownership (board_id, household_id, valid_from)
		VALUES ($1, $2, NOW())
	`, boardID, newHouseholdID)
	if err != nil {
		return fmt.Errorf("insert new board_ownership for board %d to household %d: %w", boardID, newHouseholdID, err)
	}

	// Update board.household_id to the new household (O(1) current-value access).
	_, err = tx.Exec(ctx, `
		UPDATE board SET household_id = $2
		WHERE board_id = $1
	`, boardID, newHouseholdID)
	if err != nil {
		return fmt.Errorf("update board %d household_id: %w", boardID, err)
	}

	// Commit the transaction.
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transfer board %d ownership: %w", boardID, err)
	}

	return nil
}

// ── Plant placement (region) history ─────────────────────────────────────────

// BackdatingRefusal captures details of a rejected back-dating attempt (FR59.3).
// Placement boundaries are never back-dated: an interval opens at the instant
// the change is recorded. This error is returned when a caller attempts to set
// a valid_from earlier than the current time.
type BackdatingRefusal struct {
	PlantID       int64     // plant_id being moved
	AttemptedFrom time.Time // the timestamp the caller requested
	CurrentTime   time.Time // the actual current time
	Reason        string    // human-readable explanation
}

func (e *BackdatingRefusal) Error() string {
	return fmt.Sprintf(
		"back-dating rejected for plant %d: attempted valid_from %v is earlier than current time %v (%s)",
		e.PlantID, e.AttemptedFrom, e.CurrentTime, e.Reason,
	)
}

// MovePlantRegion moves a plant to a new region via SCD2 close-and-open.
// Closes the current (valid_to IS NULL) interval and opens a new one.
// Per SCD2 pattern: UPDATE valid_to on current placement, then INSERT new placement.
// Returns error if the plant is not found or if a back-dating attempt is made.
func (r *Repository) MovePlantRegion(ctx context.Context, plantID int64, newRegionID int64) error {
	// Per FR19/FR21: placement boundaries are never back-dated.
	// The interval opens at NOW(); any attempt to set an earlier valid_from is refused.
	// This is enforced by always using NOW() in the INSERT — the caller cannot override it.

	// Use a transaction to ensure atomicity of placement close and open.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin move plant %d to region %d: %w", plantID, newRegionID, err)
	}
	defer tx.Rollback(ctx)

	// Close the current placement record (set valid_to = NOW()).
	result, err := tx.Exec(ctx, `
		UPDATE plant_region_history
		SET valid_to = NOW()
		WHERE plant_id = $1 AND valid_to IS NULL
	`, plantID)
	if err != nil {
		return fmt.Errorf("close placement for plant %d: %w", plantID, err)
	}

	// Verify the plant had an active placement (if no row was updated, the plant doesn't exist or has no active placement).
	if result.RowsAffected() == 0 {
		return fmt.Errorf("plant %d has no active placement or does not exist", plantID)
	}

	// Insert new placement record. valid_from defaults to NOW() per table definition.
	_, err = tx.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, NOW())
	`, plantID, newRegionID)
	if err != nil {
		return fmt.Errorf("insert new placement for plant %d to region %d: %w", plantID, newRegionID, err)
	}

	// Commit the transaction.
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit move plant %d to region %d: %w", plantID, newRegionID, err)
	}

	return nil
}

// GetPlantCurrentRegion returns the current (valid_to IS NULL) region_id for a plant.
// Returns pgx.ErrNoRows if the plant has no active placement.
func (r *Repository) GetPlantCurrentRegion(ctx context.Context, plantID int64) (int64, error) {
	var regionID int64
	err := r.db.QueryRow(ctx, `
		SELECT region_id FROM plant_region_history
		WHERE plant_id = $1 AND valid_to IS NULL
	`, plantID).Scan(&regionID)
	if err != nil {
		return 0, fmt.Errorf("get plant %d current region: %w", plantID, err)
	}
	return regionID, nil
}

// GetPlantRegionAtTime returns the region_id a plant was in at a specific timestamp.
// Used for temporal joins (e.g., v_sensor_reading_with_plant attribution).
// Returns pgx.ErrNoRows if no placement interval contains the timestamp.
func (r *Repository) GetPlantRegionAtTime(ctx context.Context, plantID int64, atTime time.Time) (int64, error) {
	var regionID int64
	err := r.db.QueryRow(ctx, `
		SELECT region_id FROM plant_region_history
		WHERE plant_id = $1
		  AND valid_from <= $2
		  AND (valid_to IS NULL OR valid_to > $2)
	`, plantID, atTime).Scan(&regionID)
	if err != nil {
		return 0, fmt.Errorf("get plant %d region at time %v: %w", plantID, atTime, err)
	}
	return regionID, nil
}

// MovePlantRegionAt moves a plant to a new region at a specific timestamp via SCD2 close-and-open.
// Closes the current (valid_to IS NULL) interval and opens a new one.
// Per SCD2 pattern: UPDATE valid_to on current placement, then INSERT new placement.
// Returns BackdatingRefusal if the requested timestamp is in the past (FR19/FR21).
// Returns error if the plant is not found or database operations fail.
func (r *Repository) MovePlantRegionAt(ctx context.Context, plantID int64, newRegionID int64, validFrom time.Time) error {
	// Validate that the requested timestamp is not in the past (FR19/FR21: no back-dating).
	now := time.Now()
	if validFrom.Before(now) {
		return &BackdatingRefusal{
			PlantID:       plantID,
			AttemptedFrom: validFrom,
			CurrentTime:   now,
			Reason:        "placement boundaries are immutable once recorded; a new interval opens at the instant the change is recorded",
		}
	}

	// Use a transaction to ensure atomicity of placement close and open.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin move plant %d to region %d: %w", plantID, newRegionID, err)
	}
	defer tx.Rollback(ctx)

	// Close the current placement record (set valid_to = validFrom).
	result, err := tx.Exec(ctx, `
		UPDATE plant_region_history
		SET valid_to = $2
		WHERE plant_id = $1 AND valid_to IS NULL
	`, plantID, validFrom)
	if err != nil {
		return fmt.Errorf("close placement for plant %d: %w", plantID, err)
	}

	// Verify the plant had an active placement (if no row was updated, the plant doesn't exist or has no active placement).
	if result.RowsAffected() == 0 {
		return fmt.Errorf("plant %d has no active placement or does not exist", plantID)
	}

	// Insert new placement record with the requested valid_from timestamp.
	_, err = tx.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from)
		VALUES ($1, $2, $3)
	`, plantID, newRegionID, validFrom)
	if err != nil {
		return fmt.Errorf("insert new placement for plant %d to region %d: %w", plantID, newRegionID, err)
	}

	// Commit the transaction.
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit move plant %d to region %d: %w", plantID, newRegionID, err)
	}

	return nil
}

// RemovePlantPlacement closes the current placement interval for a plant.
// Used when a plant is removed from the system.
// Per SCD2 pattern: UPDATE valid_to on current placement to the given removal time.
// Returns BackdatingRefusal if the removal time is in the past (FR19/FR21).
// Returns error if the plant is not found or database operations fail.
func (r *Repository) RemovePlantPlacement(ctx context.Context, plantID int64, removalTime time.Time) error {
	// Validate that the removal time is not in the past.
	now := time.Now()
	if removalTime.Before(now) {
		return &BackdatingRefusal{
			PlantID:       plantID,
			AttemptedFrom: removalTime,
			CurrentTime:   now,
			Reason:        "plant removal timestamp cannot be in the past; the interval closes at the instant removal is recorded",
		}
	}

	// Close the current placement record.
	result, err := r.db.Exec(ctx, `
		UPDATE plant_region_history
		SET valid_to = $2
		WHERE plant_id = $1 AND valid_to IS NULL
	`, plantID, removalTime)
	if err != nil {
		return fmt.Errorf("close placement for removed plant %d: %w", plantID, err)
	}

	// Verify the plant had an active placement.
	if result.RowsAffected() == 0 {
		return fmt.Errorf("plant %d has no active placement or does not exist", plantID)
	}

	return nil
}

// ── Reading attribution (FR23) ──────────────────────────────────────────────
//
// FR23: nearest-ancestor attribution with sibling disclosure and no double-counting.
// A reading attributes to the nearest ancestor region (including its own) holding
// an active plant at reading time, and to all active plants in that one region.

// PlantAtRegionAtTime represents a plant active in a region at a specific time.
type PlantAtRegionAtTime struct {
	PlantID     int64
	PlantName   string
	PlantTypeID int64
}

// ReadingAttributionResult holds the resolution of a reading's attribution:
// the nearest ancestor region holding an active plant and all active plants
// in that region at the reading time.
type ReadingAttributionResult struct {
	AttributedRegionID int64
	ActivePlants       []PlantAtRegionAtTime
}

// ResolveReadingAttribution resolves a reading to its nearest ancestor region
// holding an active plant at the reading time, and returns all active plants
// in that region. Implements FR23 nearest-ancestor attribution.
//
// Given a reading's region_id and recorded_at timestamp, walks the region tree
// upward until finding a region with at least one active plant at that instant.
// Returns the attributed region and the full set of active plants in it (sibling
// disclosure). If no ancestor (including the reading's own region) has an active
// plant, returns an error.
//
// Active plant determination uses plant_region_history: a plant is active in a
// region at time T if there exists a row with:
//
//	valid_from <= T AND (valid_to IS NULL OR valid_to > T)
func (r *Repository) ResolveReadingAttribution(ctx context.Context, regionID int64, readingTime time.Time) (*ReadingAttributionResult, error) {
	// Recursive CTE to walk up the region tree, stopping at the first region
	// with an active plant at readingTime.
	var attributedRegionID int64
	err := r.db.QueryRow(ctx, `
		WITH RECURSIVE region_path AS (
			-- Base case: start with the given region
			SELECT region_id, parent_region_id
			FROM region
			WHERE region_id = $1

			UNION ALL

			-- Recursive case: move up to parent
			SELECT r.region_id, r.parent_region_id
			FROM region r
			JOIN region_path rp ON r.region_id = rp.parent_region_id
		)
		-- Find the first region in the path that has an active plant at reading time
		SELECT rp.region_id
		FROM region_path rp
		WHERE EXISTS (
			SELECT 1 FROM plant_region_history prh
			WHERE prh.region_id = rp.region_id
			  AND prh.valid_from <= $2
			  AND (prh.valid_to IS NULL OR prh.valid_to > $2)
		)
		ORDER BY (
			-- Order by distance from the given region (nearest first)
			-- Count how many steps up the tree this region is
			WITH counted AS (
				SELECT region_id, 
					   ROW_NUMBER() OVER (ORDER BY region_id) AS step_count
				FROM region_path
				WHERE region_id = rp.region_id
			)
			SELECT COUNT(*) FROM counted
		) ASC
		LIMIT 1
	`, regionID, readingTime).Scan(&attributedRegionID)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("no ancestor region holds an active plant at time %v for region %d", readingTime, regionID)
		}
		return nil, fmt.Errorf("resolve reading attribution for region %d at %v: %w", regionID, readingTime, err)
	}

	// Fetch all active plants in the attributed region at reading time.
	rows, err := r.db.Query(ctx, `
		SELECT p.plant_id, p.name, p.plant_type_id
		FROM plant p
		JOIN plant_region_history prh ON prh.plant_id = p.plant_id
		WHERE prh.region_id = $1
		  AND prh.valid_from <= $2
		  AND (prh.valid_to IS NULL OR prh.valid_to > $2)
		ORDER BY p.plant_id
	`, attributedRegionID, readingTime)

	if err != nil {
		return nil, fmt.Errorf("fetch active plants in region %d at %v: %w", attributedRegionID, readingTime, err)
	}
	defer rows.Close()

	var activePlants []PlantAtRegionAtTime
	for rows.Next() {
		var p PlantAtRegionAtTime
		if err := rows.Scan(&p.PlantID, &p.PlantName, &p.PlantTypeID); err != nil {
			return nil, fmt.Errorf("scan active plant: %w", err)
		}
		activePlants = append(activePlants, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active plants: %w", err)
	}

	return &ReadingAttributionResult{
		AttributedRegionID: attributedRegionID,
		ActivePlants:       activePlants,
	}, nil
}

// GetActivePlantsInRegionAtTime returns all active plants in a specific region
// at a given timestamp. Used internally by attribution and by other features
// that need to enumerate plants in a region at a point in time.
func (r *Repository) GetActivePlantsInRegionAtTime(ctx context.Context, regionID int64, atTime time.Time) ([]PlantAtRegionAtTime, error) {
	rows, err := r.db.Query(ctx, `
		SELECT p.plant_id, p.name, p.plant_type_id
		FROM plant p
		JOIN plant_region_history prh ON prh.plant_id = p.plant_id
		WHERE prh.region_id = $1
		  AND prh.valid_from <= $2
		  AND (prh.valid_to IS NULL OR prh.valid_to > $2)
		ORDER BY p.plant_id
	`, regionID, atTime)

	if err != nil {
		return nil, fmt.Errorf("get active plants in region %d at %v: %w", regionID, atTime, err)
	}
	defer rows.Close()

	var plants []PlantAtRegionAtTime
	for rows.Next() {
		var p PlantAtRegionAtTime
		if err := rows.Scan(&p.PlantID, &p.PlantName, &p.PlantTypeID); err != nil {
			return nil, fmt.Errorf("scan active plant: %w", err)
		}
		plants = append(plants, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active plants: %w", err)
	}

	return plants, nil
}

const (
	DefaultPageSize = 50
	MaxPageSize     = 1000
)

// TierReadingBucket is one bucketed row from a granularity tier (FR71,
// NFR5): bucket_start/min/max/sum/count, keyed by sensor and region only.
// Raw rows are reported as a bucket of one (BucketStart == the reading's own
// recorded_at, MinValue == MaxValue == SumValue == the raw value,
// ReadingCount == 1) so callers can treat every tier uniformly instead of
// type-switching on Tier. Enrichment (attribution, plant/board identity) is
// never applied here -- it happens above this function, per NFR5: aggregates
// carry no dimension joins, and this is their thinnest possible caller.
type TierReadingBucket struct {
	SensorID     int64
	RegionID     int64
	BucketStart  time.Time
	MinValue     float64
	MaxValue     float64
	SumValue     float64
	ReadingCount int64
}

// GetSeriesAtTier queries the pre-aggregated source for the tier ResolveTier
// selected (FR71/NFR5), for one sensor over [windowStart, windowEnd). It is
// the real query path behind ResolveTier's decision: ResolveTier says which
// tier answers and why; this is what actually answers from it.
//
// Every tier is queried directly against its own table -- sensor_reading,
// sensor_reading_5m or sensor_reading_1h -- with no dimension join, matching
// how the aggregates are defined (NFR5). RegionID is returned per bucket,
// not filtered on: a sensor that moved regions mid-bucket has more than one
// region_id in that bucket by construction (the aggregates are grouped by
// sensor_id, region_id, bucket_start), and collapsing that here would hide
// exactly the straddling case FR20's boundary capture exists to answer
// exactly. Filtering by region, and any attribution above sensor identity,
// is the caller's job.
//
// GRANULARITY_TIER_UNSPECIFIED has no query path: ResolveTier never returns
// it (A14 -- an unspecified or coarser-than-hourly request resolves to
// hourly), so a caller passing it here is a programming error, not a
// coarsening decision, and is refused rather than silently guessed at.
func (r *Repository) GetSeriesAtTier(ctx context.Context, tier pb.GranularityTier, sensorID int64, windowStart, windowEnd time.Time) ([]TierReadingBucket, error) {
	var query string
	switch tier {
	case pb.GranularityTier_GRANULARITY_TIER_RAW:
		query = `
			SELECT sensor_id, region_id, recorded_at, value, value, value, 1
			FROM sensor_reading
			WHERE sensor_id = $1 AND valid = TRUE
			  AND recorded_at >= $2 AND recorded_at < $3
			ORDER BY recorded_at ASC`
	case pb.GranularityTier_GRANULARITY_TIER_5_MINUTE:
		query = `
			SELECT sensor_id, region_id, bucket_start, min_value, max_value, sum_value, reading_count
			FROM sensor_reading_5m
			WHERE sensor_id = $1
			  AND bucket_start >= $2 AND bucket_start < $3
			ORDER BY bucket_start ASC`
	case pb.GranularityTier_GRANULARITY_TIER_HOURLY:
		query = `
			SELECT sensor_id, region_id, bucket_start, min_value, max_value, sum_value, reading_count
			FROM sensor_reading_1h
			WHERE sensor_id = $1
			  AND bucket_start >= $2 AND bucket_start < $3
			ORDER BY bucket_start ASC`
	default:
		return nil, fmt.Errorf("get series at tier: no query path for tier %v", tier)
	}

	rows, err := r.db.Query(ctx, query, sensorID, windowStart, windowEnd)
	if err != nil {
		return nil, fmt.Errorf("get series at tier %v for sensor %d: %w", tier, sensorID, err)
	}
	defer rows.Close()

	var buckets []TierReadingBucket
	for rows.Next() {
		var b TierReadingBucket
		if err := rows.Scan(&b.SensorID, &b.RegionID, &b.BucketStart, &b.MinValue, &b.MaxValue, &b.SumValue, &b.ReadingCount); err != nil {
			return nil, fmt.Errorf("scan tier %v bucket for sensor %d: %w", tier, sensorID, err)
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tier %v buckets for sensor %d: %w", tier, sensorID, err)
	}

	return buckets, nil
}

// GetSensorTimelines retrieves the three aligned timelines (name, hardware, region)
// for a sensor identified by sensor_id. Returns all historical entries in order.
// A sensor dropped from desired state keeps all three timelines.
func (r *Repository) GetSensorTimelines(ctx context.Context, sensorID int64) (*SensorTimelinesResult, error) {
	result := &SensorTimelinesResult{
		SensorID:         sensorID,
		NameTimeline:     []*SensorNameEntry{},
		HardwareTimeline: []*SensorHardwareEntry{},
		RegionTimeline:   []*SensorRegionEntry{},
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
