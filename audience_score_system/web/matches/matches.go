// Package matches is `web`'s Channel-scoped pending-matches browse page
// (milestone M4.3, capability C9): serves GET /channels/{id}/matches, the
// `web` mirror of `list_pending_matches`
// (audience_score_system/mcp/tools/matches.go). Handlers and templ views
// live together in this one package, mirroring web/research's (which
// itself mirrors web/schedule's) package doc comment rationale: the read
// flow and its views are tightly coupled with no reuse outside this
// package.
//
// Read-only in #1926 (FR7/FR8/NFR2, plus the "Resolve pending matches"
// link half of FR11); this task (#1927) adds the confirm/reject write
// path (FR9, FR10, NFR1, NFR3). Per the plan's Out of scope, this package
// adds no new store method, migration, or schema change: every read/write
// it needs (store.MatchStore.ListPending/Resolve, store.VideoScriptStore.
// ListByChannel/GetByID, store.Idempotency, plus the same
// store.SyncStore/store.VideoScriptStore/store.VerdictStore enrichment
// mcp/tools/matches.go's renderPendingMatch already performs) already
// exists.
//
// renderMatchVideo/renderMatchScript below deliberately reproduce
// mcp/tools/matches.go's identically-named functions against the same
// stores (LB5: no parallel query, no new store method) rather than
// importing them -- they are unexported in package tools, and the two
// surfaces (MCP JSON output vs. templ view) render different shapes from
// the same read, so a shared exported helper would need to serve both
// callers' output types anyway. HandleResolve's mutate step mirrors
// mcp/tools/matches.go's resolvePendingMatchMutate for the same reason:
// the Channel guard and video_script_id override validation are copied
// rule-for-rule (LB5, one write path -- both surfaces call the IDENTICAL
// store.MatchStore.Resolve), not re-exported, since resolvePendingMatchMutate
// itself is unexported in package tools.
//
// Idempotency (FR10, NFR1): this is `web`'s FIRST write path that must
// call store.Idempotency directly rather than passing a key straight
// through to a store method (unlike web/research's SaveNote/Append/
// Propose, which each accept their own IdempotencyKey field) --
// store.MatchStore.Resolve carries no key parameter of its own. HandleResolve
// wraps the Resolve call in h.store.Idempotency().Do(...), mirroring
// mcp/server.RunIdempotent's (audience_score_system/mcp/server/idempotency.go)
// thin pass-through to the same store.Idempotency.Do, WITHOUT importing
// mcp/server: a web package must not depend on the MCP server layer, so
// the small call is reimplemented here rather than re-exported. See
// resolveToolName's doc comment for why its value is namespaced apart
// from MCP's "resolve_pending_match".
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/matches -- HandleList (FR7, FR8).
//   - POST /channels/{id}/matches/{matchID}/resolve -- HandleResolve
//     (FR9, FR10, NFR1, NFR3).
package matches

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// defaultPendingLimit bounds HandleList's response to the same fixed page
// size as defaultListPendingMatchesLimit (mcp/tools/matches.go), per
// NFR2: no "since", no "load more", no client-driven paging control
// exists anywhere in this package. truncated (from ListPending) is
// surfaced in views.templ as a static note, never a paging control.
const defaultPendingLimit = 50

// Handlers holds the dependencies matches' route needs: the Store (for
// store.CanRead plus Matches()/Sync()/VideoScripts()/Verdicts()/
// Channels()/Roles()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// matchVideoView is the SyncedVideo half of one rendered pending match --
// the view-layer mirror of mcp/tools/matches.go's MatchVideoOutput.
// Metrics fields are nil when no video_metrics row exists yet (never a
// zero -- views.templ renders "no metrics synced yet" for that case).
type matchVideoView struct {
	Title       string
	PublishedAt *time.Time

	Views                      *int64
	AverageViewDurationSeconds *float64
	AverageViewPercentage      *float64
	Impressions                *int64
	ImpressionCTR              *float64
	MetricsMeasuredAt          *time.Time
}

// matchScriptView is a pending match's best-guess video_script -- the
// view-layer mirror of mcp/tools/matches.go's MatchScriptOutput.
// VerdictVersion/Verdict are the BOUND verdict version (LB3: the version
// the script was proposed under, never the Idea's current verdict).
// TargetPublishDate is nil for an undated script -- normal per FR36, not
// an error.
type matchScriptView struct {
	ID                uuid.UUID
	Title             string
	Status            store.VideoScriptStatus
	TargetPublishDate *time.Time
	VerdictVersion    int
	Verdict           store.VerdictValue
}

// pendingMatchView is one pending video_schedule_match assembled for
// List -- views.templ does no store calls itself, matching web/research's
// split (see this file's package doc comment). BestGuessScript is nil
// when the match's video_script_id is nil (a match recorded with no
// plausible candidate at all, see MatchStore.HasMatch's doc on issue
// #1652 -- normal, not an error); Confidence is 0 in that case.
// ResolveForm carries this row's confirm/reject form (FR9, FR10) -- see
// resolveFormData's doc comment.
type pendingMatchView struct {
	MatchID         uuid.UUID
	Video           matchVideoView
	BestGuessScript *matchScriptView
	Confidence      float64
	ResolveForm     resolveFormData
}

// renderMatchVideo resolves syncedVideoID's SyncedVideo plus its latest
// VideoMetrics (if any), reproducing mcp/tools/matches.go's
// renderMatchVideo against the same store.SyncStore (LB5).
func renderMatchVideo(ctx context.Context, sync store.SyncStore, syncedVideoID uuid.UUID) (matchVideoView, error) {
	video, err := sync.GetByID(ctx, syncedVideoID)
	if err != nil {
		return matchVideoView{}, err
	}
	metrics, err := sync.LatestMetricsFor(ctx, syncedVideoID)
	if err != nil {
		return matchVideoView{}, err
	}

	out := matchVideoView{Title: video.Title, PublishedAt: video.PublishedAt}
	if metrics != nil {
		out.Views = metrics.Views
		out.AverageViewDurationSeconds = metrics.AverageViewDurationSeconds
		out.AverageViewPercentage = metrics.AverageViewPercentage
		out.Impressions = metrics.Impressions
		out.ImpressionCTR = metrics.ImpressionCTR
		measuredAt := metrics.MeasuredAt
		out.MetricsMeasuredAt = &measuredAt
	}
	return out, nil
}

// renderMatchScript resolves scriptID's VideoScript and its bound
// Verdict, reproducing mcp/tools/matches.go's renderMatchScript against
// the same store.VideoScriptStore/store.VerdictStore (LB5).
func renderMatchScript(ctx context.Context, videoScripts store.VideoScriptStore, verdicts store.VerdictStore, scriptID uuid.UUID) (matchScriptView, error) {
	script, err := videoScripts.GetByID(ctx, scriptID)
	if err != nil {
		return matchScriptView{}, err
	}
	verdict, err := verdicts.GetByID(ctx, script.VerdictID)
	if err != nil {
		return matchScriptView{}, err
	}
	return matchScriptView{
		ID:                script.ID,
		Title:             script.Title,
		Status:            script.Status,
		TargetPublishDate: script.TargetPublishDate,
		VerdictVersion:    verdict.Version,
		Verdict:           verdict.Verdict,
	}, nil
}

// newIdempotencyKey mints a server-generated idempotency key (FR10),
// created ONCE at render time and carried as a hidden
// <input name="idempotency_key"> on this task's confirm/reject form.
// Copied verbatim from web/research's identically-named/documented
// helper: never client-generated and never derived from the form's own
// content -- two separate GETs of the same page must always mint two
// different keys, so the browser back-button/refresh double-submit case
// (NFR1) is caught by the (tool, person, key) idempotency guard, not by
// content hashing.
func newIdempotencyKey() string {
	return uuid.NewString()
}

// resolveFormData carries the confirm/reject form's (FR9, FR10) current
// values through a render: on a plain GET (HandleList/loadPendingViews,
// via newResolveFormData) it holds MatchID, a freshly minted
// IdempotencyKey, and -- when the match has a best-guess script --
// VideoScriptID pre-selected to that script's ID; on a validation-failure
// or idempotency-conflict re-render from HandleResolve it instead carries
// the SUBMITTED VideoScriptID plus an Error message, with the SAME
// IdempotencyKey the failed POST carried -- so a corrected resubmit is
// still the same logical write (FR10), mirroring web/research's
// noteFormData/verdictFormData contract exactly. There is no Confirm
// field here: which button the client pressed is never trusted as page
// state, only as this request's own form value (NFR3).
type resolveFormData struct {
	MatchID        string
	IdempotencyKey string
	VideoScriptID  string // "" means matcher's best guess / no override.
	Error          string
}

// newResolveFormData mints a fresh resolveFormData for a plain render of
// matchID's row: a freshly minted IdempotencyKey (FR10) and, when
// bestGuessScriptID is non-nil, that script pre-selected as the default
// override selection.
func newResolveFormData(matchID uuid.UUID, bestGuessScriptID *uuid.UUID) resolveFormData {
	form := resolveFormData{MatchID: matchID.String(), IdempotencyKey: newIdempotencyKey()}
	if bestGuessScriptID != nil {
		form.VideoScriptID = bestGuessScriptID.String()
	}
	return form
}

// resolveFormWithError returns a copy of form with Error set to msg,
// mirroring web/research's formWithError/verdictFormWithError convention.
func resolveFormWithError(form resolveFormData, msg string) resolveFormData {
	form.Error = msg
	return form
}

// authorizeWrite is the shared authorization + parse preamble for
// HandleResolve, structurally identical to web/research's
// identically-named/documented helper: resolve the signed-in Person (401
// if none), parse {id} (400 on malformed), load the Channel (404 via
// pgx.ErrNoRows), and re-derive store.CanWrite fresh from Postgres ON
// THIS REQUEST (403 when false) -- never from session state, a hidden
// form field, or which button was rendered (NFR3). ok is false after this
// has already written the appropriate error response; callers MUST
// return immediately when ok is false.
func (h *Handlers) authorizeWrite(w http.ResponseWriter, r *http.Request) (person *store.Person, ch store.Channel, ok bool) {
	ctx := r.Context()
	person = auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return nil, store.Channel{}, false
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return nil, store.Channel{}, false
	}

	ch, err = h.store.Channels().GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return nil, store.Channel{}, false
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, store.Channel{}, false
	}

	// store.CanWrite -- the shared Creator-or-Analyst write authority: an
	// Analyst may resolve a pending match. This is deliberately unlike
	// schedule/script decisions' Creator-tier-only approval gate -- see
	// this package's Testing coverage for the negative that proves that
	// stricter gate never applies to this path.
	canWrite, err := store.CanWrite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, store.Channel{}, false
	}
	if !canWrite {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, store.Channel{}, false
	}

	return person, ch, true
}

// loadPendingViews assembles the identical query set HandleList's GET and
// HandleResolve's validation-failure re-render both need: every pending
// match on channelID rendered as pendingMatchView (each row's
// ResolveForm freshly minted via newResolveFormData), plus truncated and
// the Channel's full VideoScript list (store.VideoScriptStore.ListByChannel
// -- no status filter, backing the override <select>'s options per this
// issue's body: a human may confirm against ANY script on the Channel,
// including an archived one).
func (h *Handlers) loadPendingViews(ctx context.Context, channelID uuid.UUID) ([]pendingMatchView, bool, []store.VideoScript, error) {
	pending, truncated, err := h.store.Matches().ListPending(ctx, channelID, nil, defaultPendingLimit)
	if err != nil {
		return nil, false, nil, err
	}

	sync := h.store.Sync()
	videoScripts := h.store.VideoScripts()
	verdicts := h.store.Verdicts()

	views := make([]pendingMatchView, 0, len(pending))
	for _, m := range pending {
		video, err := renderMatchVideo(ctx, sync, m.SyncedVideoID)
		if err != nil {
			return nil, false, nil, err
		}
		v := pendingMatchView{
			MatchID:     m.ID,
			Video:       video,
			Confidence:  m.Confidence,
			ResolveForm: newResolveFormData(m.ID, m.VideoScriptID),
		}
		if m.VideoScriptID != nil {
			script, err := renderMatchScript(ctx, videoScripts, verdicts, *m.VideoScriptID)
			if err != nil {
				return nil, false, nil, err
			}
			v.BestGuessScript = &script
		}
		views = append(views, v)
	}

	scripts, err := videoScripts.ListByChannel(ctx, channelID)
	if err != nil {
		return nil, false, nil, err
	}

	return views, truncated, scripts, nil
}

// renderList assembles canWrite (a second store.CanWrite call alongside
// HandleList's CanRead, mirroring web/research's renderChannelIndex/
// renderIdeaDetail convention -- gates whether List renders a
// confirm/reject form at all, FR9's presentation-only omission, see
// views.templ) and renders /channels/{id}/matches for views/truncated/
// scripts already loaded by loadPendingViews.
func (h *Handlers) renderList(w http.ResponseWriter, r *http.Request, person *store.Person, ch store.Channel, views []pendingMatchView, truncated bool, scripts []store.VideoScript, status int) {
	ctx := r.Context()

	canWrite, err := store.CanWrite(ctx, h.store.Roles(), ch.ID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	title := ch.Title + " pending matches"
	data := components.LayoutData{Title: title, User: person}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := components.Render(w, r, title, List(data, ch, views, truncated, canWrite, scripts)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleList serves GET /channels/{id}/matches (FR7, FR8). The
// auth/404/403 preamble mirrors web/schedule.Handlers.HandleList and
// research.Handlers.HandleChannelIndex exactly (load-bearing, not
// stylistic, per this issue's body): resolve the signed-in Person (401),
// parse {id} (400), load the Channel (404 on pgx.ErrNoRows -- an unknown
// Channel 404s before authorization can turn it into a 403), then
// store.CanRead (403). Rows come from store.MatchStore.ListPending --
// the exact same store call listPendingMatchesHandler makes, with no
// parallel query (LB5).
func (h *Handlers) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	ch, err := h.store.Channels().GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canRead, err := store.CanRead(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canRead {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	views, truncated, scripts, err := h.loadPendingViews(ctx, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.renderList(w, r, person, ch, views, truncated, scripts, http.StatusOK)
}

// resolveToolName is HandleResolve's store.Idempotency tool_name (FR10,
// NFR1) -- deliberately "web:resolve_pending_match" rather than MCP's bare
// "resolve_pending_match" (mcp/tools/matches.go), so the two surfaces can
// never collide inside mcp_idempotency's (tool_name, person_id,
// idempotency_key) primary key (migration 002): without this namespace, a
// Person resolving a match via `web` and later (or concurrently) via MCP
// under the same idempotency_key would have one surface's call silently
// replay or conflict against the other's, even though they are two
// distinct logical requests.
const resolveToolName = "web:resolve_pending_match"

// resolveFingerprint computes FR10/NFR1's stable request_fingerprint over
// this request's meaningful inputs -- matchID, confirm, and the raw
// video_script_id override string -- mirroring
// mcp/server/idempotency.go's computeFingerprint (a SHA-256 of the tool
// name plus a stable JSON encoding of the inputs) closely enough that a
// DIFFERENT request replaying the same idempotency_key still surfaces
// store.ErrIdempotencyConflict rather than a stale replay. Reimplemented
// here (not imported) per this file's package doc comment: a web package
// must not depend on mcp/server.
func resolveFingerprint(matchID uuid.UUID, confirm bool, videoScriptID string) string {
	body, _ := json.Marshal(struct {
		MatchID       string `json:"match_id"`
		Confirm       bool   `json:"confirm"`
		VideoScriptID string `json:"video_script_id,omitempty"`
	}{MatchID: matchID.String(), Confirm: confirm, VideoScriptID: videoScriptID})
	sum := sha256.Sum256(append([]byte(resolveToolName+"\x00"), body...))
	return hex.EncodeToString(sum[:])
}

// HandleResolve serves POST /channels/{id}/matches/{matchID}/resolve (FR9,
// FR10, NFR1, NFR3): confirms or rejects a pending match through the
// IDENTICAL store.MatchStore.Resolve method resolve_pending_match's
// mutate step calls (mcp/tools/matches.go, LB5 -- one write path, never a
// parallel one), then 303-redirects back to /channels/{id}/matches (post-
// redirect-get, so a refresh re-GETs rather than re-POSTs).
//
// Field handling:
//   - confirm: required, must be exactly "true" or "false" (the two
//     submit buttons' values, see views.templ) -- anything else 400s
//     with nothing written.
//   - video_script_id: optional, confirm only. Rejected (400, nothing
//     written) when set alongside confirm=false, matching
//     resolve_pending_match's identical "video_script_id may only be set
//     when confirm is true" rule. When set, validated for existence +
//     Channel membership ONLY -- no status filter (script.Status is never
//     inspected here), so a human may confirm against ANY script on the
//     Channel, including an archived one (FR40's archive/match
//     interaction note, FR44) -- the primary resolution path for an
//     undated script, which can never auto-link.
//   - idempotency_key: read from the hidden field the rendering GET set
//     (newResolveFormData). Resolve is wrapped in
//     h.store.Idempotency().Do under resolveToolName: a replay (same key,
//     same fingerprint) is a no-op returning the original result (redirect
//     as success, no second state change); store.ErrIdempotencyConflict
//     (same key, different fingerprint) re-renders with a conflict error,
//     never a silent state flip; store.ErrMatchNotPending (already-
//     resolved match, whether via a fresh key or none) re-renders with an
//     explanatory error, never a silent second resolution.
//
// Channel guard: mirrors resolvePendingMatchMutate exactly -- matchID's
// video (via store.SyncStore.GetByID) must belong to the path's {id}, or
// this 404s exactly like an unknown match (never 403, never
// distinguishable from "does not exist"), the same rule
// research.HandleSaveVerdict applies to a cross-Channel Idea.
func (h *Handlers) HandleResolve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person, ch, ok := h.authorizeWrite(w, r)
	if !ok {
		return
	}
	channelID := ch.ID

	matchID, err := uuid.Parse(r.PathValue("matchID"))
	if err != nil {
		http.Error(w, "invalid match id", http.StatusBadRequest)
		return
	}

	m, err := h.store.Matches().GetByID(ctx, matchID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Channel guard, mirroring resolvePendingMatchMutate exactly
	// (mcp/tools/matches.go): a match that exists but belongs to a
	// different Channel than the path's {id} 404s exactly like an unknown
	// match.
	video, err := h.store.Sync().GetByID(ctx, m.SyncedVideoID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if video.ChannelID != channelID {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	form := resolveFormData{
		MatchID:        matchID.String(),
		IdempotencyKey: r.FormValue("idempotency_key"),
		VideoScriptID:  r.FormValue("video_script_id"),
	}

	// renderErr re-renders the WHOLE list (loadPendingViews' identical
	// query set) with ONLY matchID's row carrying the submitted form
	// values plus msg -- every other row gets a freshly minted form, as if
	// it had just been GET-ed, mirroring web/research's HandleSaveVerdict/
	// HandleProposeVideoScript convention of minting fresh keys for every
	// form that was not the one that failed.
	renderErr := func(msg string, status int) {
		views, truncated, scripts, lerr := h.loadPendingViews(ctx, channelID)
		if lerr != nil {
			http.Error(w, lerr.Error(), http.StatusInternalServerError)
			return
		}
		for i := range views {
			if views[i].MatchID == matchID {
				views[i].ResolveForm = resolveFormWithError(form, msg)
			}
		}
		h.renderList(w, r, person, ch, views, truncated, scripts, status)
	}

	rawConfirm := r.FormValue("confirm")
	if rawConfirm != "true" && rawConfirm != "false" {
		renderErr("confirm is required", http.StatusBadRequest)
		return
	}
	confirm := rawConfirm == "true"

	var overrideScriptID *uuid.UUID
	if form.VideoScriptID != "" {
		if !confirm {
			renderErr("video_script_id may only be set when confirm is true", http.StatusBadRequest)
			return
		}
		scriptID, err := uuid.Parse(form.VideoScriptID)
		if err != nil {
			renderErr("invalid video script selection", http.StatusBadRequest)
			return
		}
		script, err := h.store.VideoScripts().GetByID(ctx, scriptID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				renderErr("invalid video script selection", http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Existence + Channel membership ONLY -- deliberately no status
		// filter, matching resolvePendingMatchMutate's identical override
		// validation (see this handler's doc comment).
		if script.ChannelID != channelID {
			renderErr("invalid video script selection", http.StatusBadRequest)
			return
		}
		overrideScriptID = &scriptID
	}

	fingerprint := resolveFingerprint(matchID, confirm, form.VideoScriptID)
	_, _, err = h.store.Idempotency().Do(ctx, resolveToolName, person.ID, form.IdempotencyKey, fingerprint, func(ctx context.Context) (uuid.UUID, error) {
		if err := h.store.Matches().Resolve(ctx, matchID, person.ID, confirm, overrideScriptID); err != nil {
			return uuid.Nil, err
		}
		return matchID, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrIdempotencyConflict):
			renderErr("this request conflicts with an earlier submission using the same idempotency key -- refresh the page and try again", http.StatusConflict)
		case errors.Is(err, store.ErrMatchNotPending):
			renderErr("this match has already been resolved", http.StatusBadRequest)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	http.Redirect(w, r, "/channels/"+channelID.String()+"/matches", http.StatusSeeOther)
}
