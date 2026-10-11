package session

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// DefaultIdleTimeout is the platform idle timeout used when neither a session
// override nor a definition value is set. The env-var override is documented
// in ENV.md.
const DefaultIdleTimeout = 24 * time.Hour

// EffectiveIdleTimeout resolves override > definition > platformDefault. The
// definition is never mutated.
func EffectiveIdleTimeout(override *int32, def AgentDefinition, platformDefault time.Duration) time.Duration {
	if override != nil {
		return time.Duration(*override) * time.Second
	}
	if def.IdleTimeoutSeconds != nil {
		return time.Duration(*def.IdleTimeoutSeconds) * time.Second
	}
	return platformDefault
}

// ParseIdleTimeout parses a Go duration string for field and returns it. It
// rejects unparseable input, values that are not positive, and values that
// are not a whole number of seconds (the stored unit), naming field and raw
// in the error.
func ParseIdleTimeout(field, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration string such as \"24h\": supplied %q", field, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be > 0: supplied %q", field, raw)
	}
	if err := ValidateIdleTimeout(field, d); err != nil {
		return 0, err
	}
	if d%time.Second != 0 {
		return 0, fmt.Errorf("%s must be a whole number of seconds: supplied %q", field, raw)
	}
	return d, nil
}

// ValidateIdleTimeout rejects a non-positive duration, naming field in the error.
// It also rejects durations whose whole-second count does not fit the stored
// INT column.
func ValidateIdleTimeout(field string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("%s must be > 0: supplied %s", field, d.String())
	}
	if d/time.Second > math.MaxInt32 {
		return fmt.Errorf("%s is too large: supplied %s", field, d.String())
	}
	return nil
}

// IdleTimeoutSeconds converts a validated duration to the integer seconds
// stored on the sessions and agent_definition rows.
func IdleTimeoutSeconds(d time.Duration) int32 {
	return int32(d / time.Second)
}

// FormatIdleTimeout renders seconds as the shortest Go duration string that
// ParseIdleTimeout reads back to the same value (e.g. "24h", "90s").
func FormatIdleTimeout(seconds int32) string {
	s := (time.Duration(seconds) * time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
