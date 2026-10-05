// The response every refused intervention takes, for all four verbs and on
// both the JavaScript and the no-JavaScript paths (FR c69a42b4).
//
// An intervention can be refused two ways. krill's store refuses it -- the
// task is no longer claimed or escalated, or the claim or escalation the row
// observed is no longer current -- or the write never reached krill at all.
// Either way the operator is looking at a page that is now wrong, and the
// console's job is to say so in krill's own words and show them the state
// that is actually current. This file is that one mapping; the handlers in
// interventions.go call it rather than each composing an answer.
//
// The one thing no response here may carry is the store's own error text.
// krill api returns a store sentinel verbatim in its {"error"} body, and
// those messages are Go package-qualified ("krill/store: observed claim or
// escalation is no longer current"), so rendering one tells the operator
// nothing they can act on while naming an internal package. krill/ui's
// writeOpsQueryError (ops.go) maps the console's own store errors the same
// way and for the same reason: the operator gets krill's wording, on-call
// humans get the real error from the logger.
package main

import (
	"net/http"
	"strings"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// interventionRefusalKind is why an intervention was refused. It is the
// distinction the requirement draws: a claim or escalation that changed
// since the page was loaded is a different answer from an action that no
// longer applies to the task at all, even though the operator sees both as
// a failed intervention.
type interventionRefusalKind int

const (
	// refusalStateChanged: the claim or escalation the acting row observed
	// is no longer the task's current one (store.ErrObservedStateMismatch).
	// The requirement's own words for this one -- "changed since the page
	// loaded" -- are part of the answer, so it is classified separately.
	refusalStateChanged interventionRefusalKind = iota
	// refusalNotApplicable: the task is not in a state any of this verb's
	// outcomes apply to -- already cancelled, no open claim, no active
	// escalation, already in the terminal Done lane, or gone entirely.
	refusalNotApplicable
	// refusalReported: krill api refused the write for a reason of its own,
	// already worded for a reader and carrying no store text.
	refusalReported
)

// interventionRefusal is what the operator is told about a refused
// intervention, and the one place that wording is decided.
//
// reason is the refusal's own sentence with no verb in it; heading and
// message below are the only two ways it is ever presented, so the htmx
// fragment and the in-shell page cannot state the same refusal two
// different ways.
type interventionRefusal struct {
	Kind   interventionRefusalKind
	reason string
}

// heading is the one-line statement of the refusal, for the in-shell page a
// no-JS browser lands on.
func (ref interventionRefusal) heading(action string) string {
	return "krill rejected the " + actionLabel(action) + "."
}

// message is the whole sentence the operator reads, wherever it is shown.
func (ref interventionRefusal) message(action string) string {
	return ref.heading(action) + " " + ref.reason
}

// interventionRefusalOf maps a rejected write onto the wording the operator
// is given.
//
// The api preserves the store's error string, so the sentinel's own message
// is the only identity of the refusal that survives the round trip. That
// makes this switch the wire-level counterpart of ops.go's consoleQueryError
// errors.Is chain: the observed-state guard is recognised by name and gets
// the requirement's own "changed since the page was loaded", and any other
// package-qualified store text is replaced wholesale rather than shown --
// which is what makes "no store text reaches the browser" hold for a
// sentinel this table has never heard of, rather than only for the ones
// krill api happens to map today.
//
// status is deliberately not part of the classification: the same 409
// carries both a changed guard and a legality refusal, so only the message
// tells them apart. It is passed in so the answer's own status can be the
// api's, and so the log line records it.
func interventionRefusalOf(status int, apiMessage string) interventionRefusal {
	switch {
	case strings.Contains(apiMessage, store.ErrObservedStateMismatch.Error()):
		return interventionRefusal{
			Kind: refusalStateChanged,
			reason: "The claim or escalation changed since the page was loaded. " +
				"This view has been re-read from the task's current state; check it and try again.",
		}
	case hasStoreText(apiMessage):
		return interventionRefusal{
			Kind: refusalNotApplicable,
			reason: "The task is no longer in a state this action applies to. " +
				"This view has been re-read from the task's current state; check it and try again.",
		}
	default:
		return interventionRefusal{Kind: refusalReported, reason: apiMessage}
	}
}

// storeTextPrefix is the one package whose error text krill api can return
// verbatim. Matching the bare prefix rather than every sentinel's full
// message is what makes the guarantee hold for a sentinel this binary has
// never heard of -- a new refusal can never turn into a leak just because
// nobody added it to a list.
const storeTextPrefix = "krill/store:"

// hasStoreText reports whether a message carries a package-qualified store
// error. It is the last line of defence behind the classification above, and
// the reason a table that falls behind still cannot leak.
func hasStoreText(message string) bool {
	return strings.Contains(message, storeTextPrefix)
}

// writeInterventionRefusal is the one refusal-to-response mapping every
// verb's handler answers a refused intervention through.
//
// An htmx caller gets 200 with the refusal inline in the results region,
// re-derived from freshly read state, because htmx does not swap on a
// non-2xx -- a status code or a bare error would leave the operator looking
// at an unchanged table with no explanation of why their action did nothing.
//
// A browser gets the same sentence inside the shell, at the status the error
// earns, with a link back to the view the operator acted from. A bare
// http.Error would answer text/plain with no nav, on exactly the page the
// operator most needs to navigate away from.
//
// status is the status the failure earns -- the api's own for a refusal it
// made, and the transport's own for one that never reached it. It is only
// ever used on the browser half: an htmx response is always 200 because the
// swap target's status is not surfaced to the operator.
func (app *App) writeInterventionRefusal(w http.ResponseWriter, r *http.Request, action, returnTo string, ref interventionRefusal, status int) {
	if isHtmxRequest(r) {
		app.renderInterventionResults(w, r, returnTo, ref.message(action), "")
		return
	}
	app.renderShellStatus(w, r, "Intervention rejected", opsPath, pages.InterventionError(pages.InterventionErrorData{
		Heading:  ref.heading(action),
		Detail:   ref.reason,
		ReturnTo: returnTo,
	}), status)
}

// logInterventionRefusal records the refused write's own words, which are
// the one thing that must never reach the page. An expected refusal is not a
// failure of this binary and stays at INFO; api failing to apply the write
// at all is worth an on-call human's attention.
func logInterventionRefusal(action string, status int, ref interventionRefusal, apiMessage string) {
	fields := []any{
		"action", action,
		"status", status,
		"refusal", ref.reason,
		"api_message", apiMessage,
	}
	if status >= http.StatusInternalServerError {
		logger.Error("failed to apply an operator intervention", fields...)
		return
	}
	logger.Info("krill refused an operator intervention", fields...)
}
