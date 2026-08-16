package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/components"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// rollbackPreviewReason is used ONLY on the informational dry-run call this
// screen issues on GET (and on any re-render before a reason has been
// entered) to populate the FR-17 confirmation cards. RollbackRequest's
// reason-required-above-dev check (server/handlers/promotion.go:239) runs
// unconditionally, before the dry_run branch, so a genuinely empty reason
// would block even fetching the preview for rank>0 environments. A dry run
// never writes anything and the reason is never included in a
// RollbackResponse, so this placeholder is never observable by anyone and
// is never sent on the real commit -- FR-13's actual (UI-policy) reason
// requirement is enforced separately, in handleRollbackSubmit, against the
// reason the user actually typed.
const rollbackPreviewReason = "(preview only -- not the recorded reason)"

// rollbackFingerprint is FR-51's intent identity for this screen: a hash of
// every field a change to which must invalidate the idempotency key. There
// is no artifact/override choice on this screen (FR-17: the user never
// supplies a version) so, unlike promoteFingerprint, this covers only
// owner/kind/env/reason.
func rollbackFingerprint(owner, kind, envKey, reason string) string {
	h := sha256.New()
	h.Write([]byte(owner))
	h.Write([]byte{0})
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(envKey))
	h.Write([]byte{0})
	h.Write([]byte(reason))
	return hex.EncodeToString(h.Sum(nil))
}

// handleRollback serves both screen 51-rollback's GET (render, pre-scoped
// to (entity, environment) per FR-6) and POST (commit) requests.
func (app *App) handleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		app.handleRollbackShow(w, r)
		return
	}
	if r.Method == http.MethodPost {
		app.handleRollbackSubmit(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handleRollbackShow renders the confirmation screen. FR-17's target is
// derived entirely from a RollbackRequest.dry_run response -- there is no
// version input anywhere on this screen. This is also the moment (FR-51)
// exactly one idempotency key is minted for the intent this render
// represents -- the informational dry run above never reads or spends it
// (it "short-circuits before the key is read", same as Promote's).
func (app *App) handleRollbackShow(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	owner := r.URL.Query().Get("owner")
	kindRaw := r.URL.Query().Get("kind")
	envKey := r.URL.Query().Get("env")

	kind, err := promoteKindFromQuery(kindRaw)
	if owner == "" || envKey == "" || err != nil {
		http.Error(w, "missing or invalid 'owner'/'kind'/'env' query parameters", http.StatusBadRequest)
		return
	}

	s := pages.RollbackViewState{Owner: owner, Kind: kindRaw, EnvKey: envKey}

	envResp, err := app.registry.Environment.GetEnvironment(r.Context(), &pb.GetEnvironmentRequest{Key: envKey})
	if err != nil {
		log.Printf("GetEnvironment(%q) failed: %v", envKey, err)
		s.EnvErr = grpcErrorMessage(err)
		app.renderRollback(w, r, user, s)
		return
	}
	s.Env = envResp.GetEnvironment()

	// FR-43/44/45 -- and per the issue, the confirmation screen is
	// unreachable by a read-only principal. Check client-side first so a
	// denied caller never sees preview data for a write they can't make,
	// but the server's own auth.RequirePromoter (enforced on every Rollback
	// call, including this informational dry run) is what actually makes
	// this true, not this check alone.
	decision := components.GateCellAction(user, components.EnvironmentPromoterRole(envKey), false)
	s.Denied = !decision.Allowed
	s.DenyReason = decision.Reason
	if s.DenyReason == "" {
		s.DenyReason = "Requires role: " + decision.MissingRole
	}
	if s.Denied {
		app.renderRollback(w, r, user, s)
		return
	}

	app.loadRollbackPreview(r.Context(), &s, owner, kind, envKey)
	if !s.NoHistory && s.LoadErr == "" {
		s.IdemKey = uuid.NewString()
		s.IntentFP = rollbackFingerprint(owner, kindRaw, envKey, "")
	}

	app.renderRollback(w, r, user, s)
}

// loadRollbackPreview issues the informational dry run (see
// rollbackPreviewReason) and fills in s.Target/s.Current from its response,
// or s.NoHistory/s.NoHistoryReason (FR-18) when there is nothing to roll
// back to, or s.LoadErr for anything else (including PermissionDenied, per
// FR-45 -- grpcErrorMessage renders that distinctly from a transport
// failure).
func (app *App) loadRollbackPreview(ctx context.Context, s *pages.RollbackViewState, owner string, kind pb.ArtifactKind, envKey string) {
	resp, err := app.registry.Promotion.Rollback(ctx, &pb.RollbackRequest{
		EnvironmentKey: envKey,
		OwnerFullName:  owner,
		Kind:           kind,
		Reason:         rollbackPreviewReason,
		DryRun:         true,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
			// Rollback's only FailedPrecondition source is "no prior
			// promotion to roll back to" (server/handlers/promotion.go:247)
			// -- the environment.Archived check Promote has doesn't exist
			// on this RPC. FR-18: state the reason inline, never just grey
			// out the control silently.
			s.NoHistory = true
			s.NoHistoryReason = st.Message()
			return
		}
		log.Printf("Rollback(dry_run preview, owner=%q env=%q) failed: %v", owner, envKey, err)
		s.LoadErr = grpcErrorMessage(err)
		return
	}
	s.Target = resp.GetPromotion()
	s.Current = resp.GetSuperseded()
}

// handleRollbackSubmit handles the single "Confirm rollback" submit against
// the idempotency-key lifecycle described in the issue (FR-51/FR-52,
// reused from #647) and the UI-layer reason policy (FR-13).
func (app *App) handleRollbackSubmit(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
		return
	}

	owner := r.FormValue("owner")
	kindRaw := r.FormValue("kind")
	envKey := r.FormValue("env")
	reason := strings.TrimSpace(r.FormValue("reason"))
	postedIdemKey := r.FormValue("idem_key")
	postedIntentFP := r.FormValue("intent_fp")

	kind, err := promoteKindFromQuery(kindRaw)
	if owner == "" || envKey == "" || err != nil {
		http.Error(w, "missing or invalid 'owner'/'kind'/'env' form fields", http.StatusBadRequest)
		return
	}

	s := pages.RollbackViewState{
		Owner:  owner,
		Kind:   kindRaw,
		EnvKey: envKey,
		Reason: reason,
	}

	envResp, err := app.registry.Environment.GetEnvironment(r.Context(), &pb.GetEnvironmentRequest{Key: envKey})
	if err != nil {
		log.Printf("GetEnvironment(%q) failed: %v", envKey, err)
		s.EnvErr = grpcErrorMessage(err)
		app.renderRollback(w, r, user, s)
		return
	}
	s.Env = envResp.GetEnvironment()

	decision := components.GateCellAction(user, components.EnvironmentPromoterRole(envKey), false)
	s.Denied = !decision.Allowed
	s.DenyReason = decision.Reason
	if s.DenyReason == "" {
		s.DenyReason = "Requires role: " + decision.MissingRole
	}
	if s.Denied {
		app.renderRollback(w, r, user, s)
		return
	}

	app.loadRollbackPreview(r.Context(), &s, owner, kind, envKey)
	if s.NoHistory || s.LoadErr != "" {
		app.renderRollback(w, r, user, s)
		return
	}

	// FR-51: same stability rule as Promote -- the key stays put across a
	// validation re-render / double-submit / retry of the same intent
	// (unchanged reason), and is regenerated the moment the user changes
	// the reason since that changes the fingerprint below.
	currentFP := rollbackFingerprint(owner, kindRaw, envKey, reason)
	idemKey := postedIdemKey
	if postedIdemKey == "" || postedIntentFP != currentFP {
		idemKey = uuid.NewString()
	}
	s.IdemKey = idemKey
	s.IntentFP = currentFP

	// FR-13/NFR-7: reason is required on rollback too, as a UI-layer
	// policy -- Rollback is identically permissive at rank 0 server-side.
	// This never calls the RPC, so it never spends the idempotency key.
	if reason == "" {
		s.FormErr = "Reason is required to roll back (UI policy) -- the server itself only enforces this above dev/rank 0."
		app.renderRollback(w, r, user, s)
		return
	}

	resp, err := app.registry.Promotion.Rollback(r.Context(), &pb.RollbackRequest{
		EnvironmentKey: envKey,
		OwnerFullName:  owner,
		Kind:           kind,
		Reason:         reason,
		DryRun:         false,
		IdempotencyKey: s.IdemKey,
	})
	if err != nil {
		log.Printf("Rollback(commit) failed: %v", err)
		// The write (and thus the idempotency key) never committed -- the
		// key stays valid for a retry of this exact intent (same
		// s.IdemKey/s.IntentFP on the re-render).
		s.FormErr = grpcErrorMessage(err)
		app.renderRollback(w, r, user, s)
		return
	}

	s.Committed = &pages.RollbackOutcome{
		Promotion:  resp.GetPromotion(),
		Superseded: resp.GetSuperseded(),
		Event:      resp.GetEvent(),
	}
	app.renderRollback(w, r, user, s)
}

func (app *App) renderRollback(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, s pages.RollbackViewState) {
	if err := RenderTempl(w, r, "Rollback", pages.Rollback(user, s)); err != nil {
		log.Printf("Failed to render rollback page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
