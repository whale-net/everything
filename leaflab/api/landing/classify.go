// Package landing implements FR62's server-side, five-condition household
// landing classification: the single decision function every consumer of
// GetHouseholdLanding (leaflab/api/proto's api.proto) routes through,
// mirroring leaflab/api/health's role as the one A23 authority. The BFF
// (leaflab/ui) never classifies -- it renders whatever this package (via
// the RPC handler, Implementation-phase work) already decided (NFR18.1).
//
// Classify itself is a pure function over pre-computed boolean signals: it
// owns the *priority ordering* and the *sentence text* FR62 requires, not
// the SQL that derives those signals. Gathering the signals under NFR3.1's
// bounded-query constraint (a household-wide aggregate query, not a
// per-board or per-sensor loop) is the RPC handler's job, added at
// Implementation -- see api.proto's GetHouseholdLanding doc comment and
// this task's issue body ("Bounded queries" under Implementation).
package landing

// Condition is FR62's classification result: exactly one of five fault
// conditions, or ConditionHealthy when none applies. Mirrors api.proto's
// LandingCondition enum field-for-field; the RPC handler (Implementation)
// converts between the two rather than this package importing the
// generated pb type, so this package stays usable without a grpc/protobuf
// dependency (the same shape libs/go/health-style pure packages in this
// repo favor).
type Condition int

const (
	ConditionUnspecified Condition = iota
	// ConditionHealthy is the nominal case: no fault condition below
	// applies. Only TopLine is meaningful; Sentence and NextSteps are empty.
	ConditionHealthy
	// ConditionSensorSilent is FR62 condition 1: one sensor silent while its
	// board still reports.
	ConditionSensorSilent
	// ConditionBoardSilent is FR62 condition 2: the whole board silent.
	ConditionBoardSilent
	// ConditionHouseholdSilent is FR62 condition 3: every board in the
	// household silent at once. Retired boards (FR22.4) are excluded from
	// the signal this condition is derived from -- see Input.
	ConditionHouseholdSilent
	// ConditionServiceDegraded is FR62 condition 4: the service itself
	// degraded -- the same DEGRADED signal GetHealth reports (FR63), not a
	// second copy of that probe.
	ConditionServiceDegraded
	// ConditionNoHousehold is FR62 condition 5: nothing is linked to the
	// caller's account yet. Always carries NextSteps -- never a blank page.
	ConditionNoHousehold
)

// NextStepAction names FR62 condition 5's two possible named next steps --
// never a third, unnamed option. Mirrors api.proto's LandingNextStepAction.
type NextStepAction int

const (
	NextStepActionUnspecified NextStepAction = iota
	// NextStepActionClaimBoard: claim a board the caller already has in
	// hand (FR76).
	NextStepActionClaimBoard
	// NextStepActionRequestInvite: ask the person who set up an existing
	// household to add the caller to it (FR75).
	NextStepActionRequestInvite
)

// NextStep is one of condition 5's named next steps. Label/Path are filled
// in by the RPC handler at Implementation (server-named, per NFR18.1 -- the
// BFF renders Path as a working link without choosing it); Classify itself
// only decides Action, since it has no knowledge of the BFF's routes.
type NextStep struct {
	Action NextStepAction
}

// Input carries the raw signals Classify decides between. Every field is a
// boolean derived elsewhere (Implementation-phase repository/RPC-handler
// work) from a single bounded, household-wide query -- Classify never
// queries anything itself and never loops per board or per sensor; it is
// pure decision logic over signals its caller already computed.
//
// Priority is evaluated in the field order documented on Classify below,
// not the order fields are declared here -- more than one signal can be
// true at once (e.g. a household can be wholly silent AND the service can
// be degraded); Classify picks the single most-encompassing condition.
type Input struct {
	// HasHousehold is false only for a principal with no current household
	// membership anywhere (FR62 condition 5's trigger). When false, every
	// other field below is meaningless and ignored.
	HasHousehold bool

	// ServiceDegraded mirrors GetHealth's HEALTH_DEGRADED signal (FR63) --
	// the same probe result, never re-derived here (FR62: "the service
	// itself degraded").
	ServiceDegraded bool

	// HouseholdWhollySilent is true when every non-retired board in the
	// household is A23-stale (leaflab/api/health.IsStale) as of now.
	// Retired boards (FR22.4) are excluded from this signal by whoever
	// computes it -- Classify trusts that exclusion already happened and
	// does not re-check it. Meaningless (should be false) when the
	// household has no non-retired boards at all -- that state is outside
	// FR62's five conditions and is left to the caller's top-line wording,
	// not this classifier.
	HouseholdWhollySilent bool

	// AnyBoardWhollySilent is true when at least one non-retired board is
	// A23-stale, independent of HouseholdWhollySilent (a caller may set
	// both true; Classify's priority order resolves which condition wins).
	AnyBoardWhollySilent bool

	// AnySensorSilentWhileBoardReports is true when at least one sensor's
	// most recent reading is A23-stale while its own board's last_seen_at
	// is not -- FR62 condition 1's exact phrasing ("one sensor silent while
	// its board still reports").
	AnySensorSilentWhileBoardReports bool
}

// Result is Classify's output: the single Condition that won, its
// authoritative Sentence text (empty for ConditionHealthy -- TopLine alone
// covers that case), and NextSteps (populated only for
// ConditionNoHousehold).
type Result struct {
	Condition Condition
	// Sentence is the complete, server-worded sentence FR62 requires for
	// Condition -- "worded differently ... a complete sentence", never a
	// verdict on the plants. Empty when Condition is ConditionHealthy.
	Sentence string
	// NextSteps is non-empty only for ConditionNoHousehold: both named next
	// steps (claim, FR76; request an invite, FR75) together, never one at
	// the exclusion of the other -- see api.proto's LandingNextStep doc
	// comment.
	NextSteps []NextStep
}

// sentenceNoHousehold, sentenceServiceDegraded, sentenceHouseholdSilent,
// sentenceBoardSilent and sentenceSensorSilent are FR62's five canned
// sentences: each a complete sentence, each worded differently from every
// other (Testing: "assert all five sentence texts are distinct"), and none
// naming a plant, a reading value or a plant-health verdict -- these are
// device-scoped statements about reporting, not about the plants those
// devices monitor (Testing: "no sentence contains plant vocabulary").
const (
	sentenceNoHousehold     = "Nothing is linked to your account yet."
	sentenceServiceDegraded = "LeafLab is having trouble on our end right now."
	sentenceHouseholdSilent = "Every board in your household has stopped reporting."
	sentenceBoardSilent     = "One of your boards has stopped reporting."
	sentenceSensorSilent    = "One of your sensors has stopped reporting, though its board is still checking in."
)

// Classify decides FR62's classification from in, evaluating conditions in
// this fixed priority order -- most encompassing first, first match wins,
// so a household that is simultaneously e.g. wholly silent and running a
// degraded service is reported as degraded (the caller's own problem, not
// theirs, takes precedence over a symptom that degraded service could
// itself be causing):
//
//  1. ConditionNoHousehold     (in.HasHousehold == false)
//  2. ConditionServiceDegraded (in.ServiceDegraded)
//  3. ConditionHouseholdSilent (in.HouseholdWhollySilent)
//  4. ConditionBoardSilent     (in.AnyBoardWhollySilent)
//  5. ConditionSensorSilent    (in.AnySensorSilentWhileBoardReports)
//  6. ConditionHealthy         (none of the above)
//
// This ordering, not the Input struct's field order, is the priority FR62's
// five conditions are checked in -- do not re-derive it at a call site.
func Classify(in Input) Result {
	if !in.HasHousehold {
		return Result{
			Condition: ConditionNoHousehold,
			Sentence:  sentenceNoHousehold,
			NextSteps: []NextStep{
				{Action: NextStepActionClaimBoard},
				{Action: NextStepActionRequestInvite},
			},
		}
	}

	if in.ServiceDegraded {
		return Result{Condition: ConditionServiceDegraded, Sentence: sentenceServiceDegraded}
	}

	if in.HouseholdWhollySilent {
		return Result{Condition: ConditionHouseholdSilent, Sentence: sentenceHouseholdSilent}
	}

	if in.AnyBoardWhollySilent {
		return Result{Condition: ConditionBoardSilent, Sentence: sentenceBoardSilent}
	}

	if in.AnySensorSilentWhileBoardReports {
		return Result{Condition: ConditionSensorSilent, Sentence: sentenceSensorSilent}
	}

	return Result{Condition: ConditionHealthy}
}
