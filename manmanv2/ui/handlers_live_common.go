package main

import "net/http"

// requireLiveTopics guards htmxsse.Handler's precondition that it panics on
// an empty topic list (see htmxsse.Handler's doc comment). Both of this
// app's live-SSE routes -- handleDeploymentsLiveSSE's server-scoped
// derivation (handlers_sessions_live.go, #1724) and
// handleActivityLiveSSE's fleet-wide derivation (handlers_activity_live.go,
// #2277/FR15/NFR10) -- degrade the same way an unavailable hub does
// (NFR8/NFR10) rather than panic: an empty derived topic set is a handled
// 503, never a crash. Factored here so option (b)'s sibling route can't
// fork this guard from the shipped one (#2277's explicit requirement).
//
// Returns false (having already written the 503) when topics is empty;
// callers must return immediately without invoking htmxsse.Handler.
func requireLiveTopics(w http.ResponseWriter, topics []string, message string) bool {
	if len(topics) == 0 {
		http.Error(w, message, http.StatusServiceUnavailable)
		return false
	}
	return true
}
