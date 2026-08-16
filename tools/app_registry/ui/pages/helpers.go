package pages

import (
	"fmt"
	"time"
)

// intToStr formats a proto int32 count for table display.
func intToStr(n int32) string {
	return fmt.Sprintf("%d", n)
}

// unixToRFC3339 renders a Unix timestamp (UTC) for the promote screen's
// (50) post-write confirmation (FR-52) — a full, unambiguous timestamp
// rather than timeAgo's relative form, since this is an audit-facing
// record, not a dense list.
func unixToRFC3339(timestamp int64) string {
	if timestamp == 0 {
		return "—"
	}
	return time.Unix(timestamp, 0).UTC().Format(time.RFC3339)
}

// timeAgo renders a Unix timestamp as a short relative duration for dense,
// scannable tables (NFR-10) — the builds (30), build-detail (31), and
// reconcile-runs (32) screens all use this instead of a full timestamp.
func timeAgo(timestamp int64) string {
	if timestamp == 0 {
		return "—"
	}
	t := time.Unix(timestamp, 0)
	duration := time.Since(t)
	switch {
	case duration < time.Minute:
		return "Just now"
	case duration < time.Hour:
		minutes := int(duration.Minutes())
		if minutes == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", minutes)
	case duration < 24*time.Hour:
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	default:
		days := int(duration.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
