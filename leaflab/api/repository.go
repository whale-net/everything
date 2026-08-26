package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	configpb "github.com/whale-net/everything/firmware/proto/config"
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

// ListBoardsByHousehold returns boards owned by a specific household.
// FR5: Board listings are household-scoped.
func (r *Repository) ListBoardsByHousehold(ctx context.Context, householdID int64) ([]BoardRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT board_id, device_id FROM board
		WHERE household_id = $1
		ORDER BY board_id
	`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list boards by household %d: %w", householdID, err)
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

// GetBoardByDeviceID resolves a device_id to its board_id and household_id.
// Returns pgx.ErrNoRows if the board doesn't exist.
// Used for per-entity authorization checks in PushDeviceConfig and GetDeviceConfig.
func (r *Repository) GetBoardByDeviceID(ctx context.Context, deviceID string) (boardID, householdID int64, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT board_id, household_id FROM board
		WHERE device_id = $1
	`, deviceID).Scan(&boardID, &householdID)
	if err != nil {
		return 0, 0, err
	}
	return boardID, householdID, nil
}

// GetPrincipalHouseholds returns all households the principal is currently a member of.
// FR75: Membership is the mechanism by which non-admin principals obtain access.
// Returns only households where valid_to IS NULL (current membership).
func (r *Repository) GetPrincipalHouseholds(ctx context.Context, principalID string) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT household_id FROM household_member
		WHERE principal_id = $1 AND valid_to IS NULL
		ORDER BY household_id
	`, principalID)
	if err != nil {
		return nil, fmt.Errorf("get households for principal %s: %w", principalID, err)
	}
	defer rows.Close()

	var householdIDs []int64
	for rows.Next() {
		var hid int64
		if err := rows.Scan(&hid); err != nil {
			return nil, fmt.Errorf("scan household_id: %w", err)
		}
		householdIDs = append(householdIDs, hid)
	}
	return householdIDs, rows.Err()
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

// ── Region CRUD (FR50, FR22.2, FR22.5, NFR6.2) ────────────────────────────────
//
// Region lifecycle write paths. Parentage immutability is enforced entirely by
// the region_freeze_parent_once_attributed trigger (migration 025) — this file
// never re-implements that recursive descendant test; it only translates the
// trigger's raised exception into ErrParentageFrozen for the server layer.

// ErrParentageFrozen is returned when the database trigger
// (region_freeze_parent_once_attributed, NFR6.2) refuses a re-parent because a
// reading has already been attributed to the region or a descendant. This is a
// translation of the trigger's RAISE EXCEPTION (SQLSTATE 23514, check_violation)
// — the immutability check itself lives only in the trigger, per NFR6.2.
var ErrParentageFrozen = errors.New("region parentage is frozen: a reading has been attributed to this region or a descendant")

// RegionRow carries a region's identity, root-to-leaf path, retirement state,
// and resolved household — everything a single GetRegion/ListRegions row needs
// (FR50.2, FR22.5).
type RegionRow struct {
	RegionID          int64
	Name              string
	ParentRegionID    int64 // 0 means root
	PathNames         []string
	RetiredAt         *time.Time
	RetiredOperation  *string
	RetiredPrincipal  *string
	SuccessorRegionID int64 // 0 means no successor
	HouseholdID       int64
}

// regionRowFromScan converts the nullable scan targets used by GetRegion /
// ListRegionsByHousehold into the RegionRow's zero-means-absent convention.
func regionRowFromScan(regionID int64, name string, parentRegionID *int64, pathNames []string,
	retiredAt *time.Time, retiredOperation, retiredPrincipal *string, successorRegionID *int64,
	householdID int64) RegionRow {
	row := RegionRow{
		RegionID:         regionID,
		Name:             name,
		PathNames:        pathNames,
		RetiredAt:        retiredAt,
		RetiredOperation: retiredOperation,
		RetiredPrincipal: retiredPrincipal,
		HouseholdID:      householdID,
	}
	if parentRegionID != nil {
		row.ParentRegionID = *parentRegionID
	}
	if successorRegionID != nil {
		row.SuccessorRegionID = *successorRegionID
	}
	return row
}

// GetRegionDepth returns a region's depth in its tree (root = 0), via
// v_region_path (migration 012). Returns pgx.ErrNoRows if the region does not
// exist.
func (r *Repository) GetRegionDepth(ctx context.Context, regionID int64) (int64, error) {
	var depth int64
	err := r.db.QueryRow(ctx, `
		SELECT depth FROM v_region_path WHERE region_id = $1
	`, regionID).Scan(&depth)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, pgx.ErrNoRows
		}
		return 0, fmt.Errorf("get region %d depth: %w", regionID, err)
	}
	return depth, nil
}

// CountChildren returns the number of direct children of a region (retired or
// not — retirement does not free up a structural slot).
func (r *Repository) CountChildren(ctx context.Context, parentRegionID int64) (int64, error) {
	var count int64
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM region WHERE parent_region_id = $1
	`, parentRegionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count children of region %d: %w", parentRegionID, err)
	}
	return count, nil
}

// CreateRegion inserts a new region (FR50.1). household_id is stamped on the
// row only when parentRegionID is nil (root); descendants inherit household
// through tree traversal (GetRegionHousehold), per FR1.1.
func (r *Repository) CreateRegion(ctx context.Context, name, description string, parentRegionID *int64, householdID int64) (int64, error) {
	var regionID int64
	var rootHouseholdID *int64
	if parentRegionID == nil {
		rootHouseholdID = &householdID
	}
	err := r.db.QueryRow(ctx, `
		INSERT INTO region (parent_region_id, name, description, household_id)
		VALUES ($1, $2, $3, $4)
		RETURNING region_id
	`, parentRegionID, name, description, rootHouseholdID).Scan(&regionID)
	if err != nil {
		return 0, fmt.Errorf("create region %q: %w", name, err)
	}
	return regionID, nil
}

// GetRegion returns a single region's full row (identity, root-to-leaf path,
// retirement state, resolved household) by id — including a retired region
// (FR22.5: "still readable by explicit id"). Returns pgx.ErrNoRows if the
// region does not exist.
func (r *Repository) GetRegion(ctx context.Context, regionID int64) (RegionRow, error) {
	var (
		name                               string
		parentRegionID, successorID        *int64
		pathNames                          []string
		retiredAt                          *time.Time
		retiredOperation, retiredPrincipal *string
		householdID                        int64
	)
	err := r.db.QueryRow(ctx, `
		WITH RECURSIVE ancestry AS (
			SELECT region_id, parent_region_id, household_id FROM region WHERE region_id = $1
			UNION ALL
			SELECT r.region_id, r.parent_region_id, r.household_id
			FROM region r
			JOIN ancestry a ON r.region_id = a.parent_region_id
		)
		SELECT r.region_id, r.name, r.parent_region_id, rp.path_names,
		       r.retired_at, r.retired_operation, r.retired_principal, r.successor_region_id,
		       (SELECT household_id FROM ancestry WHERE parent_region_id IS NULL LIMIT 1) AS household_id
		FROM region r
		JOIN v_region_path rp ON rp.region_id = r.region_id
		WHERE r.region_id = $1
	`, regionID).Scan(&regionID, &name, &parentRegionID, &pathNames,
		&retiredAt, &retiredOperation, &retiredPrincipal, &successorID, &householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RegionRow{}, pgx.ErrNoRows
		}
		return RegionRow{}, fmt.Errorf("get region %d: %w", regionID, err)
	}
	return regionRowFromScan(regionID, name, parentRegionID, pathNames, retiredAt, retiredOperation, retiredPrincipal, successorID, householdID), nil
}

// ListRegionsByHousehold lists regions belonging to a household (root region's
// household plus all descendants), ordered by region_id. Retired regions are
// excluded unless includeRetired is set (FR22.5).
func (r *Repository) ListRegionsByHousehold(ctx context.Context, householdID int64, includeRetired bool) ([]RegionRow, error) {
	rows, err := r.db.Query(ctx, `
		WITH RECURSIVE region_household AS (
			SELECT region_id, household_id FROM region WHERE parent_region_id IS NULL
			UNION ALL
			SELECT r.region_id, rh.household_id
			FROM region r
			JOIN region_household rh ON r.parent_region_id = rh.region_id
		)
		SELECT r.region_id, r.name, r.parent_region_id, rp.path_names,
		       r.retired_at, r.retired_operation, r.retired_principal, r.successor_region_id,
		       rh.household_id
		FROM region r
		JOIN region_household rh ON rh.region_id = r.region_id
		JOIN v_region_path rp ON rp.region_id = r.region_id
		WHERE rh.household_id = $1
		  AND ($2::boolean OR r.retired_at IS NULL)
		ORDER BY r.region_id
	`, householdID, includeRetired)
	if err != nil {
		return nil, fmt.Errorf("list regions for household %d: %w", householdID, err)
	}
	defer rows.Close()

	var result []RegionRow
	for rows.Next() {
		var (
			regionID, hid                      int64
			name                               string
			parentRegionID, successorID        *int64
			pathNames                          []string
			retiredAt                          *time.Time
			retiredOperation, retiredPrincipal *string
		)
		if err := rows.Scan(&regionID, &name, &parentRegionID, &pathNames,
			&retiredAt, &retiredOperation, &retiredPrincipal, &successorID, &hid); err != nil {
			return nil, fmt.Errorf("scan region: %w", err)
		}
		result = append(result, regionRowFromScan(regionID, name, parentRegionID, pathNames, retiredAt, retiredOperation, retiredPrincipal, successorID, hid))
	}
	return result, rows.Err()
}

// RenameRegion updates a region's display name (FR50.1). Never touches
// parentage, so it never invokes the parentage-freeze trigger (NFR6.2).
// Returns pgx.ErrNoRows if the region does not exist.
func (r *Repository) RenameRegion(ctx context.Context, regionID int64, name string) error {
	result, err := r.db.Exec(ctx, `UPDATE region SET name = $2 WHERE region_id = $1`, regionID, name)
	if err != nil {
		return fmt.Errorf("rename region %d: %w", regionID, err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// UpdateRegionParent re-parents a region (FR50.3). The immutability check
// lives entirely in the region_freeze_parent_once_attributed trigger
// (NFR6.2, migration 025) — this method never re-implements it; it only
// translates the trigger's raised exception (SQLSTATE 23514) into
// ErrParentageFrozen. Returns pgx.ErrNoRows if the region does not exist.
func (r *Repository) UpdateRegionParent(ctx context.Context, regionID int64, newParentRegionID *int64) error {
	var updated int64
	err := r.db.QueryRow(ctx, `
		UPDATE region SET parent_region_id = $2
		WHERE region_id = $1
		RETURNING region_id
	`, regionID, newParentRegionID).Scan(&updated)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgx.ErrNoRows
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return ErrParentageFrozen
		}
		return fmt.Errorf("update region %d parent: %w", regionID, err)
	}
	return nil
}

// RetireRegion soft-retires a region (FR22.2, FR22.5): sets retired_at,
// names the operation and acting principal, and optionally names a successor
// region (FR74 relocation). Retirement never touches parent_region_id, so it
// never invokes the parentage-freeze trigger and never unfreezes parentage.
// Returns pgx.ErrNoRows if the region does not exist.
func (r *Repository) RetireRegion(ctx context.Context, regionID int64, operation, principal string, successorRegionID *int64) error {
	result, err := r.db.Exec(ctx, `
		UPDATE region
		SET retired_at = NOW(), retired_operation = $2, retired_principal = $3, successor_region_id = $4
		WHERE region_id = $1
	`, regionID, operation, principal, successorRegionID)
	if err != nil {
		return fmt.Errorf("retire region %d: %w", regionID, err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// GetReachableHouseholds returns every household a principal can reach either
// as a current member or as an active (non-expired, non-revoked) grantee
// (FR7: member-or-grantee). Ordered by household_id so callers needing a
// single deterministic choice (e.g. resolving the household for a new root
// region) can take the first.
func (r *Repository) GetReachableHouseholds(ctx context.Context, principalID string) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT household_id FROM household_member WHERE principal_id = $1 AND valid_to IS NULL
		UNION
		SELECT household_id FROM household_grant
		WHERE grantee = $1 AND expires_at > NOW() AND revoked_at IS NULL
		ORDER BY household_id
	`, principalID)
	if err != nil {
		return nil, fmt.Errorf("get reachable households for principal %s: %w", principalID, err)
	}
	defer rows.Close()

	var households []int64
	for rows.Next() {
		var hid int64
		if err := rows.Scan(&hid); err != nil {
			return nil, fmt.Errorf("scan household_id: %w", err)
		}
		households = append(households, hid)
	}
	return households, rows.Err()
}
