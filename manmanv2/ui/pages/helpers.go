package pages

import (
	"fmt"
	"time"

	"github.com/whale-net/everything/libs/go/htmxui"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// statusBadgeVariant is the pages package's entry point into
// components.StatusBadgeVariant (#1008, FR7): pages already normalize raw
// server/session/workshop-install state into the same status-key
// vocabulary ("online", "success", "danger", ...) the pre-migration
// badgeClasses(status) switch consumed, via their own
// serverStatusVariant/sessionStatusVariant/installStatusVariant helpers or
// (for sgc/session detail's own Status field) the raw enum string
// directly. This just forwards that key to the shared mapping so every
// page's status badge stays on one colour table instead of each page
// re-deriving its own.
func statusBadgeVariant(status string) htmxui.BadgeVariant {
	return components.StatusBadgeVariant(status)
}

func timeAgo(timestamp int64) string {
	if timestamp == 0 {
		return "Never"
	}
	t := time.Unix(timestamp, 0)
	duration := time.Since(t)
	if duration < time.Minute {
		return "Just now"
	} else if duration < time.Hour {
		minutes := int(duration.Minutes())
		if minutes == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", minutes)
	} else if duration < 24*time.Hour {
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	} else {
		days := int(duration.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
