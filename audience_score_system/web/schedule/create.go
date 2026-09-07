package schedule

import (
	"net/http"

	"github.com/google/uuid"
)

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
// Idea's viable-valued verdicts to propose from (FR13).
//
// Preview (FR14) is an explicit edit/rendered-preview mode toggle, not
// two different fields: switching modes must never lose unsaved
// ScriptText (Implementation phase wires the actual round trip).
type scriptFormData struct {
	VerdictID      string // raw submitted value; "" on a plain GET.
	StrategyID     string // raw submitted value; "" on a plain GET.
	Title          string
	ScriptText     string
	IdempotencyKey string
	Preview        bool // FR14: editor vs. rendered-preview mode.
	Error          string
}

// newScriptFormData mints a fresh scriptFormData for a plain render -- no
// submitted content, just a freshly minted IdempotencyKey (FR18/NFR2).
// Used by HandleNewScript's GET, mirroring
// web/research.newProposeFormData's identical rationale.
func newScriptFormData() scriptFormData {
	return scriptFormData{IdempotencyKey: newIdempotencyKey()}
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

// HandleNewScript serves GET /channels/{id}/scripts/new (#2036, FR13,
// FR14): the create-video-script form -- a viable-verdict picker, an
// active-Strategy picker (or FR13's zero-active-Strategy blocked-form
// message), title/script-text fields, and FR14's markdown editor/preview
// toggle. Stubbed for Scaffold -- this task's Implementation step adds
// the real auth/404 preamble (mirroring HandleList's order-of-operations
// exactly: store.CanRead re-derived only after confirming the Channel
// exists), the viable-verdict and active-Strategy loads, and the
// rendered form.
func (h *Handlers) HandleNewScript(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleCreateScript serves POST /channels/{id}/scripts (#2036, FR13,
// FR18/NFR2, FR19): the create form's submit target. Calls the IDENTICAL
// store.VideoScriptStore.Propose method save_video_script's mutate step
// and web/research.Handlers.HandleProposeVideoScript's Idea-page entry
// point both call (LB5 -- one write path, never a parallel one), gated by
// store.CanWrite (Creator-or-Analyst), never store.CanApprove. Stubbed
// for Scaffold -- this task's Implementation step adds the real
// authorization preamble, viable-verdict/active-Strategy/title/
// script-text validation, the Propose call, and the idempotency-key
// threading.
func (h *Handlers) HandleCreateScript(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleScriptDetail serves GET /channels/{id}/scripts/{scriptID}
// (#2036, FR14, FR15): the per-script authoring page -- the script's
// current body in the SAME markdown editor/preview toggle as the create
// form, and FR15's link back to the bound verdict (its Idea/Channel,
// version, and value). Stubbed for Scaffold -- this task's Implementation
// step adds the real store.CanRead preamble (mirroring HandleList's
// order-of-operations), the store.VideoScripts().GetByID load, the bound
// Verdict resolution, and the rendered page. Script body editing
// (FR16/FR17) is a deliberately separate follow-on task -- this route is
// read-only display of the create-time body plus the FR15 verdict link.
func (h *Handlers) HandleScriptDetail(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
