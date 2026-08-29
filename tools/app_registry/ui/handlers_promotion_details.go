package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handlePromotionDetails serves the Promotion Details screen (FR7, FR7a,
// FR8, FR9, issue #1032): a durable page keyed by promotion_id (path
// param), rendering GetPromotionDetails' full response -- requester/
// request time, from/to version, writeback outcome, and the full ArgoCD
// sync/health history (#1028).
//
// This is a plain read, so it is fully wired here in the Scaffold phase --
// the same precedent handleReleaseStatus (handlers_release.go) sets for
// release-status: unlike a submit/write path, there is no permission-
// gating or idempotency complexity to defer.
func (app *App) handlePromotionDetails(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	promotionID := r.PathValue("id")
	if promotionID == "" {
		http.NotFound(w, r)
		return
	}

	app.renderPromotionDetails(w, r, user, promotionID, "")
}

// handleRetryArgoSync serves the FR10-FR13 "Retry refresh/sync" submit
// (issue #1033): calls RetryArgoSync, then re-renders the SAME details
// page via renderPromotionDetails (never a redirect) so a successful retry
// immediately shows the new retry_triggered/retry_observed sync-history
// rows it wrote, and a failed retry surfaces its own error inline via
// PromotionDetailsViewState.RetryErr -- distinct from LoadErr, since the
// promotion itself still read back fine.
//
// UI-side admin gating (pages.retryArgoSyncButton, presentation-only) is
// not re-checked here: the real enforcement is
// server/handlers/promotion.go's RetryArgoSync, whose own auth.Require
// check runs regardless of what this handler does -- a non-admin session
// that somehow reaches this handler (e.g. by crafting the POST directly,
// bypassing the disabled button) gets exactly the same PermissionDenied
// the RPC gives any other caller, surfaced here as RetryErr like any other
// RetryArgoSync failure.
func (app *App) handleRetryArgoSync(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	promotionID := r.PathValue("id")
	if promotionID == "" {
		http.NotFound(w, r)
		return
	}

	var retryErr string
	if _, err := app.registry.Promotion.RetryArgoSync(r.Context(), &pb.RetryArgoSyncRequest{PromotionId: promotionID}); err != nil {
		log.Printf("RetryArgoSync(%q) failed: %v", promotionID, err)
		retryErr = grpcErrorMessage(err)
	}

	app.renderPromotionDetails(w, r, user, promotionID, retryErr)
}

// renderPromotionDetails is handlePromotionDetails/handleRetryArgoSync's
// shared GetPromotionDetails-then-render body -- see
// PromotionDetailsViewState's doc comment for the LoadErr/RetryErr split
// this populates.
func (app *App) renderPromotionDetails(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, promotionID, retryErr string) {
	resp, err := app.registry.Promotion.GetPromotionDetails(r.Context(), &pb.GetPromotionDetailsRequest{PromotionId: promotionID})
	if err != nil {
		log.Printf("GetPromotionDetails(%q) failed: %v", promotionID, err)
		heartbeatMs := int(app.sseHub.Config().HeartbeatInterval.Milliseconds())
		s := pages.PromotionDetailsViewState{PromotionID: promotionID, LoadErr: grpcErrorMessage(err), HeartbeatIntervalMs: heartbeatMs}
		if renderErr := RenderTempl(w, r, "Promotion Details", pages.PromotionDetails(user, s)); renderErr != nil {
			log.Printf("Failed to render promotion details page: %v", renderErr)
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	heartbeatMs := int(app.sseHub.Config().HeartbeatInterval.Milliseconds())
	s := pages.PromotionDetailsViewState{PromotionID: promotionID, Details: resp, RetryErr: retryErr, HeartbeatIntervalMs: heartbeatMs}
	if renderErr := RenderTempl(w, r, "Promotion Details", pages.PromotionDetails(user, s)); renderErr != nil {
		log.Printf("Failed to render promotion details page: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
