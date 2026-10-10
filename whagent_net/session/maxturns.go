package session

// EffectiveMaxTurns is the single max-turns resolver: the session's pinned
// override when set, else the definition's max_turns, else the platform
// default (100) when the definition value is 0. Scaffold stub; Implementation
// fills in the body.
func EffectiveMaxTurns(override *int32, def AgentDefinition) int {
	panic("session.EffectiveMaxTurns: not implemented")
}
