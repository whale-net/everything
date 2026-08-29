package main

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/htmxsse/templadapter"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/events"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handlePromoStatusSSE handles SSE connections for promotion status updates.
// (FR6, FR27, FR28, NFR4, NFR13, issue #1033)
//
// This handler:
// - Receives authenticated requests via RequireAuthFunc (wrapped with noRedirectWriter at route level)
// - Does NOT use WithAccessToken (which redirects, violating FR28)
// - Re-acquires access token on every delivery via GetAccessToken(r)
// - Implements FR27 failure discrimination between terminal and transient errors
// - Provides full-state fragments on connect/reconnect
// - Streams updates via htmxsse.Handler
//
// The context is cancellable per-request (FR2, FR28e) so FR27's terminal
// class can cancel the stream mid-flight.
func (app *App) handlePromoStatusSSE(w http.ResponseWriter, r *http.Request) {
	// Per-request cancellable context (FR28e).
	// The fragment closure captures this cancel function and calls it on
	// terminal errors (FR27's terminal class).
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)

	// Get the promotion ID from the URL path.
	promID := r.PathValue("id")
	if promID == "" {
		http.Error(w, "missing promotion ID", http.StatusBadRequest)
		return
	}

	topic := events.TopicForPromotion(promID)
	topics := []string{topic}

	// Create the fragment function. This closure is called on connect, reconnect,
	// and heartbeat to produce the current state of the promotion.
	// (FR3, FR27: re-acquires token on every delivery)
	// Fragment renders exactly @promotionDetailsBody(user, s.Details.GetDetails()) (FR3)
	componentFunc := func(r *http.Request, topic string) templ.Component {
		return renderPromoDetailsFragment(r, promID, cancel, app)
	}

	// Adapt the templ component function to the htmxsse.Fragment interface
	// (FR4: templ adapter)
	fragment := templadapter.Adapt(componentFunc)

	// Create the SSE handler with the hub from the app.
	// (FR5, FR2, FR3: full-state on connect/reconnect, heartbeat, per-connection rendering)
	handler := htmxsse.Handler(app.sseHub, topics, fragment)

	// Serve the connection.
	handler(w, r)
}

// renderPromoDetailsFragment renders the promotion details fragment that gets
// pushed over SSE. This is the rendering half of FR3 — the auth/token half
// is implemented in handlePromoStatusSSE.
//
// The fragment renders exactly @PromotionDetailsBody(user, s.Details.GetDetails()),
// reading current state at delivery time (FR3, NFR2, NFR11: deterministic).
// The publisher payload's advisory field must not be rendered — the fragment
// is produced from the fresh read; if payload and fragment disagree, fragment wins.
func renderPromoDetailsFragment(r *http.Request, promID string, cancel context.CancelFunc, app *App) templ.Component {
	// Use a wrapper component that performs the token refresh and data fetch
	return renderPromoDetailsFragmentComponent{r, promID, cancel, app}
}

// renderPromoDetailsFragmentComponent implements templ.Component to render
// the promotion details body with freshly acquired credentials (FR27).
type renderPromoDetailsFragmentComponent struct {
	r      *http.Request
	promID string
	cancel context.CancelFunc
	app    *App
}

// Render implements templ.Component by fetching fresh data and rendering
// the promotion details body (FR3).
func (c renderPromoDetailsFragmentComponent) Render(ctx context.Context, w io.Writer) error {
	// Re-acquire the access token on every delivery (FR27).
	token, err := c.app.auth.GetAccessToken(c.r)
	if err != nil {
		// Check if this is a terminal error (session gone) or transient (credential failed).
		if c.app.sessionMgr != nil {
			_, checkErr := c.app.sessionMgr.GetUserInfo(c.r)
			if checkErr != nil {
				// Terminal: session is gone, cancel the stream
				c.cancel()
				return fmt.Errorf("session lost: %w", checkErr)
			}
		}
		// Transient: credential failure, don't cancel
		return fmt.Errorf("token refresh failed: %w", err)
	}

	// Inject the fresh token into the context for gRPC calls.
	grpcCtx := grpcauth.WithUserToken(c.r.Context(), token)

	// Fetch the promotion details from the registry.
	resp, err := c.app.registry.Promotion.GetPromotionDetails(grpcCtx, &pb.GetPromotionDetailsRequest{PromotionId: c.promID})
	if err != nil {
		// All gRPC errors during fragment render are treated as transient
		// unless the session is gone.
		if c.app.sessionMgr != nil {
			_, checkErr := c.app.sessionMgr.GetUserInfo(c.r)
			if checkErr != nil {
				// Terminal: session is gone, cancel the stream
				c.cancel()
				return fmt.Errorf("session lost: %w", checkErr)
			}
		}
		// Transient: gRPC call failed, but session is intact
		return fmt.Errorf("get promotion details failed: %w", err)
	}

	// Get the current user (FR3)
	user := htmxauth.GetUser(c.r.Context())
	if user == nil {
		return fmt.Errorf("user not found in context")
	}

	// Render exactly @PromotionDetailsBody(user, s.Details.GetDetails())
	// (FR3, NFR2: fragment function produces the exact output the user sees)
	details := resp.Details
	if details == nil {
		return fmt.Errorf("promotion details is nil")
	}

	return pages.PromotionDetailsBody(user, details).Render(ctx, w)
}
