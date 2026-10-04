// The design-session read surface: a product's session list, one session's
// full ordered revision-event log, and its currently-open questions.
//
// The list reads DesignSessionStore.SummarizeByProduct -- the one
// product-wide aggregate -- rather than the accessors the MCP tools call
// one at a time. That is the same read overview_tiles.go already makes, so
// the Overview's blocking-question tile and this table cannot disagree
// about a session's stage or its open blocking count, and neither costs a
// query per session. The detail reads the exact accessors get_design_session
// and list_open_questions call (GetByID, ListBySession, ListOpenQuestions),
// so a browser and an MCP client still see one session, one ordering, and
// one last-event-wins open-question derivation.
//
// Unlike a write, a read carries no attribution, so it needs no krill
// session. These pages are behind readerRoute (sign-in plus a reader or
// operator role), like the rest of the read pages.
//
// The view models themselves (pages.DesignSessionListPage and friends) are
// declared in krill/ui/pages/design.templ beside the components that
// render them; the pure builders that populate them stay here.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// designSessionPath is one session's detail page -- product-scoped, which is
// what makes it canonical (FR a77852a9): a session id alone cannot answer
// "is this session under the product in the URL?", so the pid the reader
// arrived with has to be part of the address.
func designSessionPath(productID, id uuid.UUID) string {
	return designProductSessionsPath(productID) + "/" + id.String()
}

// legacyDesignSessionPath is the pre-redesign, unscoped detail URL. It is
// spelled here rather than assembled from designSessionPath because it is a
// different page's address, not this one's: it names no product, so it
// cannot be the successor any URL redirects into.
func legacyDesignSessionPath(id uuid.UUID) string {
	return designPath + "/design-sessions/" + id.String()
}

// designProductSessionsPath is a product's session list.
func designProductSessionsPath(productID uuid.UUID) string {
	return designPath + "/products/" + productID.String() + "/design-sessions"
}

// designAnswersPath is one session's follow-up answer action. It hangs off
// the canonical, product-scoped detail for the same reason that detail
// does: the handler answers 303 to the session it just wrote to, and a
// redirect that then had to 302 again would make the write's own outcome a
// two-hop navigation.
func designAnswersPath(productID, id uuid.UUID) string {
	return designSessionPath(productID, id) + "/answers"
}

// designGoPath is the design root's product-id browse target.
const designGoPath = designPath + "/go"

// ── read helpers ─────────────────────────────────────────────────────────────

// designSessionRows builds productID's session list rows from the
// product-wide aggregate read (FR d0a63ffb), newest first, exactly as
// DesignSessionStore.SummarizeByProduct orders them.
//
// One call, never the loop this replaced: the aggregate already carries
// each session's derived Stage and its open blocking-question count, so
// re-deriving either here would be a second notion of the two facts this
// table is read for. Re-deriving the stage in particular is what would let
// a list and a session's own detail disagree about what stage it is in.
//
// An unknown product is an empty list rather than an error, which is what
// this page has always answered and what its read of the aggregate's
// ErrNotFound preserves: the URL resolved and there is nothing behind it,
// which the empty state says in as many words. Every other failure -- the
// one that is a broken read rather than an empty product -- is returned.
func (app *App) designSessionRows(ctx context.Context, productID uuid.UUID, now time.Time) ([]pages.DesignSessionRow, error) {
	summary, err := app.designSessions.SummarizeByProduct(ctx, productID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows := make([]pages.DesignSessionRow, 0, len(summary.Sessions))
	for _, s := range summary.Sessions {
		rows = append(rows, pages.DesignSessionRow{
			ID:                    s.ID.String(),
			OpeningRequest:        firstLine(s.OpeningSubmission),
			DetailPath:            designSessionPath(productID, s.ID),
			Stage:                 string(s.Stage),
			OpenBlockingQuestions: s.OpenBlockingQuestions,
			OpenedRelative:        relativeTime(s.CreatedAt, now),
			OpenedExact:           s.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return rows, nil
}

// firstLine is an opening submission's first line: what the list shows,
// where the full submission is the detail page's subject. Trimmed, because
// a submission that opens with a blank line would otherwise render a row
// whose title is empty.
func firstLine(s string) string {
	line := s
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// buildDesignSessionDetail assembles one session's read view.
//
// Three reads, each named for what it is the browser's twin of:
//
//   - GetSummaryByID is the aggregate the sessions LIST already reads this
//     same session out of, narrowed to one row. It supplies the derived
//     stage and the opening identity, so the badge beside this page's h1
//     is the same badge the row linking here showed. Reading the stage any
//     other way -- a ListLatestSignoffBySessionIDs call, or a second
//     derivation here -- is how a list and a detail come to disagree about
//     the same session's stage.
//   - ListBySession and ListOpenQuestions are the exact accessors
//     get_design_session and list_open_questions call, so the log and the
//     question set an operator reads are the ones an MCP client reads.
//
// The product's name and link come from the request the route already
// resolved (productNameFor), not from a read of the product here: the pid
// is in the path, so a second product lookup would be the same row asked
// for twice.
func (app *App) buildDesignSessionDetail(ctx context.Context, productID, id uuid.UUID) (pages.DesignSessionDetailPage, error) {
	summary, err := app.designSessions.GetSummaryByID(ctx, id)
	if err != nil {
		return pages.DesignSessionDetailPage{}, err
	}
	events, err := app.revisionEvents.ListBySession(ctx, id)
	if err != nil {
		return pages.DesignSessionDetailPage{}, err
	}
	questions, err := app.revisionEvents.ListOpenQuestions(ctx, id)
	if err != nil {
		return pages.DesignSessionDetailPage{}, err
	}
	openedBy, openedByTitle := openingOperatorLabel(summary.OpenedBy)
	return pages.DesignSessionDetailPage{
		ID:                     summary.ID.String(),
		ProductID:              summary.ProductID.String(),
		ProductName:            app.productNameFor(ctx, productID),
		ProductOverviewPath:    productHref(productID, overviewSuffix),
		OpeningRequest:         firstLine(summary.OpeningSubmission),
		OpeningSubmission:      summary.OpeningSubmission,
		Stage:                  string(summary.Stage),
		OpenedBy:               openedBy,
		OpenedByTitle:          openedByTitle,
		OpenedByKrillSessionID: summary.OpenedByKrillSessionID.String(),
		CreatedAt:              formatTime(summary.CreatedAt),
		ProductSessionsPath:    designProductSessionsPath(summary.ProductID),
		Events:                 revisionEventRows(events),
		OpenQuestions:          openQuestionRows(questions),
		AnswersPath:            designAnswersPath(summary.ProductID, summary.ID),
	}, nil
}

// openingOperatorLabel renders the identity that opened a session for the
// properties card: the bare subject an operator recognises, with the full
// (issuer, subject) pair in the element's title for the case where the sub
// alone is ambiguous across issuers.
//
// An unreadable krill_session row is the zero Subject, and it renders as
// two empty strings -- not as a placeholder identity. The card then shows
// the session id chip with no "Opened by" line at all, which is the honest
// rendering: the page records that a session was opened and by which krill
// session, and says nothing at all about a person it could not read.
func openingOperatorLabel(s store.Subject) (label, title string) {
	if s.Sub == "" {
		return "", ""
	}
	return s.Sub, s.Sub + "@" + s.Iss
}

func revisionEventRows(events []store.RevisionEvent) []pages.RevisionEventRow {
	rows := make([]pages.RevisionEventRow, 0, len(events))
	for _, ev := range events {
		deltas := make([]pages.EntityDeltaRow, 0, len(ev.EntityDeltas))
		for _, d := range ev.EntityDeltas {
			deltas = append(deltas, pages.EntityDeltaRow{
				EntityID:    d.EntityID.String(),
				Change:      string(d.Change),
				SummaryLine: d.SummaryLine,
			})
		}
		opened := make([]pages.OpenQuestionDeltaRow, 0, len(ev.OpenQuestionsDelta.Opened))
		for _, q := range ev.OpenQuestionsDelta.Opened {
			opened = append(opened, pages.OpenQuestionDeltaRow{
				QuestionID: q.QuestionID,
				Text:       q.Text,
				Blocking:   blockingTag(q.Blocking),
			})
		}
		rows = append(rows, pages.RevisionEventRow{
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

func openQuestionRows(questions []store.OpenQuestion) []pages.OpenQuestionRow {
	rows := make([]pages.OpenQuestionRow, 0, len(questions))
	for _, q := range questions {
		rows = append(rows, pages.OpenQuestionRow{
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

// ── handlers ─────────────────────────────────────────────────────────────────

// handleDesignGo is the design root's old product-id browse target: a
// JS-free GET /design/go?product_id=X that validates the id and 302s to
// that product's session list. Nothing links here any more -- the root
// resolves a product itself (FR c4bd4bf8) -- but the route stays
// registered so an already-bookmarked URL keeps landing (FR 2544224c).
//
// The only user-controlled path segment is the product id, and it is
// parsed as a UUID before it is interpolated into a path this package
// builds -- so a malformed or hostile value is a 400, and there is no
// open-redirect surface.
func (app *App) handleDesignGo(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("product_id")))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, designProductSessionsPath(productID), http.StatusFound)
}

// handleDesignSessionList renders a product's design sessions -- the
// open and approved ones alike -- so a contributor can navigate into one
// without already holding its id. One route, two modes: an htmx request
// gets the page body as a bare fragment at 200, everything else gets it
// inside the shell.
func (app *App) handleDesignSessionList(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("productID"))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	app.renderDesignSessionList(w, r, productID, r.URL.Path)
}

// renderDesignSessionList is the session list, shared by the product-scoped
// URL and by the un-prefixed /design root that resolved this product --
// one page at two URLs, exactly as "/" and /products/{pid}/overview are one
// Overview at two URLs.
//
// activePath is passed rather than read from r.URL.Path because the two
// callers own different nav keys: the product-scoped URL is the Design
// sessions nav item's own path, and /design is the area root, which owns no
// nav item.
func (app *App) renderDesignSessionList(w http.ResponseWriter, r *http.Request, productID uuid.UUID, activePath string) {
	// now is read once, here, so every row's relative age is measured
	// against one instant: a table whose rows disagree about "now" by the
	// time the page took to render reads as a table of different ages.
	rows, err := app.designSessionRows(r.Context(), productID, time.Now())
	if err != nil {
		logger.Error("failed to list design sessions", "product_id", productID, "error", err)
		http.Error(w, "failed to list design sessions", http.StatusInternalServerError)
		return
	}
	page := pages.DesignSessionListPage{
		ProductID:   productID.String(),
		ProductName: app.productNameFor(r.Context(), productID),
		Path:        designProductSessionsPath(productID),
		Sessions:    rows,
		FormAction:  designProductSessionsPath(productID),
	}
	if isHXRequest(r) {
		renderFragment(w, r, pages.DesignSessionList(page))
		return
	}
	// The id in the path becomes the last-viewed product so the operator's
	// next un-prefixed page stays on the product they are reading. An id
	// that turns out to be outside the scope is simply dropped when the
	// cookie is read back, so this page needs no scope check of its own.
	setLastViewedProductCookie(w, productID)
	app.renderShell(w, r, "Design sessions", activePath, pages.DesignSessionList(page))
}

// productNameFor is the product's name for a page header. The design area's
// URLs hang off /design rather than off the product prefix, so the request
// carries no resolved product and the name is read from the scope listing.
//
// A listing that cannot be read yields the empty string rather than a
// failure: the table's own rows are this page's subject, and a header line
// is not worth taking the page down for. The empty header then names no
// product, which is the honest rendering of "the name is unavailable".
func (app *App) productNameFor(ctx context.Context, productID uuid.UUID) string {
	if product, ok := currentProduct(ctx); ok && product.ID == productID {
		return product.Name
	}
	products, err := app.scopeProducts(ctx)
	if err != nil {
		logger.Warn("design sessions: product name read failed", "product_id", productID, "error", err)
		return ""
	}
	for _, p := range products {
		if p.ID == productID {
			return p.Name
		}
	}
	return ""
}

// handleDesignSessionDetail renders one session's full ordered
// revision-event log and its currently-open questions. One route, two
// modes, exactly as handleDesignSessionList.
//
// The pid is in the path rather than inferred, because "is this session
// under the product the reader is looking at?" is a question the reader
// asked and the answer may be no (FR a77852a9). A session that is unknown,
// or that belongs to another product, is an in-shell 404 rather than a
// bare http.Error: an operator who followed a stale or hand-edited link
// lands on a page that looks like the rest of the UI and says what is
// missing, not on a plain-text error with no way back.
func (app *App) handleDesignSessionDetail(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("productID"))
	if err != nil {
		http.Error(w, "invalid product id: must be a UUID", http.StatusBadRequest)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid design session id: must be a UUID", http.StatusBadRequest)
		return
	}
	detail, err := app.buildDesignSessionDetail(r.Context(), productID, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && detail.ProductID != productID.String()) {
		if err == nil {
			logger.Info("design session not under the product the URL names",
				"product_id", productID, "design_session_id", id)
		}
		app.renderShellStatus(w, r, "Not found", r.URL.Path, pages.SpecStatus(pages.StatusPage{
			Title:  "Design session not found",
			Detail: "No design session of this id exists under this product. It may have been opened under another product, or the link may be out of date.",
		}), http.StatusNotFound)
		return
	}
	if err != nil {
		logger.Error("failed to load design session", "design_session_id", id, "error", err)
		http.Error(w, "failed to load design session", http.StatusInternalServerError)
		return
	}
	if isHXRequest(r) {
		renderFragment(w, r, pages.DesignSessionDetail(detail))
		return
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Design session", r.URL.Path, pages.DesignSessionDetail(detail))
}

// isHXRequest reports whether the caller is htmx. One route serves both
// modes: the header is what selects chrome vs. bare fragment.
func isHXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") != ""
}
