package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/htmxsse/templadapter"
	"github.com/whale-net/everything/tools/app_registry/events"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handleReleaseStatusSSE handles SSE connections for release-run status
// updates on /releases/{id}/status/sse -- the release-run counterpart of
// handlePromoStatusSSE (handlers_sse.go). Structured identically: same
// auth composition (RequireAuthFunc wrapped by newNoRedirectWriter at the
// route level, never WithAccessToken), same app.sseHub, same
// htmxsse.Handler/templadapter wiring, same per-request cancellable
// context (#1699 FR9, FR10, FR12, FR16; no change to libs/go/htmxsse or
// libs/go/htmxauth).
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
type renderReleaseStatusFragmentComponent struct {
	r            *http.Request
	releaseRunID string
	cancel       context.CancelFunc
	app          *App
}

// Render implements templ.Component by re-acquiring credentials, reading
// current release-run state via the same GetRelease RPC the initial GET
// uses (FR12, no new read path), resolving target commits through the
// #1703 process-lifetime cache, and rendering exactly
// pages.ReleaseStatusLiveBody -- the same component the initial GET
// renders (NFR2). Terminal vs. transient failure discrimination mirrors
// renderPromoDetailsFragmentComponent.Render in handlers_sse.go.
func (c renderReleaseStatusFragmentComponent) Render(ctx context.Context, w io.Writer) error {
	// Re-acquire the access token on every delivery (FR9).
	grpcCtx, err := c.app.auth.ReacquireGRPCContext(c.r, c.cancel)
	if err != nil {
		return err
	}

	// Fetch current release-run state from the registry (FR12: same read
	// path the initial GET uses, no new gRPC method).
	resp, err := c.app.registry.Release.GetRelease(grpcCtx, &pb.GetReleaseRequest{ReleaseRunId: c.releaseRunID})
	if err != nil {
		// All gRPC errors during fragment render are treated as transient
		// unless the session is gone.
		if c.app.sessionMgr != nil {
			_, checkErr := c.app.sessionMgr.GetUserInfo(c.r)
			if checkErr != nil {
				// Terminal: session is gone, cancel the stream.
				c.cancel()
				return fmt.Errorf("session lost: %w", checkErr)
			}
		}
		// Transient: gRPC call failed, but session is intact.
		log.Printf("GetRelease(%q) failed: %v", c.releaseRunID, err)
		return fmt.Errorf("get release failed: %w", err)
	}

	// FR12/NFR9: resolve build_id -> commit through the process-lifetime
	// cache so heartbeats and events do not re-issue GetBuild.
	commits := c.app.resolveTargetCommits(grpcCtx, resp.GetTargets())

	// NFR2: render exactly pages.ReleaseStatusLiveBody -- the same
	// component the initial GET renders. FR16: no all-terminal special
	// case here; a fully-terminal run still renders and streams normally.
	return pages.ReleaseStatusLiveBody(resp, commits).Render(ctx, w)
}
