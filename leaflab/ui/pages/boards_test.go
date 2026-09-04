package pages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"google.golang.org/protobuf/types/known/timestamppb"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/ui/components"
)

// renderPage mirrors manmanv2/ui/pages/daisyui_migration_test.go's helper
// of the same name -- render a templ.Component to its output HTML string
// for substring assertions.
func renderPage(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf strings.Builder
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return buf.String()
}

func layoutData() components.LayoutData {
	return components.LayoutData{Title: "Boards"}
}

// TestBoards_ThreeStates_AllRenderedDistinctly covers the Testing section's
// "Three boards, one per state -> all three rendered, each with its own
// distinct state treatment" case.
func TestBoards_ThreeStates_AllRenderedDistinctly(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{BoardId: 1, DeviceId: "leaflab-aaaaaaaaaaaa", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
		{
			BoardId:        2,
			DeviceId:       "leaflab-bbbbbbbbbbbb",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_STALE,
			LastReadingAt:  timestamppb.New(time.Now().Add(-42 * time.Minute)),
		},
		{BoardId: 3, DeviceId: "leaflab-cccccccccccc", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_NEVER_REPORTED},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

	for _, id := range []string{"leaflab-aaaaaaaaaaaa", "leaflab-bbbbbbbbbbbb", "leaflab-cccccccccccc"} {
		if !strings.Contains(body, id) {
			t.Errorf("expected device_id %q to appear in rendered output, got %q", id, body)
		}
	}

	// Each state gets exactly its own distinct badge colour class -- a
	// swapped mapping would produce e.g. 2 of one colour and 0 of another.
	if got := strings.Count(body, "badge-success"); got != 1 {
		t.Errorf("expected exactly 1 badge-success (reporting), got %d in %q", got, body)
	}
	if got := strings.Count(body, "badge-warning"); got != 1 {
		t.Errorf("expected exactly 1 badge-warning (stale), got %d in %q", got, body)
	}
	if got := strings.Count(body, "badge-neutral"); got != 1 {
		t.Errorf("expected exactly 1 badge-neutral (never reported), got %d in %q", got, body)
	}
	if strings.Contains(body, "badge-error") {
		t.Errorf("expected no badge-error treatment among the three FR5 states, got %q", body)
	}
}

// TestBoards_DeviceIDFullLengthVerbatim guards the "shown in full, not
// truncated or abbreviated" requirement. Verify red/green (per the issue's
// instructions): truncating boards.templ's `{ board.GetDeviceId() }` to
// e.g. `{ board.GetDeviceId()[:12] }` makes this test fail on the missing
// suffix -- confirmed by hand, then reverted; see the commit message for
// the paired before/after run.
func TestBoards_DeviceIDFullLengthVerbatim(t *testing.T) {
	const fullID = "leaflab-ccdba79f5fac1234567890abcdef"
	boards := []*leaflabapipb.BoardWithState{
		{BoardId: 1, DeviceId: fullID, ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

	if !strings.Contains(body, fullID) {
		t.Errorf("expected full device_id %q to appear verbatim and uncut, got %q", fullID, body)
	}
}

// TestBoards_NeverReported_NoTimestampNoErrorStyling covers the "A never
// reported board renders without a timestamp and without error styling"
// case.
func TestBoards_NeverReported_NoTimestampNoErrorStyling(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{BoardId: 1, DeviceId: "leaflab-deadbeef0000", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_NEVER_REPORTED},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

	if strings.Contains(body, "ago") {
		t.Errorf("expected no relative-age timestamp for a never-reported board, got %q", body)
	}
	if strings.Contains(body, "alert-error") || strings.Contains(body, "badge-error") {
		t.Errorf("expected no error styling for a never-reported board, got %q", body)
	}
	if !strings.Contains(body, "Never reported") {
		t.Errorf("expected the neutral 'Never reported' label, got %q", body)
	}
}

// TestBoards_Stale_RendersRelativeAge covers the "A stale board renders the
// relative age of its most recent reading" case.
func TestBoards_Stale_RendersRelativeAge(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{
			BoardId:        1,
			DeviceId:       "leaflab-feedface0001",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_STALE,
			LastReadingAt:  timestamppb.New(time.Now().Add(-42 * time.Minute)),
		},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

	if !strings.Contains(body, "last reading 42 minutes ago") {
		t.Errorf("expected the stale board's relative reading age, got %q", body)
	}
}

// TestBoards_ZeroBoards_EmptyMessageNoError covers the "Zero boards -> the
// empty message, no error" case, and FR4's requirement that an unowned-in-
// M1 zero-board system not read as something being wrong.
func TestBoards_ZeroBoards_EmptyMessageNoError(t *testing.T) {
	body := renderPage(t, Boards(layoutData(), nil, nil))

	if !strings.Contains(body, "No boards yet.") {
		t.Errorf("expected the empty-state message, got %q", body)
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("expected no error styling for a zero-board system, got %q", body)
	}
}

// TestBoards_LoadError_RendersErrorState covers the failing-gRPC-call path
// distinct from the zero-boards empty state: a real load error must render
// visibly as an error, not silently look like "no boards yet."
func TestBoards_LoadError_RendersErrorState(t *testing.T) {
	body := renderPage(t, Boards(layoutData(), nil, errors.New("leaflab-api unavailable")))

	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected alert-error styling on a load failure, got %q", body)
	}
	if !strings.Contains(body, "leaflab-api unavailable") {
		t.Errorf("expected the load error's message in the rendered output, got %q", body)
	}
	if strings.Contains(body, "No boards yet.") {
		t.Errorf("expected the error state, not the empty-boards message, got %q", body)
	}
}

// TestBoards_UnspecifiedReportingState_RendersNeutralWithoutCrashing
// guards api.proto's "REPORTING_STATE_UNSPECIFIED should never arrive; if
// it does, render neutrally rather than crashing the page" requirement.
func TestBoards_UnspecifiedReportingState_RendersNeutralWithoutCrashing(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{BoardId: 1, DeviceId: "leaflab-00000000dead", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_UNSPECIFIED},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

	if !strings.Contains(body, "Never reported") {
		t.Errorf("expected UNSPECIFIED to render as the neutral never-reported label, got %q", body)
	}
	if strings.Contains(body, "badge-error") {
		t.Errorf("expected no error badge for UNSPECIFIED, got %q", body)
	}
}

// -- #1765 owner/name column tests (Testing criterion 9) --------------------

// TestBoards_Owner_UnownedShowsClaimButton proves an unowned board's row
// renders the "Unowned" label plus a Claim button targeting that board's
// own claim route.
func TestBoards_Owner_UnownedShowsClaimButton(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{BoardId: 42, DeviceId: "leaflab-aaaaaaaaaaaa", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

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

// TestBoards_Owner_OwnedByCallerShowsYou proves the calling user's own
// board renders "You", not a Claim button and not the raw owner name.
func TestBoards_Owner_OwnedByCallerShowsYou(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{
			BoardId:        42,
			DeviceId:       "leaflab-aaaaaaaaaaaa",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
			OwnedByCaller:  true,
			Owner:          &leaflabapipb.LeafLabUser{LeaflabUserId: 1, DisplayName: "Board Owner"},
		},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

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

// TestBoards_Owner_OwnedByOtherShowsDisplayName proves a board owned by
// someone other than the caller renders that owner's display name, not
// "You" and not a Claim button.
func TestBoards_Owner_OwnedByOtherShowsDisplayName(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{
			BoardId:        42,
			DeviceId:       "leaflab-aaaaaaaaaaaa",
			ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
			OwnedByCaller:  false,
			Owner:          &leaflabapipb.LeafLabUser{LeaflabUserId: 2, DisplayName: "Someone Else"},
		},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

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

// TestBoards_Name_FallsBackToDeviceIDWhenEmpty proves an empty board_name
// renders device_id as the primary label.
func TestBoards_Name_FallsBackToDeviceIDWhenEmpty(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{BoardId: 1, DeviceId: "leaflab-aaaaaaaaaaaa", BoardName: "", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

	if !strings.Contains(body, "leaflab-aaaaaaaaaaaa") {
		t.Errorf("expected device_id to appear as the fallback label, got %q", body)
	}
}

// TestBoards_Name_UsesBoardNameAsPrimaryLabel proves a named board shows
// its name as the primary label, with device_id still visible underneath
// as secondary text.
func TestBoards_Name_UsesBoardNameAsPrimaryLabel(t *testing.T) {
	boards := []*leaflabapipb.BoardWithState{
		{BoardId: 1, DeviceId: "leaflab-aaaaaaaaaaaa", BoardName: "Greenhouse Board", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
	}
	body := renderPage(t, Boards(layoutData(), boards, nil))

	if !strings.Contains(body, "Greenhouse Board") {
		t.Errorf("expected the board's name as the primary label, got %q", body)
	}
	if !strings.Contains(body, "leaflab-aaaaaaaaaaaa") {
		t.Errorf("expected device_id to still appear as secondary text when the board is named, got %q", body)
	}
}

// TestBoards_NoAutoRefreshMarkup is NFR1's guard: the rendered page must
// never carry an hx-trigger polling interval or an sse-connect attribute,
// across every state this page can render (three boards, empty, and error).
func TestBoards_NoAutoRefreshMarkup(t *testing.T) {
	fixtures := map[string]templ.Component{
		"three boards": Boards(layoutData(), []*leaflabapipb.BoardWithState{
			{BoardId: 1, DeviceId: "leaflab-aaaaaaaaaaaa", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			{BoardId: 2, DeviceId: "leaflab-bbbbbbbbbbbb", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_STALE, LastReadingAt: timestamppb.New(time.Now())},
			{BoardId: 3, DeviceId: "leaflab-cccccccccccc", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_NEVER_REPORTED},
		}, nil),
		"empty":         Boards(layoutData(), nil, nil),
		"load error":    Boards(layoutData(), nil, errors.New("boom")),
	}
	for name, component := range fixtures {
		t.Run(name, func(t *testing.T) {
			body := renderPage(t, component)
			if strings.Contains(body, "sse-connect") {
				t.Errorf("[%s] expected no sse-connect anywhere on the boards page (NFR1), got %q", name, body)
			}
			// hx-trigger with a time interval looks like `hx-trigger="every 5s"`
			// or similar; a plain hx-trigger with no "every" clause (e.g. a
			// click handler) would be fine, but this page has none at all.
			if strings.Contains(body, "hx-trigger") {
				t.Errorf("[%s] expected no hx-trigger anywhere on the boards page (NFR1: no polling interval), got %q", name, body)
			}
			if strings.Contains(body, `hx-trigger="every`) {
				t.Errorf("[%s] expected no hx-trigger polling interval (NFR1), got %q", name, body)
			}
		})
	}
}
