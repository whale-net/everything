package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/leaflab/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// defaultHistoryPreset is the range handleSensorHistory loads on first
// page view, before the user picks a preset or drags a selection. Not a
// fifth preset -- reading_chart.templ's ReadingChart still offers exactly
// the same four buttons (1h/24h/7d/30d); this only picks which of them the
// page starts on.
const defaultHistoryPreset = "24h"

// presetDurations is FR8's complete, closed preset set -- exactly these
// four trailing windows and no others (no free-form date/time picker, no
// range over 30 days constructible through this map).
var presetDurations = map[string]time.Duration{
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// presetRange computes a trailing window ending at now for one of FR8's
// four fixed presets. ok is false for anything else, including an attempt
// to smuggle a longer span through this parameter -- presetDurations above
// is the complete set, not a lookup with a fallback.
func presetRange(preset string, now time.Time) (from, to time.Time, ok bool) {
	d, ok := presetDurations[preset]
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return now.Add(-d), now, true
}

// toChartData converts one GetSensorReadingHistoryResponse plus the
// absolute range that was requested into reading_chart.templ's
// components.ReadingChartData -- the only place this conversion happens,
// shared by handleSensorHistory's initial server-rendered load and
// handleSensorHistoryData's JSON responses, so both take the exact same
// path from protobuf response to wire/render shape.
func toChartData(sensorID int64, unit string, from, to time.Time, resp *leaflabapipb.GetSensorReadingHistoryResponse) components.ReadingChartData {
	data := components.ReadingChartData{
		SensorID:             sensorID,
		Unit:                 unit,
		FromUnix:             from.Unix(),
		ToUnix:               to.Unix(),
		Capped:               resp.GetCapped(),
		PointCap:             resp.GetPointCap(),
		ExcludedInvalidCount: resp.GetExcludedInvalidCount(),
	}
	if resp.GetCoveredFrom() != nil {
		data.CoveredFromUnix = resp.GetCoveredFrom().AsTime().Unix()
	}
	if resp.GetCoveredTo() != nil {
		data.CoveredToUnix = resp.GetCoveredTo().AsTime().Unix()
	}
	for _, p := range resp.GetPoints() {
		data.Points = append(data.Points, components.ReadingPointData{
			RecordedAtUnix: p.GetRecordedAt().AsTime().Unix(),
			Value:          p.GetValue(),
		})
	}
	return data
}

// handleSensorHistory is the sensor reading-history chart page (#1504:
// FR8, FR9, FR10, NFR1), routed at "/sensors/{sensor_id}/history". It
// loads defaultHistoryPreset's range via GetSensorReadingHistory on
// leaflab-api with the signed-in user's own access token (NFR2, forwarded
// by htmxauth.Authenticator.WithAccessToken in setupRoutes) so the chart
// paints on first load without an extra client round trip; every
// subsequent preset click or drag-select goes through
// handleSensorHistoryData instead (NFR1: nothing here polls). This
// handler runs no SQL and touches no board/sensor/sensor_reading table
// itself.
//
// board_id/device_id/sensor_name/unit arrive as query parameters from
// board_detail.templ's link -- see sensor_history.templ's doc comment for
// why (no RPC maps a sensor_id back to its board or name). Their absence
// is handled the same non-blocking way FR10 handles an empty range: the
// page still renders in full, just with less chrome.
//
// A malformed or unknown sensor_id gets a real HTTP 404 (matching
// handleBoardDetail's board_id handling), distinguishable from a
// zero-point range (which is FR10's non-blocking empty-range indicator,
// not a 404).
func (app *App) handleSensorHistory(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	sensorID, parseErr := strconv.ParseInt(r.PathValue("sensor_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	query := r.URL.Query()
	boardID, _ := strconv.ParseInt(query.Get("board_id"), 10, 64)
	deviceID := query.Get("device_id")
	sensorName := query.Get("sensor_name")
	unit := query.Get("unit")

	from, to, _ := presetRange(defaultHistoryPreset, time.Now())

	resp, err := app.api.GetSensorReadingHistory(r.Context(), sensorID, from, to)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				// Same re-authenticate flow as handleBoards'/
				// handleBoardDetail's identical branch -- see
				// handlers_boards.go's comment for why this is a
				// redirect, not an error page.
				loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", loginURL)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, loginURL, http.StatusSeeOther)
				return
			case codes.NotFound:
				http.NotFound(w, r)
				return
			}
		}
		app.log().Warn("GetSensorReadingHistory failed", "sensor_id", sensorID, "err", err)
	}

	layoutData := components.LayoutData{
		Title: "Sensor History",
		User:  user,
	}

	var chartData components.ReadingChartData
	if err == nil {
		chartData = toChartData(sensorID, unit, from, to, resp)
		if resp.GetCapped() {
			app.log().Warn("sensor history chart truncated — response hit the point cap",
				"sensor_id", sensorID, "point_cap", resp.GetPointCap())
		}
	}

	if renderErr := RenderTempl(w, r, "Sensor History", pages.SensorHistory(layoutData, boardID, deviceID, sensorID, sensorName, chartData, err)); renderErr != nil {
		app.log().Error("failed to render sensor history page", "sensor_id", sensorID, "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleSensorHistoryData is the small JSON endpoint reading_chart.templ's
// script fetches from on every preset click or drag-select (FR8) -- never
// on a timer (NFR1). It accepts either "?preset=<1h|24h|7d|30d>" (computed
// to absolute bounds here via presetRange, so the client never constructs
// its own preset math) or an explicit "?from=<unix>&to=<unix>" pair (a
// drag-select's exact endpoints, computed client-side from the
// already-rendered chart's own domain -- see reading_chart.templ's
// xToTime). It calls GetSensorReadingHistory on leaflab-api with the
// signed-in user's own access token (NFR2) and returns exactly what the
// API reports, reshaped to JSON via toChartData: no client-side
// aggregation, downsampling, or smoothing (FR9), and no SQL here.
func (app *App) handleSensorHistoryData(w http.ResponseWriter, r *http.Request) {
	sensorID, parseErr := strconv.ParseInt(r.PathValue("sensor_id"), 10, 64)
	if parseErr != nil {
		http.Error(w, "invalid sensor_id", http.StatusBadRequest)
		return
	}

	query := r.URL.Query()
	var from, to time.Time
	if preset := query.Get("preset"); preset != "" {
		var ok bool
		from, to, ok = presetRange(preset, time.Now())
		if !ok {
			http.Error(w, fmt.Sprintf("unknown preset %q", preset), http.StatusBadRequest)
			return
		}
	} else {
		fromUnix, fromErr := strconv.ParseInt(query.Get("from"), 10, 64)
		toUnix, toErr := strconv.ParseInt(query.Get("to"), 10, 64)
		if fromErr != nil || toErr != nil {
			http.Error(w, "either preset or from/to (unix seconds) query params are required", http.StatusBadRequest)
			return
		}
		from = time.Unix(fromUnix, 0).UTC()
		to = time.Unix(toUnix, 0).UTC()
	}

	resp, err := app.api.GetSensorReadingHistory(r.Context(), sensorID, from, to)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			case codes.InvalidArgument:
				http.Error(w, st.Message(), http.StatusBadRequest)
				return
			case codes.NotFound:
				http.Error(w, st.Message(), http.StatusNotFound)
				return
			}
		}
		app.log().Warn("GetSensorReadingHistory failed", "sensor_id", sensorID, "err", err)
		http.Error(w, "failed to load reading history", http.StatusBadGateway)
		return
	}

	if resp.GetCapped() {
		app.log().Warn("sensor history chart data truncated — response hit the point cap",
			"sensor_id", sensorID, "point_cap", resp.GetPointCap())
	}

	// unit is not carried on the wire response (ReadingChartData tags it
	// json:"-") -- the JS already has it from data-unit -- so it is left
	// blank in this conversion.
	data := toChartData(sensorID, "", from, to, resp)

	w.Header().Set("Content-Type", "application/json")
	if encErr := json.NewEncoder(w).Encode(data); encErr != nil {
		app.log().Error("failed to encode sensor history JSON", "sensor_id", sensorID, "err", encErr)
	}
}
