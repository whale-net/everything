package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/staleness"
	"google.golang.org/protobuf/encoding/protojson"
)

// defaultPollInterval is the firmware's compile-time publish cadence
// (sensorboard_dynamic_main.cc:136, SENSOR_POLL_INTERVAL_MS) used whenever a
// sensor's configured poll_interval_ms is 0 ("use device default") or a
// board has no accepted config at all. A23's threshold is computed from the
// board's longest configured poll interval; this is the fallback input to
// that computation, not a second threshold.
const defaultPollInterval = 60 * time.Second

// ── FR10: Elevation lifecycle ─────────────────────────────────────────────

// ElevationRow describes an admin's currently active elevation window
// against a target household.
type ElevationRow struct {
	ElevationID int64
	Reason      string
	EnteredAt   time.Time
	ExpiresAt   time.Time
}

// HouseholdExists reports whether a household_id refers to a real household.
func (r *Repository) HouseholdExists(ctx context.Context, householdID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM household WHERE household_id = $1)
	`, householdID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check household %d exists: %w", householdID, err)
	}
	return exists, nil
}

// GetActiveElevation returns the admin's currently active (not expired)
// elevation window against targetHouseholdID, or nil if none exists.
func (r *Repository) GetActiveElevation(ctx context.Context, adminSubject string, targetHouseholdID int64) (*ElevationRow, error) {
	var row ElevationRow
	err := r.db.QueryRow(ctx, `
		SELECT elevation_id, reason, entered_at, expires_at
		FROM elevation
		WHERE admin_subject = $1
		  AND target_household_id = $2
		  AND expires_at > NOW()
		ORDER BY expires_at DESC
		LIMIT 1
	`, adminSubject, targetHouseholdID).Scan(&row.ElevationID, &row.Reason, &row.EnteredAt, &row.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get active elevation for %s against household %d: %w", adminSubject, targetHouseholdID, err)
	}
	return &row, nil
}

// InsertElevation records a new elevation window (FR10): an EnterElevation
// call when renewedFrom is nil, a RenewElevation call when it names the
// elevation_id being extended. The elevation table is append-only; renewal
// never mutates a prior row.
func (r *Repository) InsertElevation(ctx context.Context, adminSubject string, targetHouseholdID int64, reason string, duration time.Duration, renewedFrom *int64) (elevationID int64, expiresAt time.Time, err error) {
	err = r.db.QueryRow(ctx, `
		INSERT INTO elevation (admin_subject, target_household_id, reason, entered_at, expires_at, renewed_from)
		VALUES ($1, $2, $3, NOW(), NOW() + MAKE_INTERVAL(secs => $4::float8), $5)
		RETURNING elevation_id, expires_at
	`, adminSubject, targetHouseholdID, reason, duration.Seconds(), renewedFrom).Scan(&elevationID, &expiresAt)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("insert elevation for %s against household %d: %w", adminSubject, targetHouseholdID, err)
	}
	return elevationID, expiresAt, nil
}

// ── FR80: Support references ──────────────────────────────────────────────

const (
	// supportCodeAlphabet excludes visually ambiguous characters (0/O, 1/I/L)
	// so a code read aloud over a phone is unambiguous to transcribe.
	supportCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	supportCodeLength   = 10
)

// generateSupportCode produces a short, opaque, phone-readable code (FR80).
// The plaintext is disclosed to the caller exactly once, at creation; only
// its hash is persisted.
func generateSupportCode() (string, error) {
	buf := make([]byte, supportCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate support code: %w", err)
	}
	code := make([]byte, supportCodeLength)
	for i, b := range buf {
		code[i] = supportCodeAlphabet[int(b)%len(supportCodeAlphabet)]
	}
	return string(code), nil
}

// hashSupportCode computes the SHA-256 hex digest stored in place of the
// plaintext code (migration 025): a database read alone cannot recover a
// valid, resolvable code.
func hashSupportCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// InsertSupportReference creates a new support reference for householdID
// (FR80). Returns the row id and absolute expiry.
func (r *Repository) InsertSupportReference(ctx context.Context, householdID int64, codeHash string, createdBy string, ttl time.Duration) (id int64, expiresAt time.Time, err error) {
	err = r.db.QueryRow(ctx, `
		INSERT INTO support_reference (household_id, code_hash, created_by, expires_at)
		VALUES ($1, $2, $3, NOW() + MAKE_INTERVAL(secs => $4::float8))
		RETURNING support_reference_id, expires_at
	`, householdID, codeHash, createdBy, ttl.Seconds()).Scan(&id, &expiresAt)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("insert support reference for household %d: %w", householdID, err)
	}
	return id, expiresAt, nil
}

// ResolveSupportReference resolves a hashed code to its owning household
// (FR10.2's standing lane). Per the migration 025 comment, a hash is not
// unique across expired/revoked rows; this picks the most recent
// unrevoked, unexpired match. Returns found=false for unknown, expired and
// revoked codes alike — the caller (Resolve) must not distinguish these
// (NFR2).
func (r *Repository) ResolveSupportReference(ctx context.Context, codeHash string) (householdID int64, found bool, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT household_id FROM support_reference
		WHERE code_hash = $1
		  AND expires_at > NOW()
		  AND revoked_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1
	`, codeHash).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("resolve support reference: %w", err)
	}
	return householdID, true, nil
}

// RevokeSupportReferenceByCode revokes the caller's own household's active
// reference matching codeHash. Returns revoked=false if no matching active
// reference exists for that household (already revoked, expired, unknown,
// or belonging to a different household — RevokeSupportReference is an
// owner-managed action, not the admin existence oracle Resolve is, so these
// need not be indistinguishable per NFR2).
func (r *Repository) RevokeSupportReferenceByCode(ctx context.Context, householdID int64, codeHash string) (revoked bool, err error) {
	var id int64
	err = r.db.QueryRow(ctx, `
		UPDATE support_reference
		SET revoked_at = NOW()
		WHERE household_id = $1
		  AND code_hash = $2
		  AND revoked_at IS NULL
		  AND expires_at > NOW()
		RETURNING support_reference_id
	`, householdID, codeHash).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("revoke support reference for household %d: %w", householdID, err)
	}
	return true, nil
}

// ── FR10.2: Standing-lane person / device-id-prefix resolution ───────────

// ResolveDeviceIDPrefixHousehold resolves a partial device_id to the single
// household owning every board matching that prefix. Returns found=false
// when zero boards match or when matching boards span more than one
// household — an ambiguous prefix cannot be "resolved" to one household
// (FR10.2), and is treated identically to no match at all.
func (r *Repository) ResolveDeviceIDPrefixHousehold(ctx context.Context, prefix string) (householdID int64, found bool, err error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT household_id FROM board
		WHERE device_id LIKE $1 || '%'
		  AND household_id IS NOT NULL
		LIMIT 2
	`, prefix)
	if err != nil {
		return 0, false, fmt.Errorf("resolve device_id_prefix %q: %w", prefix, err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, false, fmt.Errorf("scan household: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("rows error: %w", err)
	}

	if len(ids) != 1 {
		return 0, false, nil
	}
	return ids[0], true, nil
}

// ── FR79: Fleet health ─────────────────────────────────────────────────────

// boardHealthFilters narrows a fleet health query. Zero values mean
// unfiltered, except Limit which must be positive.
type boardHealthFilters struct {
	DevicePrefix string
	HouseholdID  int64
	RegionID     int64
	AfterBoardID int64 // keyset cursor: board_id > AfterBoardID
	Limit        int32
}

// boardHealthRow is the raw per-board data queryBoardHealth returns. Health
// derivation (staleness, push_outstanding) happens in toBoardHealth, kept
// separate from the query so it can be unit tested against the staleness
// package without a database.
type boardHealthRow struct {
	BoardID             int64
	DeviceID            string
	HouseholdID         *int64
	LastSeenEpoch       int64
	SensorCount         int32
	ActiveConfigVersion int64
	LatestAccepted      *bool
	LatestPushedEpoch   *int64
	ActiveConfigJSON    []byte
}

// QueryBoardHealth returns boards (retired boards excluded — FR22.4 leaves
// them out of FR79's reporting population entirely) matching f, ordered by
// board_id ascending for keyset pagination. Retired-board exclusion is
// unconditional: it is what makes "excluded from offline counts" true,
// since an excluded board cannot appear in an unhealthy_only result either.
func (r *Repository) QueryBoardHealth(ctx context.Context, f boardHealthFilters) ([]boardHealthRow, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	rows, err := r.db.Query(ctx, `
		SELECT
			b.board_id,
			b.device_id,
			b.household_id,
			EXTRACT(EPOCH FROM b.last_seen_at)::bigint,
			(SELECT COUNT(*) FROM sensor s WHERE s.board_id = b.board_id)::int,
			COALESCE((
				SELECT dc.version FROM device_config dc
				WHERE dc.board_id = b.board_id AND dc.accepted = TRUE
				ORDER BY dc.version DESC LIMIT 1
			), 0),
			(SELECT dc.accepted FROM device_config dc WHERE dc.board_id = b.board_id ORDER BY dc.version DESC LIMIT 1),
			(SELECT EXTRACT(EPOCH FROM dc.pushed_at)::bigint FROM device_config dc WHERE dc.board_id = b.board_id ORDER BY dc.version DESC LIMIT 1),
			(SELECT dc.config_json FROM device_config dc WHERE dc.board_id = b.board_id AND dc.accepted = TRUE ORDER BY dc.version DESC LIMIT 1)
		FROM board b
		WHERE b.retired_at IS NULL
		  AND ($1 = '' OR b.device_id LIKE $1 || '%')
		  AND ($2::bigint = 0 OR b.household_id = $2)
		  AND ($3::bigint = 0 OR EXISTS (
			SELECT 1 FROM sensor s
			JOIN v_region_path rp ON rp.region_id = s.region_id
			WHERE s.board_id = b.board_id AND $3::bigint = ANY(rp.path_ids)
		  ))
		  AND ($4::bigint = 0 OR b.board_id > $4)
		ORDER BY b.board_id ASC
		LIMIT $5
	`, f.DevicePrefix, f.HouseholdID, f.RegionID, f.AfterBoardID, limit)
	if err != nil {
		return nil, fmt.Errorf("query board health: %w", err)
	}
	defer rows.Close()

	var out []boardHealthRow
	for rows.Next() {
		var row boardHealthRow
		if err := rows.Scan(
			&row.BoardID, &row.DeviceID, &row.HouseholdID, &row.LastSeenEpoch, &row.SensorCount,
			&row.ActiveConfigVersion, &row.LatestAccepted, &row.LatestPushedEpoch, &row.ActiveConfigJSON,
		); err != nil {
			return nil, fmt.Errorf("scan board health: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return out, nil
}

// CountBoardHealth counts boards matching f's device/household/region
// filters (retired boards excluded), ignoring any unhealthy_only narrowing —
// that classification is computed application-side from each board's
// accepted config and cannot be pushed into this count without repeating
// the JSON parse per row here too. Used only for PageResponse.total_size,
// which is a progress-display estimate, not a correctness-bearing value.
func (r *Repository) CountBoardHealth(ctx context.Context, devicePrefix string, householdID, regionID int64) (int32, error) {
	var count int32
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM board b
		WHERE b.retired_at IS NULL
		  AND ($1 = '' OR b.device_id LIKE $1 || '%')
		  AND ($2::bigint = 0 OR b.household_id = $2)
		  AND ($3::bigint = 0 OR EXISTS (
			SELECT 1 FROM sensor s
			JOIN v_region_path rp ON rp.region_id = s.region_id
			WHERE s.board_id = b.board_id AND $3::bigint = ANY(rp.path_ids)
		  ))
	`, devicePrefix, householdID, regionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count board health: %w", err)
	}
	return count, nil
}

// toBoardHealth derives FR79's health fields for one board: last-seen age
// against the A23 staleness threshold, active accepted config version,
// outstanding-push duration, and sensor count. longestPollInterval is read
// from the board's active accepted config (defaultPollInterval if there is
// none, or a sensor leaves poll_interval_ms at 0) — the single input A23's
// architect note says should eventually change; staleness.Config.Threshold
// is the single place that change would land.
func toBoardHealth(row boardHealthRow, cfg staleness.Config, now time.Time) BoardHealthResult {
	lastSeen := time.Unix(row.LastSeenEpoch, 0)

	longest := defaultPollInterval
	if len(row.ActiveConfigJSON) > 0 {
		var cfgProto configpb.DeviceConfig
		if err := protojson.Unmarshal(row.ActiveConfigJSON, &cfgProto); err == nil {
			for _, sensor := range cfgProto.Sensors {
				interval := time.Duration(sensor.PollIntervalMs) * time.Millisecond
				if sensor.PollIntervalMs == 0 {
					interval = defaultPollInterval
				}
				if interval > longest {
					longest = interval
				}
			}
		}
	}

	pushOutstanding := row.LatestAccepted != nil && !*row.LatestAccepted
	var pushOutstandingSeconds int64
	if pushOutstanding && row.LatestPushedEpoch != nil {
		pushOutstandingSeconds = int64(now.Sub(time.Unix(*row.LatestPushedEpoch, 0)).Seconds())
		if pushOutstandingSeconds < 0 {
			pushOutstandingSeconds = 0
		}
	}

	lastSeenAge := int64(now.Sub(lastSeen).Seconds())
	if lastSeenAge < 0 {
		lastSeenAge = 0
	}

	return BoardHealthResult{
		DeviceID:               row.DeviceID,
		BoardID:                row.BoardID,
		HouseholdID:            row.HouseholdID,
		LastSeenAgeSeconds:     lastSeenAge,
		Reporting:              !cfg.IsStale(now, lastSeen, longest),
		ActiveConfigVersion:    row.ActiveConfigVersion,
		PushOutstanding:        pushOutstanding,
		PushOutstandingSeconds: pushOutstandingSeconds,
		SensorCount:            row.SensorCount,
	}
}

// BoardHealthResult is toBoardHealth's output: FR79's health fields plus the
// owning household_id (not part of the proto response but needed by callers
// for standing-lane audit-at-query-granularity).
type BoardHealthResult struct {
	DeviceID               string
	BoardID                int64
	HouseholdID            *int64
	LastSeenAgeSeconds     int64
	Reporting              bool
	ActiveConfigVersion    int64
	PushOutstanding        bool
	PushOutstandingSeconds int64
	SensorCount            int32
}
