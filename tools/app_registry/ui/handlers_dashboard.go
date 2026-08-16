package main

import (
	"context"
	"log"
	"net/http"
	"sort"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// recentPromotionsLimit bounds the dashboard's "recent promotions" table
// (FR-10) — this is a glance view, not the full audit log (screen 13/D10
// own that).
const recentPromotionsLimit = 10

// handleDashboard is screen 01-dashboard: cross-environment state,
// drift/health alerts, and recent promotions, each read-only and linking to
// the screen that explains it (FR-10). It reuses the same current-state
// (at=0) matrix screen 10 builds, so the two screens can never disagree
// about what's promoted where.
func (app *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	m, err := app.buildDeploymentMatrix(r.Context(), 0)
	if err != nil {
		log.Printf("buildDeploymentMatrix failed: %v", err)
		http.Error(w, "Failed to load environment state from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Recent promotions is supplementary to the environment overview above
	// — a failure here degrades that one card rather than failing the
	// whole dashboard (which already proved every reachable environment
	// reachable, above).
	promotions, listErr := app.recentPromotions(r.Context())
	if listErr != nil {
		log.Printf("ListPromotions failed: %v", listErr)
	}

	if err := RenderTempl(w, r, "Dashboard", pages.Dashboard(user, m, promotions, listErr)); err != nil {
		log.Printf("Failed to render dashboard page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// recentPromotions returns the most recently-started current promotions
// across every environment, most-recent-first. ListPromotions is
// unfiltered and current-only (include_history defaults false); sorting by
// valid_from client-side is necessary because the RPC has no
// environment-spanning "recent" ordering of its own — it's scoped per
// environment_key/owner_full_name filter, neither of which the dashboard
// wants to pin.
func (app *App) recentPromotions(ctx context.Context) ([]*pb.Promotion, error) {
	resp, err := app.registry.Promotion.ListPromotions(ctx, &pb.ListPromotionsRequest{})
	if err != nil {
		return nil, err
	}
	promotions := resp.GetPromotions()
	sort.Slice(promotions, func(i, j int) bool {
		return promotions[i].GetValidFrom() > promotions[j].GetValidFrom()
	})
	if len(promotions) > recentPromotionsLimit {
		promotions = promotions[:recentPromotionsLimit]
	}
	return promotions, nil
}
