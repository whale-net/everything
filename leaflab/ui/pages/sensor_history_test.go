package pages

import (
	"errors"
	"strings"
	"testing"

	"github.com/whale-net/everything/leaflab/ui/components"
)

// --- SensorHistory (#1504: FR8, FR9, FR10, NFR1) ------------------------

// TestSensorHistory_Capped_RendersCoverageNoticeFromCoveredRange covers
// the Testing section's "Capped response fixture -> the coverage notice
// renders, phrased from covered_from/covered_to, and point_cap (not a
// literal 15000) is the source of any count shown."
func TestSensorHistory_Capped_RendersCoverageNoticeFromCoveredRange(t *testing.T) {
	chartData := components.ReadingChartData{
		SensorID:        1,
		Unit:            "%",
		FromUnix:        0,
		ToUnix:          30 * 24 * 3600,
		Capped:          true,
		CoveredFromUnix: 20 * 24 * 3600,
		CoveredToUnix:   30 * 24 * 3600,
		PointCap:        5000,
		Points:          []components.ReadingPointData{{RecordedAtUnix: 25 * 24 * 3600, Value: 1}},
	}
	body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", chartData, nil))

	if !strings.Contains(body, "10 days") || !strings.Contains(body, "30 days") {
		t.Errorf("expected the coverage notice phrased from the covered (10 day) and requested (30 day) spans, got %q", body)
	}
	if strings.Contains(body, "15000") || strings.Contains(body, "15,000") {
		t.Errorf("expected no literal 15000 point cap, got %q", body)
	}
	if !strings.Contains(body, "5000") {
		t.Errorf("expected the actual point_cap (5000) to be the source of any count shown, got %q", body)
	}
}

// Exact opening tags reading_chart.templ's ReadingChart emits for each
// notice. These are used, instead of a bare class-name substring check,
// because reading_chart.templ's own inline <script> mirrors the same
// class names and phrase fragments as JS string literals (for the
// notices it re-renders on a later preset/drag-select fetch) -- a bare
// substring check against e.g. "reading-chart-excluded-notice" or
// "excluded" would pass even if the SSR conditional that actually gates
// this notice were broken, since that text is unconditionally present in
// the page's <script> block too. The exact "<div role=... class=...>"
// tag syntax below is never written by the script (it builds elements via
// createElement/className, not by writing this angle-bracket form), so it
// only ever appears when ReadingChart's own if-branch rendered it.
const (
	coverageNoticeOpenTag = `<div role="status" class="alert alert-info py-2 reading-chart-coverage-notice">`
	excludedNoticeOpenTag = `<div role="status" class="alert alert-warning py-2 reading-chart-excluded-notice">`
	emptyNoticeOpenTag    = `<div role="status" class="text-base-content/60 text-sm py-2 reading-chart-empty-notice">`
)

// TestSensorHistory_ExcludedInvalid_RendersExcludedNotice covers "Invalid-
// exclusion fixture -> the excluded-count note renders."
func TestSensorHistory_ExcludedInvalid_RendersExcludedNotice(t *testing.T) {
	chartData := components.ReadingChartData{
		SensorID:             1,
		ExcludedInvalidCount: 7,
		Points:               []components.ReadingPointData{{RecordedAtUnix: 1, Value: 1}},
	}
	body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", chartData, nil))

	if !strings.Contains(body, excludedNoticeOpenTag) {
		t.Errorf("expected the excluded notice's markup to render, got %q", body)
	}
	if !strings.Contains(body, "7 readings in this range were excluded for being marked invalid.") {
		t.Errorf("expected the excluded-count note's exact phrasing, got %q", body)
	}
}

// TestSensorHistory_CappedAndExcluded_BothRenderIndependently covers the
// Testing section's "Capped and invalid together -> both notices render
// in the same output. The independence assertion."
func TestSensorHistory_CappedAndExcluded_BothRenderIndependently(t *testing.T) {
	chartData := components.ReadingChartData{
		SensorID:             1,
		FromUnix:             0,
		ToUnix:               30 * 24 * 3600,
		Capped:               true,
		CoveredFromUnix:      20 * 24 * 3600,
		CoveredToUnix:        30 * 24 * 3600,
		PointCap:             5000,
		ExcludedInvalidCount: 12,
		Points:               []components.ReadingPointData{{RecordedAtUnix: 25 * 24 * 3600, Value: 1}},
	}
	body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", chartData, nil))

	if !strings.Contains(body, coverageNoticeOpenTag) {
		t.Errorf("expected the coverage notice's markup to render alongside the excluded notice, got %q", body)
	}
	if !strings.Contains(body, excludedNoticeOpenTag) {
		t.Errorf("expected the excluded notice's markup to render alongside the coverage notice, got %q", body)
	}
	if !strings.Contains(body, "12 readings in this range were excluded for being marked invalid.") {
		t.Errorf("expected the excluded count (12) in the rendered output, got %q", body)
	}
}

// TestSensorHistory_EmptyRange_ZeroExcluded_RendersEmptyIndicator covers
// "Zero points, zero excluded -> the empty-range indicator, page renders
// fully, no error markup."
func TestSensorHistory_EmptyRange_ZeroExcluded_RendersEmptyIndicator(t *testing.T) {
	chartData := components.ReadingChartData{SensorID: 1}
	body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", chartData, nil))

	if !strings.Contains(body, emptyNoticeOpenTag) {
		t.Errorf("expected the empty-range indicator's markup, got %q", body)
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("expected no page-level error styling for an empty range, got %q", body)
	}
}

// TestSensorHistory_EmptyRange_NonZeroExcluded_ShowsBothTogether covers
// "Zero points, non-zero excluded -> the empty indicator and the excluded
// count together."
func TestSensorHistory_EmptyRange_NonZeroExcluded_ShowsBothTogether(t *testing.T) {
	chartData := components.ReadingChartData{SensorID: 1, ExcludedInvalidCount: 4}
	body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", chartData, nil))

	if !strings.Contains(body, emptyNoticeOpenTag) {
		t.Errorf("expected the empty-range indicator's markup, got %q", body)
	}
	if !strings.Contains(body, "All 4 readings in this range were marked invalid.") {
		t.Errorf("expected the all-invalid explanation naming the count, got %q", body)
	}
}

// TestSensorHistory_NoDatePickerOrPanControl guards the "Do not build a
// free-form date/time picker... Do not build a pan control either"
// instruction: assert the rendered output contains no date/time input
// element and no pan control.
func TestSensorHistory_NoDatePickerOrPanControl(t *testing.T) {
	chartData := components.ReadingChartData{SensorID: 1, Points: []components.ReadingPointData{{RecordedAtUnix: 1, Value: 1}}}
	body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", chartData, nil))

	for _, banned := range []string{`type="date"`, `type="datetime-local"`, `type="time"`, "pan-left", "pan-right", "pan-control"} {
		if strings.Contains(body, banned) {
			t.Errorf("expected no %q in the rendered output (no free-form picker, no pan control), got %q", banned, body)
		}
	}
}

// TestSensorHistory_NoAutoRefreshMarkup is NFR1's guard: no hx-trigger
// polling interval, no sse-connect, across every state this page can
// render.
func TestSensorHistory_NoAutoRefreshMarkup(t *testing.T) {
	fixtures := map[string]struct {
		chartData components.ReadingChartData
		loadErr   error
	}{
		"with points": {chartData: components.ReadingChartData{SensorID: 1, Points: []components.ReadingPointData{{RecordedAtUnix: 1, Value: 1}}}},
		"empty":       {chartData: components.ReadingChartData{SensorID: 1}},
		"capped":      {chartData: components.ReadingChartData{SensorID: 1, Capped: true, PointCap: 5000, Points: []components.ReadingPointData{{RecordedAtUnix: 1, Value: 1}}}},
		"load error":  {loadErr: errors.New("leaflab-api unavailable")},
	}
	for name, f := range fixtures {
		t.Run(name, func(t *testing.T) {
			body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", f.chartData, f.loadErr))
			if strings.Contains(body, "sse-connect") {
				t.Errorf("[%s] expected no sse-connect anywhere on the sensor history page (NFR1), got %q", name, body)
			}
			if strings.Contains(body, "hx-trigger") {
				t.Errorf("[%s] expected no hx-trigger anywhere on the sensor history page (NFR1: no polling interval), got %q", name, body)
			}
		})
	}
}

// TestSensorHistory_LoadError_RendersErrorState covers a real load
// failure, distinct from the zero-points empty state.
func TestSensorHistory_LoadError_RendersErrorState(t *testing.T) {
	body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", components.ReadingChartData{}, errors.New("leaflab-api unavailable")))

	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected alert-error styling on a load failure, got %q", body)
	}
	if !strings.Contains(body, "leaflab-api unavailable") {
		t.Errorf("expected the load error's message in the rendered output, got %q", body)
	}
	if strings.Contains(body, "No readings in this range") {
		t.Errorf("expected the error state, not the empty-range message, got %q", body)
	}
}

// TestSensorHistory_NoContext_FallsBackWithoutError covers a direct
// navigation with no board/device/sensor-name context (FR10-style
// non-blocking chrome): the page still renders in full.
func TestSensorHistory_NoContext_FallsBackWithoutError(t *testing.T) {
	body := renderPage(t, SensorHistory(layoutData(), 0, "", 7, "", components.ReadingChartData{SensorID: 7}, nil))

	if !strings.Contains(body, "Sensor 7") {
		t.Errorf("expected the bare-sensor-id fallback heading, got %q", body)
	}
	if !strings.Contains(body, "/boards") {
		t.Errorf("expected a fallback link to the boards list, got %q", body)
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("expected no error styling from missing context alone, got %q", body)
	}
}

// TestSensorHistory_ExactlyFourPresets guards FR8's "these four and no
// others" -- the preset button set the rendered markup exposes.
func TestSensorHistory_ExactlyFourPresets(t *testing.T) {
	chartData := components.ReadingChartData{SensorID: 1, Points: []components.ReadingPointData{{RecordedAtUnix: 1, Value: 1}}}
	body := renderPage(t, SensorHistory(layoutData(), 3, "leaflab-aaaaaaaaaaaa", 1, "Soil Moisture", chartData, nil))

	for _, preset := range []string{`data-preset="1h"`, `data-preset="24h"`, `data-preset="7d"`, `data-preset="30d"`} {
		if !strings.Contains(body, preset) {
			t.Errorf("expected preset button %q, got %q", preset, body)
		}
	}
	if got := strings.Count(body, `class="btn btn-sm reading-chart-preset-btn"`); got != 4 {
		t.Errorf("expected exactly 4 preset buttons, got %d in %q", got, body)
	}
}
