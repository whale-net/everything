package session

// DefaultMaxCostUSD is the platform cost cap applied when neither a session
// override nor a non-zero definition max_cost_usd is set.
const DefaultMaxCostUSD = 1.0

// MaxCostUSDCeiling is the highest effective cost cap any session may have,
// for both overrides and definition values.
const MaxCostUSDCeiling = 10.0

// EffectiveMaxCostUSD is the single cost-cap resolver: the session's pinned
// override when set, else the definition's max_cost_usd, else
// DefaultMaxCostUSD when that is 0. Stub -- implementation pending.
func EffectiveMaxCostUSD(override *float64, def AgentDefinition) float64 {
	return 0
}
