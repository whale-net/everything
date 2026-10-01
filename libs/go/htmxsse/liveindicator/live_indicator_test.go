package liveindicator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func render(t *testing.T, opts Options) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, LiveIndicator(opts).Render(context.Background(), &buf))
	return buf.String()
}

func TestLiveIndicator_ListensForHtmx4SSEEvents(t *testing.T) {
	html := render(t, Options{HeartbeatIntervalMs: 5000, ReloadHref: "/releases/abc"})

	// Keepalive heartbeats are named events, which hx-sse only surfaces as
	// htmx:sse:after:message -- the same event a swap fires -- so one
	// listener covers both.
	for _, event := range []string{"htmx:sse:after:connection", "htmx:sse:after:message", "htmx:sse:error"} {
		require.Contains(t, html, "addEventListener('"+event+"'")
	}
	require.NotContains(t, html, "htmx:sseMessage", "htmx 1.x event names no longer fire")
}

func TestLiveIndicator_RendersNoPerTopicTargets(t *testing.T) {
	html := render(t, Options{HeartbeatIntervalMs: 5000, ReloadHref: "/x"})

	require.NotContains(t, html, "sse-swap")
	require.NotContains(t, html, "data-sse-topic")
}

func TestLiveIndicator_HeartbeatSurfacesAsDataAttribute(t *testing.T) {
	html := render(t, Options{HeartbeatIntervalMs: 5000, ReloadHref: "/releases/abc"})

	require.Contains(t, html, `data-live-heartbeat-ms="5000"`)
}

func TestLiveIndicator_ReloadHrefWired(t *testing.T) {
	html := render(t, Options{HeartbeatIntervalMs: 5000, ReloadHref: "/releases/abc"})

	require.Contains(t, html, `data-live-reload`)
	require.Contains(t, html, `href="/releases/abc"`)
}

func TestLiveIndicator_InitialStateIsLive(t *testing.T) {
	html := render(t, Options{HeartbeatIntervalMs: 5000, ReloadHref: "/releases/abc"})

	require.Contains(t, html, `data-live-status class="badge badge-success"`)
	require.Contains(t, html, `data-live-status class="badge badge-success" title="SSE connection is healthy">Live</div>`)
}

func TestLiveIndicator_ScriptGuardsAgainstDoubleInit(t *testing.T) {
	// Two LiveIndicator instances on one page (unusual, but must not
	// double-register the document-level listeners) each render their own
	// copy of the script; the guard flag is what keeps that safe.
	html := render(t, Options{HeartbeatIntervalMs: 1000, ReloadHref: "/a"}) +
		render(t, Options{HeartbeatIntervalMs: 1000, ReloadHref: "/b"})

	require.Equal(t, 4, strings.Count(html, "__htmxsseLiveIndicatorInit"),
		"expected the guard flag (checked once, set once) per rendered script copy")
	require.Equal(t, 2, strings.Count(html, "addEventListener('htmx:sse:after:message'"))
}
