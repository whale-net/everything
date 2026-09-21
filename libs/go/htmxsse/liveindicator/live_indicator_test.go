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

func TestLiveIndicator_RendersHiddenKeepaliveTargetPerTopic(t *testing.T) {
	html := render(t, Options{
		Topics:              []string{"release_run.abc", "promotion.def"},
		HeartbeatIntervalMs: 5000,
		ReloadHref:          "/releases/abc",
	})

	// The whole point of this package: a hidden sse-swap target per topic
	// bound to "<topic>-keepalive", so htmx's sse extension actually
	// registers a listener for the heartbeat event handler.go's
	// emitKeepalive already sends -- without this, htmx never dispatches
	// htmx:sseMessage for it at all (see package doc comment).
	require.Contains(t, html, `sse-swap="release_run.abc-keepalive"`)
	require.Contains(t, html, `sse-swap="promotion.def-keepalive"`)
}

func TestLiveIndicator_TopicsAndHeartbeatSurfaceAsDataAttributes(t *testing.T) {
	html := render(t, Options{
		Topics:              []string{"release_run.abc", "promotion.def"},
		HeartbeatIntervalMs: 5000,
		ReloadHref:          "/releases/abc",
	})

	require.Contains(t, html, `data-live-topics="release_run.abc,promotion.def"`)
	require.Contains(t, html, `data-live-heartbeat-ms="5000"`)
}

func TestLiveIndicator_ReloadHrefWired(t *testing.T) {
	html := render(t, Options{
		Topics:              []string{"release_run.abc"},
		HeartbeatIntervalMs: 5000,
		ReloadHref:          "/releases/abc",
	})

	require.Contains(t, html, `data-live-reload`)
	require.Contains(t, html, `href="/releases/abc"`)
}

func TestLiveIndicator_InitialStateIsLive(t *testing.T) {
	html := render(t, Options{
		Topics:              []string{"release_run.abc"},
		HeartbeatIntervalMs: 5000,
		ReloadHref:          "/releases/abc",
	})

	require.Contains(t, html, `data-live-status class="badge badge-success"`)
	require.Contains(t, html, `data-live-status class="badge badge-success" title="SSE connection is healthy">Live</div>`)
}

func TestLiveIndicator_NoTopicsRendersNoKeepaliveTargets(t *testing.T) {
	html := render(t, Options{
		HeartbeatIntervalMs: 5000,
		ReloadHref:          "/x",
	})

	require.NotContains(t, html, "sse-swap=")
	require.Contains(t, html, `data-live-topics=""`)
}

func TestLiveIndicator_ScriptGuardsAgainstDoubleInit(t *testing.T) {
	// Two LiveIndicator instances on one page (unusual, but must not
	// double-register the document-level listeners) each render their own
	// copy of the script; the guard flag is what keeps that safe.
	html := render(t, Options{Topics: []string{"a"}, HeartbeatIntervalMs: 1000, ReloadHref: "/a"}) +
		render(t, Options{Topics: []string{"b"}, HeartbeatIntervalMs: 1000, ReloadHref: "/b"})

	require.Equal(t, 4, strings.Count(html, "__htmxsseLiveIndicatorInit"),
		"expected the guard flag (checked once, set once) per rendered script copy")
	require.Equal(t, 2, strings.Count(html, "addEventListener('htmx:sseMessage'"))
}
