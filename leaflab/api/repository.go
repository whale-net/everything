package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
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

// ── Admin (FR10, FR12 activation) ────────────────────────────────────────────
//
// AdminBoardHealthByPerson/AdminBoardHealthByPartialDeviceID back
// ResolveToHousehold (FR10.2's standing lane): both return FR79's health
// projection only, never a wider board/household row. OpenElevation/
// RenewElevation/EndElevation/ActiveElevation back the FR10.1 elevation
// lifecycle over admin_elevation (migration 029).

// ErrNoActiveElevation is returned by RenewElevation/EndElevation/
// ActiveElevation when adminSubject holds no unexpired, unended
// admin_elevation row against targetHouseholdID (FR10.1, FR10.3).
var ErrNoActiveElevation = errors.New("no active elevation for this household")

// AdminBoardHealthRow is FR79's health-field projection: device id, board
// display name (falls back to device_id -- see AdminBoardHealth's proto
// doc comment), household identity, last-seen age, active (highest
// *accepted*) version, whether the latest pushed version is still
// outstanding (pushed but not yet accepted), and sensor count. Never
// joined with readings, region/plant structure, config payloads or audit
// rows -- ResolveToHousehold's "whole lane" (FR10.2).
type AdminBoardHealthRow struct {
	DeviceID         string
	BoardDisplayName string
	HouseholdID      int64
	HouseholdName    string
	LastSeenAt       time.Time
	ActiveVersion    uint64
	OutstandingPush  bool
	SensorCount      int32
}

// adminBoardHealthQuery is shared by AdminBoardHealthByPerson and
// AdminBoardHealthByPartialDeviceID: only the WHERE fragment naming the
// candidate boards differs between a person-identifier resolution and a
// partial-device-id resolution. Excludes retired boards and boards with no
// current household (FR1.1's unclaimed exception -- the standing lane
// resolves *to* a household, so a board with none can't be a match) and
// orders by board_id for stable output.
//
// active_version is the highest *accepted* version (a1), independent of
// outstanding_push, which reports whether the most recently pushed version
// (latest, regardless of acceptance) has not been accepted -- matching
// AdminBoardHealth's "a pushed config version has not yet been accepted"
// doc comment exactly, including the rejected case.
const adminBoardHealthQuery = `
	SELECT
		b.device_id,
		b.household_id,
		h.name,
		b.last_seen_at,
		COALESCE(av.active_version, 0),
		COALESCE(latest.version, 0) != 0 AND NOT latest.accepted,
		COALESCE(sc.sensor_count, 0)
	FROM board b
	JOIN household h ON h.household_id = b.household_id
	LEFT JOIN LATERAL (
		SELECT MAX(version) AS active_version
		FROM device_config
		WHERE board_id = b.board_id AND accepted = TRUE
	) av ON TRUE
	LEFT JOIN LATERAL (
		SELECT version, accepted
		FROM device_config
		WHERE board_id = b.board_id
		ORDER BY version DESC
		LIMIT 1
	) latest ON TRUE
	LEFT JOIN LATERAL (
		SELECT COUNT(*) AS sensor_count
		FROM sensor
		WHERE board_id = b.board_id
	) sc ON TRUE
	WHERE b.retired_at IS NULL
	  AND b.household_id IS NOT NULL
	  AND %s
	ORDER BY b.board_id
`

func (r *Repository) scanAdminBoardHealth(ctx context.Context, whereFragment string, args ...any) ([]AdminBoardHealthRow, error) {
	rows, err := r.db.Query(ctx, fmt.Sprintf(adminBoardHealthQuery, whereFragment), args...)
	if err != nil {
		return nil, fmt.Errorf("admin board health: %w", err)
	}
	defer rows.Close()

	var out []AdminBoardHealthRow
	for rows.Next() {
		var row AdminBoardHealthRow
		if err := rows.Scan(&row.DeviceID, &row.HouseholdID, &row.HouseholdName, &row.LastSeenAt, &row.ActiveVersion, &row.OutstandingPush, &row.SensorCount); err != nil {
			return nil, fmt.Errorf("scan admin board health: %w", err)
		}
		// board_display_name has no dedicated column yet -- falls back to
		// device_id (see AdminBoardHealth's proto doc comment).
		row.BoardDisplayName = row.DeviceID
		out = append(out, row)
	}
	return out, rows.Err()
}

// AdminBoardHealthByPerson resolves personIdentifier (a
// household_membership.principal_subject) to every board owned by any
// household that principal currently belongs to (FR75 permits multi-
// household membership; the standing lane doesn't special-case a single
// household any more than ScopeForPrincipal does).
func (r *Repository) AdminBoardHealthByPerson(ctx context.Context, personIdentifier string) ([]AdminBoardHealthRow, error) {
	return r.scanAdminBoardHealth(ctx, `b.household_id IN (
		SELECT household_id FROM household_membership
		WHERE principal_subject = $1 AND valid_to IS NULL
	)`, personIdentifier)
}

// AdminBoardHealthByPartialDeviceID resolves a partial device_id to every
// currently-owned board whose device_id contains it.
func (r *Repository) AdminBoardHealthByPartialDeviceID(ctx context.Context, partial string) ([]AdminBoardHealthRow, error) {
	return r.scanAdminBoardHealth(ctx, `b.device_id ILIKE $1`, "%"+partial+"%")
}

// RecordAuditEntry writes entry directly against the connection pool, not
// inside a transaction -- for an audited action with no accompanying DB
// write of its own (FR8.1), e.g. ResolveToHousehold's per-query audit row
// (FR10.4). Every other audited write in this file goes through
// auditedWrite instead, so its audit row commits or rolls back with the
// write it records -- see audit.NewPostgresAuditor's doc comment for when
// each shape applies.
func (r *Repository) RecordAuditEntry(ctx context.Context, entry audit.Entry) error {
	return audit.NewPostgresAuditor(r.db).Record(ctx, entry)
}

// HouseholdExists reports whether householdID names a row in household --
// Elevate's preflight check (FR10.1): elevating against a nonexistent
// household is refused up front rather than left to an FK violation.
func (r *Repository) HouseholdExists(ctx context.Context, householdID int64) (bool, error) {
	var exists bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM household WHERE household_id = $1)`, householdID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check household %d exists: %w", householdID, err)
	}
	return exists, nil
}

// OpenElevation inserts a fresh admin_elevation row for adminSubject
// against targetHouseholdID, expiring at expiresAt, and records entry in
// the same transaction (FR10.1, FR8) -- a rolled-back insert leaves no
// audit row, same auditedWrite guarantee as every other write in this
// file.
func (r *Repository) OpenElevation(ctx context.Context, adminSubject string, targetHouseholdID int64, reason string, expiresAt time.Time, entry audit.Entry) error {
	return r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		_, err := tx.Exec(ctx, `
			INSERT INTO admin_elevation (admin_subject, target_household_id, reason, expires_at)
			VALUES ($1, $2, $3, $4)
		`, adminSubject, targetHouseholdID, reason, expiresAt)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("open elevation for %q household %d: %w", adminSubject, targetHouseholdID, err)
		}
		return entry, nil
	})
}

// RenewElevation extends adminSubject's single currently-open (unexpired,
// unended) elevation against targetHouseholdID to newExpiresAt and
// restates its reason (FR10.1's "renewable by re-stating a reason").
// Returns ErrNoActiveElevation if no such elevation is currently open --
// renewal never opens a new one.
func (r *Repository) RenewElevation(ctx context.Context, adminSubject string, targetHouseholdID int64, reason string, newExpiresAt time.Time, entry audit.Entry) error {
	return r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		ct, err := tx.Exec(ctx, `
			UPDATE admin_elevation
			SET reason = $1, expires_at = $2
			WHERE elevation_id = (
				SELECT elevation_id FROM admin_elevation
				WHERE admin_subject = $3
				  AND target_household_id = $4
				  AND ended_at IS NULL
				  AND expires_at > NOW()
				ORDER BY started_at DESC
				LIMIT 1
			)
		`, reason, newExpiresAt, adminSubject, targetHouseholdID)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("renew elevation for %q household %d: %w", adminSubject, targetHouseholdID, err)
		}
		if ct.RowsAffected() == 0 {
			return audit.Entry{}, ErrNoActiveElevation
		}
		return entry, nil
	})
}

// EndElevation ends every currently-open elevation adminSubject holds
// against targetHouseholdID before its natural expiry. Returns
// ErrNoActiveElevation if none is currently open.
func (r *Repository) EndElevation(ctx context.Context, adminSubject string, targetHouseholdID int64, entry audit.Entry) error {
	return r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		ct, err := tx.Exec(ctx, `
			UPDATE admin_elevation
			SET ended_at = NOW()
			WHERE admin_subject = $1
			  AND target_household_id = $2
			  AND ended_at IS NULL
			  AND expires_at > NOW()
		`, adminSubject, targetHouseholdID)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("end elevation for %q household %d: %w", adminSubject, targetHouseholdID, err)
		}
		if ct.RowsAffected() == 0 {
			return audit.Entry{}, ErrNoActiveElevation
		}
		return entry, nil
	})
}

// ActiveElevation returns the expiry of adminSubject's currently-open
// elevation against targetHouseholdID, or ErrNoActiveElevation if none is
// open. Backs GetElevationStatus's remaining-time read and is the gate a
// handler must pass before constructing an authz.ElevatedScope (see that
// type's doc comment).
func (r *Repository) ActiveElevation(ctx context.Context, adminSubject string, targetHouseholdID int64) (time.Time, error) {
	var expiresAt time.Time
	err := r.db.QueryRow(ctx, `
		SELECT expires_at
		FROM admin_elevation
		WHERE admin_subject = $1
		  AND target_household_id = $2
		  AND ended_at IS NULL
		  AND expires_at > NOW()
		ORDER BY expires_at DESC
		LIMIT 1
	`, adminSubject, targetHouseholdID).Scan(&expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, ErrNoActiveElevation
		}
		return time.Time{}, fmt.Errorf("active elevation for %q household %d: %w", adminSubject, targetHouseholdID, err)
	}
	return expiresAt, nil
}

// ── Support reference (FR80) ─────────────────────────────────────────────────
//
// CreateSupportReference/RevokeSupportReference/ListSupportReferences back
// the owner-facing lifecycle; LookupSupportReferenceByHash/
// RecordSupportReferenceResolve/AdminBoardHealthByHousehold back FR10.2's
// admin resolve of a support reference to its household (server.go's
// resolveSupportReference).

// ErrSupportReferenceNotFound is returned by RevokeSupportReference when
// (household_id, support_reference_id) names no currently-unrevoked row --
// covering "doesn't exist", "belongs to a different household" and
// "already revoked" alike, so a caller (server.go's RevokeSupportReference
// handler) maps all three to the same failure shape.
var ErrSupportReferenceNotFound = errors.New("support reference not found")

// SupportReferenceRow is a reference's owner-facing metadata, as listed by
// ListSupportReferences -- never the plaintext code (FR80), which this
// package never even reads back out of the database once created.
type SupportReferenceRow struct {
	SupportReferenceID int64
	CreatedAt          time.Time
	ExpiresAt          time.Time
	RevokedAt          *time.Time
	LastResolvedAt     *time.Time
	ResolveCount       int32
}

// SupportReferenceLookup is LookupSupportReferenceByHash's result: a
// support_reference row's authorization-relevant state, off the single
// query NFR2's timing-equalization requires (see that method's doc
// comment).
type SupportReferenceLookup struct {
	SupportReferenceID int64
	HouseholdID        int64
	ExpiresAt          time.Time
	RevokedAt          *time.Time
}

// CreateSupportReference inserts a fresh support_reference row and records
// entry in the same transaction (FR8; FR80's "creation ... write audit
// rows"). codeHash is the caller-computed hash of the generated code
// (server.go's hashSupportReferenceCode) -- this method never sees, and
// this package never stores, the plaintext.
func (r *Repository) CreateSupportReference(ctx context.Context, householdID int64, codeHash string, createdBySubject string, expiresAt time.Time, entry audit.Entry) (int64, error) {
	var id int64
	err := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		if scanErr := tx.QueryRow(ctx, `
			INSERT INTO support_reference (household_id, code_hash, created_by_subject, expires_at)
			VALUES ($1, $2, $3, $4)
			RETURNING support_reference_id
		`, householdID, codeHash, createdBySubject, expiresAt).Scan(&id); scanErr != nil {
			return audit.Entry{}, fmt.Errorf("create support reference for household %d: %w", householdID, scanErr)
		}
		idStr := strconv.FormatInt(id, 10)
		entry.EntityID = &idStr
		return entry, nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// RevokeSupportReference immediately revokes (household_id,
// supportReferenceID) and records entry in the same transaction (FR80's
// "revocation is one call and immediate"; FR8). Returns
// ErrSupportReferenceNotFound if the row doesn't exist, belongs to a
// different household, or is already revoked -- revocation is not
// idempotent-by-design, matching RetireBoard's ErrBoardAlreadyRetired
// convention above.
func (r *Repository) RevokeSupportReference(ctx context.Context, householdID, supportReferenceID int64, entry audit.Entry) error {
	return r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		ct, err := tx.Exec(ctx, `
			UPDATE support_reference
			SET revoked_at = NOW()
			WHERE support_reference_id = $1
			  AND household_id = $2
			  AND revoked_at IS NULL
		`, supportReferenceID, householdID)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("revoke support reference %d: %w", supportReferenceID, err)
		}
		if ct.RowsAffected() == 0 {
			return audit.Entry{}, ErrSupportReferenceNotFound
		}
		return entry, nil
	})
}

// ListSupportReferences returns up to limit of householdID's support
// references, keyset-paginated on (support_reference_id) per FR61 --
// afterID/hasAfter is the last support_reference_id of the previous page
// (contract.DecodeSupportReferenceCursor), not an offset. Never returns
// the plaintext code (FR80) -- this table has no column to return it from.
func (r *Repository) ListSupportReferences(ctx context.Context, householdID int64, afterID int64, hasAfter bool, limit int32) ([]SupportReferenceRow, error) {
	var sqlQuery string
	var args []any
	if hasAfter {
		sqlQuery = `
			SELECT support_reference_id, created_at, expires_at, revoked_at, last_resolved_at, resolve_count
			FROM support_reference
			WHERE household_id = $1 AND support_reference_id > $2
			ORDER BY support_reference_id
			LIMIT $3
		`
		args = []any{householdID, afterID, limit}
	} else {
		sqlQuery = `
			SELECT support_reference_id, created_at, expires_at, revoked_at, last_resolved_at, resolve_count
			FROM support_reference
			WHERE household_id = $1
			ORDER BY support_reference_id
			LIMIT $2
		`
		args = []any{householdID, limit}
	}

	rows, err := r.db.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list support references for household %d: %w", householdID, err)
	}
	defer rows.Close()

	var out []SupportReferenceRow
	for rows.Next() {
		var row SupportReferenceRow
		if err := rows.Scan(&row.SupportReferenceID, &row.CreatedAt, &row.ExpiresAt, &row.RevokedAt, &row.LastResolvedAt, &row.ResolveCount); err != nil {
			return nil, fmt.Errorf("scan support reference: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// LookupSupportReferenceByHash is the one query behind FR10.2's
// support-reference resolution (server.go's resolveSupportReference): it
// looks up a row by codeHash regardless of whether the code is unknown,
// expired, revoked or valid, so an unknown hash and a known-but-invalid
// one cost the same single unique-index lookup (NFR2's timing-
// equalization clause) -- classification into unknown/expired/revoked/
// valid happens entirely in Go afterward, off this one result, never as a
// second query.
func (r *Repository) LookupSupportReferenceByHash(ctx context.Context, codeHash string) (SupportReferenceLookup, bool, error) {
	var l SupportReferenceLookup
	err := r.db.QueryRow(ctx, `
		SELECT support_reference_id, household_id, expires_at, revoked_at
		FROM support_reference
		WHERE code_hash = $1
	`, codeHash).Scan(&l.SupportReferenceID, &l.HouseholdID, &l.ExpiresAt, &l.RevokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SupportReferenceLookup{}, false, nil
		}
		return SupportReferenceLookup{}, false, fmt.Errorf("lookup support reference by hash: %w", err)
	}
	return l, true, nil
}

// RecordSupportReferenceResolve increments a valid reference's use
// counters (resolve_count, last_resolved_at) and records entry in the same
// transaction (FR8; FR80's "every use write audit rows"). Called only
// after LookupSupportReferenceByHash has classified the row as valid
// (unexpired, unrevoked) -- never on an unknown/expired/revoked hash, so
// this extra work is exactly what makes a genuinely successful resolve
// take more work than a failed one, which NFR2 does not forbid (see
// resolveSupportReference's doc comment): NFR2 only requires the failure
// outcomes to be indistinguishable from each other, not from a success.
func (r *Repository) RecordSupportReferenceResolve(ctx context.Context, supportReferenceID int64, entry audit.Entry) error {
	return r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		ct, err := tx.Exec(ctx, `
			UPDATE support_reference
			SET resolve_count = resolve_count + 1,
			    last_resolved_at = NOW()
			WHERE support_reference_id = $1
		`, supportReferenceID)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("record support reference resolve %d: %w", supportReferenceID, err)
		}
		if ct.RowsAffected() == 0 {
			return audit.Entry{}, ErrSupportReferenceNotFound
		}
		return entry, nil
	})
}

// AdminBoardHealthByHousehold resolves a support reference's household
// directly to FR79's health projection for every board in it -- shares
// adminBoardHealthQuery with AdminBoardHealthByPerson/
// AdminBoardHealthByPartialDeviceID above.
func (r *Repository) AdminBoardHealthByHousehold(ctx context.Context, householdID int64) ([]AdminBoardHealthRow, error) {
	return r.scanAdminBoardHealth(ctx, `b.household_id = $1`, householdID)
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
