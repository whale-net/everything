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
