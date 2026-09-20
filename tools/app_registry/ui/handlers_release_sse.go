package main

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/htmxsse/templadapter"
	"github.com/whale-net/everything/tools/app_registry/events"
)

// handleReleaseStatusSSE handles SSE connections for release-run status
// updates on /releases/{id}/status/sse -- the release-run counterpart of
// handlePromoStatusSSE (handlers_sse.go). Structured identically: same
// auth composition (RequireAuthFunc wrapped by newNoRedirectWriter at the
// route level, never WithAccessToken), same app.sseHub, same
// htmxsse.Handler/templadapter wiring, same per-request cancellable
// context (#1699 FR9, FR10, FR12, FR16; no change to libs/go/htmxsse or
// libs/go/htmxauth).
//
// Scaffold: route wiring and topic derivation only. Implementation phase
// fills in the fragment's GetRelease read, resolveTargetCommits (#1703)
// cache reuse, and pages.ReleaseStatusLiveBody rendering (#1704).
func (app *App) handleReleaseStatusSSE(w http.ResponseWriter, r *http.Request) {
	// Per-request cancellable context, captured by the fragment closure and
	// cancelled on a terminal error (mirrors handlePromoStatusSSE).
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)

	releaseRunID := r.PathValue("id")
	if releaseRunID == "" {
		http.Error(w, "missing release run id", http.StatusBadRequest)
		return
	}

	topic := events.TopicForReleaseRun(releaseRunID)
	topics := []string{topic}

	componentFunc := func(r *http.Request, topic string) templ.Component {
		return renderReleaseStatusFragmentComponent{r, releaseRunID, cancel, app}
	}

	fragment := templadapter.Adapt(componentFunc)

	// Use the app's existing shared Hub -- no second htmxsse.NewHub call.
	handler := htmxsse.Handler(app.sseHub, topics, fragment)
	handler(w, r)
}

// renderReleaseStatusFragmentComponent implements templ.Component to
// render the release-run status fragment on every connect, reconnect, and
// heartbeat delivery, with freshly acquired credentials each time (FR9,
// FR10, NFR7).
//
// Scaffold stub -- Implementation phase fills in
// app.auth.ReacquireGRPCContext, the GetRelease read, resolveTargetCommits
// (#1703), and pages.ReleaseStatusLiveBody(rel, commits) rendering (NFR2,
// FR12).
type renderReleaseStatusFragmentComponent struct {
	r            *http.Request
	releaseRunID string
	cancel       context.CancelFunc
	app          *App
}

func (c renderReleaseStatusFragmentComponent) Render(ctx context.Context, w io.Writer) error {
	return fmt.Errorf("renderReleaseStatusFragmentComponent.Render: not implemented (#1705 Implementation phase)")
}
