package main

// Fast, no-DB unit coverage for landing.go's glue between
// leaflab/api/landing.Classify (pure, tested in that package) and this
// package's Repository/proto layer: landingSignalsForHousehold's boolean
// derivation from LandingBoardSignals rows (retired-board exclusion itself
// is proved with a real Postgres fixture in landing_integration_test.go --
// this file only proves what landingSignalsForHousehold does with rows it
// is handed, which the SQL query has *already* filtered), and the
// Classify-Result -> wire-response mapping (toLandingResponse and its
// helpers).

import (
	"regexp"
	"testing"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/landing"
)

// -- landingSignalsForHousehold -----------------------------------------------

func TestLandingSignalsForHousehold_NoBoards_AllSignalsFalse(t *testing.T) {
	now := time.Now()
	in := landingSignalsForHousehold(nil, false, now)
	if !in.HasHousehold {
		t.Error("HasHousehold = false, want true (caller already resolved a household)")
	}
	if in.HouseholdWhollySilent || in.AnyBoardWhollySilent || in.AnySensorSilentWhileBoardReports {
		t.Errorf("a household with zero non-retired boards must leave every silence signal false, got %+v", in)
	}
}

func TestLandingSignalsForHousehold_ServiceDegraded_ThreadedThroughUnconditionally(t *testing.T) {
	now := time.Now()
	for _, degraded := range []bool{true, false} {
		in := landingSignalsForHousehold(nil, degraded, now)
		if in.ServiceDegraded != degraded {
			t.Errorf("landingSignalsForHousehold(nil, %v, now).ServiceDegraded = %v, want %v", degraded, in.ServiceDegraded, degraded)
		}
	}
}

func TestLandingSignalsForHousehold_OneStaleBoard_WhollySilent(t *testing.T) {
	now := time.Now()
	cfg := fleetConfigJSON(t, 60000) // 60s configured poll -> A23 floor of 15 minutes
	rows := []LandingBoardSignalRow{
		{BoardID: 1, LastSeenAt: now.Add(-1 * time.Hour), AcceptedConfigJSON: cfg},
	}
	in := landingSignalsForHousehold(rows, false, now)
	if !in.AnyBoardWhollySilent {
		t.Error("AnyBoardWhollySilent = false, want true for a board stale by more than an hour")
	}
	if !in.HouseholdWhollySilent {
		t.Error("HouseholdWhollySilent = false, want true -- the household's only board is stale")
	}
	if in.AnySensorSilentWhileBoardReports {
		t.Error("AnySensorSilentWhileBoardReports = true, want false -- the board itself is stale, not just a sensor on a reporting board")
	}
}

func TestLandingSignalsForHousehold_OneStaleOneFresh_BoardSilentButNotHouseholdWide(t *testing.T) {
	now := time.Now()
	cfg := fleetConfigJSON(t, 60000)
	rows := []LandingBoardSignalRow{
		{BoardID: 1, LastSeenAt: now.Add(-1 * time.Hour), AcceptedConfigJSON: cfg},
		{BoardID: 2, LastSeenAt: now, AcceptedConfigJSON: cfg},
	}
	in := landingSignalsForHousehold(rows, false, now)
	if !in.AnyBoardWhollySilent {
		t.Error("AnyBoardWhollySilent = false, want true -- board 1 is stale")
	}
	if in.HouseholdWhollySilent {
		t.Error("HouseholdWhollySilent = true, want false -- board 2 is still reporting")
	}
}

func TestLandingSignalsForHousehold_BoardFreshSensorStale_SensorSilentOnly(t *testing.T) {
	now := time.Now()
	cfg := fleetConfigJSON(t, 60000)
	staleSensor := now.Add(-1 * time.Hour)
	rows := []LandingBoardSignalRow{
		{BoardID: 1, LastSeenAt: now, AcceptedConfigJSON: cfg, OldestSensorSeen: &staleSensor},
	}
	in := landingSignalsForHousehold(rows, false, now)
	if in.AnyBoardWhollySilent {
		t.Error("AnyBoardWhollySilent = true, want false -- the board itself is still reporting")
	}
	if in.HouseholdWhollySilent {
		t.Error("HouseholdWhollySilent = true, want false -- the board itself is still reporting")
	}
	if !in.AnySensorSilentWhileBoardReports {
		t.Error("AnySensorSilentWhileBoardReports = false, want true -- FR62 condition 1: one sensor silent while its board still reports")
	}
}

func TestLandingSignalsForHousehold_AllFresh_Healthy(t *testing.T) {
	now := time.Now()
	cfg := fleetConfigJSON(t, 60000)
	freshSensor := now
	rows := []LandingBoardSignalRow{
		{BoardID: 1, LastSeenAt: now, AcceptedConfigJSON: cfg, OldestSensorSeen: &freshSensor},
	}
	in := landingSignalsForHousehold(rows, false, now)
	if in.HouseholdWhollySilent || in.AnyBoardWhollySilent || in.AnySensorSilentWhileBoardReports {
		t.Errorf("an all-fresh household must leave every silence signal false, got %+v", in)
	}
	if got := landing.Classify(in).Condition; got != landing.ConditionHealthy {
		t.Errorf("Classify(%+v).Condition = %v, want ConditionHealthy", in, got)
	}
}

// -- toLandingResponse and its helpers ---------------------------------------

func TestToLandingResponse_Healthy_TopLineAndNoNextSteps(t *testing.T) {
	now := time.Now()
	resp := toLandingResponse(landing.Result{Condition: landing.ConditionHealthy}, now)
	if resp.GetTopLine() != landingTopLineHealthy {
		t.Errorf("TopLine = %q, want %q", resp.GetTopLine(), landingTopLineHealthy)
	}
	if resp.GetCondition() != pb.LandingCondition_LANDING_CONDITION_HEALTHY {
		t.Errorf("Condition = %v, want LANDING_CONDITION_HEALTHY", resp.GetCondition())
	}
	if len(resp.GetNextSteps()) != 0 {
		t.Errorf("NextSteps = %v, want none for the healthy condition", resp.GetNextSteps())
	}
}

func TestToLandingResponse_NoHousehold_NextStepsHaveWorkingLinks(t *testing.T) {
	now := time.Now()
	result := landing.Classify(landing.Input{HasHousehold: false})
	resp := toLandingResponse(result, now)

	if resp.GetTopLine() != landingTopLineNotHealthy {
		t.Errorf("TopLine = %q, want %q (device-scoped, non-healthy)", resp.GetTopLine(), landingTopLineNotHealthy)
	}
	if resp.GetCondition() != pb.LandingCondition_LANDING_CONDITION_NO_HOUSEHOLD {
		t.Fatalf("Condition = %v, want LANDING_CONDITION_NO_HOUSEHOLD", resp.GetCondition())
	}
	if resp.GetSentenceKey() == "" {
		t.Error("SentenceKey is empty -- FR62: never a blank page")
	}
	steps := resp.GetNextSteps()
	if len(steps) != 2 {
		t.Fatalf("got %d next steps, want 2 (claim + invite)", len(steps))
	}
	var sawClaim, sawInvite bool
	for _, step := range steps {
		if step.GetLabel() == "" || step.GetPath() == "" {
			t.Errorf("next step %+v has an empty label or path -- must be a working link, never a blank page", step)
		}
		switch step.GetAction() {
		case pb.LandingNextStepAction_LANDING_NEXT_STEP_ACTION_CLAIM_BOARD:
			sawClaim = true
			if step.GetPath() != landingClaimPath {
				t.Errorf("claim next step Path = %q, want %q", step.GetPath(), landingClaimPath)
			}
		case pb.LandingNextStepAction_LANDING_NEXT_STEP_ACTION_REQUEST_INVITE:
			sawInvite = true
			if step.GetPath() != landingInvitePath {
				t.Errorf("invite next step Path = %q, want %q", step.GetPath(), landingInvitePath)
			}
		}
	}
	if !sawClaim {
		t.Error("missing the claim-a-board next step (FR76)")
	}
	if !sawInvite {
		t.Error("missing the request-an-invite next step (FR75)")
	}
}

func TestToLandingConditionProto_EveryConditionMaps(t *testing.T) {
	cases := map[landing.Condition]pb.LandingCondition{
		landing.ConditionHealthy:         pb.LandingCondition_LANDING_CONDITION_HEALTHY,
		landing.ConditionSensorSilent:    pb.LandingCondition_LANDING_CONDITION_SENSOR_SILENT,
		landing.ConditionBoardSilent:     pb.LandingCondition_LANDING_CONDITION_BOARD_SILENT,
		landing.ConditionHouseholdSilent: pb.LandingCondition_LANDING_CONDITION_HOUSEHOLD_SILENT,
		landing.ConditionServiceDegraded: pb.LandingCondition_LANDING_CONDITION_SERVICE_DEGRADED,
		landing.ConditionNoHousehold:     pb.LandingCondition_LANDING_CONDITION_NO_HOUSEHOLD,
	}
	for in, want := range cases {
		if got := toLandingConditionProto(in); got != want {
			t.Errorf("toLandingConditionProto(%v) = %v, want %v", in, got, want)
		}
	}
}

// nfr62PlantVocabularyInAPIPackage mirrors leaflab/api/landing's own
// word-boundary-safe plant-vocabulary check (that package's
// nfr62PlantVocabulary is unexported and this package cannot import a test
// helper across package boundaries) -- applied here to the two top-line
// strings landing.go declares, which leaflab/api/landing itself has no
// knowledge of.
var nfr62PlantVocabularyInAPIPackage = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bplants?\b`),
	regexp.MustCompile(`(?i)\bleaves\b`),
	regexp.MustCompile(`(?i)\bsoil\b`),
	regexp.MustCompile(`(?i)\bmoisture\b`),
	regexp.MustCompile(`(?i)\bwater(ing)?\b`),
	regexp.MustCompile(`(?i)\bwilt(ed|ing)?\b`),
	regexp.MustCompile(`(?i)\bgrow(th|ing)?\b`),
	regexp.MustCompile(`(?i)\broots?\b`),
}

// TestLandingTopLines_NoPlantVocabulary is classify_test.go's forward
// reference: FR62's "the top-line sentence is device-scoped ... and is
// never worded as a verdict on the plants", checked against both top-line
// strings landing.go declares (leaflab/api/landing.Classify has no
// knowledge of these -- they're rendered here, not by Classify).
func TestLandingTopLines_NoPlantVocabulary(t *testing.T) {
	for name, line := range map[string]string{
		"landingTopLineHealthy":    landingTopLineHealthy,
		"landingTopLineNotHealthy": landingTopLineNotHealthy,
	} {
		for _, re := range nfr62PlantVocabularyInAPIPackage {
			if re.MatchString(line) {
				t.Errorf("%s (%q) matches forbidden plant-vocabulary pattern %s", name, line, re.String())
			}
		}
	}
}
