// Terminal error classification (issue #2119, FR3/FR2; ARCHITECTURE.md
// "Guardrails"): the single retryable/non_retryable judgement a failed
// session's GetSession and its failure transcript event both report
// (session.ErrorCategory) -- one classification, two surfaces, never two
// independently-derived answers (the invariant workflow.go's processTurn
// preserves: failTurn calls classifyError exactly once per failure and
// passes the same (category, detail) pair to both the UpdateSessionStatus
// and CommitTerminalEvent activity calls).
//
// classifyError is pure and I/O-free -- safe to call directly from
// workflow code (no activity needed), the same way checkCaps (caps.go) is
// -- it only inspects the error value processTurn's activity calls
// already returned.
//
// Classification rules (documented here per the issue body; mirrored into
// whagent_net/ARCHITECTURE.md "Guardrails"):
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
// # Why this is message-based, not a type switch
//
// By the time classifyError runs, err is whatever
// workflow.ExecuteActivity(...).Get(ctx, ...) returned to workflow code --
// it has already crossed the activity-to-workflow boundary through
// Temporal's failure conversion (go.temporal.io/sdk/internal/error.go),
// which flattens an activity's returned Go error to its Error() string
// plus an opaque reflect-derived type name (getErrType: the unqualified
// name of the *outermost* wrapper type, e.g. "wrapError" for a plain
// fmt.Errorf chain -- never the concrete type of whatever llm.Client or
// ResolveAgentDefinition actually returned several fmt.Errorf("...: %w",
// err) layers down). So `errors.As(err, &someConcreteType)` cannot reach
// through that boundary reliably, and classifyError instead pattern-
// matches the flattened message text -- which *does* survive intact,
// since every wrapping layer's fmt.Errorf("...: %w", ...) concatenates the
// wrapped error's own Error() string into its own. The one exception is
// Temporal's own *temporal.ActivityError, which is reconstructed as a real
// typed value on the workflow side (it is Temporal's error, not an
// arbitrary Go one) -- checked first, below, for its RetryState.
package main

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"

	"github.com/whale-net/everything/whagent_net/session"
)

// maxErrorDetailLen bounds classifyError's returned detail string (this
// file's package doc comment, "detail is a short, human-readable
// string... never the full error's Go-formatted text verbatim if that
// would be unwieldy") -- long enough to keep a status code and a short
// provider message, short enough that GetSession's error_detail column
// and the failure transcript event payload both stay small.
const maxErrorDetailLen = 240

// statusCodePattern extracts an HTTP status code from an OpenRouter/
// OpenAI API error's Error() string. llm.Client wraps *openai.Error
// (github.com/openai/openai-go/v2, aliasing internal/apierror.Error),
// whose Error() method formats as `"<method> <url>: <status> <status
// text> <body>"` -- see that package's (*Error).Error(). Matched
// positionally (": <3 digits> ") rather than via a type assertion, per
// this file's package doc comment on why classification here is
// message-based.
var statusCodePattern = regexp.MustCompile(`:\s(\d{3})\s`)

// classifyError maps err -- whatever processTurn's activity calls
// returned -- to FR3's (category, detail) pair.
func classifyError(err error) (session.ErrorCategory, string) {
	if err == nil {
		return session.ErrorCategoryNonRetryable, "classifyError called with a nil error"
	}

	// Temporal's own *ActivityError survives the boundary as a real typed
	// value (it is constructed fresh by the SDK on the workflow side, not
	// reconstructed from a generic Go error) and reports RetryState: why
	// Temporal itself stopped retrying the activity. RETRY_STATE_TIMEOUT
	// means the activity's StartToCloseTimeout elapsed -- exactly FR3's
	// "timeout" bucket -- and is reported here before any message
	// matching runs, since a timed-out activity's wrapped cause (often a
	// generic context.deadlineExceededError) carries no useful message
	// text of its own. Every other RetryState (including
	// RETRY_STATE_MAXIMUM_ATTEMPTS_REACHED, "an exhausted retry budget"
	// in the issue body's non_retryable list) falls through to the
	// message-based rules below: RetryState alone cannot distinguish a
	// rate limit that Temporal retried five times without success (still
	// classified retryable -- an operator-initiated retry may land at a
	// calmer moment) from an auth failure Temporal retried five times for
	// no reason (non_retryable) or a plain, no-further-signal exhausted
	// budget (also non_retryable, the safe default below).
	var activityErr *temporal.ActivityError
	if errors.As(err, &activityErr) && activityErr.RetryState() == enumspb.RETRY_STATE_TIMEOUT {
		return session.ErrorCategoryRetryable, trimDetail("activity timed out: " + err.Error())
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "timeout"),
		strings.Contains(lower, "timed out"),
		isNetTimeout(err):
		return session.ErrorCategoryRetryable, trimDetail("timeout: " + msg)

	case strings.Contains(lower, "rate limit"), hasStatusCode(msg, 429):
		return session.ErrorCategoryRetryable, trimDetail("provider rate limit: " + msg)

	case isServerErrorStatus(msg),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "eof"):
		return session.ErrorCategoryRetryable, trimDetail("transient transport failure: " + msg)

	case hasStatusCode(msg, 401), hasStatusCode(msg, 403),
		strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "permission"),
		strings.Contains(lower, "invalid api key"),
		strings.Contains(lower, "invalid_api_key"):
		return session.ErrorCategoryNonRetryable, trimDetail("auth/permission failure: " + msg)

	case hasStatusCode(msg, 404),
		strings.Contains(lower, "not served"),
		strings.Contains(lower, "model_not_found"),
		strings.Contains(lower, "does not exist"):
		return session.ErrorCategoryNonRetryable, trimDetail("model not served: " + msg)

	case strings.Contains(lower, "agent definition"):
		return session.ErrorCategoryNonRetryable, trimDetail("malformed agent definition: " + msg)

	default:
		// Safe default (matches the Scaffold-phase stub's choice): an
		// error this file's rules do not recognize is reported
		// non_retryable rather than guessed retryable, per this file's
		// package doc comment ("an operator who cannot yet distinguish
		// retryable from non_retryable should not be told a failure is
		// safe to retry").
		return session.ErrorCategoryNonRetryable, trimDetail(msg)
	}
}

// hasStatusCode reports whether msg contains code formatted the way
// *openai.Error's Error() formats an HTTP status (statusCodePattern).
func hasStatusCode(msg string, code int) bool {
	m := statusCodePattern.FindStringSubmatch(msg)
	if m == nil {
		return false
	}
	got, err := strconv.Atoi(m[1])
	return err == nil && got == code
}

// isServerErrorStatus reports whether msg carries a 5xx status code
// (statusCodePattern) -- a transient provider-side failure, FR3's
// "transient transport failure" bucket alongside connection-level errors.
func isServerErrorStatus(msg string) bool {
	m := statusCodePattern.FindStringSubmatch(msg)
	if m == nil {
		return false
	}
	code, err := strconv.Atoi(m[1])
	return err == nil && code >= 500 && code < 600
}

// isNetTimeout reports whether err (or anything it wraps) is a net.Error
// whose Timeout() is true -- a lower-level transport timeout distinct
// from Temporal's own activity StartToCloseTimeout (handled separately,
// above).
func isNetTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// trimDetail bounds s to maxErrorDetailLen, matching this file's package
// doc comment on classifyError's detail contract.
func trimDetail(s string) string {
	if len(s) <= maxErrorDetailLen {
		return s
	}
	return s[:maxErrorDetailLen-1] + "…"
}
