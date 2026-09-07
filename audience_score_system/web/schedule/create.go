package schedule

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// zeroActiveStrategyMessage is FR13's exact, persona-neutral message
// rendered (200, not a form error) when a Channel has no active
// Strategy: create.go / newScriptForm block the entire form rather than
// offering an empty Strategy picker that could only ever fail at submit.
// Deliberately names the MCP tool a Creator OR Analyst can both call
// (save_strategy is gated by the same store.CanWrite tier as script
// propose -- confirmed for Analyst via
// mcp/tools/strategy_integration_test.go) and never says or implies "ask
// your Creator" -- any persona reaching this page can resolve it
// themselves. No inline Strategy-create affordance is added here:
// Strategy authoring on `web` is out of scope for this batch.
const zeroActiveStrategyMessage = "This Channel has no active Strategy -- create one via the MCP `save_strategy` tool before proposing a script."

// scriptFormData carries the create-video-script form's (#2036, FR13-FR15)
// current values through a render: on a plain GET (HandleNewScript, via
// newScriptFormData) it holds nothing but a freshly minted IdempotencyKey
// (newIdempotencyKey, FR18/NFR2); on a validation-failure re-render from
// HandleCreateScript it additionally carries the submitted
// VerdictID/StrategyID/Title/ScriptText and an Error message, with the
// SAME IdempotencyKey the failed POST carried -- so a corrected resubmit
// is still the same logical write. Mirrors
// web/research.Handlers' proposeFormData contract exactly, with one
// addition: VerdictID IS a form field here (unlike proposeFormData's
// deliberate omission), because this page is not scoped to a single Idea
// the way the Idea-detail propose form is -- the user picks which of the
// Channel's Ideas' viable current verdicts to propose from (FR13).
//
// Preview (FR14) is an explicit edit/rendered-preview mode toggle, not
// two different fields: switching modes (HandleCreateScript's
// submit_action="preview"/"edit" branches) re-renders this SAME form with
// every submitted field preserved and NO store call, so unsaved
// ScriptText is never lost. PreviewHTML is populated by renderNewScript
// ONLY when Preview is true -- it is never a submitted field (there is no
// such form input), just the sanitized (markdown.go's renderScriptMarkdown)
// rendering of ScriptText computed fresh for that one render.
type scriptFormData struct {
	VerdictID      string // raw submitted value; "" on a plain GET.
	StrategyID     string // raw submitted value; "" on a plain GET.
	Title          string
	ScriptText     string
	IdempotencyKey string
	Preview        bool   // FR14: editor vs. rendered-preview mode.
	PreviewHTML    string // computed, only set when Preview is true.
	Error          string
}

// newScriptFormData mints a fresh scriptFormData for a plain render -- no
// submitted content, just a freshly minted IdempotencyKey (FR18/NFR2).
// Used by HandleNewScript's GET, mirroring
// web/research.newProposeFormData's identical rationale.
func newScriptFormData() scriptFormData {
	return scriptFormData{IdempotencyKey: newIdempotencyKey()}
}

// newEditFormData mints scriptFormData for a fresh edit-mode GET render
// (#2037, FR16, FR18/NFR2): the script's CURRENT title/script_text as the
// starting edit values, plus a freshly minted IdempotencyKey -- mirrors
// newScriptFormData's rationale exactly, adapted for editing an existing
// row instead of creating one. VerdictID/StrategyID are left "" and
// ignored by scriptEditForm -- an edit never changes which verdict/
// strategy a script is bound to.
func newEditFormData(script store.VideoScript) scriptFormData {
	return scriptFormData{
		Title:          script.Title,
		ScriptText:     script.ScriptText,
		IdempotencyKey: newIdempotencyKey(),
	}
}

// scriptFreezeReason returns FR17's status-specific indicator text once a
// video_script has left the editable window store.VideoScriptStore.
// UpdateContent enforces (status != proposed OR published) -- "" when the
// script is still editable. This function is PRESENTATION ONLY: it must
// never disagree with UpdateContent's own combined check (see that
// method's doc comment), but it does not itself enforce anything --
// HandleUpdateScript rejects an edit attempt via UpdateContent's returned
// error regardless of what this renders (see that handler and
// store.ErrVideoScriptDecided).
//
// Distinguishes WHY a script is frozen rather than one generic read-only
// string:
//   - published (reachable while status is STILL 'proposed', via the
//     match-confirm-then-sync path UpdateContent's doc comment describes)
//     takes priority over status -- it is the more specific, more current
//     truth, including for a script that is ALSO greenlit.
//   - greenlit, not (yet) published -> "approved, awaiting publish".
//   - denied -> "denied".
//   - archived -> "archived".
func scriptFreezeReason(status store.VideoScriptStatus, published bool) string {
	if published {
		return "This script's video has already been published, so it can no longer be edited."
	}
	switch status {
	case store.VideoScriptStatusGreenlit:
		return "This script has been approved and is awaiting publish, so it can no longer be edited."
	case store.VideoScriptStatusDenied:
		return "This script was denied, so it can no longer be edited."
	case store.VideoScriptStatusArchived:
		return "This script has been archived, so it can no longer be edited."
	default:
		return ""
	}
}

// newIdempotencyKey mints a server-generated idempotency key (FR18/NFR2),
// created ONCE at render time and carried as a hidden
// <input name="idempotency_key"> on the create form, threaded to
// store.ProposeVideoScriptInput.IdempotencyKey exactly as Propose's
// existing dedupe expects. Never client-generated and never derived from
// the form's own content, so a browser back-button/refresh double-submit
// is caught by Propose's dedupe, not by content hashing. Mirrors
// web/research.newIdempotencyKey's identical rationale and
// implementation -- duplicated here rather than exported cross-package,
// since both are one-line uuid.NewString() wrappers scoped to their own
// package's form-data types.
func newIdempotencyKey() string {
	return uuid.NewString()
}

// viableVerdictOption is one Idea's CURRENT verdict, included in the
// create form's verdict picker only when that verdict is
// store.VerdictViable (FR13). Resolved via viableVerdictOptions below.
type viableVerdictOption struct {
	VerdictID uuid.UUID
	IdeaID    uuid.UUID
	IdeaTitle string
	Version   int
}

// viableVerdictOptions returns every CURRENT verdict across channelID's
// Ideas that is store.VerdictViable (FR13's picker universe) -- an Idea
// with no verdict yet, or whose current verdict is not-viable or
// needs-more-research, contributes no option at all. Backed by
// store.BrowseStore.IdeasWithCurrentVerdict (limit=0, unbounded -- this
// picker must never silently omit a viable verdict because the Channel
// happens to have a lot of Ideas), the SAME "current, not any stale
// version" resolution get_channel_overview uses (FR24) -- this page and
// that overview can never disagree about which verdict version is "the"
// verdict for an Idea. No new VideoScriptStore or VerdictStore method:
// this composes an existing cross-entity read.
//
// Called identically from HandleNewScript's render (via renderNewScript)
// and HandleCreateScript's POST-time membership check -- never a
// client-trusted hidden field -- so a not-viable verdict, or one whose
// Idea's verdict has since changed, can neither be offered NOR accepted,
// even if a client forges the verdict_id field.
func (h *Handlers) viableVerdictOptions(ctx context.Context, channelID uuid.UUID) ([]viableVerdictOption, error) {
	overviews, _, err := h.store.Browse().IdeasWithCurrentVerdict(ctx, channelID, 0)
	if err != nil {
		return nil, err
	}
	options := make([]viableVerdictOption, 0, len(overviews))
	for _, o := range overviews {
		if o.CurrentVerdict == nil || *o.CurrentVerdict != store.VerdictViable {
			continue
		}
		options = append(options, viableVerdictOption{
			VerdictID: *o.CurrentVerdictID,
			IdeaID:    o.ID,
			IdeaTitle: o.Title,
			Version:   *o.CurrentVerdictVersion,
		})
	}
	return options, nil
}

// authorizeWrite is create.go's authorization + parse preamble for
// HandleCreateScript: resolve the signed-in Person (401 if none --
// RequireSignedIn should already guarantee this), parse {id} (400 on
// malformed), load the Channel (404 via pgx.ErrNoRows), and re-derive
// store.CanWrite fresh from Postgres ON THIS REQUEST (403 when false) --
// never from session state, a hidden form field, or which button the
// client rendered (FR19, NFR4). Mirrors
// web/research.Handlers.authorizeWrite's identical contract and order of
// operations -- duplicated here rather than exported cross-package, since
// both are small, package-scoped helpers (see schedule.go's package doc
// comment addendum for why this authoring surface lives in THIS package).
// ok is false after this has already written the appropriate error
// response; callers MUST return immediately when ok is false.
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

// HandleNewScript serves GET /channels/{id}/scripts/new (#2036, FR13,
// FR14): the create-video-script form -- a viable-verdict picker, an
// active-Strategy picker (or FR13's zero-active-Strategy blocked-form
// message), title/script-text fields, and FR14's markdown editor/preview
// toggle. Order of operations mirrors HandleList's exactly (load-bearing,
// not stylistic): an unknown Channel 404s before store.CanRead ever runs,
// so an unknown id never leaks as 403.
func (h *Handlers) HandleNewScript(w http.ResponseWriter, r *http.Request) {
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

	// canWrite (Creator-or-Analyst, FR19) gates whether the rendered page
	// includes the create form at all -- read access to the page (above)
	// uses the three-tier store.CanRead, presentation of the create
	// affordance uses store.CanWrite, mirroring HandleList's
	// canApprove-gates-affordances pattern one tier down. store/authz.go
	// currently defines CanRead and CanWrite identically (both
	// Founder/Co-Creator/Analyst), so a CanRead-but-not-CanWrite persona
	// is not reachable today -- this check is still re-derived fresh and
	// kept as real defense in depth, not dead code, in case that ever
	// changes.
	canWrite, err := store.CanWrite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.renderNewScript(w, r, person, ch, canWrite, newScriptFormData(), http.StatusOK)
}

// renderNewScript loads FR13's two pickers (viable verdicts, active
// Strategies) fresh on every render -- never cached across GET/POST --
// computes form.PreviewHTML when form.Preview is true (FR14), and renders
// NewScript. canWrite gates whether the form itself appears at all
// (FR19); when it does, an empty active-Strategy list additionally blocks
// the form with FR13's zeroActiveStrategyMessage (views.templ's
// newScriptForm), UNLESS form.Error is already set (NFR3-style: a
// Strategy deactivated between render and submit must not silently drop
// the user's other field values and rejection reason).
func (h *Handlers) renderNewScript(w http.ResponseWriter, r *http.Request, person *store.Person, ch store.Channel, canWrite bool, form scriptFormData, status int) {
	ctx := r.Context()

	verdicts, err := h.viableVerdictOptions(ctx, ch.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	activeStrategies, _, err := h.store.Strategies().ListByChannel(ctx, ch.ID, true, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if form.Preview {
		html, err := renderScriptMarkdown(form.ScriptText)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		form.PreviewHTML = html
	}

	title := ch.Title + " -- new video script"
	data := components.LayoutData{Title: title, User: person}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := components.Render(w, r, title, NewScript(data, ch, verdicts, activeStrategies, canWrite, form)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleCreateScript serves POST /channels/{id}/scripts (#2036, FR13,
// FR14, FR18/NFR2, FR19): the create form's submit target, dispatched by
// the submitted submit_action field:
//
//   - "preview"/"edit" (FR14): toggle form.Preview and re-render this SAME
//     form with every submitted field preserved -- no store call at all,
//     so switching modes can never lose unsaved ScriptText and never
//     rotates or consumes the idempotency key.
//   - anything else (the "Create script" submit, or an omitted field):
//     validates and, on success, calls the IDENTICAL
//     store.VideoScriptStore.Propose method save_video_script's mutate
//     step and web/research.Handlers.HandleProposeVideoScript's Idea-page
//     entry point both call (LB5 -- one write path, never a parallel
//     one), gated by store.CanWrite (Creator-or-Analyst, via
//     authorizeWrite), never store.CanApprove.
//
// Field handling:
//   - verdict_id: required, must parse as a UUID AND be a member of THIS
//     request's freshly-resolved viableVerdictOptions (FR13) -- a
//     not-viable/needs-more-research verdict, or one belonging to another
//     Channel, is rejected here with NO write, before Propose is ever
//     called (Propose's own ErrVerdictNotViable check is defense in
//     depth, not the only gate).
//   - strategy_id: required, must parse as a UUID AND be a member of THIS
//     request's freshly-resolved active Strategy list (FR13) -- an
//     inactive or cross-Channel strategy_id is rejected here with NO
//     write. This check is NOT duplicated by
//     store.VideoScriptStore.Propose, which only confirms the Strategy
//     exists on the Channel, not that it is active -- this handler is the
//     one place "active" is enforced for the create flow.
//   - title / script_text: required, non-empty after strings.TrimSpace.
//   - idempotency_key: read from the hidden field the rendering GET set
//     (newScriptFormData); if absent, treated as empty, so Propose simply
//     does not dedupe rather than this handler inventing a key
//     server-side per submit (mirrors HandleSaveNote's/
//     HandleProposeVideoScript's identical rationale).
//
// Any validation failure, and any error Propose itself returns, re-renders
// this form (400) with a form error and the submitted values preserved,
// carrying the SAME idempotency_key the failed POST carried -- so a
// corrected resubmit stays one logical write. Success redirects (303) to
// the new script's detail page (FR15).
func (h *Handlers) HandleCreateScript(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person, ch, ok := h.authorizeWrite(w, r)
	if !ok {
		return
	}
	channelID := ch.ID

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	form := scriptFormData{
		IdempotencyKey: r.FormValue("idempotency_key"),
		VerdictID:      r.FormValue("verdict_id"),
		StrategyID:     r.FormValue("strategy_id"),
		Title:          r.FormValue("title"),
		ScriptText:     r.FormValue("script_text"),
	}

	// FR14's edit/preview toggle: a plain re-render of this SAME form
	// with every submitted field preserved. No store call, no
	// idempotency-key rotation -- a later "Create script" submit from
	// either mode is still one logical write.
	switch r.FormValue("submit_action") {
	case "preview":
		form.Preview = true
		h.renderNewScript(w, r, person, ch, true, form, http.StatusOK)
		return
	case "edit":
		form.Preview = false
		h.renderNewScript(w, r, person, ch, true, form, http.StatusOK)
		return
	}

	renderErr := func(msg string) {
		form.Error = msg
		h.renderNewScript(w, r, person, ch, true, form, http.StatusBadRequest)
	}

	// FR13: re-resolve both pickers fresh here -- never trusted from the
	// form's own hidden state -- so a not-viable/needs-more-research
	// verdict, a cross-Channel verdict, or an inactive/cross-Channel
	// Strategy is rejected with NO write, before Propose ever runs.
	verdicts, err := h.viableVerdictOptions(ctx, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	verdictID, err := uuid.Parse(form.VerdictID)
	if err != nil {
		renderErr("invalid verdict selection -- choose one of this Channel's current viable verdicts")
		return
	}
	verdictFound := false
	for _, v := range verdicts {
		if v.VerdictID == verdictID {
			verdictFound = true
			break
		}
	}
	if !verdictFound {
		renderErr("invalid verdict selection -- only this Channel's current viable verdicts may be used")
		return
	}

	activeStrategies, _, err := h.store.Strategies().ListByChannel(ctx, channelID, true, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	strategyID, err := uuid.Parse(form.StrategyID)
	if err != nil {
		renderErr("invalid strategy selection")
		return
	}
	strategyFound := false
	for _, s := range activeStrategies {
		if s.ID == strategyID {
			strategyFound = true
			break
		}
	}
	if !strategyFound {
		renderErr("invalid strategy selection -- only this Channel's active Strategies may be used")
		return
	}

	title := strings.TrimSpace(form.Title)
	if title == "" {
		renderErr("title is required")
		return
	}

	scriptText := strings.TrimSpace(form.ScriptText)
	if scriptText == "" {
		renderErr("script text is required")
		return
	}

	script, err := h.store.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID:         channelID,
		VerdictID:         verdictID,
		StrategyID:        strategyID,
		Title:             title,
		ScriptText:        scriptText,
		CreatedByPersonID: person.ID,
		IdempotencyKey:    form.IdempotencyKey,
	})
	if err != nil {
		// store.ErrVerdictNotViable/store.ErrStrategyNotFound (defense in
		// depth -- the membership checks above should already have caught
		// these) and any other error from Propose all re-render with a
		// form error, never a 500, mirroring
		// HandleProposeVideoScript's renderErr(err.Error()) convention.
		renderErr(err.Error())
		return
	}

	http.Redirect(w, r, "/channels/"+channelID.String()+"/scripts/"+script.ID.String(), http.StatusSeeOther)
}

// HandleScriptDetail serves GET /channels/{id}/scripts/{scriptID}
// (#2036, FR14, FR15; #2037, FR16, FR17): the per-script authoring page --
// the script's current body in the SAME markdown editor/preview toggle as
// the create form (here a plain ?preview=1 query-string toggle for the
// read-only render, since that content is static -- there is no unsaved
// text to lose), FR15's link back to the bound verdict (its Idea, version,
// and value), and -- new in #2037 -- an in-place edit mode
// (?edit=1) that swaps the read-only body for scriptEditForm, but ONLY
// while the script is still editable (FR16/FR17): status = 'proposed' AND
// not published. Order of operations mirrors HandleList's exactly: an
// unknown Channel or script 404s before/regardless of authorization, and
// a script that exists but belongs to a different Channel than the
// path's {id} 404s exactly like an unknown script (mirrors
// HandleIdeaDetail's identical cross-Channel rule in web/research) --
// never 403, never distinguishable from "does not exist".
//
// canRead (store.CanRead) gates the page itself, same three-tier
// visibility as HandleList; canWrite (store.CanWrite, FR19) is derived
// separately and gates ONLY whether an edit affordance can ever be
// rendered -- renderScriptDetail additionally requires the script be
// Editable before actually entering edit mode, so a manually-appended
// ?edit=1 on a frozen script, or one from a CanRead-but-not-CanWrite
// Analyst-tier-below persona (not reachable today, see authorizeWrite's
// identical note), silently falls back to the read-only render rather than
// ever exposing an edit form that would just 409 on submit.
func (h *Handlers) HandleScriptDetail(w http.ResponseWriter, r *http.Request) {
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

	scriptID, err := uuid.Parse(r.PathValue("scriptID"))
	if err != nil {
		http.Error(w, "invalid video script id", http.StatusBadRequest)
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

	script, err := h.store.VideoScripts().GetByID(ctx, scriptID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if script.ChannelID != channelID {
		http.NotFound(w, r)
		return
	}

	// canWrite (Creator-or-Analyst, FR19) gates whether the Edit
	// affordance/edit mode can appear at all -- re-derived fresh on every
	// request, never cached, mirroring HandleNewScript's identical
	// canWrite derivation one route over.
	canWrite, err := store.CanWrite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	editRequested := r.URL.Query().Get("edit") == "1"
	readPreview := !editRequested && r.URL.Query().Get("preview") == "1"

	var form scriptFormData
	if editRequested {
		form = newEditFormData(script)
	}

	h.renderScriptDetail(w, r, person, ch, script, canWrite, editRequested, form, readPreview, http.StatusOK)
}

// renderScriptDetail is HandleScriptDetail's and HandleUpdateScript's
// shared render body (#2037): loads FR15's verdict/idea link fields fresh,
// computes FR16/FR17's published/freeze state fresh (never cached across
// GET/POST, and never trusted from a caller's stale copy), computes
// whichever preview HTML is relevant for the active mode, and renders
// ScriptDetail exactly once. canWrite is passed in rather than re-derived
// here since every caller already has it from its own authorization step
// (HandleScriptDetail's store.CanRead+CanWrite pair, or
// authorizeScriptWrite's CanWrite).
//
// requestedEditMode is the CALLER's intent (an explicit ?edit=1, or "stay
// in edit mode" after a preview toggle/validation/freeze error on
// HandleUpdateScript's POST) -- the view's actual EditMode is that AND
// canWrite AND Editable, so this function is the single place that can
// never render an edit form for a script that is not, right now, both
// writable by this caller and still in its editable window.
func (h *Handlers) renderScriptDetail(w http.ResponseWriter, r *http.Request, person *store.Person, ch store.Channel, script store.VideoScript, canWrite bool, requestedEditMode bool, form scriptFormData, readPreview bool, status int) {
	ctx := r.Context()

	// verdict is FR15's load-bearing link target: the SPECIFIC verdict
	// version this script is bound to (LB3), never just the idea's
	// current verdict, which may have changed since.
	verdict, err := h.store.Verdicts().GetByID(ctx, script.VerdictID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	idea, err := h.store.Ideas().GetByID(ctx, script.IdeaID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	published, err := h.store.VideoScripts().IsPublished(ctx, script.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	freezeReason := scriptFreezeReason(script.Status, published)
	editable := freezeReason == ""

	if requestedEditMode && form.Preview {
		html, err := renderScriptMarkdown(form.ScriptText)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		form.PreviewHTML = html
	}

	var previewHTML string
	if readPreview {
		previewHTML, err = renderScriptMarkdown(script.ScriptText)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	view := scriptDetailView{
		Ch:      ch,
		Script:  script,
		Idea:    idea,
		Verdict: verdict,

		Preview:     readPreview,
		PreviewHTML: previewHTML,

		CanWrite:     canWrite,
		Editable:     editable,
		FreezeReason: freezeReason,
		EditMode:     requestedEditMode && canWrite && editable,
		Form:         form,
	}

	title := script.Title
	data := components.LayoutData{Title: title, User: person}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := components.Render(w, r, title, ScriptDetail(data, view)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// authorizeScriptWrite is HandleUpdateScript's authorization + parse
// preamble (#2037, FR19, NFR4): resolve the signed-in Person (401), parse
// {id} and {scriptID} (400 on either malformed), load the Channel (404 via
// pgx.ErrNoRows) and the script (404 via pgx.ErrNoRows), 404 again if the
// script's own ChannelID does not match the path's {id} (mirrors
// HandleScriptDetail's identical cross-Channel rule -- never
// distinguishable from "does not exist"), and re-derive store.CanWrite
// fresh from Postgres ON THIS REQUEST (403 when false) -- never from
// session state, a hidden form field, or which button the client rendered.
//
// This is the TIER authorization check ONLY (FR19, "WHO"). The orthogonal
// status/published freeze (FR16/FR17, "WHEN") is deliberately NOT checked
// here -- it is enforced solely inside store.VideoScriptStore.UpdateContent
// itself (ErrVideoScriptDecided), so there is exactly one place that
// decides editability, never two that could drift apart. This also covers
// FR19's orthogonality note: a Creator who greenlit the very script they
// are now trying to edit still passes THIS check (they hold CanWrite) and
// is frozen only by UpdateContent's own status gate, not by anything here.
func (h *Handlers) authorizeScriptWrite(w http.ResponseWriter, r *http.Request) (person *store.Person, ch store.Channel, script store.VideoScript, ok bool) {
	ctx := r.Context()
	person = auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return nil, store.Channel{}, store.VideoScript{}, false
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return nil, store.Channel{}, store.VideoScript{}, false
	}

	scriptID, err := uuid.Parse(r.PathValue("scriptID"))
	if err != nil {
		http.Error(w, "invalid video script id", http.StatusBadRequest)
		return nil, store.Channel{}, store.VideoScript{}, false
	}

	ch, err = h.store.Channels().GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return nil, store.Channel{}, store.VideoScript{}, false
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, store.Channel{}, store.VideoScript{}, false
	}

	script, err = h.store.VideoScripts().GetByID(ctx, scriptID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return nil, store.Channel{}, store.VideoScript{}, false
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, store.Channel{}, store.VideoScript{}, false
	}
	if script.ChannelID != channelID {
		http.NotFound(w, r)
		return nil, store.Channel{}, store.VideoScript{}, false
	}

	canWrite, err := store.CanWrite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, store.Channel{}, store.VideoScript{}, false
	}
	if !canWrite {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, store.Channel{}, store.VideoScript{}, false
	}

	return person, ch, script, true
}

// HandleUpdateScript serves POST /channels/{id}/scripts/{scriptID}
// (#2037, FR16, FR18/NFR2, FR19): the edit form's submit target, dispatched
// by the submitted submit_action field, mirroring HandleCreateScript's
// preview/edit/save dispatch exactly:
//
//   - "preview"/"edit" (reuses FR14's toggle): re-render THIS SAME edit
//     form with every submitted field preserved -- no store call at all,
//     so switching modes can never lose unsaved ScriptText and never
//     rotates or consumes the idempotency key.
//   - anything else (the "Save changes" submit, or an omitted field):
//     validates non-empty title/script_text, then calls
//     store.VideoScriptStore.UpdateContent -- the ONE place that decides
//     whether the script is still editable (ErrVideoScriptDecided, FR16/
//     FR17) and applies the idempotent replay (FR18/NFR2).
//
// A validation failure re-renders the edit form (400) with a form error
// and the submitted values preserved, carrying the SAME idempotency_key
// the failed POST carried. ErrVideoScriptDecided re-renders the READ-ONLY
// view instead (409, editable is now known false) with the rejection
// surfaced as a visible error alongside FR17's freeze indicator -- forcing
// the edit form back open here would contradict the very state check that
// just failed. Any other error from UpdateContent is a 500, mirroring
// HandleCreateScript's convention of never masking an unexpected store
// error as a form error.
func (h *Handlers) HandleUpdateScript(w http.ResponseWriter, r *http.Request) {
	person, ch, script, ok := h.authorizeScriptWrite(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	form := scriptFormData{
		IdempotencyKey: r.FormValue("idempotency_key"),
		Title:          r.FormValue("title"),
		ScriptText:     r.FormValue("script_text"),
	}

	switch r.FormValue("submit_action") {
	case "preview":
		form.Preview = true
		h.renderScriptDetail(w, r, person, ch, script, true, true, form, false, http.StatusOK)
		return
	case "edit":
		form.Preview = false
		h.renderScriptDetail(w, r, person, ch, script, true, true, form, false, http.StatusOK)
		return
	}

	title := strings.TrimSpace(form.Title)
	if title == "" {
		form.Error = "title is required"
		h.renderScriptDetail(w, r, person, ch, script, true, true, form, false, http.StatusBadRequest)
		return
	}
	scriptText := strings.TrimSpace(form.ScriptText)
	if scriptText == "" {
		form.Error = "script text is required"
		h.renderScriptDetail(w, r, person, ch, script, true, true, form, false, http.StatusBadRequest)
		return
	}

	if err := h.store.VideoScripts().UpdateContent(ctx, script.ID, title, scriptText, person.ID, form.IdempotencyKey); err != nil {
		if errors.Is(err, store.ErrVideoScriptDecided) {
			// Re-fetch: UpdateContent's own rejection means status and/or
			// published has moved since authorizeScriptWrite loaded script
			// above (or was already frozen when this request started) --
			// render against the CURRENT row so FR17's freeze indicator
			// (renderScriptDetail's own status/published check) reflects
			// reality, not the stale pre-POST snapshot.
			current, getErr := h.store.VideoScripts().GetByID(ctx, script.ID)
			if getErr != nil {
				http.Error(w, getErr.Error(), http.StatusInternalServerError)
				return
			}
			form.Error = "this script can no longer be edited: it has been decided, or its video has already been published"
			h.renderScriptDetail(w, r, person, ch, current, true, false, form, false, http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), http.StatusSeeOther)
}
