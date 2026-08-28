package contract

import (
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// ToInstant converts t to the wire pb.Instant used throughout the LeafLab
// API, so every response carries an absolute, unambiguous point in time
// (FR64) instead of a value tied to the server's local timezone. t is
// normalized to UTC before conversion.
func ToInstant(t time.Time) *pb.Instant {
	return &pb.Instant{UnixMillis: t.UTC().UnixMilli()}
}

// Now returns the current time as a pb.Instant, for populating a response
// envelope's server_now field. Every response that carries an Instant also
// carries server_now, so a browser or CLI renders "3 minutes ago" in the
// viewer's own timezone without trusting its own clock (FR64).
func Now() *pb.Instant {
	return ToInstant(time.Now())
}

// FromInstant is ToInstant's inverse. A nil Instant converts to the zero
// time.Time.
func FromInstant(i *pb.Instant) time.Time {
	if i == nil {
		return time.Time{}
	}
	return time.UnixMilli(i.UnixMillis).UTC()
}
