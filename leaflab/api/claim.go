package main

// Board claim (FR76) -- implementation for #1342.
//
// Self-service possession challenge: a board is claimable by an
// authenticated principal who discharges a possession challenge (r >= 2
// challenger-marked device restarts, A28) when the board is never-claimed or
// resolves to the member-less Unadopted household (FR70.1, migration 015).
// Schema: leaflab/migrate/migrations/021_claim_challenge.up.sql. Constants:
// leaflab/api/claim/config.go.
//
// Requirement 1 (uniform initiation, "no oracle at open") is the load-bearing
// invariant of this file: OpenClaimChallenge and everything it calls below
// must never query the board table. Every other method here (MarkClaimRound,
// GetClaimChallengeStatus, CompleteClaim) operates only on claim_challenge/
// claim_challenge_round/claim_cooldown rows keyed by the caller's own opaque
// handle -- CompleteClaim is the one exception that legitimately needs to
// resolve board (requirement 6's ownership check), and even then only after
// the challenge itself is confirmed 'discharged'.
//
// Round bookkeeping (challenger-marked rounds, requirement 3) writes here are
// mirrored by leaflab/processor's restart-signal writes (SatisfyOpenClaimRound,
// leaflab/processor/repository.go): this file owns the challenge lifecycle
// (open/mark/status/complete); the processor owns turning an observed
// uptime_s regression or non-retained manifest into round-closing evidence.
//
// discharged is a two-step gate, matching requirement 6 precisely:
// claim_challenge.state = 'discharged' records only that r rounds were
// satisfied -- CompleteClaim additionally requires the board to be
// never-claimed or Unadopted before it actually moves ownership. A discharged
// challenge against a real household's board is recorded (discharged_at,
// FR77 evidence) but CompleteClaim still returns the identical not-discharged
// failure -- see completeClaim below.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/claim"
)

// ErrClaimChallengeNotFound is returned when a challenge_handle names no row
// -- either it never existed, or it names a value the caller fabricated.
// This is a caller-error class, not part of FR76's device_id oracle (a
// handle is opaque and caller-held, never guessable from a device_id).
var ErrClaimChallengeNotFound = errors.New("claim challenge not found")

// ErrClaimCooldownActive is returned by OpenClaimChallenge when the
// (principal, device_id) pair is still in claim_cooldown (requirement 5).
// Checking this adds no oracle: claim_cooldown is populated only by this
// same principal's own prior attempts against this same device_id, never
// from board's existence.
var ErrClaimCooldownActive = errors.New("claim cooldown active for this principal/device pair")

// ErrClaimTooManyOpenChallenges is returned by OpenClaimChallenge when the
// calling principal already holds claim.Config.MaxConcurrentOpenChallenges
// open challenges (requirement 2's "bounded number of concurrent open
// challenges per principal"). Keyed on principal alone -- never on
// device_id -- so it discloses nothing about any particular board.
var ErrClaimTooManyOpenChallenges = errors.New("too many concurrent open claim challenges for this principal")

// ErrClaimNotDischarged is CompleteClaim's single failure class covering
// every non-success case the requirement text demands be indistinguishable:
// challenge not found, wrong principal, still open, expired, exhausted, or
// discharged against a board a real household already owns (requirement 6).
var ErrClaimNotDischarged = errors.New("claim challenge not discharged")

// ClaimChallengeRow is OpenClaimChallenge's result: the caller-facing
// projection of a claim_challenge row.
type ClaimChallengeRow struct {
	Handle         string
	ExpiresAt      time.Time
	RoundsRequired int32
}

// generateClaimHandle returns a fresh opaque external handle for a
// claim_challenge row (never the numeric challenge_id -- see the migration's
// doc comment on why). 20 random bytes / 40 hex chars is comfortably
// unguessable and short enough to round-trip through a BFF session without
// friction.
func generateClaimHandle() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate claim handle: %w", err)
	}
	return "cc_" + hex.EncodeToString(buf), nil
}

// OpenClaimChallenge opens a possession challenge against deviceID for
// principalSubject (requirement 1). It never queries the board table --
// only claim_challenge/claim_cooldown, both keyed purely on
// (principal_subject, device_id) as submitted, so a caller cannot learn
// anything about whether deviceID resolves to a real board from this call's
// status, body shape or timing.
//
// Idempotent per (principal, device_id): a second open while one is already
// open returns the same handle rather than erroring or creating a second row
// -- consistent with requirement 2's "one open challenge per (principal,
// device_id) pair", backed by idx_claim_challenge_open_per_principal_device.
func (r *Repository) OpenClaimChallenge(ctx context.Context, principalSubject, deviceID string, cfg claim.Config) (ClaimChallengeRow, error) {
	if existing, ok, err := r.currentOpenClaimChallenge(ctx, principalSubject, deviceID); err != nil {
		return ClaimChallengeRow{}, err
	} else if ok {
		return existing, nil
	}

	inCooldown, err := r.isInClaimCooldown(ctx, principalSubject, deviceID)
	if err != nil {
		return ClaimChallengeRow{}, err
	}
	if inCooldown {
		return ClaimChallengeRow{}, ErrClaimCooldownActive
	}

	openCount, err := r.countOpenClaimChallenges(ctx, principalSubject)
	if err != nil {
		return ClaimChallengeRow{}, err
	}
	if openCount >= cfg.MaxConcurrentOpenChallenges {
		return ClaimChallengeRow{}, ErrClaimTooManyOpenChallenges
	}

	handle, err := generateClaimHandle()
	if err != nil {
		return ClaimChallengeRow{}, err
	}

	row, inserted, err := r.insertClaimChallenge(ctx, handle, principalSubject, deviceID, cfg)
	if err != nil {
		return ClaimChallengeRow{}, err
	}
	if inserted {
		return row, nil
	}

	// Lost a race to a concurrent OpenClaimChallenge for the same pair
	// between the check above and the insert -- the unique partial index
	// (idx_claim_challenge_open_per_principal_device) rejected our insert.
	// Return the winner's row rather than erroring, preserving idempotency.
	if existing, ok, err := r.currentOpenClaimChallenge(ctx, principalSubject, deviceID); err != nil {
		return ClaimChallengeRow{}, err
	} else if ok {
		return existing, nil
	}
	return ClaimChallengeRow{}, fmt.Errorf("open claim challenge for %q/%q: lost insert race but no open row found", principalSubject, deviceID)
}

func (r *Repository) currentOpenClaimChallenge(ctx context.Context, principalSubject, deviceID string) (ClaimChallengeRow, bool, error) {
	var row ClaimChallengeRow
	err := r.db.QueryRow(ctx, `
		SELECT handle, expires_at, rounds_required
		FROM claim_challenge
		WHERE principal_subject = $1 AND device_id = $2 AND state = 'open'
	`, principalSubject, deviceID).Scan(&row.Handle, &row.ExpiresAt, &row.RoundsRequired)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ClaimChallengeRow{}, false, nil
		}
		return ClaimChallengeRow{}, false, fmt.Errorf("find open claim challenge for %q/%q: %w", principalSubject, deviceID, err)
	}
	return row, true, nil
}

func (r *Repository) isInClaimCooldown(ctx context.Context, principalSubject, deviceID string) (bool, error) {
	var until time.Time
	err := r.db.QueryRow(ctx, `
		SELECT until FROM claim_cooldown WHERE principal_subject = $1 AND device_id = $2
	`, principalSubject, deviceID).Scan(&until)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check claim cooldown for %q/%q: %w", principalSubject, deviceID, err)
	}
	return time.Now().Before(until), nil
}

func (r *Repository) countOpenClaimChallenges(ctx context.Context, principalSubject string) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM claim_challenge WHERE principal_subject = $1 AND state = 'open'
	`, principalSubject).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count open claim challenges for %q: %w", principalSubject, err)
	}
	return count, nil
}

// insertClaimChallenge attempts the actual INSERT, returning (row, true, nil)
// on success or (zero, false, nil) if a concurrent open beat us to it (the
// unique partial index rejected the insert).
func (r *Repository) insertClaimChallenge(ctx context.Context, handle, principalSubject, deviceID string, cfg claim.Config) (ClaimChallengeRow, bool, error) {
	row := ClaimChallengeRow{
		Handle:         handle,
		RoundsRequired: int32(cfg.RoundsRequired),
	}
	err := r.db.QueryRow(ctx, `
		INSERT INTO claim_challenge (handle, principal_subject, device_id, rounds_required, expires_at)
		VALUES ($1, $2, $3, $4, NOW() + make_interval(secs => $5))
		ON CONFLICT (principal_subject, device_id) WHERE state = 'open' DO NOTHING
		RETURNING expires_at
	`, handle, principalSubject, deviceID, cfg.RoundsRequired, cfg.ChallengeLifetime.Seconds()).Scan(&row.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ClaimChallengeRow{}, false, nil
		}
		return ClaimChallengeRow{}, false, fmt.Errorf("insert claim challenge for %q/%q: %w", principalSubject, deviceID, err)
	}
	return row, true, nil
}

// exhaustClaimChallenge terminates challengeID as 'not_discharged'
// (requirement 5's bounded-lifetime/bounded-attempts exhaustion) and places
// (principalSubject, deviceID) in cooldown. Runs inside the caller's
// transaction. Idempotent against a challenge already in a terminal state
// (the state='open' guard on the UPDATE makes it a no-op then), but callers
// above only invoke this from an 'open' branch already.
func (r *Repository) exhaustClaimChallenge(ctx context.Context, tx pgx.Tx, challengeID int64, principalSubject, deviceID string, cfg claim.Config) error {
	if _, err := tx.Exec(ctx, `
		UPDATE claim_challenge SET state = 'not_discharged' WHERE challenge_id = $1 AND state = 'open'
	`, challengeID); err != nil {
		return fmt.Errorf("exhaust claim challenge %d: %w", challengeID, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO claim_cooldown (principal_subject, device_id, until)
		VALUES ($1, $2, NOW() + make_interval(secs => $3))
		ON CONFLICT (principal_subject, device_id) DO UPDATE SET until = EXCLUDED.until
	`, principalSubject, deviceID, cfg.CooldownDuration.Seconds()); err != nil {
		return fmt.Errorf("record claim cooldown for %q/%q: %w", principalSubject, deviceID, err)
	}
	return nil
}

// claimChallengeForUpdate is the row shape MarkClaimRound/GetClaimChallengeStatus/
// CompleteClaim all start from, locked FOR UPDATE so a concurrent call
// against the same handle (e.g. two MarkClaimRound calls racing on a
// re-mark) serializes rather than corrupting round bookkeeping.
type claimChallengeForUpdate struct {
	ChallengeID      int64
	PrincipalSubject string
	DeviceID         string
	RoundsRequired   int
	RoundsSatisfied  int
	AttemptsUsed     int
	ExpiresAt        time.Time
	State            string
}

func lockClaimChallengeByHandle(ctx context.Context, tx pgx.Tx, handle string) (claimChallengeForUpdate, error) {
	var c claimChallengeForUpdate
	err := tx.QueryRow(ctx, `
		SELECT challenge_id, principal_subject, device_id, rounds_required, rounds_satisfied, attempts_used, expires_at, state
		FROM claim_challenge
		WHERE handle = $1
		FOR UPDATE
	`, handle).Scan(&c.ChallengeID, &c.PrincipalSubject, &c.DeviceID, &c.RoundsRequired, &c.RoundsSatisfied, &c.AttemptsUsed, &c.ExpiresAt, &c.State)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return claimChallengeForUpdate{}, ErrClaimChallengeNotFound
		}
		return claimChallengeForUpdate{}, fmt.Errorf("lock claim challenge %q: %w", handle, err)
	}
	return c, nil
}

// MarkClaimRound marks the start (or re-mark, up to cfg.AttemptsPerRound) of
// the challenge's next round (requirement 3). It carries no return value
// beyond error: a caller-visible "which round, whether a prior one
// succeeded" would violate requirement 3's "no per-round outcome is
// disclosed" -- so even attempt/lifetime exhaustion triggered by this call
// is silent here; GetClaimChallengeStatus/CompleteClaim are the only
// disclosure points, and both are deliberately coarse.
//
// "Round n+1's bound opens only after round n closes": enforced structurally
// by always operating on round index (rounds_satisfied + 1) -- rounds_satisfied
// only advances when leaflab/processor closes a round via SatisfyOpenClaimRound,
// so this can never get ahead of the actual satisfied-round count.
func (r *Repository) MarkClaimRound(ctx context.Context, handle string, cfg claim.Config) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- no-op once committed

	c, err := lockClaimChallengeByHandle(ctx, tx, handle)
	if err != nil {
		return err
	}

	if c.State != "open" {
		return tx.Commit(ctx) // terminal already -- uniform no-op ack.
	}

	now := time.Now()
	if !now.Before(c.ExpiresAt) {
		if err := r.exhaustClaimChallenge(ctx, tx, c.ChallengeID, c.PrincipalSubject, c.DeviceID, cfg); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	if c.RoundsSatisfied >= c.RoundsRequired {
		// Already fully satisfied (processor closed the final round between
		// GetClaimChallengeStatus polls) -- nothing left to mark.
		return tx.Commit(ctx)
	}

	roundIndex := c.RoundsSatisfied + 1
	var existingRoundID int64
	err = tx.QueryRow(ctx, `
		SELECT round_id FROM claim_challenge_round WHERE challenge_id = $1 AND round_index = $2
	`, c.ChallengeID, roundIndex).Scan(&existingRoundID)
	isReMark := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find claim round %d/%d: %w", c.ChallengeID, roundIndex, err)
	}

	attemptsForRound := 1
	if isReMark {
		attemptsForRound = c.AttemptsUsed + 1
	}
	if attemptsForRound > cfg.AttemptsPerRound {
		if err := r.exhaustClaimChallenge(ctx, tx, c.ChallengeID, c.PrincipalSubject, c.DeviceID, cfg); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	t0 := now
	boundExpiresAt := now.Add(cfg.RoundBound)
	if isReMark {
		if _, err := tx.Exec(ctx, `
			UPDATE claim_challenge_round
			SET t0 = $2, bound_expires_at = $3, closed_at = NULL,
			    satisfied_by_reading_id = NULL, satisfied_by_manifest_at = NULL, evidence_class = NULL
			WHERE round_id = $1
		`, existingRoundID, t0, boundExpiresAt); err != nil {
			return fmt.Errorf("re-mark claim round %d: %w", existingRoundID, err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			INSERT INTO claim_challenge_round (challenge_id, device_id, round_index, t0, bound_expires_at)
			VALUES ($1, $2, $3, $4, $5)
		`, c.ChallengeID, c.DeviceID, roundIndex, t0, boundExpiresAt); err != nil {
			return fmt.Errorf("mark claim round %d/%d: %w", c.ChallengeID, roundIndex, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE claim_challenge SET attempts_used = $2 WHERE challenge_id = $1
	`, c.ChallengeID, attemptsForRound); err != nil {
		return fmt.Errorf("update attempts_used for claim challenge %d: %w", c.ChallengeID, err)
	}

	return tx.Commit(ctx)
}

// GetClaimChallengeStatus reports whether handle's challenge is still
// waiting (requirement 8's presentation states are collapsed at the RPC
// layer -- server.go maps "not waiting" to a single ENDED value, never
// distinguishing not_discharged from discharged-but-against-a-real-household,
// per requirement 6). Lazily transitions an expired-but-still-'open' row to
// 'not_discharged' (and records cooldown) so status is accurate even if no
// other call has touched this challenge since it expired.
func (r *Repository) GetClaimChallengeStatus(ctx context.Context, handle string, cfg claim.Config) (waiting bool, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	c, err := lockClaimChallengeByHandle(ctx, tx, handle)
	if err != nil {
		return false, err
	}

	state := c.State
	if state == "open" && !time.Now().Before(c.ExpiresAt) {
		if err := r.exhaustClaimChallenge(ctx, tx, c.ChallengeID, c.PrincipalSubject, c.DeviceID, cfg); err != nil {
			return false, err
		}
		state = "not_discharged"
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit claim challenge status for %q: %w", handle, err)
	}
	return state == "open", nil
}

// CompleteClaim finalizes handle's challenge for principalSubject
// (requirement 6). Succeeds -- moving board ownership and returning entry's
// household -- only when the challenge discharged (r rounds satisfied) AND
// the board is never-claimed or resolves to Unadopted. Every other path
// (not found, wrong principal, still open, expired/exhausted, or discharged
// against a board a real household already owns) returns ErrClaimNotDischarged
// uniformly -- server.go maps that single error to one gRPC failure, so none
// of these cases are distinguishable to the caller.
//
// entry.EntityID/TargetHouseholdID are filled in here (matching households.go's
// pattern) once the target household is known; entry.Action/EntityKind/
// ActorSubject/ActorKind/CorrelationID are the caller's responsibility
// (server.go), same division as every other auditedWrite call in this
// package.
func (r *Repository) CompleteClaim(ctx context.Context, principalSubject, handle string, cfg claim.Config, entry audit.Entry) (HouseholdRow, error) {
	var result HouseholdRow
	err := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		c, err := lockClaimChallengeByHandle(ctx, tx, handle)
		if err != nil {
			if errors.Is(err, ErrClaimChallengeNotFound) {
				return audit.Entry{}, ErrClaimNotDischarged
			}
			return audit.Entry{}, err
		}

		if c.PrincipalSubject != principalSubject {
			// Never disclose "this handle belongs to someone else" --
			// identical to every other not-discharged case.
			return audit.Entry{}, ErrClaimNotDischarged
		}

		state := c.State
		if state == "open" && !time.Now().Before(c.ExpiresAt) {
			if err := r.exhaustClaimChallenge(ctx, tx, c.ChallengeID, c.PrincipalSubject, c.DeviceID, cfg); err != nil {
				return audit.Entry{}, err
			}
			state = "not_discharged"
		}

		if state != "discharged" {
			return audit.Entry{}, ErrClaimNotDischarged
		}

		var boardID int64
		var boardHouseholdID *int64
		var isUnadopted bool
		err = tx.QueryRow(ctx, `
			SELECT b.board_id, b.household_id, COALESCE(h.is_unadopted, FALSE)
			FROM board b
			LEFT JOIN household h ON h.household_id = b.household_id
			WHERE b.device_id = $1
			FOR UPDATE OF b
		`, c.DeviceID).Scan(&boardID, &boardHouseholdID, &isUnadopted)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Discharged against a device_id with no board row at all --
				// should not occur in practice (discharge requires an
				// observed restart, which requires a board row to exist),
				// but treated as not-discharged defensively rather than as
				// an internal error.
				return audit.Entry{}, ErrClaimNotDischarged
			}
			return audit.Entry{}, fmt.Errorf("lookup board %q for claim completion: %w", c.DeviceID, err)
		}

		claimable := boardHouseholdID == nil || isUnadopted
		if !claimable {
			// Requirement 6: discharged against a real household's board
			// confers nothing -- indistinguishable from not-discharged.
			// discharged_at is already recorded on claim_challenge (FR77
			// evidence); nothing further to write here.
			return audit.Entry{}, ErrClaimNotDischarged
		}

		targetHouseholdID, err := resolveOrCreateHouseholdForClaim(ctx, tx, principalSubject)
		if err != nil {
			return audit.Entry{}, err
		}

		if boardHouseholdID != nil {
			// Currently Unadopted -- close that ownership row before opening
			// the new one (SCD2 close-and-open, AGENTS.md).
			if _, err := tx.Exec(ctx, `
				UPDATE board_ownership SET valid_to = NOW() WHERE board_id = $1 AND valid_to IS NULL
			`, boardID); err != nil {
				return audit.Entry{}, fmt.Errorf("close board_ownership for board %d: %w", boardID, err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO board_ownership (board_id, household_id) VALUES ($1, $2)
		`, boardID, targetHouseholdID); err != nil {
			return audit.Entry{}, fmt.Errorf("insert board_ownership for board %d: %w", boardID, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE board SET household_id = $2 WHERE board_id = $1
		`, boardID, targetHouseholdID); err != nil {
			return audit.Entry{}, fmt.Errorf("update board %d household: %w", boardID, err)
		}

		if err := tx.QueryRow(ctx, `
			SELECT household_id, name FROM household WHERE household_id = $1
		`, targetHouseholdID).Scan(&result.HouseholdID, &result.Name); err != nil {
			return audit.Entry{}, fmt.Errorf("read claimed household %d: %w", targetHouseholdID, err)
		}

		boardIDStr := fmt.Sprintf("%d", boardID)
		entry.EntityID = &boardIDStr
		entry.TargetHouseholdID = &targetHouseholdID
		return entry, nil
	})
	if err != nil {
		return HouseholdRow{}, err
	}
	return result, nil
}

// resolveOrCreateHouseholdForClaim returns principalSubject's current
// household (the earliest current membership, if they hold more than one --
// FR75 permits multi-household membership but V1 has no switching
// experience, so claiming picks one deterministically) or creates a new one
// for them first (FR75's create-on-first-claim). Runs inside CompleteClaim's
// transaction so a caller with no household who claims a board atomically
// gets both.
func resolveOrCreateHouseholdForClaim(ctx context.Context, tx pgx.Tx, principalSubject string) (int64, error) {
	var householdID int64
	err := tx.QueryRow(ctx, `
		SELECT household_id FROM household_membership
		WHERE principal_subject = $1 AND valid_to IS NULL
		ORDER BY household_membership_id
		LIMIT 1
	`, principalSubject).Scan(&householdID)
	if err == nil {
		return householdID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("resolve current household for %q: %w", principalSubject, err)
	}

	if err := tx.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ($1) RETURNING household_id
	`, defaultHouseholdName(principalSubject)).Scan(&householdID); err != nil {
		return 0, fmt.Errorf("create household for %q: %w", principalSubject, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)
	`, householdID, principalSubject); err != nil {
		return 0, fmt.Errorf("insert initial membership for %q in household %d: %w", principalSubject, householdID, err)
	}
	return householdID, nil
}
