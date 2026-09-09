package components

import (
	"context"
	"strings"
	"testing"
)

func renderLiveRegion(t *testing.T, opts LiveRegionOptions) string {
	t.Helper()
	var buf strings.Builder
	if err := LiveRegion(opts).Render(context.Background(), &buf); err != nil {
		t.Fatalf("LiveRegion render failed: %v", err)
	}
	return buf.String()
}

// TestLiveRegion_SSEConnectAttribute covers the task's live_indicator_test.go
// requirement: the sse-connect attribute must be wired to opts.SSEPath, and
// the container must carry hx-ext="sse" so htmx actually attaches the SSE
// extension to it.
func TestLiveRegion_SSEConnectAttribute(t *testing.T) {
	html := renderLiveRegion(t, LiveRegionOptions{
		SSEPath:             "/api/live/deployments",
		HeartbeatIntervalMs: 15000,
		ReloadHref:          "/sessions",
	})

	if !strings.Contains(html, `sse-connect="/api/live/deployments"`) {
		t.Errorf("expected sse-connect=%q, got body %q", "/api/live/deployments", html)
	}
	if !strings.Contains(html, `hx-ext="sse"`) {
		t.Errorf("expected hx-ext=\"sse\", got body %q", html)
	}
}

// TestLiveRegion_LiveStatusBadge covers the task's requirement to assert the
// deployments-live-status badge element, starting in the Live state.
func TestLiveRegion_LiveStatusBadge(t *testing.T) {
	html := renderLiveRegion(t, LiveRegionOptions{
		SSEPath:             "/api/live/deployments",
		HeartbeatIntervalMs: 15000,
		ReloadHref:          "/sessions",
	})

	if !strings.Contains(html, `id="deployments-live-status"`) {
		t.Errorf("expected a #deployments-live-status badge element, got body %q", html)
	}
	if !strings.Contains(html, `id="deployments-live-status" class="badge badge-success"`) {
		t.Errorf("expected the badge to start in the Live (badge-success) state, got body %q", html)
	}
	if !strings.Contains(html, ">Live<") {
		t.Errorf("expected the badge to start with Live text, got body %q", html)
	}
}

// TestLiveRegion_ReloadHrefHonoured covers the task's requirement that a
// non-"/sessions" ReloadHref renders that href -- proving the reload target
// is a real parameter (needed for Activity/FR15 to point at its own path),
// not a hard-coded "/sessions" literal left over from the sessions-page
// origin.
func TestLiveRegion_ReloadHrefHonoured(t *testing.T) {
	html := renderLiveRegion(t, LiveRegionOptions{
		SSEPath:             "/api/live/activity",
		HeartbeatIntervalMs: 15000,
		ReloadHref:          "/activity",
	})

	if !strings.Contains(html, `href="/activity"`) {
		t.Errorf("expected the Reload link to point at /activity, got body %q", html)
	}
	if strings.Contains(html, `href="/sessions"`) {
		t.Errorf("expected no hard-coded /sessions href for a non-sessions caller, got body %q", html)
	}
}

// TestLiveRegion_ChildrenRenderInsideContainer proves the { children... }
// slot renders inside the hx-ext="sse" container -- the shape every M5
// caller (Sessions today, Activity/FR15 next) relies on: rows rendered as
// children live under the same SSE ancestor as the indicator/reload
// affordance, never outside it.
func TestLiveRegion_ChildrenRenderInsideContainer(t *testing.T) {
	var buf strings.Builder
	err := LiveRegion(LiveRegionOptions{
		SSEPath:             "/api/live/deployments",
		HeartbeatIntervalMs: 15000,
		ReloadHref:          "/sessions",
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("LiveRegion render failed: %v", err)
	}
	html := buf.String()

	regionIdx := strings.Index(html, `id="deployments-live-region"`)
	if regionIdx < 0 {
		t.Fatalf("expected a #deployments-live-region container, got body %q", html)
	}
	statusIdx := strings.Index(html, `id="deployments-live-status"`)
	reloadIdx := strings.Index(html, `id="deployments-reload-container"`)
	if statusIdx < regionIdx || reloadIdx < regionIdx {
		t.Errorf("expected the status badge and reload container to render after the region opens, got body %q", html)
	}
}
