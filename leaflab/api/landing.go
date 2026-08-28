package main

// Household landing (FR62, NFR3.1) -- Implementation for #1350.
//
// leaflab/api/landing.Classify (leaflab/api/landing/classify.go, added at
// Scaffold) is the pure five-way decision function: it owns priority
// ordering and sentence text but knows nothing about SQL, protobuf or this
// package's Repository. This file is the glue between the two: it gathers
// Classify's boolean Input signals from Postgres under NFR3.1's bounded-
// query constraint (landingSignalsForHousehold below, one query regardless
// of household size) and converts landing.Result into the wire response
// (toLandingResponse). The RPC handler itself (GetHouseholdLanding) lives
// in server.go, alongside every other RPC -- this file mirrors households.go/
// claim.go's convention of keeping a feature's Repository methods and
// pure-glue helpers in one file named for the feature.
//
// Bounded queries (NFR3.1): landingBoardSignalsQuery aggregates over every
// non-retired board and its sensors in one household-scoped query --
// MIN(sensor.last_seen_at) per board stands in for "does this board have
// any stale sensor", so Classify never needs a per-sensor row to decide
// AnySensorSilentWhileBoardReports. This is one query no matter how many
// boards or sensors the household has (see LandingBoardSignals's doc
// comment) -- GetHouseholdLanding issues exactly this query plus, at most,
// one to resolve "caller's own household" and one to probe service health
// (isServiceDegraded, itself a single Ping), never a query per board.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/api/landing"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// CurrentHouseholdForPrincipal resolves the one household GetHouseholdLandingRequest's
// household_id=0 ("caller's own current household") means, per api.proto's
// doc comment. FR75 permits multi-household membership (authz.ScopeForPrincipal
// unions every current membership); this picks the earliest-joined one
// deterministically (ORDER BY household_membership_id) rather than an
// unspecified one, so repeated calls for the same principal never flap
// between households. ok is false, with householdID left at 0, for a
// principal with no current household_membership row anywhere -- FR62
// condition 5's trigger.
func (r *Repository) CurrentHouseholdForPrincipal(ctx context.Context, principalSubject string) (int64, bool, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id
		FROM household_membership
		WHERE principal_subject = $1
		  AND valid_to IS NULL
		ORDER BY household_membership_id
		LIMIT 1
	`, principalSubject).Scan(&householdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("current household for %q: %w", principalSubject, err)
	}
	return householdID, true, nil
}

// LandingBoardSignalRow is one non-retired board's raw signal for FR62's
// classification: its own last_seen_at, its accepted config (to derive
// A23's longest-configured-poll-interval threshold, same as
// FleetBoardHealthRow), and OldestSensorSeen -- the least-recently-seen
// sensor on this board, or nil if the board has no sensors. Retired boards
// never appear here (landingBoardSignalsQuery excludes them).
//
// Design decision (recorded here, and in the PR, since it goes one step
// past the issue's literal text): FR62's text names retired-board exclusion
// only for condition 3 ("household-wide classification"), but this query
// excludes retired boards from condition 2 (AnyBoardWhollySilent) as well,
// not just condition 3 (HouseholdWhollySilent) -- both are derived from
// this same row set, so there is no separate "board silent" query to
// exclude them from selectively. This matches FR22.4/FR79's own precedent
// (reportingStateFor: a retired board is never classified NOT_REPORTING,
// "excluded from its offline counts") -- a board an operator intentionally
// retired should not trigger "one of your boards has stopped reporting"
// any more than it should trigger FR79's fleet-wide NOT_REPORTING tally.
type LandingBoardSignalRow struct {
	BoardID            int64
	LastSeenAt         time.Time
	AcceptedConfigJSON []byte
	OldestSensorSeen   *time.Time
}

// landingBoardSignalsQuery is NFR3.1's one bounded, household-scoped query:
// one row per non-retired board, with the board's own last_seen_at, its
// accepted config (LEFT JOIN LATERAL, same shape as fleetBoardHealthQuery's
// accepted_cfg) and the MIN of its sensors' last_seen_at (LEFT JOIN LATERAL
// aggregate -- one row summarizing every sensor on the board, never one row
// per sensor). Row count scales with the household's board count, but this
// is always exactly one query no matter how many boards or sensors that is
// -- see LandingBoardSignals's doc comment.
const landingBoardSignalsQuery = `
	SELECT
		b.board_id,
		b.last_seen_at,
		accepted_cfg.config_json,
		sensor_agg.oldest_sensor_seen
	FROM board b
	LEFT JOIN LATERAL (
		SELECT config_json
		FROM device_config
		WHERE board_id = b.board_id AND accepted = TRUE
		ORDER BY version DESC
		LIMIT 1
	) accepted_cfg ON TRUE
	LEFT JOIN LATERAL (
		SELECT MIN(last_seen_at) AS oldest_sensor_seen
		FROM sensor
		WHERE board_id = b.board_id
	) sensor_agg ON TRUE
	WHERE b.household_id = $1
	  AND b.retired_at IS NULL
`

// LandingBoardSignals returns householdID's non-retired boards' raw FR62
// signals in one query (landingBoardSignalsQuery) -- NFR3.1's "bounded,
// constant number of queries independent of the number of boards, sensors
// or plants in the household": this issues exactly one round trip whether
// the household has one board or fifty, and never loops per board to fetch
// a board's sensors.
func (r *Repository) LandingBoardSignals(ctx context.Context, householdID int64) ([]LandingBoardSignalRow, error) {
	rows, err := r.db.Query(ctx, landingBoardSignalsQuery, householdID)
	if err != nil {
		return nil, fmt.Errorf("landing board signals for household %d: %w", householdID, err)
	}
	defer rows.Close()

	var out []LandingBoardSignalRow
	for rows.Next() {
		var row LandingBoardSignalRow
		if err := rows.Scan(&row.BoardID, &row.LastSeenAt, &row.AcceptedConfigJSON, &row.OldestSensorSeen); err != nil {
			return nil, fmt.Errorf("scan landing board signal: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// landingSignalsForHousehold turns householdID's LandingBoardSignals rows
// into landing.Classify's Input, applying A23 via isBoardStale (server.go)
// per board -- the same staleness call reportingStateFor (FR79) uses, so
// FR62 and FR79 share one A23 authority (this task's Validation criterion).
//
// A household with zero non-retired boards (rows is empty) leaves every
// signal false -- per landing/classify.go's Input.HouseholdWhollySilent doc
// comment, that state is outside FR62's five conditions and Classify
// reports it as healthy, left to the caller's top-line wording.
//
// serviceDegraded is threaded straight through from the caller
// (isServiceDegraded) -- FR62 condition 4 is the identical GetHealth signal
// (FR63), never re-probed here.
func landingSignalsForHousehold(rows []LandingBoardSignalRow, serviceDegraded bool, now time.Time) landing.Input {
	in := landing.Input{HasHousehold: true, ServiceDegraded: serviceDegraded}
	if len(rows) == 0 {
		return in
	}

	householdWhollySilent := true
	for _, row := range rows {
		boardStale := isBoardStale(row.LastSeenAt, now, row.AcceptedConfigJSON)

		if boardStale {
			in.AnyBoardWhollySilent = true
		} else {
			householdWhollySilent = false
			if row.OldestSensorSeen != nil && isBoardStale(*row.OldestSensorSeen, now, row.AcceptedConfigJSON) {
				in.AnySensorSilentWhileBoardReports = true
			}
		}
	}
	in.HouseholdWhollySilent = householdWhollySilent

	return in
}

// Condition 5's two named next steps (FR62: "carrying a named next step
// (claim a board you have, per FR76; or ask the person who set it up to
// add you, per FR75). Never a blank page."). label/path are server-named
// (NFR18.1) -- the BFF renders them as working links without choosing its
// own text or destination. leaflab/ui has not yet built FR75/FR76's screens
// (both are backend-only tasks as of #1350); these paths are the
// server-named routes those screens are expected to expose, recorded here
// so the BFF has a stable contract to link against rather than inventing
// its own when it does.
const (
	landingClaimLabel  = "Claim a board you have"
	landingClaimPath   = "/claim"
	landingInviteLabel = "Ask to be invited to a household"
	landingInvitePath  = "/invite"
)

// toLandingConditionProto maps landing.Condition to api.proto's
// LandingCondition. Both enums are declared in the same order (Scaffold),
// but this is an explicit switch, not a numeric cast -- so a future
// reordering of either enum fails to compile instead of silently
// mismatching.
func toLandingConditionProto(c landing.Condition) pb.LandingCondition {
	switch c {
	case landing.ConditionHealthy:
		return pb.LandingCondition_LANDING_CONDITION_HEALTHY
	case landing.ConditionSensorSilent:
		return pb.LandingCondition_LANDING_CONDITION_SENSOR_SILENT
	case landing.ConditionBoardSilent:
		return pb.LandingCondition_LANDING_CONDITION_BOARD_SILENT
	case landing.ConditionHouseholdSilent:
		return pb.LandingCondition_LANDING_CONDITION_HOUSEHOLD_SILENT
	case landing.ConditionServiceDegraded:
		return pb.LandingCondition_LANDING_CONDITION_SERVICE_DEGRADED
	case landing.ConditionNoHousehold:
		return pb.LandingCondition_LANDING_CONDITION_NO_HOUSEHOLD
	default:
		return pb.LandingCondition_LANDING_CONDITION_UNSPECIFIED
	}
}

// toLandingNextStepProto fills in label/path for one landing.NextStep
// (Action alone -- Classify has no knowledge of the BFF's routes; see
// NextStep's doc comment in classify.go).
func toLandingNextStepProto(step landing.NextStep) *pb.LandingNextStep {
	switch step.Action {
	case landing.NextStepActionClaimBoard:
		return &pb.LandingNextStep{
			Action: pb.LandingNextStepAction_LANDING_NEXT_STEP_ACTION_CLAIM_BOARD,
			Label:  landingClaimLabel,
			Path:   landingClaimPath,
		}
	case landing.NextStepActionRequestInvite:
		return &pb.LandingNextStep{
			Action: pb.LandingNextStepAction_LANDING_NEXT_STEP_ACTION_REQUEST_INVITE,
			Label:  landingInviteLabel,
			Path:   landingInvitePath,
		}
	default:
		return &pb.LandingNextStep{Action: pb.LandingNextStepAction_LANDING_NEXT_STEP_ACTION_UNSPECIFIED}
	}
}

// landingTopLine is FR62's device-scoped top-line sentence, rendered for
// every condition including the five fault conditions ("everything is
// reporting" when healthy) -- "never worded as a verdict on the plants".
// Deliberately the single top-line text for every non-healthy condition
// too: the fault-specific wording already lives in Sentence
// (toLandingResponse's sentence_key), so top_line never duplicates or
// contradicts it.
const (
	landingTopLineHealthy    = "Everything is reporting."
	landingTopLineNotHealthy = "Not everything is reporting."
)

// toLandingResponse converts result (leaflab/api/landing.Classify's output)
// into the wire response. NFR18.1: every field here is already the final
// worded text or a server-chosen enum value -- the BFF renders this
// verbatim and classifies nothing itself.
func toLandingResponse(result landing.Result, now time.Time) *pb.GetHouseholdLandingResponse {
	resp := &pb.GetHouseholdLandingResponse{
		Condition:   toLandingConditionProto(result.Condition),
		SentenceKey: result.Sentence,
		TopLine:     landingTopLineHealthy,
		ServerNow:   contract.ToInstant(now),
	}
	if result.Condition != landing.ConditionHealthy {
		resp.TopLine = landingTopLineNotHealthy
	}
	for _, step := range result.NextSteps {
		resp.NextSteps = append(resp.NextSteps, toLandingNextStepProto(step))
	}
	return resp
}
