package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/pagetoken"
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

// GetPrincipalHousehold returns the current household for a principal.
// Returns 0, nil if the principal has no active household membership.
func (r *Repository) GetPrincipalHousehold(ctx context.Context, principalID string) (int64, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id FROM household_member
		WHERE principal_id = $1 AND valid_to IS NULL
		LIMIT 1
	`, principalID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("get principal %s household: %w", principalID, err)
	}
	return householdID, nil
}

// ListActivityRecords returns audit records for a household in reverse chronological order,
// using keyset pagination on audit_id (monotonic, so also chronological for ties).
// The audit_record table and RecordAudit/RecordAuditWithConfig writers are FR8 infrastructure
// shared with #1192.
func (r *Repository) ListActivityRecords(ctx context.Context, householdID int64, pageToken string, pageSize int32) (records []AuditRecord, nextToken string, err error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	var lastAuditID int64
	if pageToken != "" {
		if _, err := fmt.Sscanf(pageToken, "%d", &lastAuditID); err != nil {
			return nil, "", fmt.Errorf("invalid page token: %w", err)
		}
	}

	rows, err := r.db.Query(ctx, `
		SELECT audit_id, actor_subject, target_household_id, action, entity_type, entity_id,
		       EXTRACT(EPOCH FROM occurred_at)::bigint, reason, config_version, i2c_address, mux_path
		FROM audit_record
		WHERE target_household_id = $1
		  AND ($2 = 0 OR audit_id < $2)
		ORDER BY audit_id DESC
		LIMIT $3
	`, householdID, lastAuditID, pageSize+1)
	if err != nil {
		return nil, "", fmt.Errorf("list activity for household %d: %w", householdID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var rec AuditRecord
		var occurredAtEpoch int64
		var muxPathJSON []byte
		if err := rows.Scan(&rec.AuditID, &rec.ActorSubject, &rec.TargetHouseholdID, &rec.Action,
			&rec.EntityType, &rec.EntityID, &occurredAtEpoch, &rec.Reason, &rec.ConfigVersion,
			&rec.I2CAddress, &muxPathJSON); err != nil {
			return nil, "", fmt.Errorf("scan audit record: %w", err)
		}
		rec.OccurredAtUnix = occurredAtEpoch
		rec.MuxPath = muxPathJSON
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("rows error: %w", err)
	}

	if len(records) > int(pageSize) {
		records = records[:pageSize]
		nextToken = fmt.Sprintf("%d", records[len(records)-1].AuditID)
	}

	return records, nextToken, nil
}

type AuditRecord struct {
	AuditID           int64
	ActorSubject      string
	TargetHouseholdID int64
	Action            string
	EntityType        string
	EntityID          int64
	OccurredAtUnix    int64
	Reason            *string
	ConfigVersion     *int64
	I2CAddress        *uint32
	MuxPath           []byte
}

// ── Household validation (FR1.2 write invariant enforcement) ─────────────────

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
// by examining board.household_id (current ownership).
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

// ── Audit logging (FR8) ────────────────────────────────────────────────────

// RecordAudit writes an audit record to the audit_record table.
// This is the single audit-writing helper used by every write path.
func (r *Repository) RecordAudit(ctx context.Context, actorSubject string, targetHouseholdID int64,
	action, entityType string, entityID int64, reason string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO audit_record (actor_subject, target_household_id, action, entity_type, entity_id, reason)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, actorSubject, targetHouseholdID, action, entityType, entityID, reason)
	if err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

// RecordAuditWithConfig writes an audit record for a config-related action,
// including optional config_version and sensor hardware address context.
func (r *Repository) RecordAuditWithConfig(ctx context.Context, actorSubject string, targetHouseholdID int64,
	action, entityType string, entityID int64, configVersion *int64, i2cAddress *uint32, muxPath []byte, reason string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO audit_record (actor_subject, target_household_id, action, entity_type, entity_id,
		                           config_version, i2c_address, mux_path, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
	`, actorSubject, targetHouseholdID, action, entityType, entityID, configVersion, i2cAddress, muxPath, reason)
	if err != nil {
		return fmt.Errorf("record audit with config: %w", err)
	}
	return nil
}

// ── Household management (FR75, FR7) ──────────────────────────────────────────

// CreateHousehold creates a new household and adds the caller as the initial member.
// Returns the household_id on success.
func (r *Repository) CreateHousehold(ctx context.Context, householdName string, callerPrincipalID string, role string) (int64, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin create household: %w", err)
	}
	defer tx.Rollback(ctx)

	// Create the household
	var householdID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO household (name, created_at)
		VALUES ($1, NOW())
		RETURNING household_id
	`, householdName).Scan(&householdID)
	if err != nil {
		return 0, fmt.Errorf("create household %s: %w", householdName, err)
	}

	// Add the caller as the initial member
	_, err = tx.Exec(ctx, `
		INSERT INTO household_member (household_id, principal_id, role, valid_from)
		VALUES ($1, $2, $3, NOW())
	`, householdID, callerPrincipalID, role)
	if err != nil {
		return 0, fmt.Errorf("add initial member to household %d: %w", householdID, err)
	}

	// Commit the transaction
	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit create household: %w", err)
	}

	return householdID, nil
}

// GetCurrentMembers returns all current (non-expired) members of a household.
func (r *Repository) GetCurrentMembers(ctx context.Context, householdID int64) ([]struct {
	PrincipalID string
	Role        string
}, error) {
	rows, err := r.db.Query(ctx, `
		SELECT principal_id, role FROM household_member
		WHERE household_id = $1 AND valid_to IS NULL
		ORDER BY principal_id
	`, householdID)
	if err != nil {
		return nil, fmt.Errorf("get current members for household %d: %w", householdID, err)
	}
	defer rows.Close()

	var members []struct {
		PrincipalID string
		Role        string
	}
	for rows.Next() {
		var m struct {
			PrincipalID string
			Role        string
		}
		if err := rows.Scan(&m.PrincipalID, &m.Role); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// CountActiveMembers returns the number of active (current) members of a household.
func (r *Repository) CountActiveMembers(ctx context.Context, householdID int64) (int64, error) {
	var count int64
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM household_member
		WHERE household_id = $1 AND valid_to IS NULL
	`, householdID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active members for household %d: %w", householdID, err)
	}
	return count, nil
}

// IsHouseholdMember checks if a principal is a current member of a household.
func (r *Repository) IsHouseholdMember(ctx context.Context, householdID int64, principalID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM household_member
			WHERE household_id = $1 AND principal_id = $2 AND valid_to IS NULL
		)
	`, householdID, principalID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check household membership for %s in %d: %w", principalID, householdID, err)
	}
	return exists, nil
}

// CreateGrant creates a time-boxed grant for a principal.
// Returns the grant_id on success.
func (r *Repository) CreateGrant(ctx context.Context, householdID int64, granteeID string, grantedByID string, durationSeconds int64) (int64, error) {
	var grantID int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO household_grant (household_id, grantee, granted_by, expires_at, created_at)
		VALUES ($1, $2, $3, NOW() + MAKE_INTERVAL(secs => $4::float8), NOW())
		RETURNING grant_id
	`, householdID, granteeID, grantedByID, durationSeconds).Scan(&grantID)
	if err != nil {
		return 0, fmt.Errorf("create grant for household %d grantee %s: %w", householdID, granteeID, err)
	}
	return grantID, nil
}

// RevokeGrant revokes a grant by setting revoked_at to NOW().
func (r *Repository) RevokeGrant(ctx context.Context, grantID int64) error {
	_, err := r.db.Exec(ctx, `
		UPDATE household_grant
		SET revoked_at = NOW()
		WHERE grant_id = $1
	`, grantID)
	if err != nil {
		return fmt.Errorf("revoke grant %d: %w", grantID, err)
	}
	return nil
}

// GetActiveGrants returns all active (not expired, not revoked) grants for a household.
func (r *Repository) GetActiveGrants(ctx context.Context, householdID int64) ([]struct {
	GrantID   int64
	Grantee   string
	ExpiresAt int64 // Unix timestamp
}, error) {
	rows, err := r.db.Query(ctx, `
		SELECT grant_id, grantee, EXTRACT(EPOCH FROM expires_at)::bigint
		FROM household_grant
		WHERE household_id = $1 AND expires_at > NOW() AND revoked_at IS NULL
		ORDER BY expires_at DESC
	`, householdID)
	if err != nil {
		return nil, fmt.Errorf("get active grants for household %d: %w", householdID, err)
	}
	defer rows.Close()

	var grants []struct {
		GrantID   int64
		Grantee   string
		ExpiresAt int64
	}
	for rows.Next() {
		var g struct {
			GrantID   int64
			Grantee   string
			ExpiresAt int64
		}
		if err := rows.Scan(&g.GrantID, &g.Grantee, &g.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// GetGrantHousehold returns the household_id for a grant.
func (r *Repository) GetGrantHousehold(ctx context.Context, grantID int64) (int64, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id FROM household_grant
		WHERE grant_id = $1
	`, grantID).Scan(&householdID)
	if err != nil {
		return 0, fmt.Errorf("get grant %d household: %w", grantID, err)
	}
	return householdID, nil
}

const (
	DefaultPageSize = 50
	MaxPageSize     = 1000
)
