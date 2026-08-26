package pages

import (
	"fmt"
	"time"
)

// elapsedTime renders an absolute Unix timestamp as a relative duration
// from now, per FR64 (elapsed time in the viewer's timezone). NFR18.1 ensures
// no presentation rule is in the BFF — this helper is pure time formatting,
// not policy.
func elapsedTime(timestamp int64) string {
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
