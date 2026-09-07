// Package videos is `web`'s Channel-scoped published-videos listing page
// (capability C21, issue #2031, milestone re-anchor of #1960's FR27-FR30):
// serves GET /channels/{id}/videos -- every published SyncedVideo on the
// Channel with its latest recorded metrics, filterable by title substring,
// publish-date range, and sync status (FR28/FR29/FR30). Handlers and templ
// views live together in this one package, mirroring web/outcomes's (which
// mirrors web/research's) package doc comment rationale: the read flow and
// its views are tightly coupled with no reuse outside this package.
//
// Web-only in this milestone (no MCP mirror planned, per this task's
// issue): the one read this package needs, store.SyncStore.
// ListPublishedWithMetrics, is new in this task, added alongside
// SyncStore's existing ListSchedule (store/sync.go).
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/videos -- HandleList (FR27, FR28, FR29, FR30).
package videos

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// defaultVideosLimit bounds HandleList's response, consistent with the
// rest of `web` (outcomes.defaultOutcomesLimit is 25; research's browse
// sections truncate at 50) -- there is no since/before/limit query
// parameter, no "load more" control anywhere in this package. truncated
// (from ListPublishedWithMetrics) is surfaced in views.templ as a static
// note, never a paging control (FR30).
const defaultVideosLimit = 100

// publishedDateLayout is the wire format this package's "from"/"to" query
// parameters use -- an HTML <input type="date">'s value format (FR28),
// matching web/research's identically-documented target_publish_date
// convention (research.go's HandleProposeVideoScript doc comment): not
// RFC3339, since these values come from a date picker, not a caller-typed
// timestamp string.
const publishedDateLayout = "2006-01-02"

// Handlers holds the dependencies videos' route needs: the Store (for
// store.CanRead plus Sync()/Channels()/Roles()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// filterFormData is FR28's filter form's current query-param values,
// preserved verbatim across a render so a re-submitted GET's controls
// show exactly what the URL already encodes (bookmarkable, no POST). The
// raw strings ("", "true", "false" for Synced) are what views.templ
// renders back into the form controls; parseFilters below is what turns
// them into a store.PublishedVideoFilter.
type filterFormData struct {
	Title string
	From  string
	To    string
	// Synced is "" (no filter), "true", or "false" -- FR29's binary
	// bucket, never a third value.
	Synced string
}

// parseFilters turns q (the request's raw URL query values) into a
// store.PublishedVideoFilter plus the filterFormData views.templ re-renders
// the form with (FR28: submitted values preserved). Returns a non-empty
// errMsg, with the zero filter, when "from", "to", or "synced" fails to
// parse -- HandleList maps that to a 400 rather than silently ignoring a
// malformed filter or 500ing.
func parseFilters(q map[string][]string) (store.PublishedVideoFilter, filterFormData, string) {
	get := func(key string) string {
		vs := q[key]
		if len(vs) == 0 {
			return ""
		}
		return vs[0]
	}

	form := filterFormData{
		Title:  get("title"),
		From:   get("from"),
		To:     get("to"),
		Synced: get("synced"),
	}

	f := store.PublishedVideoFilter{TitleContains: form.Title}

	if form.From != "" {
		parsed, err := time.Parse(publishedDateLayout, form.From)
		if err != nil {
			return store.PublishedVideoFilter{}, form, "invalid from date"
		}
		f.PublishedFrom = &parsed
	}

	if form.To != "" {
		parsed, err := time.Parse(publishedDateLayout, form.To)
		if err != nil {
			return store.PublishedVideoFilter{}, form, "invalid to date"
		}
		// "to" is a calendar date, but PublishedTo is an inclusive bound
		// compared directly against published_at (a full timestamp,
		// store.PublishedVideoFilter's doc comment) -- pushing the bound
		// to the last instant of that day (rather than its midnight) is
		// what makes "to" actually include every video published ON
		// that day, not just ones published at exactly 00:00:00.
		endOfDay := parsed.Add(24*time.Hour - time.Nanosecond)
		f.PublishedTo = &endOfDay
	}

	switch form.Synced {
	case "":
		// no filter
	case "true":
		v := true
		f.Synced = &v
	case "false":
		v := false
		f.Synced = &v
	default:
		return store.PublishedVideoFilter{}, form, "invalid synced filter"
	}

	return f, form, ""
}

// HandleList serves GET /channels/{id}/videos (FR27, FR28, FR29, FR30).
// The auth/404/403 preamble mirrors outcomes.Handlers.HandleList/
// research.Handlers.HandleChannelIndex exactly (load-bearing, not
// stylistic, per this issue's Authorization section): resolve the
// signed-in Person (401), parse {id} (400), load the Channel (404 on
// pgx.ErrNoRows -- an unknown Channel 404s before authorization can turn
// it into a 403), then store.CanRead (403). Filters are read straight
// from the request's query string (FR28: bookmarkable GET, no POST, no
// hidden state) and combine with AND in the single
// store.SyncStore.ListPublishedWithMetrics call (FR30, NFR3).
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

	filter, form, errMsg := parseFilters(r.URL.Query())
	if errMsg != "" {
		h.render(w, r, person, ch, form, nil, false, errMsg, http.StatusBadRequest)
		return
	}

	rows, truncated, err := h.store.Sync().ListPublishedWithMetrics(ctx, channelID, filter, defaultVideosLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.render(w, r, person, ch, form, rows, truncated, "", http.StatusOK)
}

// render assembles and renders /channels/{id}/videos: the same query set
// for a valid GET (rows/truncated populated, errMsg empty, status 200)
// and for a GET whose filter params failed to parse (rows nil,
// truncated false, errMsg set, status 400, form still carrying the
// submitted-but-invalid values so the control that failed is visible) --
// differing only in what was queried and the status, never in the shape
// of the render itself.
func (h *Handlers) render(w http.ResponseWriter, r *http.Request, person *store.Person, ch store.Channel, form filterFormData, rows []store.PublishedVideoRow, truncated bool, errMsg string, status int) {
	title := ch.Title + " videos"
	data := components.LayoutData{Title: title, User: person}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := components.Render(w, r, title, List(data, ch, form, rows, truncated, errMsg)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
