// Package outcomes is `web`'s Channel-scoped prediction-vs-outcome browse
// page (milestone M4.3, capability C10/C14): serves GET
// /channels/{id}/outcomes, the `web` mirror of `get_prediction_vs_outcome`
// (audience_score_system/mcp/tools/browse.go), PLUS this task's (#1929)
// calibration-trend section (FR3, FR4) and inline set-outcome-bar form
// (FR5, FR6) rendered on that SAME page -- never a separate route.
// Handlers and templ views live together in this one package, mirroring
// web/matches's (which itself mirrors web/research's, which mirrors
// web/schedule's) package doc comment rationale: the read/write flow and
// its views are tightly coupled with no reuse outside this package.
//
// #1928 (FR1, FR2, NFR2, plus the "View outcomes" half of FR11) built the
// read-only prediction-vs-outcome browse section this file's HandleList
// still serves unchanged. This task (#1929) adds the calibration-trend
// chart, the not-configured pointer, and the write form below it on the
// same page. Per the plan's Out of scope, this package adds no new store
// method, migration, or schema change: it calls store.OutcomeBarStore.
// GetByChannel/Upsert and store.CalibrationStore.MonthlyTrend, all
// pre-existing, and (for the trend read) the IDENTICAL pair of calls
// get_calibration_trend's handler makes (mcp/tools/outcome_bar.go, LB5 --
// one read path, never a parallel one).
//
// Chart rendering: there is no chart component anywhere in this repo
// (libs/go/htmxui has shell/card/button/badge/confirm/theme-switcher
// only). Rather than add a JS charting library, an npm dependency, or a
// new shared libs/go/htmxui component, the calibration-trend chart is
// rendered as inline SVG computed entirely in Go (calibrationView/
// calibrationPoint below) and stays local to this package for this
// milestone -- views.templ performs no arithmetic beyond emitting the
// coordinates this file already computed.
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/outcomes -- HandleList (FR1, FR2, FR3, FR4).
//   - POST /channels/{id}/outcome-bar -- HandleSetOutcomeBar (FR5, FR6).
package outcomes

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// defaultOutcomesLimit bounds HandleList's response to the same fixed page
// size as mcp/tools/browse.go's defaultPredictionVsOutcomeLimit, per NFR2:
// no since/before/limit query parameter, no "load more" control exists
// anywhere in this package. truncated (from PredictionVsOutcome) is
// surfaced in views.templ as a static note, never a paging control.
const defaultOutcomesLimit = 25

// defaultCalibrationTrendLimit matches mcp/tools/outcome_bar.go's
// defaultCalibrationTrendLimit exactly (NFR2, FR3): twelve month buckets,
// no since/before/limit query parameter, no "load more" control. A later
// reader must not let these two constants drift -- if MCP's default ever
// changes, this one must change with it, since both surfaces classify the
// same trend and neither should let the other imply a longer history than
// it actually rendered.
const defaultCalibrationTrendLimit = 12

// chart geometry constants for the inline SVG calibration-trend chart
// (FR3). Kept small and local to this package -- see this file's package
// doc comment on why there is no shared chart component.
const (
	chartWidth   = 640
	chartHeight  = 200
	chartMarginX = 32
	chartMarginY = 16
)

// Handlers holds the dependencies outcomes' routes need: the Store (for
// store.CanRead/store.CanWrite plus Browse()/Channels()/Roles()/
// OutcomeBars()/Calibration()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// outcomeBarFormData carries the inline set-outcome-bar form's (FR5)
// current value through a render: on a plain GET (HandleList, via
// newOutcomeBarFormData) it holds the current bar's ThresholdValue,
// formatted as a string, when one is configured, or "" when FR4's
// not-configured state applies; on a validation-failure re-render from
// HandleSetOutcomeBar it carries the submitted ThresholdValue and a
// non-empty Error. There is deliberately NO IdempotencyKey field (FR6) --
// see HandleSetOutcomeBar's doc comment on why none is needed, unlike
// every other write form in this repo.
type outcomeBarFormData struct {
	ThresholdValue string
	Error          string
}

// newOutcomeBarFormData mints a plain-GET outcomeBarFormData: the current
// bar's threshold prefilled when calib is configured, empty otherwise
// (FR5 prefill requirement).
func newOutcomeBarFormData(calib calibrationView) outcomeBarFormData {
	if !calib.Configured {
		return outcomeBarFormData{}
	}
	return outcomeBarFormData{ThresholdValue: strconv.FormatFloat(calib.Threshold, 'f', -1, 64)}
}

// calibrationPoint is one calendar-month bucket (store.CalibrationBucket,
// rendered verbatim, in the SAME order MonthlyTrend returned it -- NFR4)
// plus the plot geometry views.templ needs to draw it: X/Y are the
// point's SVG coordinates, precomputed here so views.templ performs no
// arithmetic of its own.
type calibrationPoint struct {
	Label         string // "2026-01", from BucketStart
	Candidates    int
	Calibrated    int
	Miscalibrated int
	Rate          float64
	X             float64
	Y             float64
}

// calibrationView is the package-local view struct (per this issue's
// Scaffold section) HandleList/HandleSetOutcomeBar assemble for the
// calibration-trend section: the already-ordered buckets (NFR4: NEVER
// re-sorted here, in loadCalibration below, or anywhere else in this
// package -- see that function's doc comment) plus precomputed SVG plot
// geometry. views.templ reads this struct and emits coordinates; it makes
// no store calls and does no bucket arithmetic.
type calibrationView struct {
	// Configured mirrors OutcomeBarOutput.Configured (FR4): false means
	// GetByChannel returned pgx.ErrNoRows -- a successful render, not an
	// error -- and every other field below is zero/empty. MonthlyTrend is
	// never called in that state.
	Configured bool
	MetricName string
	Threshold  float64

	// Unsupported carries store.ErrUnsupportedOutcomeBarMetric's message
	// when MonthlyTrend rejects the current bar's metric (a bar whose
	// MetricName is not store.OutcomeBarMetricViews) -- surfaced as a
	// rendered message (FR3), never a 500. Points/Truncated are empty when
	// this is set.
	Unsupported string

	Points    []calibrationPoint
	Truncated bool

	// ChartWidth/ChartHeight size the <svg> viewBox; Polyline is the
	// space-separated "x,y" pairs for the trend line, in Points' exact
	// order (NFR4).
	ChartWidth  int
	ChartHeight int
	Polyline    string
}

// loadCalibration assembles the calibration-trend section (FR3, FR4) for
// channelID: GetByChannel first (FR4 -- pgx.ErrNoRows is NOT an error,
// MonthlyTrend is never called in that branch), then, when configured,
// MonthlyTrend(ctx, channelID, bar, nil, nil, defaultCalibrationTrendLimit)
// -- the IDENTICAL pair of calls get_calibration_trend's handler makes
// (mcp/tools/outcome_bar.go), so `web` and `mcp` can never disagree on
// which rows are classified or how. store.ErrUnsupportedOutcomeBarMetric
// is rendered as calibrationView.Unsupported, never a 500.
//
// NFR4: buckets are placed into calibrationView.Points in EXACTLY the
// order MonthlyTrend returned them (chronological, oldest -> newest) --
// no sort.* call and no re-bucketing anywhere in this function or this
// package. A later reader must not "fix" an apparent ordering issue by
// adding one; grep for `sort.` in this package finding nothing is a
// load-bearing invariant, asserted in this task's tests.
func (h *Handlers) loadCalibration(r *http.Request, channelID uuid.UUID) (calibrationView, error) {
	ctx := r.Context()

	bar, err := h.store.OutcomeBars().GetByChannel(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return calibrationView{Configured: false}, nil
		}
		return calibrationView{}, err
	}

	buckets, truncated, err := h.store.Calibration().MonthlyTrend(ctx, channelID, bar, nil /* since */, nil /* before */, defaultCalibrationTrendLimit)
	if err != nil {
		if errors.Is(err, store.ErrUnsupportedOutcomeBarMetric) {
			return calibrationView{
				Configured:  true,
				MetricName:  bar.MetricName,
				Threshold:   bar.ThresholdValue,
				Unsupported: err.Error(),
			}, nil
		}
		return calibrationView{}, err
	}

	return calibrationView{
		Configured:  true,
		MetricName:  bar.MetricName,
		Threshold:   bar.ThresholdValue,
		Points:      calibrationPoints(buckets),
		Truncated:   truncated,
		ChartWidth:  chartWidth,
		ChartHeight: chartHeight,
		Polyline:    calibrationPolyline(buckets),
	}, nil
}

// calibrationPoints computes each bucket's SVG X/Y in buckets' EXACT
// order (NFR4 -- no re-sorting), plotting Rate in [0,1] against the
// chart's plottable rectangle. A single-bucket trend is centered on the
// chart's horizontal midpoint rather than dividing by zero.
func calibrationPoints(buckets []store.CalibrationBucket) []calibrationPoint {
	points := make([]calibrationPoint, 0, len(buckets))
	for i, b := range buckets {
		points = append(points, calibrationPoint{
			Label:         b.BucketStart.Format("2006-01"),
			Candidates:    b.Candidates,
			Calibrated:    b.Calibrated,
			Miscalibrated: b.Miscalibrated,
			Rate:          b.Rate,
			X:             plotX(i, len(buckets)),
			Y:             plotY(b.Rate),
		})
	}
	return points
}

// calibrationPolyline renders buckets' Rate values as a single SVG
// polyline `points` attribute, in buckets' EXACT order (NFR4).
func calibrationPolyline(buckets []store.CalibrationBucket) string {
	s := ""
	for i, b := range buckets {
		if i > 0 {
			s += " "
		}
		s += strconv.FormatFloat(plotX(i, len(buckets)), 'f', 1, 64) + "," + strconv.FormatFloat(plotY(b.Rate), 'f', 1, 64)
	}
	return s
}

// plotX returns the SVG x-coordinate for point i of n, spread evenly
// across the chart's plottable width; a single point centers horizontally
// rather than dividing by zero.
func plotX(i, n int) float64 {
	if n <= 1 {
		return float64(chartWidth) / 2
	}
	step := float64(chartWidth-2*chartMarginX) / float64(n-1)
	return float64(chartMarginX) + float64(i)*step
}

// plotY returns the SVG y-coordinate for a calibration rate in [0,1] --
// SVG's y axis grows downward, so rate 1.0 plots at the top margin and
// rate 0.0 plots at the bottom margin.
func plotY(rate float64) float64 {
	return float64(chartHeight-chartMarginY) - rate*float64(chartHeight-2*chartMarginY)
}

// HandleList serves GET /channels/{id}/outcomes (FR1, FR2, FR3, FR4). The
// auth/404/403 preamble mirrors web/matches.Handlers.HandleList/
// research.Handlers.HandleChannelIndex exactly (load-bearing, not
// stylistic, per this issue's body): resolve the signed-in Person (401),
// parse {id} (400), load the Channel (404 on pgx.ErrNoRows -- an unknown
// Channel 404s before authorization can turn it into a 403), then
// store.CanRead (403).
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

	calib, err := h.loadCalibration(r, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.renderList(w, r, person, ch, newOutcomeBarFormData(calib), http.StatusOK)
}

// renderList assembles and renders /channels/{id}/outcomes: the identical
// query set for a plain GET (HandleList, form carrying only the current
// bar's prefilled threshold or "" when not configured, status 200) and
// for the same page re-rendered after a failed POST
// /channels/{id}/outcome-bar (HandleSetOutcomeBar, form carrying the
// submitted ThresholdValue plus a non-empty Error, status 400) --
// differing only in form and status, never in what is queried. canWrite
// (store.CanWrite, re-derived fresh here alongside HandleList's CanRead)
// gates whether the set-outcome-bar form renders at all (presentation
// only -- the actual rejection of a forged POST happens in
// HandleSetOutcomeBar's authorizeWrite, never here).
func (h *Handlers) renderList(w http.ResponseWriter, r *http.Request, person *store.Person, ch store.Channel, form outcomeBarFormData, status int) {
	ctx := r.Context()
	channelID := ch.ID

	canWrite, err := store.CanWrite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rows, truncated, err := h.store.Browse().PredictionVsOutcome(ctx, channelID, nil /* ideaID */, nil /* since */, nil /* before */, defaultOutcomesLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	calib, err := h.loadCalibration(r, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	title := ch.Title + " outcomes"
	data := components.LayoutData{Title: title, User: person}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := components.Render(w, r, title, List(data, ch, rows, truncated, calib, canWrite, form)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// authorizeWrite is outcomes' POST preamble (FR5), reproducing
// research.Handlers.authorizeWrite's/matches.Handlers.authorizeWrite's
// exact shape (load-bearing, not stylistic, per this issue's body):
// resolve the signed-in Person (401), parse {id} (400), load the Channel
// (404 via pgx.ErrNoRows -- an unknown Channel 404s before authorization
// can turn it into a 403), then re-derive store.CanWrite fresh from
// Postgres on THIS request (403 when false) -- never store.CanApprove:
// this configuration surface shares the FR17-authority Creator-or-Analyst
// tier set_outcome_bar's MCP handler uses (mcp/tools/outcome_bar.go's doc
// comment), deliberately not Creator-only. ok is false after this has
// already written the appropriate error response; callers MUST return
// immediately when ok is false.
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

// HandleSetOutcomeBar serves POST /channels/{id}/outcome-bar (FR5, FR6):
// the inline form configuring what "calibrated" means for this Channel,
// calling the EXACT SAME store.OutcomeBarStore.Upsert call
// set_outcome_bar's mutate step calls (mcp/tools/outcome_bar.go, LB5 --
// one write path, never a parallel one). metric_name is fixed to
// store.OutcomeBarMetricViews in this milestone's form markup (a
// disabled/static control), but is STILL validated server-side here --
// Upsert rejects any other value with store.ErrUnsupportedOutcomeBarMetric
// regardless of what the form rendered, since a forged POST must be
// rejected the same way an MCP caller's would be.
//
// FR6: this form carries NO idempotency_key field, deliberately.
// store.OutcomeBarStore.Upsert converges on a single row per channel_id
// via ON CONFLICT (channel_id) DO UPDATE (SetOutcomeBarInput's own doc
// comment), so a double-submit (back button, refresh-after-POST) is
// already safe without LB4's idempotency-key mechanism: repeated
// identical submits leave exactly one outcome_bar row for the Channel.
// NFR1 is satisfied by that natural-key convergence alone -- there is no
// in-memory or session-held state on this write path. Do not "fix" this
// by adding a key back.
func (h *Handlers) HandleSetOutcomeBar(w http.ResponseWriter, r *http.Request) {
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

	metricName := r.FormValue("metric_name")
	rawThreshold := r.FormValue("threshold_value")

	renderErr := func(msg string) {
		h.renderList(w, r, person, ch, outcomeBarFormData{ThresholdValue: rawThreshold, Error: msg}, http.StatusBadRequest)
	}

	threshold, err := strconv.ParseFloat(rawThreshold, 64)
	if err != nil {
		renderErr("threshold_value must be a number")
		return
	}

	_, err = h.store.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID:         channelID,
		MetricName:        metricName,
		ThresholdValue:    threshold,
		UpdatedByPersonID: person.ID,
	})
	if err != nil {
		if errors.Is(err, store.ErrUnsupportedOutcomeBarMetric) || errors.Is(err, store.ErrInvalidOutcomeBarThreshold) {
			renderErr(err.Error())
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/channels/"+channelID.String()+"/outcomes", http.StatusSeeOther)
}
