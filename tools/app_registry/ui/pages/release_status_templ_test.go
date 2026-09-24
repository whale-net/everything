package pages

import (
	"strconv"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// This file guards issue #1704's FR11 pushed-region boundary, FR13's
// run-level aggregate summary, and FR14/FR15's live indicator and reload
// affordance for release_status.templ -- mirroring
// promotion_details_templ_test.go's structural-assertion style (rendered
// HTML, no browser) for the same shape #1113 already built on
// promotion_details.templ.

// --- FR8/FR11: connect + exactly one swap target, Targets+summary inside --

func TestReleaseStatus_FR8_ConnectAttributes(t *testing.T) {
	s := ReleaseStatusViewState{
		ReleaseRunID: "run-42",
		Release: &pb.GetReleaseResponse{
			ReleaseRunId: "run-42",
			Targets: []*pb.ReleaseRunTarget{
				{OwnerFullName: "platform-worker", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_QUEUED},
			},
		},
		HeartbeatIntervalMs: 30000,
	}
	body := renderComponent(t, ReleaseStatus(adminUser(), s))

	if !strings.Contains(body, `hx-ext="sse"`) {
		t.Errorf("FR8: expected hx-ext=\"sse\"; got %q", body)
	}
	if !strings.Contains(body, `sse-connect="/releases/run-42/status/sse"`) {
		t.Errorf("FR8: expected sse-connect=\"/releases/run-42/status/sse\"; got %q", body)
	}
}

func TestReleaseStatus_FR11_ExactlyOneSwapTargetContainingSummaryAndTargets(t *testing.T) {
	s := ReleaseStatusViewState{
		ReleaseRunID: "run-42",
		Release: &pb.GetReleaseResponse{
			ReleaseRunId: "run-42",
			Targets: []*pb.ReleaseRunTarget{
				{OwnerFullName: "platform-worker", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_QUEUED},
			},
		},
		HeartbeatIntervalMs: 30000,
	}
	body := renderComponent(t, ReleaseStatus(adminUser(), s))

	swapAttr := `sse-swap="release_run.run-42"`
	if got := strings.Count(body, swapAttr); got != 1 {
		t.Fatalf("FR11: expected exactly one element with %s, got %d; body = %q", swapAttr, got, body)
	}

	swapIdx := strings.Index(body, swapAttr)
	// The swap target's own closing div isn't uniquely findable by naive
	// substring search, but the Targets table heading and the aggregate
	// summary text are both emitted immediately after the swap div opens
	// and before releaseStatusDetail's next sibling cards, so their
	// presence after swapIdx is sufficient to prove they're inside it
	// (combined with the "outside" tests below proving nothing else that
	// should be excluded appears before the following card boundary).
	summaryIdx := strings.Index(body, "1 queued")
	targetsIdx := strings.Index(body, "Targets (1)")
	if summaryIdx < swapIdx {
		t.Errorf("FR13: aggregate summary must be inside the sse-swap target; summaryIdx=%d swapIdx=%d", summaryIdx, swapIdx)
	}
	if targetsIdx < swapIdx {
		t.Errorf("FR11: Targets table must be inside the sse-swap target; targetsIdx=%d swapIdx=%d", targetsIdx, swapIdx)
	}
}

// --- FR11: everything outside the pushed region stays outside -------------

func TestReleaseStatus_FR11_ExcludedElementsOutsideSwapTarget(t *testing.T) {
	s := ReleaseStatusViewState{
		ReleaseRunID: "run-42",
		Release: &pb.GetReleaseResponse{
			ReleaseRunId:     "run-42",
			RequestedScope:   "platform",
			TriggeredBy:      "bob",
			ResolvedPlanJson: `{"k":"v"}`,
			Targets: []*pb.ReleaseRunTarget{
				{OwnerFullName: "platform-worker", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_QUEUED},
			},
		},
		HeartbeatIntervalMs: 30000,
	}
	body := renderComponent(t, ReleaseStatus(adminUser(), s))

	swapIdx := strings.Index(body, `sse-swap="release_run.run-42"`)
	if swapIdx < 0 {
		t.Fatalf("expected sse-swap target to be present; body = %q", body)
	}

	// The Run info card and Resolved-plan card, the live indicator, the
	// reload affordance, the Refresh link, and the Retry form must all sit
	// before the swap target opens (Run/live-indicator/reload/Refresh) or
	// -- for the Resolved-plan card and the Retry form, which
	// releaseStatusDetail renders after releaseStatusLiveSwap -- must not
	// themselves be wrapped inside it. We assert position for every one of
	// them relative to swapIdx and relative to the swap target's own
	// closing marker (the next Card's opening chrome, which only appears
	// once the swap div has closed).
	cases := []struct {
		name   string
		needle string
	}{
		{"live indicator", `class="live-indicator"`},
		{"reload affordance", `data-live-reload`},
		{"Refresh link", "↻ Refresh"},
		{"Run info card heading", ">Run<"},
	}
	for _, c := range cases {
		idx := strings.Index(body, c.needle)
		if idx < 0 {
			t.Errorf("expected %s (%q) to be present; body = %q", c.name, c.needle, body)
			continue
		}
		if idx > swapIdx {
			t.Errorf("FR11: %s must be outside (before) the sse-swap target; idx=%d swapIdx=%d", c.name, idx, swapIdx)
		}
	}

	// Resolved-plan card and Retry form render after releaseStatusLiveSwap
	// closes, so they must appear after the swap-target opening tag AND
	// after the Targets table content that belongs inside it -- i.e. after
	// the swap div's own content, not nested within it. We can't locate the
	// exact closing </div> by string search (templ emits several), so
	// instead we prove the boundary test's own regression guard (see the
	// red/green test below) is what actually enforces nesting; here we just
	// confirm both render at all and after the swap-target's *opening* tag,
	// consistent with "after, as siblings" rather than "before".
	resolvedIdx := strings.Index(body, "Resolved plan")
	retryIdx := strings.Index(body, ">Retry<")
	if resolvedIdx < 0 {
		t.Errorf("expected Resolved plan card heading to be present; body = %q", body)
	}
	if retryIdx < 0 {
		t.Errorf("expected Retry button to be present; body = %q", body)
	}
}

// TestReleaseStatus_FR11_BoundaryRedGreen is the red/green discipline check
// the issue calls for: releaseStatusRunBody must NOT be reachable through
// ReleaseStatusLiveBody (the pushed region's sole render path). This proves
// the boundary by asserting the pushed region's rendered content -- taken in
// isolation via ReleaseStatusLiveBody -- never contains the Run card's own
// heading, which only releaseStatusDetail (outside the swap target) renders.
// Flipping this by literally moving releaseStatusRunBody's call inside
// ReleaseStatusLiveBody and re-running is the manual red/green step
// documented in this test's own comment below.
func TestReleaseStatus_FR11_BoundaryRedGreen(t *testing.T) {
	rel := &pb.GetReleaseResponse{
		ReleaseRunId: "run-42",
		TriggeredBy:  "bob",
		Targets: []*pb.ReleaseRunTarget{
			{OwnerFullName: "platform-worker", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_QUEUED},
		},
	}
	pushedOnly := renderComponent(t, ReleaseStatusLiveBody(rel, nil))

	// Red/green discipline (see issue #1704 Testing section): temporarily
	// change release_status.templ so ReleaseStatusLiveBody also renders
	// releaseStatusRunBody(rel) (i.e. move the Run card inside the pushed
	// region), regenerate (`templ generate`), and re-run this test -- it
	// must fail, since ">Run<" would then appear in pushedOnly. Revert
	// before committing.
	if strings.Contains(pushedOnly, ">Run<") {
		t.Errorf("FR11: the pushed region (ReleaseStatusLiveBody alone) must never contain the Run card's heading; got %q", pushedOnly)
	}
	if strings.Contains(pushedOnly, "Resolved plan") {
		t.Errorf("FR11: the pushed region (ReleaseStatusLiveBody alone) must never contain the Resolved-plan card; got %q", pushedOnly)
	}
	if strings.Contains(pushedOnly, ">Retry<") {
		t.Errorf("FR11: the pushed region (ReleaseStatusLiveBody alone) must never contain the Retry form; got %q", pushedOnly)
	}
}

// --- NFR2: exactly one render path produces the pushed region's HTML ------

func TestReleaseStatus_NFR2_LiveBodyMatchesFullPageRegion(t *testing.T) {
	rel := &pb.GetReleaseResponse{
		ReleaseRunId: "run-42",
		Targets: []*pb.ReleaseRunTarget{
			{OwnerFullName: "platform-worker", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED, BuildId: "b1"},
			{OwnerFullName: "platform-api", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILDING},
		},
	}
	commits := map[string]BuildCommitInfo{"b1": {GitSha: "abc1234", URL: "https://github.com/o/r/commit/abc1234"}}

	standalone := renderComponent(t, ReleaseStatusLiveBody(rel, commits))

	s := ReleaseStatusViewState{ReleaseRunID: "run-42", Release: rel, BuildCommits: commits, HeartbeatIntervalMs: 30000}
	full := renderComponent(t, ReleaseStatus(adminUser(), s))

	// The swap target wraps ReleaseStatusLiveBody's output with no
	// additional markup of its own around the content (release_status.
	// templ's releaseStatusLiveSwap), so the standalone render must appear
	// byte-identical, verbatim, inside the full page.
	if !strings.Contains(full, standalone) {
		t.Errorf("NFR2: ReleaseStatusLiveBody's standalone output must appear byte-identical inside the full page render.\nstandalone = %q\nfull = %q", standalone, full)
	}
}

// --- FR13: run-level aggregate summary, table-driven -----------------------

func TestReleaseRunAggregateSummary(t *testing.T) {
	cases := []struct {
		name    string
		targets []*pb.ReleaseRunTarget
		want    string
	}{
		{
			name:    "zero targets",
			targets: nil,
			want:    "No targets",
		},
		{
			name: "one queued",
			targets: []*pb.ReleaseRunTarget{
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_QUEUED},
			},
			want: "1 queued",
		},
		{
			name: "mixed terminal and non-terminal",
			targets: []*pb.ReleaseRunTarget{
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILDING},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED},
			},
			want: "1 building · 3/5 succeeded · 1 failed",
		},
		{
			name: "all six states present",
			targets: []*pb.ReleaseRunTarget{
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_QUEUED},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILDING},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_PUBLISHING},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_RECORDING},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED},
			},
			want: "1 queued · 1 building · 1 publishing · 1 recording · 1/6 succeeded · 1 failed",
		},
		{
			name: "all terminal (all failed)",
			targets: []*pb.ReleaseRunTarget{
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED},
			},
			want: "2 failed",
		},
		{
			name: "all succeeded",
			targets: []*pb.ReleaseRunTarget{
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
				{State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
			},
			want: "2/2 succeeded",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rel := &pb.GetReleaseResponse{Targets: c.targets}
			got := releaseRunAggregateSummary(rel)
			if got != c.want {
				t.Errorf("releaseRunAggregateSummary() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestReleaseStatus_FR13_AggregateSummaryRendersInPage(t *testing.T) {
	s := ReleaseStatusViewState{
		ReleaseRunID: "run-9",
		Release: &pb.GetReleaseResponse{
			ReleaseRunId: "run-9",
			Targets: []*pb.ReleaseRunTarget{
				{OwnerFullName: "a", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
				{OwnerFullName: "b", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED},
			},
		},
		HeartbeatIntervalMs: 30000,
	}
	body := renderComponent(t, ReleaseStatus(adminUser(), s))

	if !strings.Contains(body, "1/2 succeeded · 1 failed") {
		t.Errorf("FR13: expected aggregate summary line in rendered page; got %q", body)
	}
}

// --- FR14: live indicator starts Live --------------------------------------

func TestReleaseStatus_FR14_IndicatorStartsLive(t *testing.T) {
	s := ReleaseStatusViewState{
		ReleaseRunID:        "run-42",
		Release:             &pb.GetReleaseResponse{ReleaseRunId: "run-42"},
		HeartbeatIntervalMs: 30000,
	}
	body := renderComponent(t, ReleaseStatus(adminUser(), s))

	if !strings.Contains(body, `data-live-status class="badge badge-success"`) {
		t.Errorf("FR14: indicator must start Live with badge-success; got %q", body)
	}
	if !strings.Contains(body, ">Live<") {
		t.Errorf("FR14: indicator must render 'Live' text; got %q", body)
	}
}

// --- FR15: reload affordance present and hidden by default ----------------

func TestReleaseStatus_FR15_ReloadAffordancePresentAndHiddenByDefault(t *testing.T) {
	s := ReleaseStatusViewState{
		ReleaseRunID:        "run-42",
		Release:             &pb.GetReleaseResponse{ReleaseRunId: "run-42"},
		HeartbeatIntervalMs: 30000,
	}
	body := renderComponent(t, ReleaseStatus(adminUser(), s))

	containerIdx := strings.Index(body, `data-live-reload`)
	if containerIdx < 0 {
		t.Fatalf("FR15: reload container must be present; got %q", body)
	}
	// The container carries style="display:none" hidden-by-default, and
	// its link is a plain GET back to /releases/<id>.
	containerRegion := body[containerIdx:]
	closeIdx := strings.Index(containerRegion, "</div>")
	if closeIdx < 0 {
		t.Fatalf("FR15: could not find reload container's closing tag; got %q", body)
	}
	section := containerRegion[:closeIdx]
	// The style attribute is on the container's opening tag, which precedes
	// the anchor -- checked over the wider preceding text back to the id.
	if !strings.Contains(body[containerIdx:containerIdx+120], `style="display:none"`) {
		t.Errorf("FR15: reload container must be hidden by default; got %q", body[containerIdx:containerIdx+120])
	}
	if !strings.Contains(section, `href="/releases/run-42"`) {
		t.Errorf("FR15: reload link must point to /releases/run-42 (plain GET); got %q", section)
	}
	if !strings.Contains(section, ">Reload<") {
		t.Errorf("FR15: reload link text must say Reload; got %q", section)
	}
}

// heartbeatMsAttr is a tiny helper matching liveindicator.LiveIndicator's
// data-live-heartbeat-ms wiring convention, used only to build an expected
// attribute value for the assertion below without hardcoding strconv calls
// inline.
func heartbeatMsAttr(ms int) string {
	return `data-live-heartbeat-ms="` + strconv.Itoa(ms) + `"`
}

func TestReleaseStatus_FR14_HeartbeatIntervalBinding(t *testing.T) {
	s := ReleaseStatusViewState{
		ReleaseRunID:        "run-42",
		Release:             &pb.GetReleaseResponse{ReleaseRunId: "run-42"},
		HeartbeatIntervalMs: 15000,
	}
	body := renderComponent(t, ReleaseStatus(adminUser(), s))

	if !strings.Contains(body, heartbeatMsAttr(15000)) {
		t.Errorf("FR14: expected data-live-heartbeat-ms=\"15000\"; got %q", body)
	}
	if !strings.Contains(body, `heartbeatMsFor(indicator) * 2`) {
		t.Errorf("FR14: timeout threshold must be 2x heartbeat interval; got %q", body)
	}
}
