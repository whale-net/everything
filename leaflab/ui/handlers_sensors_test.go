package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
)

// --- presetRange (#1504: FR8) ------------------------------------------

// TestPresetRange_FourFixedPresets covers the Testing section's "each of
// 1h/24h/7d/30d produces absolute bounds ending at now with the right
// span" case -- exactly these four, computed from a fixed `now` so the
// span assertion is exact, not a race against time.Now().
func TestPresetRange_FourFixedPresets(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		preset   string
		wantSpan time.Duration
	}{
		{"1h", time.Hour},
		{"24h", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.preset, func(t *testing.T) {
			from, to, ok := presetRange(tc.preset, now)
			if !ok {
				t.Fatalf("presetRange(%q) ok = false, want true", tc.preset)
			}
			if !to.Equal(now) {
				t.Errorf("presetRange(%q) to = %v, want %v (ending at now)", tc.preset, to, now)
			}
			if span := to.Sub(from); span != tc.wantSpan {
				t.Errorf("presetRange(%q) span = %v, want %v", tc.preset, span, tc.wantSpan)
			}
			if to.Sub(from) > 30*24*time.Hour {
				t.Errorf("presetRange(%q) span = %v, exceeds the 30-day maximum", tc.preset, to.Sub(from))
			}
		})
	}
}

// TestPresetRange_UnknownPreset_NotOK guards FR8's "these four and no
// others" -- nothing outside the fixed set (including an attempt to smuggle
// a longer span through this parameter) produces a range.
func TestPresetRange_UnknownPreset_NotOK(t *testing.T) {
	for _, preset := range []string{"", "1d", "90d", "365d", "1y", "all"} {
		if _, _, ok := presetRange(preset, time.Now()); ok {
			t.Errorf("presetRange(%q) ok = true, want false (not one of the four fixed presets)", preset)
		}
	}
}

// --- handleSensorHistory (#1504: FR8, FR9, FR10) ------------------------

func newSensorHistoryRequest(sensorID, query string) *http.Request {
	target := "/sensors/" + sensorID + "/history"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("sensor_id", sensorID)
	return req
}

// TestHandleSensorHistory_RendersChartFromAPI covers the happy path:
// handleSensorHistory calls GetSensorReadingHistory via app.api (no SQL)
// and renders the board/device/sensor context carried on the query
// string plus the chart for the default preset's range.
func TestHandleSensorHistory_RendersChartFromAPI(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyResp: &leaflabapipb.GetSensorReadingHistoryResponse{
		Points: []*leaflabapipb.ReadingPoint{
			{RecordedAt: timestamppb.New(time.Now().Add(-1 * time.Hour)), Value: 42.5},
		},
	}}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistory(rec, newSensorHistoryRequest("7", "board_id=3&device_id=leaflab-aaaaaaaaaaaa&sensor_name=Soil+Moisture&unit=%25"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Soil Moisture", "leaflab-aaaaaaaaaaaa"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the rendered page, got %q", want, body)
		}
	}
}

// TestHandleSensorHistory_NoContextQueryParams_StillRendersOK covers a
// direct navigation with none of board_id/device_id/sensor_name/unit set
// -- the page must still render in full (FR10-style non-blocking chrome),
// falling back to the bare sensor id rather than erroring.
func TestHandleSensorHistory_NoContextQueryParams_StillRendersOK(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyResp: &leaflabapipb.GetSensorReadingHistoryResponse{}}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistory(rec, newSensorHistoryRequest("7", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Sensor 7") {
		t.Errorf("expected the bare-sensor-id fallback heading, got %q", rec.Body.String())
	}
}

// TestHandleSensorHistory_MalformedSensorID_NotFound mirrors
// handleBoardDetail's identical guard.
func TestHandleSensorHistory_MalformedSensorID_NotFound(t *testing.T) {
	app := &App{api: &LeafLabClient{api: &fakeLeafLabAPIClient{}}}

	rec := httptest.NewRecorder()
	app.handleSensorHistory(rec, newSensorHistoryRequest("not-a-number", ""))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleSensorHistory_UnknownSensorID_NotFound covers a
// codes.NotFound response from leaflab-api (an unknown sensor_id).
func TestHandleSensorHistory_UnknownSensorID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyErr: status.Error(codes.NotFound, "sensor 999 not found")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistory(rec, newSensorHistoryRequest("999", ""))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleSensorHistory_Unauthenticated_RedirectsToLogin mirrors
// handleBoardDetail's identical branch.
func TestHandleSensorHistory_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyErr: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistory(rec, newSensorHistoryRequest("7", ""))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
}

// TestHandleSensorHistory_GenericError_RendersErrorState proves a
// non-NotFound, non-Unauthenticated gRPC failure still renders the page
// (status 200) with a visible error, not a crash and not a 404.
func TestHandleSensorHistory_GenericError_RendersErrorState(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyErr: status.Error(codes.Internal, "boom")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistory(rec, newSensorHistoryRequest("7", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (error rendered on the page, not a hard failure)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Failed to load reading history") {
		t.Errorf("expected the load-error message in the rendered page, got %q", rec.Body.String())
	}
}

// --- handleSensorHistoryData (#1504: FR8, FR9) --------------------------

func newSensorHistoryDataRequest(sensorID, query string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/sensors/"+sensorID+"/history/data?"+query, nil)
	req.SetPathValue("sensor_id", sensorID)
	return req
}

// TestHandleSensorHistoryData_Preset_ReturnsJSON covers the preset
// (FR8) fetch path: "?preset=24h" resolves to absolute bounds
// server-side via presetRange and returns the API's response as JSON.
func TestHandleSensorHistoryData_Preset_ReturnsJSON(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyResp: &leaflabapipb.GetSensorReadingHistoryResponse{
		Points: []*leaflabapipb.ReadingPoint{
			{RecordedAt: timestamppb.New(time.Now()), Value: 1.5},
		},
	}}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistoryData(rec, newSensorHistoryDataRequest("7", "preset=24h"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), `"value":1.5`) {
		t.Errorf("expected the point's value in the JSON body, got %q", rec.Body.String())
	}
}

// TestHandleSensorHistoryData_ExplicitFromTo_ReturnsJSON covers the
// drag-select fetch path: an explicit "?from=&to=" pair, as
// reading_chart.templ's script sends after a drag-select.
func TestHandleSensorHistoryData_ExplicitFromTo_ReturnsJSON(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyResp: &leaflabapipb.GetSensorReadingHistoryResponse{}}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistoryData(rec, newSensorHistoryDataRequest("7", "from=1000&to=2000"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"from":1000`) || !strings.Contains(rec.Body.String(), `"to":2000`) {
		t.Errorf("expected the requested from/to echoed back in the JSON body, got %q", rec.Body.String())
	}
}

// TestHandleSensorHistoryData_CappedAndExcluded_JSONCarriesBothSignals
// covers the wire half of FR9's independence rule: reading_chart.templ's
// script re-renders the coverage and excluded notices from this JSON on
// every preset/drag-select fetch (see the exact-open-tag comment in
// sensor_history_test.go), so both signals -- capped, the actual
// point_cap, covered_from/covered_to, and excluded_invalid_count -- must
// all be present in the same response, not just at the initial SSR load
// toChartData also feeds.
func TestHandleSensorHistoryData_CappedAndExcluded_JSONCarriesBothSignals(t *testing.T) {
	coveredFrom := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	coveredTo := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	fake := &fakeLeafLabAPIClient{historyResp: &leaflabapipb.GetSensorReadingHistoryResponse{
		Points:               []*leaflabapipb.ReadingPoint{{RecordedAt: timestamppb.New(coveredTo), Value: 3.25}},
		Capped:               true,
		CoveredFrom:          timestamppb.New(coveredFrom),
		CoveredTo:            timestamppb.New(coveredTo),
		PointCap:             5000,
		ExcludedInvalidCount: 12,
	}}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistoryData(rec, newSensorHistoryDataRequest("7", "preset=30d"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"capped":true`,
		fmt.Sprintf(`"covered_from":%d`, coveredFrom.Unix()),
		fmt.Sprintf(`"covered_to":%d`, coveredTo.Unix()),
		`"point_cap":5000`,
		`"excluded_invalid_count":12`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the JSON body (both signals must ride the same response), got %q", want, body)
		}
	}
}

// TestHandleSensorHistoryData_UnknownPreset_BadRequest guards against a
// client sending anything outside FR8's fixed preset set.
func TestHandleSensorHistoryData_UnknownPreset_BadRequest(t *testing.T) {
	app := &App{api: &LeafLabClient{api: &fakeLeafLabAPIClient{}}}

	rec := httptest.NewRecorder()
	app.handleSensorHistoryData(rec, newSensorHistoryDataRequest("7", "preset=90d"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleSensorHistoryData_MissingParams_BadRequest guards a request
// with neither preset nor from/to.
func TestHandleSensorHistoryData_MissingParams_BadRequest(t *testing.T) {
	app := &App{api: &LeafLabClient{api: &fakeLeafLabAPIClient{}}}

	rec := httptest.NewRecorder()
	app.handleSensorHistoryData(rec, newSensorHistoryDataRequest("7", ""))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleSensorHistoryData_InvalidArgument_BadRequest covers the API
// rejecting an over-30-day range (or any other InvalidArgument) with a
// matching HTTP 400, not a 500 or a silently-swallowed error.
func TestHandleSensorHistoryData_InvalidArgument_BadRequest(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyErr: status.Error(codes.InvalidArgument, "range must not exceed 30 days")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistoryData(rec, newSensorHistoryDataRequest("7", "from=0&to=999999999"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleSensorHistoryData_Unauthenticated_Returns401 covers the
// fetch-endpoint variant of the redirect handling handleSensorHistory
// itself does for a full page load: a JSON fetch can't follow a redirect
// usefully, so this returns a plain 401 instead.
func TestHandleSensorHistoryData_Unauthenticated_Returns401(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyErr: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistoryData(rec, newSensorHistoryDataRequest("7", "preset=1h"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestHandleSensorHistoryData_NotFound_Returns404 covers an unknown
// sensor_id.
func TestHandleSensorHistoryData_NotFound_Returns404(t *testing.T) {
	fake := &fakeLeafLabAPIClient{historyErr: status.Error(codes.NotFound, "sensor 999 not found")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSensorHistoryData(rec, newSensorHistoryDataRequest("999", "preset=1h"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
