package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handleReleaseTrigger serves the release-trigger screen's GET (render the
// scope-entry form) and POST (resolve the entered scope) requests -- mirrors
// handlePromote's GET/POST dispatch (handlers_promote.go).
//
// Scaffold (issue #890): scope-expansion (FR1 -- "all"/domain/comma-list
// with default demo-domain exclusion) is implemented for real here, in
// release_scope.go's resolveReleaseScope, because it is core to what makes
// this screen usable at all -- ships in the same change as the skeleton
// rather than waiting for the Implementation phase. Everything else FR2-6
// requires (the actual TriggerRelease call, the FR5 in-flight rejection,
// the FR4/NFR5 permission gate, and the post-submit redirect to the status
// page) is deliberately NOT wired yet -- see handleReleaseTriggerSubmit's
// doc comment.
func (app *App) handleReleaseTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		app.handleReleaseTriggerShow(w, r)
		return
	}
	if r.Method == http.MethodPost {
		app.handleReleaseTriggerSubmit(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handleReleaseTriggerShow renders the initial (or retry-prefilled, via the
// "scope" query param -- see pages.retryHref) form.
func (app *App) handleReleaseTriggerShow(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	s := pages.ReleaseTriggerViewState{
		Scope:       r.URL.Query().Get("scope"),
		IncludeDemo: r.URL.Query().Get("include_demo") == "true",
		Digest:      r.URL.Query().Get("digest"),
	}
	if renderErr := RenderTempl(w, r, "Trigger Release", pages.ReleaseTrigger(user, s)); renderErr != nil {
		log.Printf("Failed to render release trigger page: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handleReleaseTriggerSubmit resolves the submitted scope into a concrete
// target list (FR1, real logic -- see release_scope.go's
// resolveReleaseScope) and re-renders the form with that resolution as a
// preview.
//
// Deliberately NOT implemented yet (Implementation phase, issue #890):
//   - FR4/NFR5 permission gating (reuse the exact check the promote screen
//     applies -- role_gating_test.go's pattern, against
//     components.EnvironmentPromoterRole(releaseTriggerEnv); the server's
//     own gate is server/handlers/release.go's releaseTriggerEnv const,
//     "dev").
//   - FR2's digest field applying to the resolved target(s) (only sensible
//     when the scope resolves to exactly one target -- needs its own
//     validation).
//   - The actual TriggerRelease call, its FR5 FailedPrecondition
//     ("already releasing") inline rejection, and the success redirect to
//     the status page (TriggerReleaseResponse.release_run_id).
func (app *App) handleReleaseTriggerSubmit(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
		return
	}

	scope := r.FormValue("scope")
	includeDemo := r.FormValue("include_demo") == "true"
	digest := strings.TrimSpace(r.FormValue("digest"))

	s := pages.ReleaseTriggerViewState{
		Scope:       scope,
		IncludeDemo: includeDemo,
		Digest:      digest,
	}

	targets, err := resolveReleaseScope(r.Context(), app.registry, scope, includeDemo)
	if err != nil {
		s.ScopeErr = err.Error()
		app.renderReleaseTrigger(w, r, user, s)
		return
	}
	s.ResolvedTargets = targets

	app.renderReleaseTrigger(w, r, user, s)
}

func (app *App) renderReleaseTrigger(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, s pages.ReleaseTriggerViewState) {
	if renderErr := RenderTempl(w, r, "Trigger Release", pages.ReleaseTrigger(user, s)); renderErr != nil {
		log.Printf("Failed to render release trigger page: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handleReleaseStatus serves the release-status screen (FR10): a durable
// page keyed by release_run_id (path param), rendering GetRelease's
// response -- per-target state, the resolved plan, and error detail.
//
// This is a plain read, so it is fully wired here in the Scaffold phase
// (the same low-risk-read precedent the builds/build-detail screens set --
// GetReleaseRun in handlers_builds.go) -- unlike the trigger screen's
// submit path, there is no write, idempotency, or permission-gating
// complexity to defer. Deliberately NOT wired yet: auto-refresh/polling
// (issue #890 notes no existing in-flight-status screen in this UI has a
// polling precedent to follow -- inventing one is an Implementation-phase
// decision, not a Scaffold one) and the FR6 retry action's actual
// re-submit-to-TriggerRelease wiring (the status page's "Retry" link today
// only prefills the trigger form's scope, per pages.retryHref).
func (app *App) handleReleaseStatus(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	releaseRunID := r.PathValue("id")
	if releaseRunID == "" {
		http.Error(w, "missing release run id", http.StatusBadRequest)
		return
	}

	resp, err := app.registry.Release.GetRelease(r.Context(), &pb.GetReleaseRequest{ReleaseRunId: releaseRunID})
	if err != nil {
		log.Printf("GetRelease(%q) failed: %v", releaseRunID, err)
		s := pages.ReleaseStatusViewState{ReleaseRunID: releaseRunID, LoadErr: grpcErrorMessage(err)}
		if renderErr := RenderTempl(w, r, "Release Status", pages.ReleaseStatus(user, s)); renderErr != nil {
			log.Printf("Failed to render release status page: %v", renderErr)
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	s := pages.ReleaseStatusViewState{ReleaseRunID: releaseRunID, Release: resp}
	if renderErr := RenderTempl(w, r, "Release Status", pages.ReleaseStatus(user, s)); renderErr != nil {
		log.Printf("Failed to render release status page: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
