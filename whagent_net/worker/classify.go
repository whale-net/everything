// Terminal error classification (issue #2119, FR3/FR2; ARCHITECTURE.md
// "Guardrails"): the single retryable/non_retryable judgement a failed
// session's GetSession and its failure transcript event both report
// (session.ErrorCategory) -- one classification, two surfaces, never two
// independently-derived answers (the invariant workflow.go's processTurn
// must preserve once this is wired in: compute classifyError's result
// once per failure, pass the same (category, detail) pair to both the
// UpdateSessionStatus and CommitTerminalEvent activity calls).
//
// classifyError is pure and I/O-free -- safe to call directly from
// workflow code (no activity needed), the same way checkCaps (caps.go) is
// -- it only inspects the error value processTurn's activity calls
// already returned.
//
// Classification rules (documented here per the issue body; mirrored into
// whagent_net/ARCHITECTURE.md "Guardrails" once classifyError is real):
//
//   - retryable: a provider rate limit, a transient transport failure
//     (connection reset, DNS, 5xx from the provider), or a timeout.
//   - non_retryable: a model the provider does not serve, an
//     auth/permission failure, a malformed agent definition, or an
//     exhausted retry budget (Temporal's MaximumAttempts, see workflow.go's
//     defaultActivityOptions).
//
// The goal (issue body): an operator can decide retry-vs-escalate from
// GetSession's category alone, without reading the transcript.
//
// Scaffold phase (this task): classifyError is a stub, always returning
// non_retryable -- the safe default when nothing has classified the error
// yet (an operator who cannot yet distinguish retryable from
// non_retryable should not be told a failure is safe to retry).
// Implementation phase fills in the real rules above, including matching
// each case against the concrete error types llm.Client (llm/client.go),
// the tool-dispatch package (worker/tools), and ResolveAgentDefinition
// (activities.go) actually return.
package main

import "github.com/whale-net/everything/whagent_net/session"

// classifyError maps err -- whatever processTurn's activity calls
// returned -- to FR3's (category, detail) pair. detail is a short,
// human-readable string (never the full error's Go-formatted text
// verbatim if that would be unwieldy; Implementation phase decides the
// exact trim/format).
func classifyError(err error) (session.ErrorCategory, string) {
	return session.ErrorCategoryNonRetryable, err.Error()
}
