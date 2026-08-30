package pages

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"google.golang.org/protobuf/types/known/timestamppb"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
)

// TestBoardDetail_ThreeStates_AllRenderedNoBlockingError covers the
// Testing section's "A board with one reporting, one stale, and one
// never-reported sensor -> all three rendered, page renders fully, no
// error markup" case -- FR7's non-blocking assertion: a stale or
// never-reported sensor is a normal row, not a page-level error.
func TestBoardDetail_ThreeStates_AllRenderedNoBlockingError(t *testing.T) {
	sensors := []*leaflabapipb.SensorDetail{
		{
			SensorId:       1,
			SensorName:     "Soil Moisture",
			Unit:           "%",
			SensorTypeName: "capacitive",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
			LatestReading: &leaflabapipb.LatestReading{
				Value:      42.5,
				RecordedAt: timestamppb.New(time.Now().Add(-1 * time.Minute)),
				Valid:      true,
			},
		},
		{
			SensorId:       2,
			SensorName:     "Air Temp",
			Unit:           "C",
			SensorTypeName: "thermistor",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_STALE,
			LatestReading: &leaflabapipb.LatestReading{
				Value:      21.0,
				RecordedAt: timestamppb.New(time.Now().Add(-42 * time.Minute)),
				Valid:      true,
			},
		},
		{
			SensorId:       3,
			SensorName:     "Light",
			Unit:           "lux",
			SensorTypeName: "photodiode",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_NEVER_REPORTED,
		},
	}
	body := renderPage(t, BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", sensors, nil))

	for _, name := range []string{"Soil Moisture", "Air Temp", "Light"} {
		if !strings.Contains(body, name) {
			t.Errorf("expected sensor %q to appear in rendered output, got %q", name, body)
		}
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("expected no page-level error styling with a mix of states, got %q", body)
	}
	if got := strings.Count(body, "badge-success"); got != 1 {
		t.Errorf("expected exactly 1 badge-success (reporting), got %d in %q", got, body)
	}
	if got := strings.Count(body, "badge-warning"); got != 1 {
		t.Errorf("expected exactly 1 badge-warning (stale), got %d in %q", got, body)
	}
	if got := strings.Count(body, "badge-neutral"); got != 1 {
		t.Errorf("expected exactly 1 badge-neutral (never reported), got %d in %q", got, body)
	}
}

// TestBoardDetail_InvalidLatestReading_MarkedInvalidStateUnaffected is the
// task's "most important case": a sensor whose latest reading is one
// minute old and valid == false must still render the value, marked
// invalid, with the state badge unaffected (still "Reporting" -- state and
// validity are orthogonal per FR7). Verify red/green: making the invalid
// path blank the value (e.g. rendering nothing instead of the marked
// value) turns this test red on the missing value text; reverting restores
// green -- see the commit message for the paired before/after run.
func TestBoardDetail_InvalidLatestReading_MarkedInvalidStateUnaffected(t *testing.T) {
	sensors := []*leaflabapipb.SensorDetail{
		{
			SensorId:       1,
			SensorName:     "Soil Moisture",
			Unit:           "%",
			SensorTypeName: "capacitive",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
			LatestReading: &leaflabapipb.LatestReading{
				Value:      999.9,
				RecordedAt: timestamppb.New(time.Now().Add(-1 * time.Minute)),
				Valid:      false,
			},
		},
	}
	body := renderPage(t, BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", sensors, nil))

	if !strings.Contains(body, "999.9") {
		t.Errorf("expected the invalid reading's value to still be rendered, got %q", body)
	}
	if !strings.Contains(body, "Invalid") {
		t.Errorf("expected an 'Invalid' label on the marked reading, got %q", body)
	}
	if !strings.Contains(body, "Reporting") {
		t.Errorf("expected the state badge to still read 'Reporting' (state is orthogonal to validity), got %q", body)
	}
	if strings.Contains(body, "Never reported") {
		t.Errorf("expected no 'Never reported' fallback for an invalid-but-present reading, got %q", body)
	}
}

// TestBoardDetail_Stale_RendersLastValueAndTimestamp covers "A stale
// sensor renders its last value and timestamp, not a blank."
func TestBoardDetail_Stale_RendersLastValueAndTimestamp(t *testing.T) {
	recordedAt := time.Now().Add(-2 * time.Hour)
	sensors := []*leaflabapipb.SensorDetail{
		{
			SensorId:       1,
			SensorName:     "Air Temp",
			Unit:           "C",
			SensorTypeName: "thermistor",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_STALE,
			LatestReading: &leaflabapipb.LatestReading{
				Value:      21.5,
				RecordedAt: timestamppb.New(recordedAt),
				Valid:      true,
			},
		},
	}
	body := renderPage(t, BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", sensors, nil))

	if !strings.Contains(body, "21.5") {
		t.Errorf("expected the stale sensor's last value in the rendered output, got %q", body)
	}
	if !strings.Contains(body, formatReadingTime(recordedAt)) {
		t.Errorf("expected the stale sensor's reading timestamp in the rendered output, got %q", body)
	}
}

// TestBoardDetail_NeverReported_NoValueNoTimestamp covers "A never
// reported sensor renders no value and no timestamp."
func TestBoardDetail_NeverReported_NoValueNoTimestamp(t *testing.T) {
	sensors := []*leaflabapipb.SensorDetail{
		{
			SensorId:       1,
			SensorName:     "Light",
			Unit:           "lux",
			SensorTypeName: "photodiode",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_NEVER_REPORTED,
		},
	}
	body := renderPage(t, BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", sensors, nil))

	if strings.Contains(body, "lux") {
		t.Errorf("expected no unit/value rendered for a never-reported sensor, got %q", body)
	}
	if !strings.Contains(body, "Never reported") {
		t.Errorf("expected the neutral 'Never reported' label, got %q", body)
	}
}

// TestBoardDetail_ZeroSensors_EmptyMessageNoError covers "A board with
// zero sensors -> the empty message", distinct from an error state.
func TestBoardDetail_ZeroSensors_EmptyMessageNoError(t *testing.T) {
	body := renderPage(t, BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", nil, nil))

	if !strings.Contains(body, "This board has no sensors yet.") {
		t.Errorf("expected the empty-state message, got %q", body)
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("expected no error styling for a zero-sensor board, got %q", body)
	}
}

// TestBoardDetail_LoadError_RendersErrorState covers a real load failure
// (distinct from the zero-sensors empty state and from the 404 the
// handler takes for an unknown board_id before this template is ever
// reached).
func TestBoardDetail_LoadError_RendersErrorState(t *testing.T) {
	body := renderPage(t, BoardDetail(layoutData(), 1, "", nil, errors.New("leaflab-api unavailable")))

	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected alert-error styling on a load failure, got %q", body)
	}
	if !strings.Contains(body, "leaflab-api unavailable") {
		t.Errorf("expected the load error's message in the rendered output, got %q", body)
	}
	if strings.Contains(body, "This board has no sensors yet.") {
		t.Errorf("expected the error state, not the empty-sensors message, got %q", body)
	}
}

// TestBoardDetail_DeviceIDFullLengthVerbatim guards "The full device_id
// appears verbatim."
func TestBoardDetail_DeviceIDFullLengthVerbatim(t *testing.T) {
	const fullID = "leaflab-ccdba79f5fac1234567890abcdef"
	sensors := []*leaflabapipb.SensorDetail{
		{SensorId: 1, SensorName: "Soil Moisture", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
	}
	body := renderPage(t, BoardDetail(layoutData(), 1, fullID, sensors, nil))

	if !strings.Contains(body, fullID) {
		t.Errorf("expected full device_id %q to appear verbatim and uncut, got %q", fullID, body)
	}
}

// TestBoardDetail_NoAutoRefreshMarkup is NFR1's guard: no hx-trigger
// polling interval, no sse-connect, across every state this page can
// render.
func TestBoardDetail_NoAutoRefreshMarkup(t *testing.T) {
	sensors := []*leaflabapipb.SensorDetail{
		{
			SensorId:       1,
			SensorName:     "Soil Moisture",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
			LatestReading: &leaflabapipb.LatestReading{
				Value:      42.5,
				RecordedAt: timestamppb.New(time.Now()),
				Valid:      true,
			},
		},
	}
	fixtures := map[string]templ.Component{
		"with sensors": BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", sensors, nil),
		"empty":        BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", nil, nil),
		"load error":   BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", nil, errors.New("boom")),
	}
	for name, component := range fixtures {
		t.Run(name, func(t *testing.T) {
			body := renderPage(t, component)
			if strings.Contains(body, "sse-connect") {
				t.Errorf("[%s] expected no sse-connect anywhere on the board detail page (NFR1), got %q", name, body)
			}
			if strings.Contains(body, "hx-trigger") {
				t.Errorf("[%s] expected no hx-trigger anywhere on the board detail page (NFR1: no polling interval), got %q", name, body)
			}
		})
	}
}

// TestBoardDetail_NoPollIntervalRegionOrOwnerText guards the "Do not
// display" section: no poll-interval, region/location, or owner text
// appears anywhere on the page.
func TestBoardDetail_NoPollIntervalRegionOrOwnerText(t *testing.T) {
	sensors := []*leaflabapipb.SensorDetail{
		{
			SensorId:       1,
			SensorName:     "Soil Moisture",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
			LatestReading: &leaflabapipb.LatestReading{
				Value:      42.5,
				RecordedAt: timestamppb.New(time.Now()),
				Valid:      true,
			},
		},
	}
	body := renderPage(t, BoardDetail(layoutData(), 1, "leaflab-aaaaaaaaaaaa", sensors, nil))

	for _, banned := range []string{"poll interval", "Poll Interval", "Region", "Location", "Owner"} {
		if strings.Contains(body, banned) {
			t.Errorf("expected no %q text on the board detail page, got %q", banned, body)
		}
	}
}
