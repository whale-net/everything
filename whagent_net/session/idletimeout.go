package session

import (
	"time"
)

// DefaultIdleTimeout is the platform idle timeout used when neither a session
// override nor a definition value is set. The env-var override is documented
// in ENV.md.
const DefaultIdleTimeout = 24 * time.Hour

// EffectiveIdleTimeout resolves override > definition > platformDefault.
// Scaffold: not yet implemented.
func EffectiveIdleTimeout(override *int32, def AgentDefinition, platformDefault time.Duration) time.Duration {
	return 0
}

// ParseIdleTimeout parses a Go duration string for field, rejecting unparseable,
// zero, or negative values with an error naming field and value.
// Scaffold: not yet implemented.
func ParseIdleTimeout(field, raw string) (time.Duration, error) {
	return 0, nil
}

// ValidateIdleTimeout rejects a non-positive duration, naming field in the error.
// Scaffold: not yet implemented.
func ValidateIdleTimeout(field string, d time.Duration) error {
	return nil
}
