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
// requirement: the hx-sse:connect attribute must be wired to opts.SSEPath, and
// the legacy hx-ext="sse" opt-in must be gone (htmx 4's hx-sse extension
// activates from hx-sse:connect alone).
func TestLiveRegion_SSEConnectAttribute(t *testing.T) {
	html := renderLiveRegion(t, LiveRegionOptions{
		SSEPath:             "/api/live/deployments",
		HeartbeatIntervalMs: 15000,
		ReloadHref:          "/sessions",
	})

	if !strings.Contains(html, `hx-sse:connect="/api/live/deployments"`) {
		t.Errorf("expected hx-sse:connect=%q, got body %q", "/api/live/deployments", html)
	}
	if strings.Contains(html, `hx-ext`) {
		t.Errorf("expected no hx-ext attribute, got body %q", html)
	}
}

// TestLiveRegion_LiveStatusBadge covers the task's requirement to assert the
// live-status badge element, starting in the Live state.
func TestLiveRegion_LiveStatusBadge(t *testing.T) {
	html := renderLiveRegion(t, LiveRegionOptions{
		SSEPath:             "/api/live/deployments",
		HeartbeatIntervalMs: 15000,
		ReloadHref:          "/sessions",
	})

	if !strings.Contains(html, `data-live-status class="badge badge-success"`) {
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
// slot renders inside the hx-sse:connect container -- the shape every M5
// caller (Sessions originally, Activity today) relies on: rows rendered as
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

	regionIdx := strings.Index(html, `hx-sse:connect="/api/live/deployments"`)
	if regionIdx < 0 {
		t.Fatalf("expected a container carrying hx-sse:connect, got body %q", html)
	}
	statusIdx := strings.Index(html, `data-live-status`)
	reloadIdx := strings.Index(html, `data-live-reload`)
	if statusIdx < regionIdx || reloadIdx < regionIdx {
		t.Errorf("expected the status badge and reload container to render after the region opens, got body %q", html)
	}
}

// TestLiveRegion_IndicatorInsideContainerNoKeepaliveTargets proves the
// LiveIndicator renders inside the hx-sse:connect container and that no
// legacy per-topic sse-swap keepalive targets are emitted -- htmx 4's
// hx-sse dispatches htmx:sse:after:message for every named event, so the
// indicator needs no hidden listeners.
func TestLiveRegion_IndicatorInsideContainerNoKeepaliveTargets(t *testing.T) {
	html := renderLiveRegion(t, LiveRegionOptions{
		SSEPath:             "/api/live/deployments",
		HeartbeatIntervalMs: 15000,
		ReloadHref:          "/sessions",
	})

	regionIdx := strings.Index(html, `hx-sse:connect="/api/live/deployments"`)
	indicatorIdx := strings.Index(html, `class="live-indicator"`)
	if regionIdx < 0 || indicatorIdx < regionIdx {
		t.Errorf("expected the live indicator inside the hx-sse:connect container, got body %q", html)
	}
	if strings.Contains(html, "sse-swap") {
		t.Errorf("expected no sse-swap attribute anywhere, got body %q", html)
	}
}
