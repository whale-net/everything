package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handleChartDetail is screen 22-chart-detail (FR-24/FR-25): the chart's
// current version per environment and the apps published at that
// artifact's version, kept visually distinct from the chart's currently
// declared composition (chart_app) -- see chart_data.go's buildChartDetail.
// {id} is the chart's full_name, matching /apps/{id}'s and the apps
// catalog's rowDetailHref convention (pages/apps_catalog.templ).
func (app *App) handleChartDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	fullName := r.PathValue("id")
	if fullName == "" {
		http.NotFound(w, r)
		return
	}

	app.renderChartDetail(w, r, user, fullName, "")
}

// handleChartSetArgoOverride serves the ArgoCD Application name override
// editor's submit, on screen 22: calls SetChartArgoApplicationNameOverride
// for exactly the one (chart, environment_key) row the submitted form
// targets, then re-renders the SAME chart detail page (never a redirect),
// same precedent handleRetryArgoSync sets on the promotion details screen.
// An empty argo_application_name clears that environment's override
// (SetChartArgoApplicationNameOverrideRequest's own contract) -- every
// other environment's override on the chart is untouched.
//
// UI-side admin gating (pages.chartArgoOverrideCard, presentation-only) is
// not re-checked here: the real enforcement is
// server/handlers/app.go's SetChartArgoApplicationNameOverride, whose own
// auth.Require check runs regardless of what this handler does -- a
// non-admin session that somehow reaches this handler (e.g. by crafting the
// POST directly, bypassing the disabled inputs) gets exactly the same
// PermissionDenied the RPC gives any other caller, surfaced here as
// OverrideErr.
func (app *App) handleChartSetArgoOverride(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	fullName := r.PathValue("id")
	if fullName == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	var overrideErr string
	req := &pb.SetChartArgoApplicationNameOverrideRequest{
		FullName:            fullName,
		EnvironmentKey:      r.FormValue("environment_key"),
		ArgoApplicationName: r.FormValue("argo_application_name"),
		Reason:              r.FormValue("reason"),
	}
	if _, err := app.registry.App.SetChartArgoApplicationNameOverride(r.Context(), req); err != nil {
		log.Printf("SetChartArgoApplicationNameOverride(%q, %q) failed: %v", fullName, req.EnvironmentKey, err)
		overrideErr = grpcErrorMessage(err)
	}

	app.renderChartDetail(w, r, user, fullName, overrideErr)
}

// renderChartDetail is handleChartDetail/handleChartSetArgoOverride's
// shared buildChartDetail-then-render body -- see renderPromotionDetails
// for the identical LoadErr-vs-action-error split this mirrors.
func (app *App) renderChartDetail(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, fullName, overrideErr string) {
	data, err := app.buildChartDetail(r.Context(), fullName)
	if err != nil {
		log.Printf("buildChartDetail(%q) failed: %v", fullName, err)
		http.Error(w, "Failed to load chart "+fullName+" from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}
	if data == nil {
		http.Error(w, "No chart named "+fullName+" is registered", http.StatusNotFound)
		return
	}
	data.OverrideErr = overrideErr

	if err := RenderTempl(w, r, "Chart: "+fullName, pages.ChartDetail(user, data)); err != nil {
		log.Printf("Failed to render chart detail page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
