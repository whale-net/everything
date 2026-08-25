package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxsse"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// handlePromoStatusSSE handles SSE connections for promotion status updates.
// (FR6, FR27, FR28, NFR4, NFR13)
//
// This handler:
// - Requires authentication via RequireAuthFunc (wrapped with noRedirectWriter)
// - Does NOT use WithAccessToken (which redirects, violating FR28)
// - Re-acquires access token on every delivery via GetAccessToken(r)
// - Implements FR27 failure discrimination between terminal and transient errors
// - Provides full-state fragments on connect/reconnect
// - Streams updates via htmxsse.Handler
//
// The context is cancellable per-request (FR2, FR28e) so FR27's terminal
// class can cancel the stream mid-flight.
func (app *App) handlePromoStatusSSE(w http.ResponseWriter, r *http.Request) {
	// Wrap the response writer with noRedirectWriter to prevent any redirects.
	// RequireAuthFunc is composed with this shim, ensuring auth failures never
	// emit a 3xx status or Location header (FR28b).
	w = newNoRedirectWriter(w)

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

	topic := "promotion." + promID // Use events.TopicForPromotion(promID) when events package is wired in Implementation
	topics := []string{topic}

	// Create the fragment function. This closure is called on connect, reconnect,
	// and heartbeat to produce the current state of the promotion.
	// (FR3, FR27: re-acquires token on every delivery)
	fragment := func(r *http.Request, topic string) ([]byte, error) {
		// Re-acquire the access token on every delivery (FR27).
		// Note: this is a fresh call, not a stored token.
		token, err := app.auth.GetAccessToken(r)
		if err != nil {
			// Check if this is a terminal error (session gone) or transient (credential failed).
			// Terminal class: call GetUserInfo on the same session.
			// If GetUserInfo fails, the session is gone → cancel the stream.
			// If GetUserInfo succeeds, the session is intact → transient error, no cancel.
			if app.sessionMgr != nil {
				_, checkErr := app.sessionMgr.GetUserInfo(r)
				if checkErr != nil {
					// Terminal: session is gone, cancel the stream
					cancel()
					return nil, fmt.Errorf("session lost: %w", checkErr)
				}
			}
			// Transient: credential failure, don't cancel
			return nil, fmt.Errorf("token refresh failed: %w", err)
		}

		// Inject the fresh token into the context for gRPC calls.
		// (FR27: injecting the token, not just acquiring it)
		ctx := grpcauth.WithUserToken(r.Context(), token)
		r = r.WithContext(ctx)

		// Fetch the promotion details from the registry.
		resp, err := app.registry.Promotion.GetPromotionDetails(ctx, &pb.GetPromotionDetailsRequest{PromotionId: promID})
		if err != nil {
			// All gRPC errors during fragment render are treated as transient
			// (credential, network, timeouts) unless the session is gone.
			if app.sessionMgr != nil {
				_, checkErr := app.sessionMgr.GetUserInfo(r)
				if checkErr != nil {
					// Terminal: session is gone, cancel the stream
					cancel()
					return nil, fmt.Errorf("session lost: %w", checkErr)
				}
			}
			// Transient: gRPC call failed, but session is intact
			return nil, fmt.Errorf("get promotion details failed: %w", err)
		}

		// Render the promotion status as an HTML fragment.
		// For now, return a simple status display with key information.
		details := resp.Details
		if details == nil {
			return nil, fmt.Errorf("promotion details is nil")
		}

		// Build a simple HTML fragment showing promotion status.
		// This is a placeholder that can be enhanced later with templ components.
		fragment := fmt.Sprintf(
			"<div class=\"promotion-status\" data-promotion-id=\"%s\">\n"+
				"  <div class=\"status-outcome\">%s</div>\n"+
				"  <div class=\"status-from\">From: %s</div>\n"+
				"  <div class=\"status-to\">To: %s</div>\n"+
				"</div>",
			promID,
			details.Outcome.String(),
			details.FromVersion,
			details.ToVersion,
		)
		return []byte(fragment), nil
	}

	// Create the SSE handler with the hub from the app.
	// (FR5, FR2, FR3: full-state on connect/reconnect, heartbeat, per-connection rendering)
	handler := htmxsse.Handler(app.sseHub, topics, fragment)

	// Serve the connection.
	handler(w, r)
}
