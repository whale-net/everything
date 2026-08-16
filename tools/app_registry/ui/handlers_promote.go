package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// promoteKindFromQuery maps the "kind" query/form value the deployments
// matrix (screen 10) and this screen's own hidden field use ("image" |
// "chart", matching the CLI's --kind flag) to the proto enum. An unknown or
// empty value is rejected rather than silently defaulting to IMAGE.
func promoteKindFromQuery(raw string) (pb.ArtifactKind, error) {
	switch raw {
	case "image":
		return pb.ArtifactKind_ARTIFACT_KIND_IMAGE, nil
	case "chart":
		return pb.ArtifactKind_ARTIFACT_KIND_CHART, nil
	default:
		return pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED, &kindError{raw: raw}
	}
}

type kindError struct{ raw string }

func (e *kindError) Error() string {
	return "invalid or missing 'kind' (expected 'image' or 'chart'), got " + e.raw
}

// promoteFingerprint is FR-51's intent identity: a hash of every field a
// change to which must invalidate the idempotency key (entity, environment,
// target artifact, override acknowledgment, reason). It is NEVER the key
// itself, and never derived from anything that can collide across users —
// it is only ever compared against a freshly recomputed value from the same
// browser round-trip to detect "did the form's intent change since this key
// was minted", never persisted or trusted across sessions.
func promoteFingerprint(owner, kind, envKey, artifactID string, overrideAck bool, reason string) string {
	h := sha256.New()
	h.Write([]byte(owner))
	h.Write([]byte{0})
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(envKey))
	h.Write([]byte{0})
	h.Write([]byte(artifactID))
	h.Write([]byte{0})
	if overrideAck {
		h.Write([]byte{1})
	}
	h.Write([]byte{0})
	h.Write([]byte(reason))
	return hex.EncodeToString(h.Sum(nil))
}

// loadPromoteCandidates implements FR-11 ("offer only artifacts the caller
// could legally promote") against a server-side ListArtifacts filter
// (PromotableOnly) that only ever returns PROMOTABILITY_PROMOTABLE rows —
// it silently drops PROMOTABILITY_VIA_CHART, which this screen must still
// offer (with the override acknowledgment, FR-12). Promotability is
// DERIVED once per owner from that owner's DeployUnit (see
// repository.DerivePromotability), so it is uniform across every artifact
// of one owner+kind — this can therefore tell PROMOTABLE apart from
// VIA_CHART/NOT_PROMOTABLE with at most two calls: try the promotable-only
// filter first (the common, no-override case), and only fall back to an
// unfiltered call — inspecting the first row's Promotability, which speaks
// for the whole owner — when that comes back empty.
func loadPromoteCandidates(ctx context.Context, registry *RegistryClient, owner string, kind pb.ArtifactKind) (candidates []*pb.Artifact, requireOverride, notPromotable, noArtifacts bool, err error) {
	promResp, err := registry.Artifact.ListArtifacts(ctx, &pb.ListArtifactsRequest{
		OwnerFullName:  owner,
		Kind:           kind,
		PromotableOnly: true,
	})
	if err != nil {
		return nil, false, false, false, err
	}
	if len(promResp.GetArtifacts()) > 0 {
		return promResp.GetArtifacts(), false, false, false, nil
	}

	allResp, err := registry.Artifact.ListArtifacts(ctx, &pb.ListArtifactsRequest{
		OwnerFullName: owner,
		Kind:          kind,
	})
	if err != nil {
		return nil, false, false, false, err
	}
	all := allResp.GetArtifacts()
	if len(all) == 0 {
		return nil, false, false, true, nil
	}
	// Uniform across the whole owner+kind (see doc comment) — the first row
	// speaks for all of them.
	switch all[0].GetPromotability() {
	case pb.Promotability_PROMOTABILITY_VIA_CHART:
		return all, true, false, false, nil
	default:
		// PROMOTABILITY_NOT_PROMOTABLE, or an unrecognized value — never
		// render an unknown state as promotable (NFR-6).
		return nil, false, true, false, nil
	}
}

// handlePromote serves both screen 50-promote's GET (render, pre-scoped to
// (entity, environment) per FR-6) and POST (dry-run / commit) requests.
func (app *App) handlePromote(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		app.handlePromoteShow(w, r)
		return
	}
	if r.Method == http.MethodPost {
		app.handlePromoteSubmit(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handlePromoteShow renders the initial form. This is the moment (FR-51)
// exactly one idempotency key is minted for the intent this render
// represents — never on a later re-render of the SAME intent.
func (app *App) handlePromoteShow(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	owner := r.URL.Query().Get("owner")
	kindRaw := r.URL.Query().Get("kind")
	envKey := r.URL.Query().Get("env")

	kind, err := promoteKindFromQuery(kindRaw)
	if owner == "" || envKey == "" || err != nil {
		http.Error(w, "missing or invalid 'owner'/'kind'/'env' query parameters", http.StatusBadRequest)
		return
	}

	s := pages.PromoteViewState{Owner: owner, Kind: kindRaw, EnvKey: envKey}

	envResp, err := app.registry.Environment.GetEnvironment(r.Context(), &pb.GetEnvironmentRequest{Key: envKey})
	if err != nil {
		log.Printf("GetEnvironment(%q) failed: %v", envKey, err)
		s.EnvErr = grpcErrorMessage(err)
		if renderErr := RenderTempl(w, r, "Promote", pages.Promote(user, s)); renderErr != nil {
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}
	s.Env = envResp.GetEnvironment()

	candidates, requireOverride, notPromotable, noArtifacts, err := loadPromoteCandidates(r.Context(), app.registry, owner, kind)
	if err != nil {
		log.Printf("loadPromoteCandidates(%q) failed: %v", owner, err)
		s.LoadErr = grpcErrorMessage(err)
		if renderErr := RenderTempl(w, r, "Promote", pages.Promote(user, s)); renderErr != nil {
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}
	s.Candidates = candidates
	s.RequireOverride = requireOverride
	s.NotPromotable = notPromotable
	s.NoArtifacts = noArtifacts

	if len(candidates) > 0 {
		s.SelectedArtifactID = candidates[0].GetArtifactId()
	}
	// FR-51: mint exactly one key, at the moment this commit-capable form is
	// first rendered for this intent (no reason/override yet — those are
	// part of the intent too, and their zero values are what the
	// fingerprint below captures).
	if !notPromotable && !noArtifacts {
		s.IdemKey = uuid.NewString()
		s.IntentFP = promoteFingerprint(owner, kindRaw, envKey, s.SelectedArtifactID, false, "")
	}

	if err := RenderTempl(w, r, "Promote", pages.Promote(user, s)); err != nil {
		log.Printf("Failed to render promote page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handlePromoteSubmit handles both the "Run dry run" and "Promote for real"
// submit buttons (distinguished by the "action" form value) against the
// idempotency-key and dry-run/confirmation lifecycle described in the issue
// (FR-51/FR-52/FR-12/FR-13/FR-14).
func (app *App) handlePromoteSubmit(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
		return
	}

	owner := r.FormValue("owner")
	kindRaw := r.FormValue("kind")
	envKey := r.FormValue("env")
	action := r.FormValue("action")
	artifactID := r.FormValue("artifact_id")
	reason := strings.TrimSpace(r.FormValue("reason"))
	overrideAck := r.FormValue("override_ack") == "true"
	postedIdemKey := r.FormValue("idem_key")
	postedIntentFP := r.FormValue("intent_fp")
	postedDryRunFP := r.FormValue("dryrun_fp")

	kind, err := promoteKindFromQuery(kindRaw)
	if owner == "" || envKey == "" || err != nil {
		http.Error(w, "missing or invalid 'owner'/'kind'/'env' form fields", http.StatusBadRequest)
		return
	}

	s := pages.PromoteViewState{
		Owner:              owner,
		Kind:               kindRaw,
		EnvKey:             envKey,
		SelectedArtifactID: artifactID,
		Reason:             reason,
		OverrideAck:        overrideAck,
	}

	envResp, err := app.registry.Environment.GetEnvironment(r.Context(), &pb.GetEnvironmentRequest{Key: envKey})
	if err != nil {
		log.Printf("GetEnvironment(%q) failed: %v", envKey, err)
		s.EnvErr = grpcErrorMessage(err)
		if renderErr := RenderTempl(w, r, "Promote", pages.Promote(user, s)); renderErr != nil {
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}
	s.Env = envResp.GetEnvironment()

	candidates, requireOverride, notPromotable, noArtifacts, err := loadPromoteCandidates(r.Context(), app.registry, owner, kind)
	if err != nil {
		log.Printf("loadPromoteCandidates(%q) failed: %v", owner, err)
		s.LoadErr = grpcErrorMessage(err)
		if renderErr := RenderTempl(w, r, "Promote", pages.Promote(user, s)); renderErr != nil {
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}
	s.Candidates = candidates
	s.RequireOverride = requireOverride
	s.NotPromotable = notPromotable
	s.NoArtifacts = noArtifacts
	if notPromotable || noArtifacts {
		if renderErr := RenderTempl(w, r, "Promote", pages.Promote(user, s)); renderErr != nil {
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	// FR-51: the key stays stable exactly when the intent this submission
	// represents matches the intent the last render minted/re-armed it for
	// -- covers a validation re-render, a double-submit, and a retry of a
	// failed/timed-out call, all of which resubmit the same hidden fields
	// unchanged. Any field the user actually changed since that render
	// changes the fingerprint below and forces a fresh key.
	currentFP := promoteFingerprint(owner, kindRaw, envKey, artifactID, overrideAck, reason)
	idemKey := postedIdemKey
	if postedIdemKey == "" || postedIntentFP != currentFP {
		idemKey = uuid.NewString()
	}
	s.IdemKey = idemKey
	s.IntentFP = currentFP
	// Carry the prior dry-run verification forward only if it was recorded
	// against exactly this state.
	if postedDryRunFP == currentFP {
		s.DryRunFP = postedDryRunFP
	}

	switch action {
	case "dry_run":
		app.runPromoteDryRun(w, r, user, s, envKey, artifactID, reason, overrideAck, currentFP)
	case "commit":
		app.runPromoteCommit(w, r, user, s, envKey, artifactID, reason, overrideAck, currentFP)
	default:
		http.Error(w, "missing or invalid 'action'", http.StatusBadRequest)
	}
}

func (app *App) runPromoteDryRun(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, s pages.PromoteViewState, envKey, artifactID, reason string, overrideAck bool, currentFP string) {
	resp, err := app.registry.Promotion.Promote(r.Context(), &pb.PromoteRequest{
		EnvironmentKey: envKey,
		ArtifactId:     artifactID,
		Reason:         reason,
		AllowOverride:  overrideAck,
		DryRun:         true,
		IdempotencyKey: s.IdemKey,
	})
	if err != nil {
		log.Printf("Promote(dry_run) failed: %v", err)
		// FR-16: surface the API's own rejection reason inline, preserving
		// entered input -- s already carries every echoed field.
		s.FormErr = grpcErrorMessage(err)
		// This exact state was NOT verified by a successful dry run.
		s.DryRunFP = ""
	} else {
		s.DryRunResult = resp
		s.DryRunFP = currentFP
	}
	if renderErr := RenderTempl(w, r, "Promote", pages.Promote(user, s)); renderErr != nil {
		log.Printf("Failed to render promote page after dry run: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

func (app *App) runPromoteCommit(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, s pages.PromoteViewState, envKey, artifactID, reason string, overrideAck bool, currentFP string) {
	// UI-layer policy validation (FR-12/FR-13/FR-14) -- documented here, not
	// server-enforced beyond "reason required above rank 0"
	// (server/handlers/promotion.go:57-60). None of these calls the RPC, so
	// none of them can ever spend the idempotency key.
	if reason == "" {
		s.FormErr = "Reason is required to promote to any environment (UI policy) — the server itself only enforces this above dev/rank 0."
		app.rerenderPromoteForm(w, r, user, s)
		return
	}
	if s.RequireOverride && !overrideAck {
		s.FormErr = "This target is only reachable via its chart. Check the override acknowledgment to promote it directly."
		app.rerenderPromoteForm(w, r, user, s)
		return
	}
	if s.Env.GetRank() > 0 && s.DryRunFP != currentFP {
		s.FormErr = "Run a dry run against the current form state before promoting for real above dev."
		app.rerenderPromoteForm(w, r, user, s)
		return
	}
	if artifactID == "" {
		s.FormErr = "A version must be chosen before promoting."
		app.rerenderPromoteForm(w, r, user, s)
		return
	}

	resp, err := app.registry.Promotion.Promote(r.Context(), &pb.PromoteRequest{
		EnvironmentKey: envKey,
		ArtifactId:     artifactID,
		Reason:         reason,
		AllowOverride:  overrideAck,
		DryRun:         false,
		IdempotencyKey: s.IdemKey,
	})
	if err != nil {
		log.Printf("Promote(commit) failed: %v", err)
		// The write (and thus the idempotency key) never committed -- the
		// key stays valid for a retry of this exact intent, which is
		// exactly what re-rendering with the SAME s.IdemKey/s.IntentFP
		// achieves here (FR-51's "retry of a call that failed ... is safe").
		s.FormErr = grpcErrorMessage(err)
		app.rerenderPromoteForm(w, r, user, s)
		return
	}

	// The key is now spent -- whether this was a genuine write or an
	// already_promoted short-circuit, runIdempotent's transaction stored a
	// response under it (see idempotency.go), so it must never be reused
	// for a different future intent (issue #575). A later render starts
	// fresh via handlePromoteShow.
	outcome := &pages.CommitOutcome{
		Promotion:       resp.GetPromotion(),
		Superseded:      resp.GetSuperseded(),
		Event:           resp.GetEvent(),
		AlreadyPromoted: resp.GetAlreadyPromoted(),
	}
	if resp.GetAlreadyPromoted() {
		app.attachEarlierEvent(r.Context(), outcome)
	}
	s.Committed = outcome

	if renderErr := RenderTempl(w, r, "Promote", pages.Promote(user, s)); renderErr != nil {
		log.Printf("Failed to render promote confirmation: %v", renderErr)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// attachEarlierEvent implements the already_promoted half of FR-52: resolve
// the EARLIER promotion's most recent event via ListPromotionEvents (an
// existing read RPC, ordered occurred_at DESC per
// server/handlers/promotion.go's ListPromotionEvents — the first row of
// page one is the most recent), and label it explicitly as the earlier
// promotion's, never this submission's. Replay detection must never be
// inferred from comparing timestamps to the UI's own clock (prohibited by
// the issue) — AlreadyPromoted itself is the only signal used here.
func (app *App) attachEarlierEvent(ctx context.Context, outcome *pages.CommitOutcome) {
	promotionID := outcome.Promotion.GetPromotionId()
	if promotionID == "" {
		outcome.EarlierEventUnavailable = true
		return
	}
	resp, err := app.registry.Promotion.ListPromotionEvents(ctx, &pb.ListPromotionEventsRequest{
		PromotionId: promotionID,
		Page:        &pb.PageRequest{PageSize: 1},
	})
	if err != nil || len(resp.GetEvents()) == 0 {
		if err != nil {
			log.Printf("ListPromotionEvents(%q) failed while attributing an already_promoted response: %v", promotionID, err)
		}
		outcome.EarlierEventUnavailable = true
		return
	}
	outcome.EarlierEvent = resp.GetEvents()[0]
}

func (app *App) rerenderPromoteForm(w http.ResponseWriter, r *http.Request, user *htmxauth.UserInfo, s pages.PromoteViewState) {
	if err := RenderTempl(w, r, "Promote", pages.Promote(user, s)); err != nil {
		log.Printf("Failed to render promote page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
