package cmd

import (
	"fmt"
	"time"
)

// parseRFC3339 parses a --at flag value into a Unix timestamp, the wire
// format GetEnvironmentStateRequest.at expects.
func parseRFC3339(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, fmt.Errorf("invalid --at %q, want RFC3339 (e.g. 2026-01-15T00:00:00Z): %w", s, err)
	}
	return t.Unix(), nil
}
