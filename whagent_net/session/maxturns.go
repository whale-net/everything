package session

// DefaultMaxTurns is the platform turn cap applied when neither a session
// override nor a non-zero definition max_turns is set.
const DefaultMaxTurns = 100

// EffectiveMaxTurns is the single max-turns resolver: the session's pinned
// override when set, else the definition's max_turns, else DefaultMaxTurns
// when the definition value is 0. The definition is never mutated.
func EffectiveMaxTurns(override *int32, def AgentDefinition) int {
	if override != nil {
		return int(*override)
	}
	if def.MaxTurns == 0 {
		return DefaultMaxTurns
	}
	return def.MaxTurns
}
