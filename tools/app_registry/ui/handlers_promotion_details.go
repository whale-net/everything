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

	resp, err := app.registry.Promotion.GetPromotionDetails(r.Context(), &pb.GetPromotionDetailsRequest{PromotionId: promotionID})
	if err != nil {
		log.Printf("GetPromotionDetails(%q) failed: %v", promotionID, err)
		s := pages.PromotionDetailsViewState{PromotionID: promotionID, LoadErr: grpcErrorMessage(err)}
		if renderErr := RenderTempl(w, r, "Promotion Details", pages.PromotionDetails(user, s)); renderErr != nil {
			log.Printf("Failed to render promotion details page: %v", renderErr)
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	s := pages.PromotionDetailsViewState{PromotionID: promotionID, Details: resp}
	if renderErr := RenderTempl(w, r, "Promotion Details", pages.PromotionDetails(user, s)); renderErr != nil {
		log.Printf("Failed to render promotion details page: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
