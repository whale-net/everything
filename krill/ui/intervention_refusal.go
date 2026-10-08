// The single response mapping for refused interventions, for all verbs and
// both htmx and no-JS paths. Store error text (package-qualified, e.g.
// "krill/store: ...") never reaches the page; it goes to the logger instead.
package main

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// interventionRefusalKind is why an intervention was refused: a changed claim
// or escalation is a different answer from an action that no longer applies.
type interventionRefusalKind int

const (
	// refusalStateChanged: the observed claim or escalation is no longer current
	// (store.ErrObservedStateMismatch).
	refusalStateChanged interventionRefusalKind = iota
	// refusalNotApplicable: no outcome of this verb applies to the task's state
	// (cancelled, unclaimed, not escalated, Done, or gone).
	refusalNotApplicable
	// refusalReported: api refused for its own reason, already reader-worded.
	refusalReported
	// refusalNotIssued: the write never reached krill (no operator identity,
	// session mint failed, or api unreachable).
	refusalNotIssued
)

// String names the kind for log lines.
func (k interventionRefusalKind) String() string {
	switch k {
	case refusalStateChanged:
		return "state-changed"
	case refusalNotApplicable:
		return "not-applicable"
	case refusalNotIssued:
		return "not-issued"
	case refusalReported:
		return "reported"
	default:
		return "unknown"
	}
}

// interventionRefusal is the one place a refusal's wording is decided; heading
// and message are its only presentations, so htmx and shell pages agree.
type interventionRefusal struct {
	Kind   interventionRefusalKind
	reason string
}

// heading is the one-line refusal statement for the no-JS shell page.
func (ref interventionRefusal) heading(action string) string {
	return "krill rejected the " + actionLabel(action) + "."
}

func (ref interventionRefusal) message(action string) string {
	return ref.heading(action) + " " + ref.reason
}

// reloadFailure states the refusal when the view behind it could not be
// re-read. It never claims the intervention was applied.
func (ref interventionRefusal) reloadFailure(action string) string {
	return ref.message(action) + " The state that is current now could not be reloaded; reload this tab to see it."
}

// interventionRefusalOf classifies a rejected write by api's message, the
// only identity of a store sentinel that survives the round trip. Status is
// not used to classify: one 409 carries both kinds of refusal.
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

// storeTextPrefix matches any store sentinel, so a sentinel this binary has
// never seen still cannot leak.
const storeTextPrefix = "krill/store:"

// hasStoreText reports whether a message carries package-qualified store text.
func hasStoreText(message string) bool {
	return strings.Contains(message, storeTextPrefix)
}

// interventionNotIssuedRefusal answers a write that never reached krill. It
// takes the normal refusal route rather than a bare 502 that drops the nav.
func interventionNotIssuedRefusal() interventionRefusal {
	return interventionRefusal{
		Kind: refusalNotIssued,
		reason: "The write could not be issued as the signed-in operator. " +
			"Nothing was changed; this view has been re-read from the task's current state.",
	}
}

// interventionNotIssuedStatus reuses writeWriteError's mapping, so an
// unresolvable operator stays 401 rather than looking like an upstream outage.
func interventionNotIssuedStatus(err error) int {
	if errors.Is(err, errNoOperator) {
		return http.StatusUnauthorized
	}
	var rejection *writeRejection
	if errors.As(err, &rejection) {
		return rejection.status
	}
	return http.StatusBadGateway
}

// refusalBackLabel names the link out of the refusal page: a Needs attention
// tab, or the console for an /ops return_to.
func refusalBackLabel(returnTo string) string {
	if u, err := url.Parse(returnTo); err == nil {
		if _, ok := needsAttentionProductOfPath(u.Path); ok {
			return "Back to the tab"
		}
	}
	return "Back to the console"
}

// writeInterventionRefusal answers a refused intervention. htmx gets 200 with
// the refusal inline (htmx does not swap non-2xx); a browser gets the shell at
// status, with a link back. taskID guards return_to as the success path does.
func (app *App) writeInterventionRefusal(w http.ResponseWriter, r *http.Request, taskID uuid.UUID, action, returnTo string, ref interventionRefusal, status int) {
	if isHtmxRequest(r) {
		app.renderInterventionResults(w, r, taskID, returnTo, &ref, action, "")
		return
	}
	app.renderShellStatus(w, r, "Intervention rejected", opsPath, pages.InterventionError(pages.InterventionErrorData{
		Heading:   ref.heading(action),
		Detail:    ref.reason,
		ReturnTo:  returnTo,
		BackLabel: refusalBackLabel(returnTo),
	}), status)
}

// logInterventionRefusal logs the api's own words. Expected refusals stay at
// INFO; api failing to apply the write is an error.
func logInterventionRefusal(action string, status int, ref interventionRefusal, apiMessage string) {
	fields := []any{
		"action", action,
		"status", status,
		"kind", ref.Kind.String(),
		"refusal", ref.reason,
		"api_message", apiMessage,
	}
	if status >= http.StatusInternalServerError {
		logger.Error("failed to apply an operator intervention", fields...)
		return
	}
	logger.Info("krill refused an operator intervention", fields...)
}
