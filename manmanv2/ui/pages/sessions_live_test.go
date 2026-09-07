package pages

import (
	"fmt"
	"strings"
	"testing"

	"github.com/whale-net/everything/manmanv2/events"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards #1726's browser-side SSE wiring for /sessions:
//
//  1. The hx-ext="sse" + sse-connect container (DeploymentsLiveRegion) must
//     be an ancestor of every per-row sse-swap target and never itself be a
//     swap target -- see that templ's doc comment and
//     libs/go/htmxsse/README.md's "Critical for adopters" note: an
//     sse-connect element that gets swapped re-attaches its EventSource
//     listener before a reconnect can carry Last-Event-ID, breaking NFR2's
//     reconnect-baseline suppression.
//  2. Each row's sse-swap value must come from the shared manmanv2/events
//     helper (events.TopicForDeployment), never a locally formatted
//     literal, so the producer (handlers_sessions_live.go), this markup,
//     and the DOM id can't drift independently.
//  3. The Live/Not-Live indicator and Reload affordance must render outside
//     every sse-swap element (siblings of the pushed region, not
//     descendants).
//  4. The #1628 poll fragment path (handleDeploymentRowFragment, which
//     renders the same DeploymentRow component as an isolated outerHTML
//     replacement) must keep carrying sse-swap so a polled row stays a
//     valid live-update target.
//  5. When the page started with no SSE hub (LiveUpdatesEnabled=false), no
//     SSE markup renders at all -- the page still renders its server-side
//     snapshot (FR1).
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// hardcoding DeploymentRow's sse-swap attribute to
// fmt.Sprintf("deployment.%d", data.ServerGameConfigID) instead of calling
// events.TopicForDeployment made no test fail (the literal happens to
// match today's helper output) -- the real regression this guards against
// is TopicForDeployment's format ever changing without this markup
// following; separately, moving the Live indicator div to render as the
// last child *inside* the { children... } slot (so it followed the table)
// made TestSessions_LiveIndicatorAndReloadOutsideSSESwap fail on the
// indicatorIdx < firstSwapIdx assertion; reverting restored green.

// findMatchingDivClose returns the index immediately after the closing
// </div> that balances the <div ...> opening tag starting at openIdx,
// scanning nested <div>/</div> pairs. Used to prove "ancestor of the rows"
// structurally, rather than merely "appears earlier in the string".
func findMatchingDivClose(t *testing.T, html string, openIdx int) int {
	t.Helper()
	depth := 0
	i := openIdx
	for i < len(html) {
		switch {
		case strings.HasPrefix(html[i:], "<div"):
			depth++
			i += len("<div")
		case strings.HasPrefix(html[i:], "</div>"):
			depth--
			i += len("</div>")
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	t.Fatalf("no matching </div> found for <div at index %d in %q", openIdx, html)
	return -1
}

func buildSessionsPageData(liveUpdatesEnabled bool) SessionsPageData {
	latest := &manmanpb.Session{SessionId: 100, ServerGameConfigId: 40, Status: "running"}
	rows := []DeploymentRowData{
		buildDeploymentRowData(40, "Omega", "active", latest, latest, ""),
	}
	return SessionsPageData{
		DeploymentRows:      rows,
		HeartbeatIntervalMs: 15000,
		LiveUpdatesEnabled:  liveUpdatesEnabled,
	}
}

func renderSessionsPage(t *testing.T, data SessionsPageData) string {
	t.Helper()
	return renderPage(t, Sessions(components.LayoutData{}, data))
}

// --- 1. sse-connect is an ancestor of the rows, never a sibling -------------

func TestSessions_SSEConnectIsAncestorOfRows(t *testing.T) {
	html := renderSessionsPage(t, buildSessionsPageData(true))

	connectCount := strings.Count(html, `sse-connect="/api/live/deployments"`)
	if connectCount != 1 {
		t.Fatalf("expected exactly one sse-connect=\"/api/live/deployments\" element, got %d in body %q", connectCount, html)
	}

	regionIDIdx := strings.Index(html, `id="deployments-live-region"`)
	if regionIDIdx < 0 {
		t.Fatalf("expected a #deployments-live-region container, got body %q", html)
	}
	divOpenIdx := strings.LastIndex(html[:regionIDIdx], "<div")
	if divOpenIdx < 0 {
		t.Fatalf("expected an enclosing <div for #deployments-live-region, got body %q", html)
	}
	divCloseIdx := findMatchingDivClose(t, html, divOpenIdx)

	rowIdx := strings.Index(html, `id="deployment-row-40"`)
	if rowIdx < 0 {
		t.Fatalf("expected deployment row 40 to render, got body %q", html)
	}
	if rowIdx <= divOpenIdx || rowIdx >= divCloseIdx {
		t.Errorf("expected deployment row (index %d) to be nested inside #deployments-live-region (span [%d, %d)), got body %q", rowIdx, divOpenIdx, divCloseIdx, html)
	}
}

// --- 2. no SSE markup at all when live updates are disabled ----------------

func TestSessions_NoSSEMarkupWhenLiveUpdatesDisabled(t *testing.T) {
	html := renderSessionsPage(t, buildSessionsPageData(false))

	if strings.Contains(html, "sse-connect") {
		t.Errorf("expected no sse-connect element when LiveUpdatesEnabled is false, got body %q", html)
	}
	if strings.Contains(html, `hx-ext="sse"`) {
		t.Errorf("expected no hx-ext=\"sse\" when LiveUpdatesEnabled is false, got body %q", html)
	}
	// The row itself still renders (server-rendered fallback, FR1) with its
	// sse-swap attribute -- just with nothing driving it live yet.
	if !strings.Contains(html, `id="deployment-row-40"`) {
		t.Errorf("expected the deployment row to still render server-side, got body %q", html)
	}
}

// --- 3. Live indicator and Reload affordance render outside every sse-swap -

func TestSessions_LiveIndicatorAndReloadOutsideSSESwap(t *testing.T) {
	html := renderSessionsPage(t, buildSessionsPageData(true))

	indicatorIdx := strings.Index(html, `id="deployments-live-status"`)
	reloadIdx := strings.Index(html, `id="deployments-reload-container"`)
	firstSwapIdx := strings.Index(html, `sse-swap="`)

	if indicatorIdx < 0 {
		t.Fatalf("expected the Live/Not-Live indicator, got body %q", html)
	}
	if reloadIdx < 0 {
		t.Fatalf("expected the Reload affordance, got body %q", html)
	}
	if firstSwapIdx < 0 {
		t.Fatalf("expected at least one sse-swap element, got body %q", html)
	}
	if indicatorIdx >= firstSwapIdx {
		t.Errorf("expected the Live indicator (index %d) to render before the first sse-swap element (index %d), got body %q", indicatorIdx, firstSwapIdx, html)
	}
	if reloadIdx >= firstSwapIdx {
		t.Errorf("expected the Reload affordance (index %d) to render before the first sse-swap element (index %d), got body %q", reloadIdx, firstSwapIdx, html)
	}
	if !strings.Contains(html, ">Live<") {
		t.Errorf("expected the indicator to start in the Live state, got body %q", html)
	}
	if !strings.Contains(html, `href="/sessions" class="btn btn-sm btn-warning"`) {
		t.Errorf("expected the Reload link to point at /sessions, got body %q", html)
	}
}

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
