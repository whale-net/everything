package session

import "fmt"

// DefaultMaxCostUSD is the platform cost cap applied when neither a session
// override nor a non-zero definition max_cost_usd is set.
const DefaultMaxCostUSD = 1.0

// MaxCostUSDCeiling is the highest effective cost cap any session may have,
// for both overrides and definition values.
const MaxCostUSDCeiling = 10.0

// EffectiveMaxCostUSD is the single cost-cap resolver: the session's pinned
// override when set, else the definition's max_cost_usd, else
// DefaultMaxCostUSD when that is 0. The definition is never mutated.
func EffectiveMaxCostUSD(override *float64, def AgentDefinition) float64 {
	if override != nil {
		return *override
	}
	if def.MaxCostUSD == 0 {
		return DefaultMaxCostUSD
	}
	return def.MaxCostUSD
}

// ValidateMaxCostUSD rejects a cost cap that is not in (0, MaxCostUSDCeiling].
// field names the offending input in the error message.
func ValidateMaxCostUSD(field string, v float64) error {
	if v <= 0 || v > MaxCostUSDCeiling {
		return fmt.Errorf("%s must be > 0 and <= %g: supplied %g (ceiling %g)", field, MaxCostUSDCeiling, v, MaxCostUSDCeiling)
	}
	return nil
}
