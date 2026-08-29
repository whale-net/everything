package components

// FR64 rendering helpers, private to this package: BoardsRows (boards.templ)
// is the only caller. Every Instant this UI ever renders comes paired with
// the same response's server_now (see leaflab/api/proto's Instant/
// ListBoardsResponse doc comments) -- these two functions are the one place
// that pairing turns into "elapsed" text plus an absolute-instant hover
// title, so a later screen (device/region detail) reuses them rather than
// re-deriving its own elapsed math.

import (
	"fmt"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// instantToTime converts an API Instant (FR64: milliseconds since the Unix
// epoch, UTC) to a Go time.Time. Nil-safe: GetUnixMillis on a nil Instant
// returns 0, so a zero-valued field renders as the Unix epoch rather than
// panicking.
func instantToTime(i *pb.Instant) time.Time {
	return time.UnixMilli(i.GetUnixMillis()).UTC()
}

// formatElapsed renders `then` as elapsed text relative to `now` --
// "just now" / "<n>m ago" / "<n>h ago" / "<n>d ago" -- never a raw
// timestamp (FR64). `now` must always be the API response's own
// server_now, never this BFF's local clock or the browser's, so the
// screen's freshness bound matches what a programmatic gRPC caller reading
// the same response gets (NFR18.2) instead of drifting with this
// process's own clock skew.
func formatElapsed(now, then time.Time) string {
	d := now.Sub(then)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// absoluteInstantTitle renders i as an RFC3339 UTC string -- the absolute
// instant FR64 requires stay available on hover/title next to the elapsed
// primary text.
func absoluteInstantTitle(i *pb.Instant) string {
	return instantToTime(i).Format(time.RFC3339)
}
