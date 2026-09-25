// The design-session read surface: a product's session list, one session's
// full ordered revision-event log, and its currently-open questions. Every
// read here goes through the exact store accessors the MCP tools'
// get_design_session and list_open_questions call
// (store.DesignSessionStore.GetByID/ListByProduct and
// store.RevisionEventStore.ListBySession/ListOpenQuestions), so a browser
// and an MCP client see one session, one ordering, and one last-event-wins
// open-question derivation -- never a parallel query.
//
// Unlike a write, a read carries no attribution, so it needs no krill
// session: api's own GET /design-sessions/{id} and
// GET /design-sessions/{id}/open-questions are ungated for the same reason
// (krill/ARCHITECTURE/12-design-session-revision-event-http.md). These
// pages are behind the sign-in gate only, exactly like the rest of the
// nav shell.
package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// designSessionPath is one session's detail page.
func designSessionPath(id uuid.UUID) string {
	return designPath + "/design-sessions/" + id.String()
}

// designProductSessionsPath is a product's session list.
func designProductSessionsPath(productID uuid.UUID) string {
	return designPath + "/products/" + productID.String() + "/design-sessions"
}

// ── view models ──────────────────────────────────────────────────────────────

// designSessionRow is one row of a product's session list. SignedOff marks a
// session whose log ends in an approved signoff, so the list shows both the
// open and the signed-off sessions the contributor can navigate back into.
type designSessionRow struct {
	ID                string
	OpeningSubmission string
	CreatedAt         string
	SignedOff         bool
	DetailPath        string
}

// designSessionListPage is the product-scoped session list.
type designSessionListPage struct {
	ProductID string
	Sessions  []designSessionRow
}

// entityDeltaRow is one spec entity a revision event touched.
type entityDeltaRow struct {
	EntityID    string
	Change      string
	SummaryLine string
}

// openQuestionDeltaRow is one question a revision event opened.
type openQuestionDeltaRow struct {
	QuestionID string
	Text       string
	Blocking   string // "blocking" or "non-blocking"
}

// revisionEventRow is one round of a session's ordered log, carrying every
// field get_design_session's own log exposes for that round.
type revisionEventRow struct {
	ID                string
	SeqNo             int
	EventType         string
	Acting            string
	OnBehalfOf        string
	VerifiedAgainst   string
	SignoffStatus     string
	CreatedAt         string
	EntityDeltas      []entityDeltaRow
	OpenedQuestions   []openQuestionDeltaRow
	ResolvedQuestions []string
}

// openQuestionRow is one currently-open question, tagged blocking or
// non-blocking exactly as list_open_questions tags it.
type openQuestionRow struct {
	QuestionID    string
	Text          string
	Blocking      string // "blocking" or "non-blocking"
	OpenedAtSeqNo int
}

// designSessionDetailPage is one session: its row, its full ordered
// revision-event log, and its currently-open questions.
type designSessionDetailPage struct {
	ID                     string
	ProductID              string
	OpeningSubmission      string
	OpenedByKrillSessionID string
	CreatedAt              string
	ProductSessionsPath    string
	Events                 []revisionEventRow
	OpenQuestions          []openQuestionRow
}

// ── read helpers ─────────────────────────────────────────────────────────────

// listDesignSessions returns productID's design sessions as list rows, ordered
// oldest-first exactly as DesignSessionStore.ListByProduct returns them. Each
// row's signed-off state is derived from that session's own revision-event
// log, the one source of truth for a session's current state (the
// design_session row itself is never updated -- FR1's boundary comment).
func (app *App) listDesignSessions(ctx context.Context, productID uuid.UUID) ([]designSessionRow, error) {
	sessions, err := app.designSessions.ListByProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	rows := make([]designSessionRow, 0, len(sessions))
	for _, ds := range sessions {
		events, err := app.revisionEvents.ListBySession(ctx, ds.ID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, designSessionRow{
			ID:                ds.ID.String(),
			OpeningSubmission: ds.OpeningSubmission,
			CreatedAt:         formatTime(ds.CreatedAt),
			SignedOff:         isSignedOff(events),
			DetailPath:        designSessionPath(ds.ID),
		})
	}
	return rows, nil
}

// isSignedOff reports whether a session's log ends in an approved signoff --
// FR4's closed outcome, read last-signoff-wins. A later changes_requested
// signoff reopens the session.
func isSignedOff(events []store.RevisionEvent) bool {
	signedOff := false
	for _, ev := range events {
		if ev.EventType != store.EventTypeSignoff {
			continue
		}
		signedOff = ev.SignoffStatus != nil && *ev.SignoffStatus == store.SignoffStatusApproved
	}
	return signedOff
}

// buildDesignSessionDetail assembles one session's read view from the same
// three store reads get_design_session (GetByID + ListBySession) and
// list_open_questions (GetByID + ListOpenQuestions) perform.
func (app *App) buildDesignSessionDetail(ctx context.Context, id uuid.UUID) (designSessionDetailPage, error) {
	ds, err := app.designSessions.GetByID(ctx, id)
	if err != nil {
		return designSessionDetailPage{}, err
	}
	events, err := app.revisionEvents.ListBySession(ctx, id)
	if err != nil {
		return designSessionDetailPage{}, err
	}
	questions, err := app.revisionEvents.ListOpenQuestions(ctx, id)
	if err != nil {
		return designSessionDetailPage{}, err
	}
	return designSessionDetailPage{
		ID:                     ds.ID.String(),
		ProductID:              ds.ProductID.String(),
		OpeningSubmission:      ds.OpeningSubmission,
		OpenedByKrillSessionID: ds.OpenedByKrillSessionID.String(),
		CreatedAt:              formatTime(ds.CreatedAt),
		ProductSessionsPath:    designProductSessionsPath(ds.ProductID),
		Events:                 revisionEventRows(events),
		OpenQuestions:          openQuestionRows(questions),
	}, nil
}

func revisionEventRows(events []store.RevisionEvent) []revisionEventRow {
	rows := make([]revisionEventRow, 0, len(events))
	for _, ev := range events {
		deltas := make([]entityDeltaRow, 0, len(ev.EntityDeltas))
		for _, d := range ev.EntityDeltas {
			deltas = append(deltas, entityDeltaRow{
				EntityID:    d.EntityID.String(),
				Change:      string(d.Change),
				SummaryLine: d.SummaryLine,
			})
		}
		opened := make([]openQuestionDeltaRow, 0, len(ev.OpenQuestionsDelta.Opened))
		for _, q := range ev.OpenQuestionsDelta.Opened {
			opened = append(opened, openQuestionDeltaRow{
				QuestionID: q.QuestionID,
				Text:       q.Text,
				Blocking:   blockingTag(q.Blocking),
			})
		}
		rows = append(rows, revisionEventRow{
			ID:                ev.ID.String(),
			SeqNo:             ev.SeqNo,
			EventType:         string(ev.EventType),
			Acting:            subjectLabel(ev.Acting),
			OnBehalfOf:        subjectLabel(ev.OnBehalfOf),
			VerifiedAgainst:   derefString(ev.VerifiedAgainst),
			SignoffStatus:     derefSignoff(ev.SignoffStatus),
			CreatedAt:         formatTime(ev.CreatedAt),
			EntityDeltas:      deltas,
			OpenedQuestions:   opened,
			ResolvedQuestions: ev.OpenQuestionsDelta.Resolved,
		})
	}
	return rows
}

func openQuestionRows(questions []store.OpenQuestion) []openQuestionRow {
	rows := make([]openQuestionRow, 0, len(questions))
	for _, q := range questions {
		rows = append(rows, openQuestionRow{
			QuestionID:    q.QuestionID,
			Text:          q.Text,
			Blocking:      blockingTag(q.Blocking),
			OpenedAtSeqNo: q.OpenedAtSeqNo,
		})
	}
	return rows
}

func subjectLabel(s store.Subject) string {
	return fmt.Sprintf("%s %s@%s", s.Kind, s.Sub, s.Iss)
}

func blockingTag(blocking bool) string {
	if blocking {
		return "blocking"
	}
	return "non-blocking"
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefSignoff(s *store.SignoffStatus) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

// formatTime renders a store timestamp in UTC; a zero time renders as the
// empty string rather than a misleading year-1 date.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// ── templates ────────────────────────────────────────────────────────────────

// designRootTemplate is the design root's landing body (routes.go's
// handleDesign): it takes a product id and navigates to that product's
// session list -- the entry point a Requirement Contributor needs to reach a
// session without already holding its id.
var designRootTemplate = template.Must(template.New("design-root").Parse(`<h2>Design sessions</h2>
<p>Browse a product's design sessions. Enter the product's id to open its session list.</p>
<p>
  <label for="product-id">Product id</label>
  <input id="product-id" type="text" placeholder="00000000-0000-0000-0000-000000000000" style="font-family: monospace;">
  <button id="browse-btn" type="button">Browse sessions</button>
</p>
<script>
document.getElementById('browse-btn').addEventListener('click', function () {
  var v = document.getElementById('product-id').value.trim();
  if (v) { window.location = '/design/products/' + encodeURIComponent(v) + '/design-sessions'; }
});
</script>`))

// designSessionListTemplate renders a product's sessions, each linking into
// its detail page.
var designSessionListTemplate = template.Must(template.New("design-session-list").Parse(`<h2>Design sessions</h2>
<p>Product <code>{{.ProductID}}</code></p>

<h3>Open a new design session</h3>
<p>Describe your idea in plain language. krill records it as the session's opening submission; it does not create or change any spec entity here.</p>
<form method="post" action="/design/products/{{.ProductID}}/design-sessions">
  <p><label for="opening-submission">Your idea or user story</label><br>
  <textarea id="opening-submission" name="opening_submission" rows="4" cols="60" required placeholder="Users need to bulk-export their data as CSV"></textarea></p>
  <p><button type="submit">Open design session</button></p>
</form>

{{if .Sessions}}
<table>
  <thead><tr><th>Session</th><th>Opening submission</th><th>Status</th><th>Created</th></tr></thead>
  <tbody>
  {{range .Sessions}}
  <tr>
    <td><a href="{{.DetailPath}}"><code>{{.ID}}</code></a></td>
    <td>{{.OpeningSubmission}}</td>
    <td>{{if .SignedOff}}signed off{{else}}open{{end}}</td>
    <td>{{.CreatedAt}}</td>
  </tr>
  {{end}}
  </tbody>
</table>
{{else}}
<p>No design sessions for this product yet.</p>
{{end}}`))

// designSessionDetailTemplate renders one session's full ordered
// revision-event log and its currently-open questions.
var designSessionDetailTemplate = template.Must(template.New("design-session-detail").Parse(`<h2>Design session</h2>
<dl>
  <dt>ID</dt><dd><code>{{.ID}}</code></dd>
  <dt>Product</dt><dd><code>{{.ProductID}}</code></dd>
  <dt>Opening submission</dt><dd>{{.OpeningSubmission}}</dd>
  <dt>Opened by krill session</dt><dd><code>{{.OpenedByKrillSessionID}}</code></dd>
  <dt>Created</dt><dd>{{.CreatedAt}}</dd>
</dl>

<h3>Revision events</h3>
{{if .Events}}
<ol>
{{range .Events}}
  <li>
    <strong>#{{.SeqNo}} {{.EventType}}</strong>{{if .SignoffStatus}} &mdash; signoff: {{.SignoffStatus}}{{end}}
    <br><small>acting: {{.Acting}} &middot; on behalf of: {{.OnBehalfOf}} &middot; {{.CreatedAt}}</small>
    <br><small>event id: <code>{{.ID}}</code></small>
    {{if .VerifiedAgainst}}<br><small>verified against: {{.VerifiedAgainst}}</small>{{end}}
    {{if .EntityDeltas}}
    <ul>
    {{range .EntityDeltas}}<li>{{.Change}} <code>{{.EntityID}}</code> &mdash; {{.SummaryLine}}</li>
    {{end}}</ul>
    {{end}}
    {{if .OpenedQuestions}}
    <p>Opened questions:</p>
    <ul>
    {{range .OpenedQuestions}}<li><code>{{.QuestionID}}</code> ({{.Blocking}}): {{.Text}}</li>
    {{end}}</ul>
    {{end}}
    {{if .ResolvedQuestions}}
    <p>Resolved questions:
    {{range .ResolvedQuestions}}<code>{{.}}</code> {{end}}</p>
    {{end}}
  </li>
{{end}}
</ol>
{{else}}
<p>No revision events yet.</p>
{{end}}

<h3>Open questions</h3>
{{if .OpenQuestions}}
<table>
  <thead><tr><th>Question</th><th>Tag</th><th>Opened at</th></tr></thead>
  <tbody>
  {{range .OpenQuestions}}
  <tr><td><code>{{.QuestionID}}</code> &mdash; {{.Text}}</td><td>{{.Blocking}}</td><td>#{{.OpenedAtSeqNo}}</td></tr>
  {{end}}
  </tbody>
</table>
{{else}}
<p>No open questions.</p>
{{end}}
<p><a href="{{.ProductSessionsPath}}">Back to this product's sessions</a></p>

<h3>Submit follow-up</h3>
<p>Answer in plain language. krill records this as an <code>answer</code> round on this session; it does not create or change any spec entity here. Tick any open question your answer closes.</p>
<form method="post" action="/design/design-sessions/{{.ID}}/answers">
  {{if .OpenQuestions}}
  <fieldset>
    <legend>Open questions this answer closes</legend>
    {{range .OpenQuestions}}
    <p><label><input type="checkbox" name="resolve" value="{{.QuestionID}}"> <code>{{.QuestionID}}</code> ({{.Blocking}}): {{.Text}}</label></p>
    {{end}}
  </fieldset>
  {{end}}
  <p><label for="follow-up">Your follow-up</label><br>
  <textarea id="follow-up" name="follow_up" rows="4" cols="60" required placeholder="It should use the postgres flag table."></textarea></p>
  <p><button type="submit">Submit follow-up</button></p>
</form>`))

// ── handlers ─────────────────────────────────────────────────────────────────

// handleDesignSessionList renders a product's design sessions -- the
// open and signed-off ones alike -- so a contributor can navigate into one
// without already holding its id.
func (app *App) handleDesignSessionList(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("productID"))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	sessions, err := app.listDesignSessions(r.Context(), productID)
	if err != nil {
		logger.Error("failed to list design sessions", "product_id", productID, "error", err)
		http.Error(w, "failed to list design sessions", http.StatusInternalServerError)
		return
	}
	renderShell(w, r, "Design sessions", designPath, renderPage(designSessionListTemplate, designSessionListPage{
		ProductID: productID.String(),
		Sessions:  sessions,
	}))
}

// handleDesignSessionDetail renders one session's full ordered
// revision-event log and its currently-open questions.
func (app *App) handleDesignSessionDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid design session id: must be a UUID", http.StatusBadRequest)
		return
	}
	detail, err := app.buildDesignSessionDetail(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "design session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		logger.Error("failed to load design session", "design_session_id", id, "error", err)
		http.Error(w, "failed to load design session", http.StatusInternalServerError)
		return
	}
	renderShell(w, r, "Design session", designPath, renderPage(designSessionDetailTemplate, detail))
}
