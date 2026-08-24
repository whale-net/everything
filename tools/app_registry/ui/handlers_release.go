package main

import (
	"context"
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

// handleReleaseTriggerShow renders the initial picker (step 1, issue #889
// follow-up: a domain/app/chart checkbox tree replacing the original
// free-form scope text field). FR4/NFR5: a caller without the promoter role
// sees the read-only denial banner instead of the form
// (handleReleaseTriggerSubmit re-checks the same gate server-round-trip
// side, since a GET-time check alone can never be trusted for the actual
// write -- see releaseTriggerGate's doc comment).
func (app *App) handleReleaseTriggerShow(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	s := pages.ReleaseTriggerViewState{}
	applyReleaseTriggerGate(&s, user)
	if !s.Denied {
		app.loadReleasePickerGroups(r, &s)
	}
	app.renderReleaseTrigger(w, r, user, s)
}

// loadReleasePickerGroups fetches the releasable catalog and groups it by
// domain for the picker (fetchReleasableCatalog, release_scope.go) -- a
// plain read, same RPCs resolveReleaseScope/resolveSelectedTargets already
// make, just surfaced up front now that the picker needs live data to
// render checkboxes rather than accepting free-form text. A fetch failure
// is surfaced as PickErr rather than a raw 500: the picker degrades to an
// empty (but still rendered) tree with an explanatory error, matching this
// screen's existing error-banner pattern for every other failure mode.
func (app *App) loadReleasePickerGroups(r *http.Request, s *pages.ReleaseTriggerViewState) {
	cat, err := fetchReleasableCatalog(r.Context(), app.registry)
	if err != nil {
		s.PickErr = fmt.Sprintf("failed to load releasable apps/charts: %v", err)
		return
	}
	s.PickerGroups = pages.BuildDomainPickerGroups(cat.apps, cat.charts)
}

// handleReleaseTriggerSubmit handles both steps of this screen's form,
// distinguished by the "do" form value:
//
//   - "" (or anything other than "trigger") -- the "Resolve selection"
//     step: resolves the submitted picker checkboxes into a concrete
//     target list (FR1, issue #889 follow-up -- see release_scope.go's
//     resolveSelectedTargets) and re-renders the page with that resolution
//     as the Draft step (pages.releaseDraft), one version-selection row per
//     target. No RPC beyond the read used to resolve -- nothing mutates,
//     safe to hit repeatedly while iterating on the selection.
//   - "trigger" -- the confirm step, submitted from the Draft step's own
//     "Trigger release" button (pages.releaseDraft), which echoes the
//     original picker selections and digest as hidden inputs, plus each
//     target's version-selection fields. Re-resolves the selection
//     (resolution is a deterministic read against current registry state,
//     so re-resolving here rather than trusting a client-echoed target list
//     costs nothing and closes a TOCTOU window against the Draft preview),
//     applies each target's version selection, then calls TriggerRelease
//     and redirects to the new release's status page on success.
//
// FR4/NFR5: gated the same way on both steps (a denied caller never
// resolves a selection preview for a write they can't make, matching
// Rollback's screen). FR5: TriggerRelease's FailedPrecondition ("already
// releasing") response is surfaced as its own specific, inline message
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

	selections := r.Form["target"]
	legacyScope := r.FormValue("scope")
	digest := strings.TrimSpace(r.FormValue("digest"))
	confirmedTrigger := r.FormValue("do") == "trigger"

	s := pages.ReleaseTriggerViewState{
		Digest:   digest,
		Selected: make(map[string]bool, len(selections)),
	}
	for _, tok := range selections {
		s.Selected[tok] = true
	}

	applyReleaseTriggerGate(&s, user)
	if s.Denied {
		app.renderReleaseTrigger(w, r, user, s)
		return
	}
	app.loadReleasePickerGroups(r, &s)

	// targets resolves from one of two shapes: the picker's "target"
	// checkboxes (the normal path, issue #889 follow-up), or a legacy
	// "scope" field with no "target" fields at all -- release_status.templ's
	// Retry action (FR6/FR11) still posts requested_scope's original
	// free-form string directly, predating the picker, and there is no
	// stored per-target selection to reconstruct picker tokens from. Retry
	// therefore still goes through resolveReleaseScope's grammar unchanged,
	// with its own pre-existing documented limitations (include_demo always
	// false, a retried digest-pinned release re-resolves to "build fresh" --
	// see release_status.templ's doc comment on this function's caller).
	var targets []*pb.ReleaseTargetInput
	var err error
	switch {
	case len(selections) > 0:
		targets, err = resolveSelectedTargets(r.Context(), app.registry, selections)
	case legacyScope != "":
		targets, err = resolveReleaseScope(r.Context(), app.registry, legacyScope, false)
	default:
		err = fmt.Errorf("select at least one domain, app, or chart to release")
	}
	if err != nil {
		s.PickErr = err.Error()
		app.renderReleaseTrigger(w, r, user, s)
		return
	}
	s.ResolvedTargets = targets

	// FR2: the digest input only makes sense against a single resolved
	// target -- a batch selection (a domain, or several apps/charts) has no
	// sensible way to apply one digest string to more than one target, so
	// this is rejected as a validation error rather than silently applied
	// to (or silently ignored for) an arbitrary member of the batch.
	if digest != "" {
		if len(targets) != 1 {
			s.DigestErr = fmt.Sprintf("a digest can only be supplied when the selection resolves to exactly one target (resolved %d)", len(targets))
			app.renderReleaseTrigger(w, r, user, s)
			return
		}
		targets[0].Digest = digest
	}

	s.VersionInputs, err = parseTargetVersionInputs(r, targets)
	if err != nil {
		s.SubmitErr = err.Error()
		app.renderReleaseTrigger(w, r, user, s)
		return
	}

	if !confirmedTrigger {
		app.renderReleaseTrigger(w, r, user, s)
		return
	}

	resp, err := app.registry.Release.TriggerRelease(r.Context(), &pb.TriggerReleaseRequest{
		Targets: targets,
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

// parseTargetVersionInputs reads each resolved target's Draft-step version-
// selection form fields (pages.modeFieldName/bumpFieldName/explicitFieldName)
// and applies the chosen value onto that target's own
// ReleaseTargetInput.VersionSelection -- an empty target list produces an
// empty (not nil) map so the Draft page's per-row lookups (s.VersionInputs
// [owner]) render the "bump"/"patch" zero-value default correctly rather
// than panicking on a nil map read (reading a nil map is safe in Go, but
// being explicit here documents the invariant). An "explicit" mode with an
// empty text value is rejected here, before ever reaching TriggerRelease --
// silently falling back to auto-patch-bump would not be what the operator
// who chose "Explicit" intended.
func parseTargetVersionInputs(r *http.Request, targets []*pb.ReleaseTargetInput) (map[string]pages.TargetVersionInput, error) {
	inputs := make(map[string]pages.TargetVersionInput, len(targets))
	for _, t := range targets {
		full := t.GetOwnerFullName()
		mode := r.FormValue("mode__" + full)
		bump := r.FormValue("bump__" + full)
		explicit := strings.TrimSpace(r.FormValue("explicit__" + full))

		if mode == "explicit" {
			if explicit == "" {
				return nil, fmt.Errorf("%s: an explicit version is required when \"Explicit\" is selected", full)
			}
			inputs[full] = pages.TargetVersionInput{Mode: "explicit", Explicit: explicit}
			t.VersionSelection = explicit
			continue
		}

		if bump == "" {
			bump = "patch"
		}
		inputs[full] = pages.TargetVersionInput{Mode: "bump", Bump: bump, Explicit: explicit}
		if bump != "patch" {
			t.VersionSelection = bump
		}
	}
	return inputs, nil
}

func (app *App) renderReleaseTrigger(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, s pages.ReleaseTriggerViewState) {
	if renderErr := RenderTempl(w, r, "Trigger Release", pages.ReleaseTrigger(user, s)); renderErr != nil {
		log.Printf("Failed to render release trigger page: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handleReleaseHistory serves the release-history screen: every release
// attempt ever triggered (ListReleaseAttempts), across every owner, most-
// recent-first. One RPC call per page load, same simplicity as
// handleReconcileRuns/handleBuilds -- no pagination controls yet (no
// precedent for consuming PageResponse.next_page_token anywhere in this UI
// today; the server-side page defaults to 50 rows).
func (app *App) handleReleaseHistory(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.registry.Release.ListReleaseAttempts(r.Context(), &pb.ListReleaseAttemptsRequest{})
	if err != nil {
		log.Printf("ListReleaseAttempts failed: %v", err)
		s := pages.ReleaseHistoryViewState{LoadErr: grpcErrorMessage(err)}
		if renderErr := RenderTempl(w, r, "Release History", pages.ReleaseHistory(user, s)); renderErr != nil {
			log.Printf("Failed to render release history page: %v", renderErr)
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	s := pages.ReleaseHistoryViewState{Releases: resp.GetReleases()}
	if renderErr := RenderTempl(w, r, "Release History", pages.ReleaseHistory(user, s)); renderErr != nil {
		log.Printf("Failed to render release history page: %v", renderErr)
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

	s := pages.ReleaseStatusViewState{
		ReleaseRunID: releaseRunID,
		Release:      resp,
		BuildCommits: app.resolveTargetCommits(r.Context(), resp.GetTargets()),
	}
	if renderErr := RenderTempl(w, r, "Release Status", pages.ReleaseStatus(user, s)); renderErr != nil {
		log.Printf("Failed to render release status page: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// resolveTargetCommits resolves each target's build_id to the commit its
// build was cut from (issue: show which commit a release targets, and link
// it to GitHub, so an operator can catch an accidental wrong-commit release
// before it ships) -- one GetBuild call per DISTINCT build_id among the
// run's targets, bounded by the run's own target count (release.yml's
// matrix keeps that small; same N+1-avoidance rationale as
// ownerIdentityIndex's doc comment in handlers_builds.go, just scoped to one
// run instead of the whole builds list since ListBuilds has no id filter to
// batch this into a single call). A GetBuild failure for one build_id is
// logged and simply omitted from the returned map -- the page renders
// "unknown" for that target rather than failing the whole page over one
// commit link.
func (app *App) resolveTargetCommits(ctx context.Context, targets []*pb.ReleaseRunTarget) map[string]pages.BuildCommitInfo {
	commits := make(map[string]pages.BuildCommitInfo, len(targets))
	for _, t := range targets {
		buildID := t.GetBuildId()
		if buildID == "" {
			continue
		}
		if _, ok := commits[buildID]; ok {
			continue
		}
		resp, err := app.registry.Artifact.GetBuild(ctx, &pb.GetBuildRequest{BuildId: buildID})
		if err != nil {
			log.Printf("GetBuild(%q) failed: %v", buildID, err)
			continue
		}
		sha := resp.GetBuild().GetGitSha()
		if sha == "" {
			continue
		}
		commits[buildID] = pages.BuildCommitInfo{
			GitSha: sha,
			URL:    githubCommitURL(sha),
		}
	}
	return commits
}
