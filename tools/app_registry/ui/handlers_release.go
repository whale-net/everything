package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/components"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// releaseTriggerEnv MUST match server/handlers/release.go's identically
// named constant character for character: TriggerRelease has no
// environment field of its own (releasing builds/publishes artifacts, it
// does not deploy to an environment), so FR4/NFR5's "reuse the exact
// permission Promote already requires" is checked against the lowest-rank
// promoter role every promoter principal is expected to hold.
const releaseTriggerEnv = "dev"

// releaseTriggerGate implements FR4/NFR5's client-side gate for this
// screen, following handleRollbackShow/Submit's pattern
// (handlers_rollback.go) rather than Promote's -- Promote's own handler
// never gates at all and relies solely on the server rejecting an
// unauthorized caller (NFR-14: role gating is presentation-only), but
// Rollback (a single-step, no-dry-run write, same shape as a release
// trigger) pre-checks client-side so a denied caller never even sees a
// preview of a write they can't make. The server's own
// auth.RequirePromoter(ctx, releaseTriggerEnv) call inside TriggerRelease
// is what actually enforces this (NFR-14) -- this check alone is only a
// presentation nicety.
func releaseTriggerGate(user *htmxauth.UserInfo) components.GateDecision {
	return components.GateCellAction(user, components.EnvironmentPromoterRole(releaseTriggerEnv), false)
}

// applyReleaseTriggerGate fills in s.Denied/s.DenyReason from
// releaseTriggerGate's decision, defaulting DenyReason to the standard
// "Requires role: X" message when the gate didn't supply a more specific
// one (mirrors handleRollbackShow/Submit's identical inline logic).
func applyReleaseTriggerGate(s *pages.ReleaseTriggerViewState, user *htmxauth.UserInfo) {
	decision := releaseTriggerGate(user)
	s.Denied = !decision.Allowed
	s.DenyReason = decision.Reason
	if s.DenyReason == "" {
		s.DenyReason = "Requires role: " + decision.MissingRole
	}
}

// handleReleaseTrigger serves the release-trigger screen's GET (render the
// scope-entry form) and POST (resolve the entered scope, and -- once
// confirmed -- actually call TriggerRelease) requests -- mirrors
// handlePromote's GET/POST dispatch (handlers_promote.go).
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
// "scope" query param -- see pages.retryHref) form. FR4/NFR5: a caller
// without the promoter role sees the read-only denial banner instead of the
// form (handleReleaseTriggerSubmit re-checks the same gate server-round-trip
// side, since a GET-time check alone can never be trusted for the actual
// write -- see releaseTriggerGate's doc comment).
func (app *App) handleReleaseTriggerShow(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	s := pages.ReleaseTriggerViewState{
		Scope:       r.URL.Query().Get("scope"),
		IncludeDemo: r.URL.Query().Get("include_demo") == "true",
		Digest:      r.URL.Query().Get("digest"),
	}
	applyReleaseTriggerGate(&s, user)
	app.renderReleaseTrigger(w, r, user, s)
}

// handleReleaseTriggerSubmit handles both steps of this screen's form,
// distinguished by the "do" form value:
//
//   - "" (or anything other than "trigger") -- the "Resolve scope" step:
//     resolves the submitted scope into a concrete target list (FR1, real
//     logic -- see release_scope.go's resolveReleaseScope) and re-renders
//     the form with that resolution as a preview. No RPC call, nothing
//     mutates -- safe to hit repeatedly while iterating on the scope input.
//   - "trigger" -- the confirm step, submitted from the preview's own
//     "Trigger release" button (resolvedTargetsPreview in
//     pages/release_trigger.templ), which echoes the same scope/
//     include_demo/digest fields as hidden inputs. Re-resolves the scope
//     (resolution is a deterministic read against current registry state,
//     so re-resolving here rather than trusting a client-echoed target list
//     costs nothing and closes a TOCTOU window against the preview), then
//     calls TriggerRelease and redirects to the new release's status page
//     on success.
//
// FR4/NFR5: gated the same way on both steps (a denied caller never
// resolves a scope preview for a write they can't make, matching Rollback's
// screen). FR5: TriggerRelease's FailedPrecondition ("already releasing")
// response is surfaced as its own specific, inline message
// (s.SubmitErr) -- never folded into grpcErrorMessage's generic
// "Transport or server failure" default, which has no FailedPrecondition
// case and would otherwise render this exactly like an unrelated backend
// error.
func (app *App) handleReleaseTriggerSubmit(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
		return
	}

	scope := r.FormValue("scope")
	includeDemo := r.FormValue("include_demo") == "true"
	digest := strings.TrimSpace(r.FormValue("digest"))
	confirmedTrigger := r.FormValue("do") == "trigger"

	s := pages.ReleaseTriggerViewState{
		Scope:       scope,
		IncludeDemo: includeDemo,
		Digest:      digest,
	}

	applyReleaseTriggerGate(&s, user)
	if s.Denied {
		app.renderReleaseTrigger(w, r, user, s)
		return
	}

	targets, err := resolveReleaseScope(r.Context(), app.registry, scope, includeDemo)
	if err != nil {
		s.ScopeErr = err.Error()
		app.renderReleaseTrigger(w, r, user, s)
		return
	}
	s.ResolvedTargets = targets

	// FR2: the digest input only makes sense against a single resolved
	// target -- a batch scope ("all"/domain/multi-entry list) has no
	// sensible way to apply one digest string to more than one target, so
	// this is rejected as a validation error rather than silently applied
	// to (or silently ignored for) an arbitrary member of the batch.
	if digest != "" {
		if len(targets) != 1 {
			s.DigestErr = fmt.Sprintf("a digest can only be supplied when the scope resolves to exactly one target (resolved %d)", len(targets))
			app.renderReleaseTrigger(w, r, user, s)
			return
		}
		targets[0].Digest = digest
	}

	if !confirmedTrigger {
		app.renderReleaseTrigger(w, r, user, s)
		return
	}

	resp, err := app.registry.Release.TriggerRelease(r.Context(), &pb.TriggerReleaseRequest{
		RequestedScope: scope,
		Targets:        targets,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
			// FR5: identify the in-flight release explicitly -- the
			// server's rejectIfAlreadyReleasing message already names the
			// conflicting target and release_run_id (see
			// server/handlers/release.go), so this only prefixes it as a
			// distinct, specific rejection rather than a generic failure.
			s.SubmitErr = "Already releasing: " + st.Message()
		} else {
			s.SubmitErr = grpcErrorMessage(err)
		}
		app.renderReleaseTrigger(w, r, user, s)
		return
	}

	http.Redirect(w, r, "/releases/"+resp.GetReleaseRunId(), http.StatusSeeOther)
}

func (app *App) renderReleaseTrigger(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, s pages.ReleaseTriggerViewState) {
	if renderErr := RenderTempl(w, r, "Trigger Release", pages.ReleaseTrigger(user, s)); renderErr != nil {
		log.Printf("Failed to render release trigger page: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handleReleaseStatus serves the release-status screen (FR10): a durable
// page keyed by release_run_id (path param), rendering GetRelease's
// response -- per-target state, the resolved plan, and error detail. This
// is a plain read, unauthenticated like every other read RPC (see
// ARCHITECTURE.md's Authorization table) -- no gating here; the retry
// action embedded on this page (pages.releaseStatusDetail's "Retry" form)
// posts straight to /releases/trigger, which re-applies the FR4/NFR5 gate
// itself.
//
// No polling/auto-refresh precedent exists anywhere in this UI today (the
// scaffold's own doc comment on this function noted the same after
// checking handlers_deployments.go/handlers_drift.go) -- rather than
// invent a new mechanism unilaterally, this screen offers a manual refresh
// link (pages.ReleaseStatus's "Refresh" control, a plain GET back to this
// same URL) as the minimum acceptable per issue #890's own guidance.
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
