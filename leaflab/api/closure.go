package main

// Ownership closure, self-service adoption, and transfer (FR70.2-.4, FR77)
// -- implementation for #1343.
//
// FR70's ownership closure of a board B is a FIXED SIX-STEP ENUMERATION over
// current placement, not a transitive closure:
//
//  1. B itself.
//  2. B's sensors.
//  3. The regions those sensors currently occupy.
//  4. The root subtree(s) containing those regions.
//  5. Every OTHER board with a sensor currently in that subtree (entangled
//     boards).
//  6. Every plant currently placed in that subtree.
//
// It deliberately does NOT recurse into step 5's entangled boards' OTHER
// sensors (which may sit in a different subtree entirely) -- doing so would
// make the closure a transitive closure, which the requirement text calls
// out as a defect, not an optimisation: "does not follow entangled boards'
// sensors into other subtrees, so adoption may leave an owned board with a
// sensor in an unowned region. That is not a live cross-household reference
// and is dischargeable." ComputeClosure below is written as six sequential
// queries, in this exact order, specifically so that shape stays visible in
// the code -- do not refactor this into a recursive walk over "boards
// reachable from B".
//
// TransferClosure (FR77) is this task's write path: it moves the WHOLE
// closure -- B, every entangled board currently owned by the same losing
// household, every subtree root region, and every plant in the subtree --
// to destinationHouseholdID in one transaction, gated on evidence (a
// release token from ReleaseBoard, FR77(a), or an elevated admin action
// carrying a discharged FR76 possession-challenge handle, FR77(b)), and
// leaves a departure_record behind on the losing side (migration
// 022_departure_record.up.sql). If the closure contains a board NOT
// currently owned by the losing household (a third household, Unadopted,
// or unclaimed), the whole operation refuses with FR59.3's shape, naming
// the offending board(s) and the shared subtree -- see ErrEntangledClosure.
//
// Adoption out of Unadopted (FR76, #1342's CompleteClaim) continues to move
// only the single claimed board today; this task's RPC surface is
// ReleaseBoard/TransferClosure/PreviewClosure only (see the issue's
// Scaffold section) -- extending CompleteClaim to move the whole closure on
// adoption is tracked as a follow-up scope note, not done here.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/audit"
)

// ErrClosureNoEvidence is returned by TransferClosure when neither
// evidence branch (release token or admin-evidence discharged challenge
// handle) is supplied -- FR77's "an admin assertion alone is never
// sufficient", checked before any DB round trip.
var ErrClosureNoEvidence = errors.New("transfer requires either a release token or a discharged possession-challenge handle")

// ErrClosureAdminReasonRequired is returned by TransferClosure when the
// admin-evidence branch is used with no reason -- FR77(b)'s "elevated,
// reasoned admin action" requires one; the release-token branch does not
// (it carries its own implicit reason).
var ErrClosureAdminReasonRequired = errors.New("an admin-evidence transfer requires a reason")

// ErrClosureInvalidReleaseToken is returned by TransferClosure when
// release_token names no row, is expired, already used, or does not match
// boardID/the board's current household.
var ErrClosureInvalidReleaseToken = errors.New("release token is invalid, expired or already used")

// ErrClosureChallengeNotDischarged is returned by TransferClosure when the
// admin-evidence branch's discharged_challenge_handle does not name an
// FR76 claim_challenge in state 'discharged' against boardID's device_id.
var ErrClosureChallengeNotDischarged = errors.New("possession challenge is not discharged against this board")

// ErrClosureNotRealHousehold is returned by ReleaseBoard/TransferClosure
// when boardID currently resolves to no household or to the member-less
// Unadopted household -- both are reached through FR76's CompleteClaim
// instead (adoption, not release/transfer).
var ErrClosureNotRealHousehold = errors.New("board is not currently owned by a real household")

// ErrClosureNotHouseholdMember is returned by ReleaseBoard when
// principalSubject holds no current household_membership row in the
// board's current household -- FR77(a)'s "a member of the losing
// household".
var ErrClosureNotHouseholdMember = errors.New("principal is not a current member of this board's household")

// ErrClosureSameHousehold is returned by TransferClosure when
// destinationHouseholdID already owns boardID -- nothing to transfer.
var ErrClosureSameHousehold = errors.New("board already belongs to the destination household")

// ErrClosureDestinationUnadopted is returned by TransferClosure when
// destinationHouseholdID names the member-less Unadopted household --
// A9's "no new arrivals" applies to a transfer destination too; the
// board_ownership/household_membership triggers would refuse the INSERT
// anyway, but this is checked first so the failure is a clean refusal
// rather than a raw constraint-violation error.
var ErrClosureDestinationUnadopted = errors.New("cannot transfer a board into the Unadopted household")

// ErrEntangledClosure is FR70's refusal path: the closure contains a board
// (or boards) not currently owned by the same household as B -- moving B's
// closure would silently reach into a household that never consented.
// ForeignBoardIDs/SubtreeRootIDs let server.go build FR59.3's
// refuse-and-name-the-alternative message (naming FR51/FR54/FR74) without
// a second query.
type ErrEntangledClosure struct {
	ForeignBoardIDs []int64
	SubtreeRootIDs  []int64
}

func (e *ErrEntangledClosure) Error() string {
	return fmt.Sprintf("closure contains board(s) %v not owned by the source household, sharing subtree root(s) %v", e.ForeignBoardIDs, e.SubtreeRootIDs)
}

// Closure is FR70's ownership closure of a board, as computed by
// ComputeClosure. Every id slice is deduplicated; a slice may be empty (but
// never nil-vs-empty-significant -- callers should treat both the same way)
// when a step finds nothing, e.g. a board with no sensors placed anywhere
// yields an empty closure beyond BoardID/SensorIDs.
type Closure struct {
	// BoardID is B (step 1).
	BoardID int64
	// SensorIDs are B's sensors (step 2), regardless of whether they are
	// currently placed in a region.
	SensorIDs []int64
	// RegionIDs are the regions B's sensors currently occupy (step 3) --
	// only sensors with a non-NULL region_id contribute here.
	RegionIDs []int64
	// SubtreeRootIDs are the root(s) of the region tree(s) containing
	// RegionIDs (step 4). Usually one root, but a board's sensors may be
	// scattered across more than one physical root region (e.g. one sensor
	// in "Greenhouse", another in "Garage") -- the closure spans every such
	// root's subtree, not just the first one found.
	SubtreeRootIDs []int64
	// SubtreeRegionIDs is the full region id membership of every subtree
	// named by SubtreeRootIDs (each root plus all of its descendants) --
	// the population EntangledBoardIDs and PlantIDs are computed against.
	// Not itself one of the six named closure members, but exposed since
	// FR70's refusal path needs to name "the shared subtree" (FR59.3).
	SubtreeRegionIDs []int64
	// EntangledBoardIDs are every OTHER board (never B itself) with a
	// sensor currently in SubtreeRegionIDs (step 5) -- the fixed
	// enumeration stops here; their own other sensors are never followed.
	EntangledBoardIDs []int64
	// PlantIDs are every plant currently placed in SubtreeRegionIDs (step
	// 6) -- removed plants (removed_at IS NOT NULL) are excluded, since
	// "currently placed" is present-tense.
	PlantIDs []int64
}

// pgxQuerier is satisfied by both *pgxpool.Pool (Repository.db) and pgx.Tx,
// letting the closure-computation step helpers below run identically
// outside a transaction (ComputeClosure/PreviewClosure, the read-only
// path) and inside one (TransferClosure's single-transaction move, which
// must read a consistent view of the closure after locking board B FOR
// UPDATE).
type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ComputeClosure computes FR70's ownership closure of boardID: the fixed
// six-step enumeration documented on this file's doc comment, executed in
// that exact order. It is read-only -- callers that need the closure locked
// against concurrent mutation (e.g. TransferClosure's write transaction)
// call computeClosureWith directly with their own tx instead.
func (r *Repository) ComputeClosure(ctx context.Context, boardID int64) (Closure, error) {
	return r.computeClosureWith(ctx, r.db, boardID)
}

// computeClosureWith is ComputeClosure's queryer-agnostic body: identical
// six-step enumeration, but runs against whatever pgxQuerier the caller
// supplies -- r.db for the read-only path, or a pgx.Tx for a caller that
// needs the read to participate in its own write transaction (TransferClosure).
func (r *Repository) computeClosureWith(ctx context.Context, db pgxQuerier, boardID int64) (Closure, error) {
	// Step 1: B itself. Verifying existence up front means every later step
	// fails fast with ErrBoardNotFound instead of silently returning an
	// empty closure for a boardID that names no row.
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM board WHERE board_id = $1)`, boardID).Scan(&exists); err != nil {
		return Closure{}, fmt.Errorf("check board %d exists: %w", boardID, err)
	}
	if !exists {
		return Closure{}, ErrBoardNotFound
	}

	closure := Closure{BoardID: boardID}

	// Step 2 + 3: B's sensors, and the regions they currently occupy.
	sensorIDs, regionIDs, err := boardSensorsAndRegions(ctx, db, boardID)
	if err != nil {
		return Closure{}, err
	}
	closure.SensorIDs = sensorIDs
	closure.RegionIDs = regionIDs

	if len(regionIDs) == 0 {
		// None of B's sensors are currently placed anywhere -- the closure
		// is B and its sensors alone; steps 4-6 have nothing to enumerate.
		return closure, nil
	}

	// Step 4: the root subtree(s) containing those regions.
	rootIDs, err := resolveRegionRoots(ctx, db, regionIDs)
	if err != nil {
		return Closure{}, err
	}
	closure.SubtreeRootIDs = rootIDs

	subtreeRegionIDs, err := subtreeRegionIDs(ctx, db, rootIDs)
	if err != nil {
		return Closure{}, err
	}
	closure.SubtreeRegionIDs = subtreeRegionIDs

	// Step 5: every OTHER board with a sensor currently in that subtree.
	// Fixed enumeration: this does not recurse into those boards' own other
	// sensors (see this file's doc comment).
	entangledBoardIDs, err := boardsWithSensorsInRegions(ctx, db, subtreeRegionIDs, boardID)
	if err != nil {
		return Closure{}, err
	}
	closure.EntangledBoardIDs = entangledBoardIDs

	// Step 6: every plant currently placed in that subtree.
	plantIDs, err := activePlantsInRegions(ctx, db, subtreeRegionIDs)
	if err != nil {
		return Closure{}, err
	}
	closure.PlantIDs = plantIDs

	return closure, nil
}

// boardSensorsAndRegions returns boardID's sensor ids (step 2) and the
// distinct, non-NULL region ids they currently occupy (step 3).
func boardSensorsAndRegions(ctx context.Context, db pgxQuerier, boardID int64) (sensorIDs []int64, regionIDs []int64, err error) {
	rows, err := db.Query(ctx, `
		SELECT sensor_id, region_id FROM sensor WHERE board_id = $1
	`, boardID)
	if err != nil {
		return nil, nil, fmt.Errorf("list sensors for board %d: %w", boardID, err)
	}
	defer rows.Close()

	seenRegion := make(map[int64]bool)
	for rows.Next() {
		var sensorID int64
		var regionID *int64
		if err := rows.Scan(&sensorID, &regionID); err != nil {
			return nil, nil, fmt.Errorf("scan sensor for board %d: %w", boardID, err)
		}
		sensorIDs = append(sensorIDs, sensorID)
		if regionID != nil && !seenRegion[*regionID] {
			seenRegion[*regionID] = true
			regionIDs = append(regionIDs, *regionID)
		}
	}
	return sensorIDs, regionIDs, rows.Err()
}

// resolveRegionRoots returns the distinct tree-root region ids reached by
// walking each of regionIDs up through parent_region_id -- step 4's "root
// subtree(s) containing those regions". A region that is itself a root
// resolves to itself.
func resolveRegionRoots(ctx context.Context, db pgxQuerier, regionIDs []int64) ([]int64, error) {
	rows, err := db.Query(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT region_id, parent_region_id
			FROM region
			WHERE region_id = ANY($1)

			UNION ALL

			SELECT r.region_id, r.parent_region_id
			FROM region r
			JOIN ancestors a ON r.region_id = a.parent_region_id
		)
		SELECT DISTINCT region_id FROM ancestors WHERE parent_region_id IS NULL
	`, regionIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve region roots for %v: %w", regionIDs, err)
	}
	defer rows.Close()

	var rootIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan region root for %v: %w", regionIDs, err)
		}
		rootIDs = append(rootIDs, id)
	}
	return rootIDs, rows.Err()
}

// subtreeRegionIDs returns every region id in the subtree(s) rooted at
// rootIDs -- each root plus all of its descendants -- the population steps
// 5 and 6 are computed against.
func subtreeRegionIDs(ctx context.Context, db pgxQuerier, rootIDs []int64) ([]int64, error) {
	rows, err := db.Query(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT region_id FROM region WHERE region_id = ANY($1)

			UNION ALL

			SELECT r.region_id
			FROM region r
			JOIN subtree s ON r.parent_region_id = s.region_id
		)
		SELECT region_id FROM subtree
	`, rootIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve subtree region ids for roots %v: %w", rootIDs, err)
	}
	defer rows.Close()

	var regionIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan subtree region for roots %v: %w", rootIDs, err)
		}
		regionIDs = append(regionIDs, id)
	}
	return regionIDs, rows.Err()
}

// boardsWithSensorsInRegions returns the distinct board ids (excluding
// excludeBoardID) with at least one sensor currently placed in regionIDs --
// step 5's entangled boards.
func boardsWithSensorsInRegions(ctx context.Context, db pgxQuerier, regionIDs []int64, excludeBoardID int64) ([]int64, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT board_id
		FROM sensor
		WHERE region_id = ANY($1) AND board_id != $2
	`, regionIDs, excludeBoardID)
	if err != nil {
		return nil, fmt.Errorf("list entangled boards for regions %v: %w", regionIDs, err)
	}
	defer rows.Close()

	var boardIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan entangled board for regions %v: %w", regionIDs, err)
		}
		boardIDs = append(boardIDs, id)
	}
	return boardIDs, rows.Err()
}

// activePlantsInRegions returns the ids of every plant currently placed
// (removed_at IS NULL) in regionIDs -- step 6.
func activePlantsInRegions(ctx context.Context, db pgxQuerier, regionIDs []int64) ([]int64, error) {
	rows, err := db.Query(ctx, `
		SELECT plant_id
		FROM plant
		WHERE region_id = ANY($1) AND removed_at IS NULL
	`, regionIDs)
	if err != nil {
		return nil, fmt.Errorf("list active plants for regions %v: %w", regionIDs, err)
	}
	defer rows.Close()

	var plantIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan active plant for regions %v: %w", regionIDs, err)
		}
		plantIDs = append(plantIDs, id)
	}
	return plantIDs, rows.Err()
}

// PreviewClosure returns boardID's ownership closure without moving
// anything -- the read-only preview a caller uses before deciding whether
// to adopt or transfer. Fully implemented: it is a thin wrapper over
// ComputeClosure, which is itself read-only (see this file's doc comment).
func (r *Repository) PreviewClosure(ctx context.Context, boardID int64) (Closure, error) {
	return r.ComputeClosure(ctx, boardID)
}

// releaseTokenLifetime is how long a ReleaseBoard token remains presentable
// to TransferClosure before it must be reissued. Not configurable today --
// no requirement text sets a specific bound, so this is chosen generously
// (a household coordinating a transfer with another household should not
// be rushed) while still being clearly finite.
const releaseTokenLifetime = 24 * time.Hour

// generateReleaseToken returns a fresh opaque external token for a
// board_release_token row -- 20 random bytes / 40 hex chars, same shape and
// rationale as claim.go's generateClaimHandle (never the numeric row id).
func generateReleaseToken() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate release token: %w", err)
	}
	return "rt_" + hex.EncodeToString(buf), nil
}

// ReleaseBoard is FR77(a)'s evidence path: a current member of boardID's
// owning household releases it, producing an opaque, single-use,
// time-bounded token TransferClosure later presents as evidence that the
// losing household consented. Refuses (ErrClosureNotRealHousehold) for a
// board with no current household or resolving to Unadopted -- there is no
// "losing household" to release from in either case; use FR76's
// CompleteClaim to adopt instead. Refuses (ErrClosureNotHouseholdMember) for
// a caller who is not currently a member of the board's owning household.
//
// entry's audit row commits in the same transaction as the token INSERT
// (auditedWrite) -- consenting to a release is itself an FR8-audited
// action, distinct from the (later, possibly by a different principal in
// the gaining household) TransferClosure call that actually moves anything.
func (r *Repository) ReleaseBoard(ctx context.Context, boardID int64, principalSubject, reason string, entry audit.Entry) (releaseToken string, err error) {
	var token string
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		householdID, isRealHousehold, err := currentRealHouseholdForBoardTx(ctx, tx, boardID)
		if err != nil {
			return audit.Entry{}, err
		}
		if !isRealHousehold {
			return audit.Entry{}, ErrClosureNotRealHousehold
		}

		var isMember bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM household_membership
				WHERE household_id = $1 AND principal_subject = $2 AND valid_to IS NULL
			)
		`, householdID, principalSubject).Scan(&isMember); err != nil {
			return audit.Entry{}, fmt.Errorf("check membership for %q in household %d: %w", principalSubject, householdID, err)
		}
		if !isMember {
			return audit.Entry{}, ErrClosureNotHouseholdMember
		}

		generated, err := generateReleaseToken()
		if err != nil {
			return audit.Entry{}, err
		}

		var reasonArg *string
		if reason != "" {
			reasonArg = &reason
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO board_release_token (token, board_id, household_id, released_by, reason, expires_at)
			VALUES ($1, $2, $3, $4, $5, NOW() + make_interval(secs => $6))
		`, generated, boardID, householdID, principalSubject, reasonArg, releaseTokenLifetime.Seconds()); err != nil {
			return audit.Entry{}, fmt.Errorf("insert release token for board %d: %w", boardID, err)
		}
		token = generated

		boardIDStr := fmt.Sprintf("%d", boardID)
		entry.EntityID = &boardIDStr
		entry.TargetHouseholdID = &householdID
		if reason != "" {
			entry.Reason = &reason
		}
		return entry, nil
	})
	if writeErr != nil {
		return "", writeErr
	}
	return token, nil
}

// currentRealHouseholdForBoardTx resolves boardID's current household
// inside tx, reporting isReal=false for both "no household" (unclaimed,
// FR1.1) and "resolves to the member-less Unadopted household" -- the two
// states ReleaseBoard/TransferClosure both refuse on identically
// (ErrClosureNotRealHousehold), since FR76's CompleteClaim, not this file,
// is the path out of either.
func currentRealHouseholdForBoardTx(ctx context.Context, tx pgx.Tx, boardID int64) (householdID int64, isReal bool, err error) {
	var householdIDNullable *int64
	var isUnadopted bool
	err = tx.QueryRow(ctx, `
		SELECT b.household_id, COALESCE(h.is_unadopted, FALSE)
		FROM board b
		LEFT JOIN household h ON h.household_id = b.household_id
		WHERE b.board_id = $1
		FOR UPDATE OF b
	`, boardID).Scan(&householdIDNullable, &isUnadopted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, ErrBoardNotFound
		}
		return 0, false, fmt.Errorf("lock board %d: %w", boardID, err)
	}
	if householdIDNullable == nil || isUnadopted {
		return 0, false, nil
	}
	return *householdIDNullable, true, nil
}

// consumeReleaseToken validates and single-use-consumes token as
// TransferClosure's FR77(a) evidence: it must name a row that is not
// expired, not already used, and was issued for exactly this boardID while
// it was owned by sourceHouseholdID (a token from a since-superseded
// ownership is not valid evidence for the current transfer). Marks the
// token used in the same transaction as the rest of TransferClosure's
// move, so a token can never be replayed even if the caller retries with
// the same token after a successful transfer.
func consumeReleaseToken(ctx context.Context, tx pgx.Tx, token string, boardID, sourceHouseholdID int64) error {
	var tokenBoardID, tokenHouseholdID int64
	var usedAt *time.Time
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT board_id, household_id, used_at, expires_at
		FROM board_release_token
		WHERE token = $1
		FOR UPDATE
	`, token).Scan(&tokenBoardID, &tokenHouseholdID, &usedAt, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClosureInvalidReleaseToken
		}
		return fmt.Errorf("lookup release token: %w", err)
	}
	if usedAt != nil || time.Now().After(expiresAt) || tokenBoardID != boardID || tokenHouseholdID != sourceHouseholdID {
		return ErrClosureInvalidReleaseToken
	}
	if _, err := tx.Exec(ctx, `UPDATE board_release_token SET used_at = NOW() WHERE token = $1`, token); err != nil {
		return fmt.Errorf("mark release token %q used: %w", token, err)
	}
	return nil
}

// verifyDischargedChallenge validates handle as TransferClosure's FR77(b)
// evidence: it must name an FR76 claim_challenge (leaflab/api/claim.go) in
// state 'discharged' whose device_id resolves to boardID. Never mutates
// claim_challenge -- unlike a release token, a discharged challenge is not
// single-use evidence (the same discharge fact may legitimately support
// more than one admin review of the same situation), and CompleteClaim
// already refuses to let a discharged-against-a-real-household challenge
// itself move ownership (claim.go's requirement 6), so there is no
// double-spend risk here to guard against.
func verifyDischargedChallenge(ctx context.Context, tx pgx.Tx, handle string, boardID int64) error {
	var state string
	err := tx.QueryRow(ctx, `
		SELECT cc.state
		FROM claim_challenge cc
		JOIN board b ON b.device_id = cc.device_id
		WHERE cc.handle = $1 AND b.board_id = $2
	`, handle, boardID).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClosureChallengeNotDischarged
		}
		return fmt.Errorf("verify discharged challenge %q for board %d: %w", handle, boardID, err)
	}
	if state != "discharged" {
		return ErrClosureChallengeNotDischarged
	}
	return nil
}

// moveBoardOwnership closes boardID's current board_ownership interval and
// opens a new one at destinationHouseholdID (SCD2 close-and-open,
// AGENTS.md), keeping board.household_id in sync as its current-value
// cache -- the same three statements CompleteClaim (claim.go) uses for the
// single-board case, factored out here so TransferClosure applies them
// uniformly to B and to every entangled board that moves with it.
func moveBoardOwnership(ctx context.Context, tx pgx.Tx, boardID, destinationHouseholdID int64) error {
	if _, err := tx.Exec(ctx, `
		UPDATE board_ownership SET valid_to = NOW() WHERE board_id = $1 AND valid_to IS NULL
	`, boardID); err != nil {
		return fmt.Errorf("close board_ownership for board %d: %w", boardID, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO board_ownership (board_id, household_id) VALUES ($1, $2)
	`, boardID, destinationHouseholdID); err != nil {
		return fmt.Errorf("insert board_ownership for board %d: %w", boardID, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE board SET household_id = $2 WHERE board_id = $1
	`, boardID, destinationHouseholdID); err != nil {
		return fmt.Errorf("update board %d household: %w", boardID, err)
	}
	return nil
}

// copyOwnedPlantTypes is FR77's plant-type-catalog copy seam: "a closure
// move copies any household-owned plant type (FR55) referenced by a
// transferred plant into the gaining household, so no plant references a
// plant type owned by another household after a transfer, and no transfer
// is refused over one." plant_type.household_id does not exist yet (the
// plant-type catalog is Phase 5) -- this hook is a documented no-op until
// it does. TransferClosure calls it unconditionally (not behind a
// feature-flag/TODO skip) so the seam is exercised, and testable, from the
// moment this task lands, per the issue's explicit instruction.
func (r *Repository) copyOwnedPlantTypes(ctx context.Context, tx pgx.Tx, plantIDs []int64, destinationHouseholdID int64) error {
	// No-op: plant_type.household_id does not exist yet (Phase 5). See this
	// function's doc comment.
	return nil
}

// departureSummary is departure_record.summary's JSONB shape: what left
// the losing household and when (occurred_at, the row's own column, covers
// "when"). Deliberately carries no destination_household_id or any other
// fact about the gaining household -- FR77's "the record ... does not
// become a cross-household oracle" -- only ids that already belonged to the
// losing household before the transfer.
type departureSummary struct {
	BoardIDs  []int64 `json:"board_ids"`
	RegionIDs []int64 `json:"region_ids"`
	PlantIDs  []int64 `json:"plant_ids"`
}

// writeDepartureRecord inserts departure_record's one append-only row for
// this transfer (NFR6.3 -- see migration 022_departure_record.up.sql),
// naming what left (boardIDs/regionIDs/plantIDs, all of which belonged to
// losingHouseholdID a moment ago) and by whom/why, but never the
// destination household -- see departureSummary's doc comment.
func writeDepartureRecord(ctx context.Context, tx pgx.Tx, losingHouseholdID int64, boardIDs, regionIDs, plantIDs []int64, actorSubject, reason string) error {
	summaryJSON, err := json.Marshal(departureSummary{BoardIDs: boardIDs, RegionIDs: regionIDs, PlantIDs: plantIDs})
	if err != nil {
		return fmt.Errorf("marshal departure summary for household %d: %w", losingHouseholdID, err)
	}

	var reasonArg *string
	if reason != "" {
		reasonArg = &reason
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO departure_record (losing_household_id, summary, board_count, region_count, plant_count, actor_subject, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, losingHouseholdID, summaryJSON, len(boardIDs), len(regionIDs), len(plantIDs), actorSubject, reasonArg); err != nil {
		return fmt.Errorf("insert departure record for household %d: %w", losingHouseholdID, err)
	}
	return nil
}

// TransferClosure is FR77's transfer operation: moves boardID's ownership
// closure (ComputeClosure) to destinationHouseholdID in one transaction --
// closing and opening board_ownership SCD2 intervals for B and every
// entangled board currently owned by the same losing household, updating
// region.household_id on the closure's subtree root(s) and
// plant.household_id on every plant in the subtree, copying any
// household-owned plant type the transferred plants reference
// (copyOwnedPlantTypes), and leaving a departure_record on the losing
// side -- or refuses entirely, leaving no partial ownership change.
//
// Evidence gating (FR77): exactly one of releaseToken/dischargedChallengeHandle
// must be non-empty (checked before any DB round trip: ErrClosureNoEvidence).
// releaseToken must have been issued by ReleaseBoard for this exact
// board/household pairing and not yet be expired or used
// (ErrClosureInvalidReleaseToken); dischargedChallengeHandle must name an
// FR76 claim_challenge discharged against this board's device_id
// (ErrClosureChallengeNotDischarged), and reason must be non-empty for
// that branch (ErrClosureAdminReasonRequired) -- "an admin assertion alone
// is never sufficient".
//
// Refusal path (FR59.3): if the closure contains a board not currently
// owned by the losing (source) household -- whether a third real
// household, Unadopted, or unclaimed -- the whole transfer refuses with
// ErrEntangledClosure, naming the offending board(s) and the shared
// subtree; server.go directs the caller to FR51/FR54/FR74 to separate them
// first. No partial move ever commits.
func (r *Repository) TransferClosure(ctx context.Context, boardID, destinationHouseholdID int64, releaseToken, dischargedChallengeHandle, actorSubject, reason string, entry audit.Entry) (HouseholdRow, error) {
	if releaseToken == "" && dischargedChallengeHandle == "" {
		return HouseholdRow{}, ErrClosureNoEvidence
	}
	if releaseToken == "" && reason == "" {
		return HouseholdRow{}, ErrClosureAdminReasonRequired
	}

	var result HouseholdRow
	err := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		sourceHouseholdID, isRealHousehold, err := currentRealHouseholdForBoardTx(ctx, tx, boardID)
		if err != nil {
			return audit.Entry{}, err
		}
		if !isRealHousehold {
			return audit.Entry{}, ErrClosureNotRealHousehold
		}
		if destinationHouseholdID == sourceHouseholdID {
			return audit.Entry{}, ErrClosureSameHousehold
		}

		var destIsUnadopted bool
		if err := tx.QueryRow(ctx, `
			SELECT is_unadopted FROM household WHERE household_id = $1
		`, destinationHouseholdID).Scan(&destIsUnadopted); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, ErrHouseholdNotFound
			}
			return audit.Entry{}, fmt.Errorf("lookup destination household %d: %w", destinationHouseholdID, err)
		}
		if destIsUnadopted {
			return audit.Entry{}, ErrClosureDestinationUnadopted
		}

		if releaseToken != "" {
			if err := consumeReleaseToken(ctx, tx, releaseToken, boardID, sourceHouseholdID); err != nil {
				return audit.Entry{}, err
			}
		} else {
			if err := verifyDischargedChallenge(ctx, tx, dischargedChallengeHandle, boardID); err != nil {
				return audit.Entry{}, err
			}
		}

		closure, err := r.computeClosureWith(ctx, tx, boardID)
		if err != nil {
			return audit.Entry{}, err
		}

		var foreign []int64
		for _, entangledID := range closure.EntangledBoardIDs {
			hhID, isReal, err := currentRealHouseholdForBoardTx(ctx, tx, entangledID)
			if err != nil {
				return audit.Entry{}, fmt.Errorf("resolve entangled board %d: %w", entangledID, err)
			}
			if !isReal || hhID != sourceHouseholdID {
				foreign = append(foreign, entangledID)
			}
		}
		if len(foreign) > 0 {
			return audit.Entry{}, &ErrEntangledClosure{ForeignBoardIDs: foreign, SubtreeRootIDs: closure.SubtreeRootIDs}
		}

		movedBoardIDs := append([]int64{closure.BoardID}, closure.EntangledBoardIDs...)
		for _, id := range movedBoardIDs {
			if err := moveBoardOwnership(ctx, tx, id, destinationHouseholdID); err != nil {
				return audit.Entry{}, err
			}
		}

		for _, rootID := range closure.SubtreeRootIDs {
			if _, err := tx.Exec(ctx, `
				UPDATE region SET household_id = $2 WHERE region_id = $1
			`, rootID, destinationHouseholdID); err != nil {
				return audit.Entry{}, fmt.Errorf("move region root %d: %w", rootID, err)
			}
		}

		if len(closure.PlantIDs) > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE plant SET household_id = $2 WHERE plant_id = ANY($1)
			`, closure.PlantIDs, destinationHouseholdID); err != nil {
				return audit.Entry{}, fmt.Errorf("move plants %v: %w", closure.PlantIDs, err)
			}
		}

		if err := r.copyOwnedPlantTypes(ctx, tx, closure.PlantIDs, destinationHouseholdID); err != nil {
			return audit.Entry{}, fmt.Errorf("copy owned plant types for transfer of board %d: %w", boardID, err)
		}

		if err := writeDepartureRecord(ctx, tx, sourceHouseholdID, movedBoardIDs, closure.SubtreeRegionIDs, closure.PlantIDs, actorSubject, reason); err != nil {
			return audit.Entry{}, err
		}

		if err := tx.QueryRow(ctx, `
			SELECT household_id, name FROM household WHERE household_id = $1
		`, destinationHouseholdID).Scan(&result.HouseholdID, &result.Name); err != nil {
			return audit.Entry{}, fmt.Errorf("read destination household %d: %w", destinationHouseholdID, err)
		}

		return entry, nil
	})
	if err != nil {
		return HouseholdRow{}, err
	}
	return result, nil
}
