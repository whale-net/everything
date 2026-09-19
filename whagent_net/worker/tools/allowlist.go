package tools

// allowAll reports whether allowed (a session.ToolServerRef.AllowedTools
// value) places no restriction on the server's exposed tool set -- nil or
// empty both mean "whatever the server exposes" (agentdef.go's
// ToolServerRef doc comment), never "allow nothing".
func allowAll(allowed []string) bool {
	return len(allowed) == 0
}

// isAllowed reports whether name may be seen/called under allowed (C22:
// whagent-side enforcement of a ToolServerRef's AllowedTools). A nil/empty
// allowed list permits every tool the server exposes; otherwise name must
// appear in it verbatim.
func isAllowed(name string, allowed []string) bool {
	if allowAll(allowed) {
		return true
	}
	for _, a := range allowed {
		if a == name {
			return true
		}
	}
	return false
}

// isUnlocked reports whether name is in unlocked (FR9: search-mode
// dispatch's own gate, dispatch.go's resolveTarget) -- exact-match, no
// wildcarding, the same shape as isAllowed above. Unlike isAllowed, a
// nil/empty unlocked list means "nothing unlocked yet", not "allow
// everything" -- the opposite of isAllowed's convention, since an empty
// AllowedTools is the server's/agent-definition's own "no restriction"
// signal, while an empty Unlocked is simply a search-mode session that
// has not called search_tools yet and so must not be able to dispatch
// anything beyond search_tools itself.
func isUnlocked(name string, unlocked []string) bool {
	for _, u := range unlocked {
		if u == name {
			return true
		}
	}
	return false
}
