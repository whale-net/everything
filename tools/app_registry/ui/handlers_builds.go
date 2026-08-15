package main

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// githubActionsRunURL builds the GitHub Actions deep link screen 31 uses
// for the zero-artifact indeterminate case (FR-29) — NFR-15 forbids any
// live GitHub API call from this UI, so this is a link only, never a
// fetch.
func githubActionsRunURL(workflowRunID string) string {
	return "https://github.com/whale-net/everything/actions/runs/" + workflowRunID
}

// handleBuilds is screen 30 (FR-26, FR-56): recent builds, one ListBuilds
// call per page load (see pages.Builds's doc comment for what this screen
// deliberately omits and why).
func (app *App) handleBuilds(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.registry.Artifact.ListBuilds(r.Context(), &pb.ListBuildsRequest{})
	if err != nil {
		log.Printf("ListBuilds failed: %v", err)
		http.Error(w, "Failed to load builds from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	if err := RenderTempl(w, r, "Builds", pages.Builds(user, resp.GetBuilds(), "")); err != nil {
		log.Printf("Failed to render builds page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handleBuildLookup is screen 30's direct workflow-run-id lookup box. It
// deliberately takes no attempt input — GetReleaseRun(workflow_run_id, 0)
// already means "the highest attempt recorded" (repository/repository.go:
// 173-179, repository/postgres/artifact.go:86-90) — and issues no new RPC:
// it reuses the same GetReleaseRun call handleBuildDetail makes, just to
// validate the run id exists before redirecting.
func (app *App) handleBuildLookup(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.URL.Query().Get("run"))
	if runID == "" {
		http.Redirect(w, r, "/builds", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/builds/"+runID, http.StatusSeeOther)
}

// handleBuildDetail is screen 31 (FR-27, FR-28, FR-29, FR-30): every
// artifact a run touched, its state, and an "incomplete only" filter. App
// Registry recording health is derived client-side (well, server-side here,
// rendered as its own labelled status — see pages.BuildDetail) from the
// per-artifact states: never merged with any notion of release/run status.
func (app *App) handleBuildDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	runID := r.PathValue("id")
	if runID == "" {
		http.NotFound(w, r)
		return
	}

	resp, err := app.registry.Artifact.GetReleaseRun(r.Context(), &pb.GetReleaseRunRequest{
		WorkflowRunId: runID,
	})
	if err != nil {
		log.Printf("GetReleaseRun(%q) failed: %v", runID, err)
		http.Error(w, "Failed to load run from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}
	if resp.GetBuild() == nil || resp.GetBuild().GetBuildId() == "" {
		http.Error(w, "No build recorded for workflow run "+runID, http.StatusNotFound)
		return
	}

	// Domain is shown on THIS screen (unlike screen 30) because an app/chart
	// identity is actually in hand per artifact. One ListApps + one
	// ListCharts call for the whole page — never a per-artifact fetch.
	domainByOwnerID, err := app.ownerDomainIndex(r.Context())
	if err != nil {
		log.Printf("ownerDomainIndex failed: %v", err)
		http.Error(w, "Failed to load app/chart identity from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	incompleteOnly := r.URL.Query().Get("incomplete") == "1"

	if err := RenderTempl(w, r, "Run #"+runID, pages.BuildDetail(
		user,
		resp.GetBuild(),
		resp.GetArtifacts(),
		domainByOwnerID,
		incompleteOnly,
		githubActionsRunURL(runID),
	)); err != nil {
		log.Printf("Failed to render build detail page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// ownerDomainIndex maps every known app_id/chart_id to its owning domain,
// so screen 31 can label each artifact's domain without a per-artifact
// GetApp/GetChart call. Charts have no GetChart RPC, so both come from
// their List* calls.
func (app *App) ownerDomainIndex(ctx context.Context) (map[string]string, error) {
	appsResp, err := app.registry.App.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		return nil, err
	}
	chartsResp, err := app.registry.App.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil {
		return nil, err
	}

	index := make(map[string]string, len(appsResp.GetApps())+len(chartsResp.GetCharts()))
	for _, a := range appsResp.GetApps() {
		index[a.GetAppId()] = a.GetDomain()
	}
	for _, c := range chartsResp.GetCharts() {
		index[c.GetChartId()] = c.GetDomain()
	}
	return index, nil
}

// handleReconcileRuns is screen 32 (FR-31): every ReconcileApps sweep that
// actually applied, on its own screen separate from builds. A sweep
// rejected as stale is never recorded as a ReconcileRun row at all
// (ReconcileRun is one row per sweep that actually applied — see
// api_messages_app.proto) — the info banner on this screen explains that
// absence as expected watermark behaviour rather than the UI trying to
// synthesize a "rejected" row it has no data for.
func (app *App) handleReconcileRuns(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.registry.App.ListReconcileRuns(r.Context(), &pb.ListReconcileRunsRequest{})
	if err != nil {
		log.Printf("ListReconcileRuns failed: %v", err)
		http.Error(w, "Failed to load reconcile runs from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	if err := RenderTempl(w, r, "Reconcile Runs", pages.ReconcileRuns(user, resp.GetReconcileRuns())); err != nil {
		log.Printf("Failed to render reconcile runs page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
