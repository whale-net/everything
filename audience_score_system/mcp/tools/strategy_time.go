// Calendar/weekday helpers strategy.go still needs after FR41's schedule-draft
// retirement (issue #1832): parseWeekdayName backs save_strategy's
// preferred_weekday field, which FR47 does not remove (only Strategy.Cadence
// and generate_schedule_plan are retired -- see issue #1833). weekBounds and
// effectiveSyncedVideoTime back generate_schedule_plan's pacingTracker only;
// they become dead code once #1833 deletes that tool and are removed there,
// not here -- this task does not touch strategy.go. All four originated in
// the now-deleted schedule_draft.go (issue #1579).
package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/whale-net/everything/audience_score_system/store"
)

var weekdayNames = map[string]string{
	"sunday":    "Sunday",
	"monday":    "Monday",
	"tuesday":   "Tuesday",
	"wednesday": "Wednesday",
	"thursday":  "Thursday",
	"friday":    "Friday",
	"saturday":  "Saturday",
}

// parseWeekdayName validates raw against weekdayNames case-insensitively,
// returning the canonical full English form.
func parseWeekdayName(raw string) (string, error) {
	canon, ok := weekdayNames[strings.ToLower(strings.TrimSpace(raw))]
	if !ok {
		return "", fmt.Errorf("preferred_days contains an invalid weekday %q -- must be a full English weekday name (Monday..Sunday)", raw)
	}
	return canon, nil
}

// effectiveSyncedVideoTime mirrors SyncStore.ListSchedule's (store/sync.go)
// notion of a SyncedVideo's effective timestamp -- PublishAt while still a
// scheduled/private draft, else PublishedAt, nil if neither is set.
func effectiveSyncedVideoTime(v store.SyncedVideo) *time.Time {
	if v.PublishAt != nil {
		return v.PublishAt
	}
	return v.PublishedAt
}

// weekBounds returns the [Monday 00:00 UTC, next Monday 00:00 UTC) window
// containing t (converted to UTC first) -- the calendar week
// generate_schedule_plan's pacingTracker reasons about.
func weekBounds(t time.Time) (start, end time.Time) {
	t = t.UTC()
	daysSinceMonday := (int(t.Weekday()) + 6) % 7 // time.Sunday==0 -> 6 days after Monday, time.Monday==1 -> 0, ...
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
	end = start.AddDate(0, 0, 7)
	return start, end
}
