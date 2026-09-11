package pages

import (
	"fmt"
	"strings"
	"testing"

	"github.com/whale-net/everything/manmanv2/events"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// This file guards #1726's browser-side SSE wiring for DeploymentRow's
// per-row sse-swap plumbing. Prior to task #2372 (M6 navigation/
// disposition, FR17) this file also guarded /sessions's own
// DeploymentsLiveRegion wrapper (hx-ext="sse" ancestor, Live/Not-Live
// indicator placement, LiveUpdatesEnabled=false degradation); that
// coverage retired along with pages/sessions.templ and
// components.DeploymentsLiveRegion when the list page itself retired --
// the equivalent SSE-ancestor/indicator contract for Activity's own
// fleet-wide live region is components.LiveRegion's own contract
// (libs/go/htmxui adjacent, exercised by pages/activity_test.go), not
// this file's. What remains here (tests 4-5 from the original numbering)
// is DeploymentRow-specific and still applies verbatim: the row itself is
// unchanged by the page split (pages/deployment_row.templ), still reached
// from Games (pages/games.templ) and the #1627/#1628 action/refresh
// endpoints.
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// hardcoding DeploymentRow's sse-swap attribute to
// fmt.Sprintf("deployment.%d", data.ServerGameConfigID) instead of calling
// events.TopicForDeployment made no test fail (the literal happens to
// match today's helper output) -- the real regression this guards against
// is TopicForDeployment's format ever changing without this markup
// following.

// --- 4. per-row sse-swap comes from the shared events helper, never a ------
//        locally formatted literal ------------------------------------------

func TestDeploymentRow_SSESwapMatchesEventsHelper(t *testing.T) {
	for _, sgcID := range []int64{7, 40, 12345} {
		latest := &manmanpb.Session{SessionId: sgcID, ServerGameConfigId: sgcID, Status: "running"}
		data := buildDeploymentRowData(sgcID, "Row", "active", latest, latest, "")
		body := deploymentRowMarkup(t, data)

		want := fmt.Sprintf(`sse-swap="%s"`, events.TopicForDeployment(sgcID))
		if !strings.Contains(body, want) {
			t.Errorf("sgc %d: expected %q (from events.TopicForDeployment), got body %q", sgcID, want, body)
		}
	}
}

// --- 5. the #1628 poll fragment path keeps carrying sse-swap ---------------
//
// handleDeploymentRowFragment (handlers_deployment_actions.go) renders
// this exact DeploymentRow component as an isolated outerHTML replacement
// for a polling row; assert that isolated render still carries sse-swap
// so a polled row stays a valid live-update target under the page's
// hx-ext="sse" ancestor (see DeploymentRow's doc comment).

func TestDeploymentRow_PollFragmentPathCarriesSSESwap(t *testing.T) {
	// A transient-status row is exactly what the #1628 poll refreshes
	// (deploymentRowPollAttrs only fires for transient statuses).
	latest := &manmanpb.Session{SessionId: 60, ServerGameConfigId: 33, Status: "starting"}
	data := buildDeploymentRowData(33, "Nu", "active", latest, nil, "")
	body := deploymentRowMarkup(t, data)

	want := fmt.Sprintf(`sse-swap="%s"`, events.TopicForDeployment(33))
	if !strings.Contains(body, want) {
		t.Errorf("expected the polled fragment's row to still carry %q, got body %q", want, body)
	}
	// Sanity: this is genuinely the poll path (hx-trigger present).
	if !strings.Contains(body, `hx-trigger="every 3s"`) {
		t.Errorf("expected this row to be on the transient poll path, got body %q", body)
	}
}
