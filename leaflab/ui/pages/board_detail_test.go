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

// boardDetailResp builds a *leaflabapipb.GetBoardDetailResponse fixture,
// carrying whatever name/owner/sensors fields a given test needs -- kept as
// a helper since #1765 widened BoardDetail's second parameter from
// (boardID int64, deviceID string, sensors []*SensorDetail) to a single
// *GetBoardDetailResponse.
func boardDetailResp(boardID int64, deviceID, boardName string, ownedByCaller bool, owner *leaflabapipb.LeafLabUser, sensors []*leaflabapipb.SensorDetail) *leaflabapipb.GetBoardDetailResponse {
	return &leaflabapipb.GetBoardDetailResponse{
		BoardId:       boardID,
		DeviceId:      deviceID,
		BoardName:     boardName,
		OwnedByCaller: ownedByCaller,
		Owner:         owner,
		Sensors:       sensors,
	}
}

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
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, sensors)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

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
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, sensors)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

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
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, sensors)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

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
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, sensors)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

	// The reading cell itself must render the "—" placeholder, not a
	// value or timestamp. "lux" can legitimately still appear elsewhere on
	// the row -- the row's History link (#1504) carries the sensor's unit
	// in its query string regardless of reporting state, since the
	// reading-history page needs it for its value axis whether or not the
	// sensor has ever reported -- so this no longer asserts "lux" is
	// absent from the whole page body.
	if !strings.Contains(body, `<span class="text-base-content/40">—</span>`) {
		t.Errorf("expected the '—' placeholder for a never-reported sensor's reading cell, got %q", body)
	}
	if !strings.Contains(body, "Never reported") {
		t.Errorf("expected the neutral 'Never reported' label, got %q", body)
	}
}

// TestBoardDetail_ZeroSensors_EmptyMessageNoError covers "A board with
// zero sensors -> the empty message", distinct from an error state.
func TestBoardDetail_ZeroSensors_EmptyMessageNoError(t *testing.T) {
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, nil)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

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
	body := renderPage(t, BoardDetail(layoutData(), nil, errors.New("leaflab-api unavailable"), ""))

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
	resp := boardDetailResp(1, fullID, "", false, nil, sensors)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

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
		"with sensors": BoardDetail(layoutData(), boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, sensors), nil, ""),
		"empty":        BoardDetail(layoutData(), boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, nil), nil, ""),
		"load error":   BoardDetail(layoutData(), nil, errors.New("boom"), ""),
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

// TestBoardDetail_NoPollIntervalRegionText guards the "Do not display"
// section: no poll-interval or region/location text appears anywhere on
// the page. Unlike boards.templ's equivalent, "Owner" text is expected on
// this page since #1765 added an explicit owner cell to the header -- see
// TestBoardDetail_Owner_* below for that coverage.
func TestBoardDetail_NoPollIntervalRegionText(t *testing.T) {
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
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, sensors)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

	for _, banned := range []string{"poll interval", "Poll Interval", "Region", "Location"} {
		if strings.Contains(body, banned) {
			t.Errorf("expected no %q text on the board detail page, got %q", banned, body)
		}
	}
}

// -- #1765 owner/name header tests (Testing criterion 9) --------------------

// TestBoardDetail_Owner_UnownedShowsClaimButton proves an unowned board
// renders the "Unowned" label plus a Claim button targeting this board's
// own claim route, and never the owner's display name.
func TestBoardDetail_Owner_UnownedShowsClaimButton(t *testing.T) {
	resp := boardDetailResp(42, "leaflab-aaaaaaaaaaaa", "", false, nil, nil)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

	if !strings.Contains(body, "Unowned") {
		t.Errorf("expected the 'Unowned' label, got %q", body)
	}
	if !strings.Contains(body, "/boards/42/claim") {
		t.Errorf("expected the Claim button's form action to target /boards/42/claim, got %q", body)
	}
	if !strings.Contains(body, ">Claim<") {
		t.Errorf("expected a Claim button, got %q", body)
	}
}

// TestBoardDetail_Owner_OwnedByCallerShowsYou proves the calling user's own
// board renders "You", not the Claim button and not the raw owner name.
func TestBoardDetail_Owner_OwnedByCallerShowsYou(t *testing.T) {
	owner := &leaflabapipb.LeafLabUser{LeaflabUserId: 1, DisplayName: "Board Owner"}
	resp := boardDetailResp(42, "leaflab-aaaaaaaaaaaa", "", true, owner, nil)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

	if !strings.Contains(body, "You") {
		t.Errorf("expected the 'You' label for the caller's own board, got %q", body)
	}
	if strings.Contains(body, ">Claim<") {
		t.Errorf("expected no Claim button for a board the caller already owns, got %q", body)
	}
	if strings.Contains(body, "Board Owner") {
		t.Errorf("expected the caller's own board to show 'You', not the raw owner display name, got %q", body)
	}
}

// TestBoardDetail_Owner_OwnedByOtherShowsDisplayName proves a board owned
// by someone other than the caller renders that owner's display name, not
// "You" and not the Claim button.
func TestBoardDetail_Owner_OwnedByOtherShowsDisplayName(t *testing.T) {
	owner := &leaflabapipb.LeafLabUser{LeaflabUserId: 2, DisplayName: "Someone Else"}
	resp := boardDetailResp(42, "leaflab-aaaaaaaaaaaa", "", false, owner, nil)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

	if !strings.Contains(body, "Someone Else") {
		t.Errorf("expected the other owner's display name, got %q", body)
	}
	if strings.Contains(body, ">Claim<") {
		t.Errorf("expected no Claim button for a board someone else owns, got %q", body)
	}
	if strings.Contains(body, ">You<") {
		t.Errorf("expected no 'You' label for a board the caller does not own, got %q", body)
	}
}

// TestBoardDetail_Name_FallsBackToDeviceIDWhenEmpty proves an empty
// board_name renders device_id as the primary label, with no separate
// device_id line underneath (that secondary line only appears once the
// board has an actual name distinct from device_id -- see boards.templ's
// boardNameCell doc comment for the same rule).
func TestBoardDetail_Name_FallsBackToDeviceIDWhenEmpty(t *testing.T) {
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, nil)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

	if !strings.Contains(body, "leaflab-aaaaaaaaaaaa") {
		t.Errorf("expected device_id to appear as the fallback label, got %q", body)
	}
}

// TestBoardDetail_Name_UsesBoardNameAsPrimaryLabel proves a named board
// shows its name as the primary heading, with device_id still visible
// underneath as secondary text -- never hidden, since it remains the
// identifier printed on the hardware.
func TestBoardDetail_Name_UsesBoardNameAsPrimaryLabel(t *testing.T) {
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "Greenhouse Board", false, nil, nil)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, ""))

	if !strings.Contains(body, "Greenhouse Board") {
		t.Errorf("expected the board's name as the primary label, got %q", body)
	}
	if !strings.Contains(body, "leaflab-aaaaaaaaaaaa") {
		t.Errorf("expected device_id to still appear as secondary text when the board is named, got %q", body)
	}
}

// TestBoardDetail_ClaimErr_RendersInlineMessage proves a non-empty claimErr
// (set by handleClaimBoard's post-claim redirect on a refused claim)
// renders as an inline warning on the page, independent of loadErr.
func TestBoardDetail_ClaimErr_RendersInlineMessage(t *testing.T) {
	resp := boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", false, nil, nil)
	body := renderPage(t, BoardDetail(layoutData(), resp, nil, "This board is already owned."))

	if !strings.Contains(body, "This board is already owned.") {
		t.Errorf("expected the inline claim-error message, got %q", body)
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("expected the claim error rendered as a warning, not an error, got %q", body)
	}
}

// --- FR3: inline rename control (#1767) -------------------------------

// TestBoardDetail_RenameControl_OwnedByCaller_Rendered is Testing
// criterion 8's positive half: the rename control renders when
// ownedByCaller is true.
func TestBoardDetail_RenameControl_OwnedByCaller_Rendered(t *testing.T) {
	body := renderPage(t, BoardDetail(layoutData(), boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "greenhouse", true, nil, nil), nil, ""))

	if !strings.Contains(body, `hx-post="/boards/1/rename"`) {
		t.Errorf("expected the rename form (hx-post to /boards/1/rename) when ownedByCaller is true, got %q", body)
	}
}

// TestBoardDetail_RenameControl_NotOwnedByCaller_Hidden is Testing
// criterion 8's negative half: the rename control is absent (not merely
// disabled) when ownedByCaller is false, since NFR1's enforcement point is
// server-side, not this hide.
func TestBoardDetail_RenameControl_NotOwnedByCaller_Hidden(t *testing.T) {
	body := renderPage(t, BoardDetail(layoutData(), boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "greenhouse", false, nil, nil), nil, ""))

	if strings.Contains(body, `hx-post="/boards/1/rename"`) {
		t.Errorf("expected no rename form when ownedByCaller is false, got %q", body)
	}
}

// TestBoardDetail_BoardName_RenderedWhenSet is Testing criterion 10's
// named half: a board with a name shows it prefilled in the rename
// control's value.
func TestBoardDetail_BoardName_RenderedWhenSet(t *testing.T) {
	body := renderPage(t, BoardDetail(layoutData(), boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "greenhouse", true, nil, nil), nil, ""))

	if !strings.Contains(body, `value="greenhouse"`) {
		t.Errorf("expected the board name prefilled as the rename input's value, got %q", body)
	}
}

// TestBoardDetail_UnnamedBoard_ShowsDeviceIDPlaceholder is Testing
// criterion 10's unnamed half: an unnamed board (boardName == "") shows an
// empty rename field with device_id as the placeholder, per the task
// issue's UI section.
func TestBoardDetail_UnnamedBoard_ShowsDeviceIDPlaceholder(t *testing.T) {
	body := renderPage(t, BoardDetail(layoutData(), boardDetailResp(1, "leaflab-aaaaaaaaaaaa", "", true, nil, nil), nil, ""))

	if !strings.Contains(body, `placeholder="leaflab-aaaaaaaaaaaa"`) {
		t.Errorf("expected device_id as the rename input's placeholder for an unnamed board, got %q", body)
	}
	if strings.Contains(body, `value="leaflab-aaaaaaaaaaaa"`) {
		t.Errorf("expected the rename input's value to stay empty (not prefilled with device_id), got %q", body)
	}
}
