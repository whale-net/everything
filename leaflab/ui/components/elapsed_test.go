package components

// FR64 rendering coverage (#1330's Testing section): "elapsed-time
// rendering test with a fixed server_now produces stable text" plus the
// underlying rule -- "a raw UTC timestamp is never the primary text on
// this screen". formatElapsed/absoluteInstantTitle are private to this
// package (elapsed.go), so this file is white-box, same package.

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// instant builds a pb.Instant t after epoch, for readable table-driven
// fixtures below.
func instant(epoch time.Time, offset time.Duration) *pb.Instant {
	return &pb.Instant{UnixMillis: epoch.Add(offset).UnixMilli()}
}

// TestFormatElapsed_FixedServerNow_ProducesStableText proves formatElapsed
// depends only on the (now, then) pair it is given -- never wall-clock
// time.Now() -- so the same fixed server_now always renders the same
// text, regardless of when the test itself runs.
func TestFormatElapsed_FixedServerNow_ProducesStableText(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"just now", -30 * time.Second, "just now"},
		{"minutes ago", -5 * time.Minute, "5m ago"},
		{"just under an hour", -59 * time.Minute, "59m ago"},
		{"hours ago", -3 * time.Hour, "3h ago"},
		{"just under a day", -23 * time.Hour, "23h ago"},
		{"days ago", -72 * time.Hour, "3d ago"},
		{"clock skew: then is after now", 5 * time.Minute, "just now"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			then := now.Add(c.offset)
			if got := formatElapsed(now, then); got != c.want {
				t.Errorf("formatElapsed(%v, %v) = %q, want %q", now, then, got, c.want)
			}
			// Stability: calling it again with the exact same inputs (as a
			// second render of the same response would) must reproduce the
			// same text -- nothing here reads the wall clock.
			if got := formatElapsed(now, then); got != c.want {
				t.Errorf("formatElapsed not stable across repeated calls: got %q, want %q", got, c.want)
			}
		})
	}
}

// TestAbsoluteInstantTitle_RendersRFC3339 proves the hover/title text FR64
// requires alongside the elapsed primary text is the absolute instant,
// not a duration.
func TestAbsoluteInstantTitle_RendersRFC3339(t *testing.T) {
	i := &pb.Instant{UnixMillis: time.Date(2026, 8, 27, 3, 4, 5, 0, time.UTC).UnixMilli()}
	got := absoluteInstantTitle(i)
	want := "2026-08-27T03:04:05Z"
	if got != want {
		t.Errorf("absoluteInstantTitle = %q, want %q", got, want)
	}
}

// TestBoardsRows_RendersElapsedAsPrimaryText_RawTimestampOnlyInTitle proves
// the FR64 contract end to end through the real templ component: the
// visible cell text is elapsed ("5m ago"), the RFC3339 absolute instant
// only ever appears inside a title="" attribute (hover detail), never as
// the row's own text content -- "a raw UTC timestamp is never the primary
// text on this screen".
func TestBoardsRows_RendersElapsedAsPrimaryText_RawTimestampOnlyInTitle(t *testing.T) {
	serverNow := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	lastSeen := serverNow.Add(-5 * time.Minute)

	boards := []*pb.BoardInfo{
		{DeviceId: "dev-1", BoardId: 1, LastSeenAt: instant(lastSeen, 0)},
	}

	var buf strings.Builder
	if err := BoardsRows(boards, instant(serverNow, 0), "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("BoardsRows render failed: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, "5m ago") {
		t.Errorf("expected elapsed text %q in rendered row, got: %s", "5m ago", html)
	}
	rawTimestamp := "2026-08-27T11:55:00Z"
	if !strings.Contains(html, `title="`+rawTimestamp+`"`) {
		t.Errorf("expected the absolute instant %q inside a title attribute, got: %s", rawTimestamp, html)
	}
	// The raw timestamp must appear exactly once (inside the title
	// attribute) -- if it also showed up as visible cell text, this count
	// would be >= 2.
	if n := strings.Count(html, rawTimestamp); n != 1 {
		t.Errorf("raw timestamp %q appeared %d times, want exactly 1 (title attribute only, never primary text), got: %s", rawTimestamp, n, html)
	}
}

// TestBoardsRows_EmptyServerNow_DoesNotPanic guards instantToTime's nil
// safety (GetUnixMillis on a nil Instant returns 0) against a malformed
// response missing server_now.
func TestBoardsRows_EmptyServerNow_DoesNotPanic(t *testing.T) {
	boards := []*pb.BoardInfo{
		{DeviceId: "dev-1", BoardId: 1, LastSeenAt: &pb.Instant{}},
	}
	var buf strings.Builder
	if err := BoardsRows(boards, nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("BoardsRows render failed with nil server_now: %v", err)
	}
}
