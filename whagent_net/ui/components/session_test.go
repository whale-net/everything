package components

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/whagent_net/events"
)

// This file guards issue #2754's FR4/FR5: stateBadge's spinner treatment is
// visible to any viewer while state == "running", and every other state's
// markup -- including the "session-state-badge" id and stateBadgeVariant's
// label/variant mapping -- is byte-identical to before this change.
//
// Red/green discipline (verified by hand, then reverted): temporarily
// changing session.templ's `if state == "running" {` guard to `if true {`
// made TestStateBadge_AwaitingInputNoSpinner and the terminal-state subtests
// fail with an unexpected loading-spinner present; changing it to
// `if false {` made TestStateBadge_RunningShowsSpinner fail with the
// spinner missing. Restoring the guard made all subtests pass again.

const loadingSpinnerMarker = "loading-spinner"

func renderStateBadge(t *testing.T, state string) string {
	t.Helper()
	var buf strings.Builder
	if err := stateBadge(state).Render(context.Background(), &buf); err != nil {
		t.Fatalf("stateBadge render failed: %v", err)
	}
	return buf.String()
}

// TestStateBadge_RunningShowsSpinner guards FR4: the running state's badge
// carries the spinner element, visible to any viewer (not gated by
// SessionDetail's data.IsOwner -- stateBadge is called from SessionLive,
// never from the IsOwner-gated composer/stop slot).
func TestStateBadge_RunningShowsSpinner(t *testing.T) {
	body := renderStateBadge(t, "running")
	if !strings.Contains(body, loadingSpinnerMarker) {
		t.Errorf("expected running state to render a spinner, got %q", body)
	}
}

// TestStateBadge_AwaitingInputNoSpinner guards FR4's "unchanged... otherwise"
// for the one other state that shares stateBadgeVariant's BadgeInfo colour
// with "running" -- the spinner must still be keyed on the exact state
// string, not the colour mapping.
func TestStateBadge_AwaitingInputNoSpinner(t *testing.T) {
	body := renderStateBadge(t, "awaiting_input")
	if strings.Contains(body, loadingSpinnerMarker) {
		t.Errorf("expected awaiting_input state to render no spinner, got %q", body)
	}
}

// TestStateBadge_TerminalStatesNoSpinner guards FR4 for every terminal
// state: a session with no running SessionWorkflow left (isTerminalSessionState's
// doc comment) must never show a live-activity spinner.
func TestStateBadge_TerminalStatesNoSpinner(t *testing.T) {
	for _, state := range []string{"done", "stopped", "capped", "failed"} {
		t.Run(state, func(t *testing.T) {
			body := renderStateBadge(t, state)
			if strings.Contains(body, loadingSpinnerMarker) {
				t.Errorf("expected %s state to render no spinner, got %q", state, body)
			}
		})
	}
}

// TestStateBadge_BadgeIDUnchanged guards against accidentally moving the
// "session-state-badge" id off the Badge span while adding the spinner
// wrapper -- nothing else currently keys off it, but this is the only place
// that markup is ever emitted.
func TestStateBadge_BadgeIDUnchanged(t *testing.T) {
	for _, state := range []string{"running", "awaiting_input", "done", "stopped", "capped", "failed", "unspecified"} {
		t.Run(state, func(t *testing.T) {
			body := renderStateBadge(t, state)
			if !strings.Contains(body, `id="session-state-badge"`) {
				t.Errorf("expected session-state-badge id present for state %q, got %q", state, body)
			}
		})
	}
}

// TestStateBadge_NonRunningLabelUnchanged guards FR4's "unchanged from
// current behavior otherwise": for every non-running state, the rendered
// badge carries the state's own text label and stateBadgeVariant's colour
// class, with no spinner markup mixed in.
func TestStateBadge_NonRunningLabelUnchanged(t *testing.T) {
	variantClass := map[string]string{
		"awaiting_input": "badge-info",
		"done":           "badge-success",
		"stopped":        "badge-neutral",
		"capped":         "badge-warning",
		"failed":         "badge-error",
		"unspecified":    "badge-ghost",
	}

	for state, wantClass := range variantClass {
		t.Run(state, func(t *testing.T) {
			body := renderStateBadge(t, state)
			if !strings.Contains(body, wantClass) {
				t.Errorf("expected variant class %q for state %q, got %q", wantClass, state, body)
			}
			if !strings.Contains(body, ">"+state+"<") {
				t.Errorf("expected label %q in rendered badge, got %q", state, body)
			}
		})
	}
}

// This block guards issue #2776/#2778: SessionLive must not trust a stale
// DB-read session.State when evs's own last event already proves the
// session went terminal. reconcileTerminalState is the pure decision
// function; the TestSessionLive_* tests below prove SessionLive actually
// wires it into the rendered badge/banner end to end.
//
// Red/green discipline (verified by hand, then reverted): temporarily
// removing the `{{ session = reconcileTerminalState(session, evs) }}`
// statement from SessionLive made
// TestSessionLive_BadgeReflectsCappedEventEvenWhenSessionStateStillRunning
// and TestSessionLive_BadgeReflectsFailureEventEvenWhenSessionStateStillRunning
// fail -- the rendered badge stayed "running" with the spinner present
// instead of flipping to the terminal badge/banner. Restoring the
// statement made both pass again.

func evOfType(eventType string, payload string) TranscriptEventView {
	return TranscriptEventView{EventID: "e1", Seq: 1, Type: eventType, Payload: []byte(payload)}
}

func TestReconcileTerminalState_CappedEventOverridesStaleRunning(t *testing.T) {
	session := SessionView{State: "running"}
	evs := []TranscriptEventView{evOfType(events.EventTypeCapped, `{"cap_kind":"turns"}`)}

	got := reconcileTerminalState(session, evs)

	if got.State != "capped" {
		t.Errorf("expected State %q, got %q", "capped", got.State)
	}
	if got.CapKind != "turns" {
		t.Errorf("expected CapKind %q, got %q", "turns", got.CapKind)
	}
}

func TestReconcileTerminalState_FailureEventOverridesStaleRunning(t *testing.T) {
	session := SessionView{State: "running"}
	evs := []TranscriptEventView{evOfType(events.EventTypeFailure, `{"error_category":"retryable","error_detail":"boom"}`)}

	got := reconcileTerminalState(session, evs)

	if got.State != "failed" {
		t.Errorf("expected State %q, got %q", "failed", got.State)
	}
	if got.ErrorCategory != "retryable" {
		t.Errorf("expected ErrorCategory %q, got %q", "retryable", got.ErrorCategory)
	}
	if got.ErrorDetail != "boom" {
		t.Errorf("expected ErrorDetail %q, got %q", "boom", got.ErrorDetail)
	}
}

// TestReconcileTerminalState_AlreadyTerminalStateUnchanged guards against
// re-decoding/overwriting fields the DB read already populated correctly
// once session.State already matches the last event's implied terminal
// state.
func TestReconcileTerminalState_AlreadyTerminalStateUnchanged(t *testing.T) {
	cases := []struct {
		name    string
		session SessionView
		evs     []TranscriptEventView
	}{
		{
			name:    "capped",
			session: SessionView{State: "capped", CapKind: "turns"},
			evs:     []TranscriptEventView{evOfType(events.EventTypeCapped, `{"cap_kind":"turns"}`)},
		},
		{
			name:    "failed",
			session: SessionView{State: "failed", ErrorCategory: "retryable", ErrorDetail: "boom"},
			evs:     []TranscriptEventView{evOfType(events.EventTypeFailure, `{"error_category":"retryable","error_detail":"boom"}`)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reconcileTerminalState(tc.session, tc.evs)
			if got != tc.session {
				t.Errorf("expected session returned unchanged, got %+v (want %+v)", got, tc.session)
			}
		})
	}
}

// TestReconcileTerminalState_NonTerminalLastEventUnchanged proves a
// genuinely still-running session (last event neither capped nor failure)
// is returned unchanged.
func TestReconcileTerminalState_NonTerminalLastEventUnchanged(t *testing.T) {
	session := SessionView{State: "running"}
	evs := []TranscriptEventView{evOfType(events.EventTypeAssistantMessage, `{"role":"assistant","content":"hi"}`)}

	got := reconcileTerminalState(session, evs)

	if got != session {
		t.Errorf("expected session returned unchanged, got %+v (want %+v)", got, session)
	}
}

// TestReconcileTerminalState_EmptyEventsUnchanged proves an empty evs
// slice is a no-op.
func TestReconcileTerminalState_EmptyEventsUnchanged(t *testing.T) {
	session := SessionView{State: "running"}

	got := reconcileTerminalState(session, nil)

	if got != session {
		t.Errorf("expected session returned unchanged, got %+v (want %+v)", got, session)
	}
}

// TestReconcileTerminalState_UndecodablePayloadUnchanged proves a
// malformed capped/failure payload never forces a state change -- the DB
// read is trusted as-is when the terminal event's own payload can't be
// decoded.
func TestReconcileTerminalState_UndecodablePayloadUnchanged(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
	}{
		{name: "capped", eventType: events.EventTypeCapped},
		{name: "failure", eventType: events.EventTypeFailure},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := SessionView{State: "running"}
			evs := []TranscriptEventView{evOfType(tc.eventType, `not json`)}

			got := reconcileTerminalState(session, evs)

			if got != session {
				t.Errorf("expected session returned unchanged, got %+v (want %+v)", got, session)
			}
		})
	}
}

func renderSessionLive(t *testing.T, session SessionView, evs []TranscriptEventView) string {
	t.Helper()
	var buf strings.Builder
	if err := SessionLive(session, evs).Render(context.Background(), &buf); err != nil {
		t.Fatalf("SessionLive render failed: %v", err)
	}
	return buf.String()
}

// TestSessionLive_BadgeReflectsCappedEventEvenWhenSessionStateStillRunning
// collapses issue #2776's actual repro into a unit test: a stale
// session.State == "running" DB read must not win over a capped event
// that is already evs's last element.
func TestSessionLive_BadgeReflectsCappedEventEvenWhenSessionStateStillRunning(t *testing.T) {
	session := SessionView{State: "running"}
	evs := []TranscriptEventView{evOfType(events.EventTypeCapped, `{"cap_kind":"turns"}`)}

	body := renderSessionLive(t, session, evs)

	if strings.Contains(body, loadingSpinnerMarker) {
		t.Errorf("expected no spinner once a capped event is the last event, got %q", body)
	}
	if !strings.Contains(body, "badge-warning") {
		t.Errorf("expected badge-warning (capped) class, got %q", body)
	}
	if strings.Contains(body, "badge-info") {
		t.Errorf("expected no badge-info (running) class, got %q", body)
	}
	if !strings.Contains(body, "Capped: turns") {
		t.Errorf("expected capped banner line, got %q", body)
	}
}

// TestSessionLive_BadgeReflectsFailureEventEvenWhenSessionStateStillRunning
// is TestSessionLive_BadgeReflectsCappedEventEvenWhenSessionStateStillRunning's
// failure-event counterpart.
func TestSessionLive_BadgeReflectsFailureEventEvenWhenSessionStateStillRunning(t *testing.T) {
	session := SessionView{State: "running"}
	evs := []TranscriptEventView{evOfType(events.EventTypeFailure, `{"error_category":"retryable","error_detail":"boom"}`)}

	body := renderSessionLive(t, session, evs)

	if strings.Contains(body, loadingSpinnerMarker) {
		t.Errorf("expected no spinner once a failure event is the last event, got %q", body)
	}
	if !strings.Contains(body, "badge-error") {
		t.Errorf("expected badge-error (failed) class, got %q", body)
	}
	if strings.Contains(body, "badge-info") {
		t.Errorf("expected no badge-info (running) class, got %q", body)
	}
	if !strings.Contains(body, "Failed (retryable)") || !strings.Contains(body, "boom") {
		t.Errorf("expected failed banner line naming the category and detail, got %q", body)
	}
}

// This block guards issue #2796: eventTypePrefix/reconcileTerminalState/
// TranscriptList must all recognize FR1's status_change:<status> transcript
// event category -- fixing the stale state badge (#2794) and the leaked
// "Unrecognized event type: status_change:..." transcript rows (#2795).

// TestEventTypePrefix_StatusChange guards eventTypePrefix's new
// "status_change:" prefix match (mirrors the existing tool_call:/
// tool_result: prefix branches) for each of FR1's four transition values.
func TestEventTypePrefix_StatusChange(t *testing.T) {
	for _, status := range []string{"running", "awaiting_input", "done", "stopped"} {
		t.Run(status, func(t *testing.T) {
			got := eventTypePrefix(events.StatusChangeEventType(status))
			if got != "status_change" {
				t.Errorf("eventTypePrefix(%q) = %q, want %q", events.StatusChangeEventType(status), got, "status_change")
			}
		})
	}
}

// TestReconcileTerminalState_StatusChangeOverridesStaleState guards #2794:
// a status_change:done/status_change:stopped last event must override a
// stale DB-read session.State, the same way the pre-existing capped/failure
// branches already do -- most visible for done/stopped since nothing
// commits after a session goes terminal, so nothing else can trigger a
// corrective re-render before the ~30s heartbeat.
func TestReconcileTerminalState_StatusChangeOverridesStaleState(t *testing.T) {
	for _, status := range []string{"done", "stopped"} {
		t.Run(status, func(t *testing.T) {
			session := SessionView{State: "awaiting_input"}
			evs := []TranscriptEventView{evOfType(events.StatusChangeEventType(status), "")}

			got := reconcileTerminalState(session, evs)

			if got.State != status {
				t.Errorf("expected State %q, got %q", status, got.State)
			}
		})
	}
}

// TestReconcileTerminalState_StatusChangeRunningAndAwaitingInputAlsoOverride
// covers the two non-terminal transitions too -- cheap defense-in-depth per
// the issue, even though running/awaiting_input self-correct quickly via the
// next transcript event in practice.
func TestReconcileTerminalState_StatusChangeRunningAndAwaitingInputAlsoOverride(t *testing.T) {
	cases := []struct {
		from string
		to   string
	}{
		{from: "awaiting_input", to: "running"},
		{from: "running", to: "awaiting_input"},
	}

	for _, tc := range cases {
		t.Run(tc.to, func(t *testing.T) {
			session := SessionView{State: tc.from}
			evs := []TranscriptEventView{evOfType(events.StatusChangeEventType(tc.to), "")}

			got := reconcileTerminalState(session, evs)

			if got.State != tc.to {
				t.Errorf("expected State %q, got %q", tc.to, got.State)
			}
		})
	}
}

// TestReconcileTerminalState_StatusChangeAlreadyMatchingUnchanged guards the
// no-op path: the last event's parsed status already equals session.State,
// so session is returned unchanged (matching the existing capped/failure
// unchanged-guard tests).
func TestReconcileTerminalState_StatusChangeAlreadyMatchingUnchanged(t *testing.T) {
	session := SessionView{State: "stopped"}
	evs := []TranscriptEventView{evOfType(events.StatusChangeEventType("stopped"), "")}

	got := reconcileTerminalState(session, evs)

	if got != session {
		t.Errorf("expected session returned unchanged, got %+v (want %+v)", got, session)
	}
}

// TestReconcileTerminalState_StatusChangeMalformedSuffixUnchanged proves a
// malformed/empty status suffix never forces a state change -- the same
// "never break rendering on a bad payload" discipline the capped/failure
// branches already follow.
func TestReconcileTerminalState_StatusChangeMalformedSuffixUnchanged(t *testing.T) {
	session := SessionView{State: "running"}
	evs := []TranscriptEventView{evOfType(events.EventTypeStatusChange+":", "")}

	got := reconcileTerminalState(session, evs)

	if got != session {
		t.Errorf("expected session returned unchanged, got %+v (want %+v)", got, session)
	}
}

func renderTranscriptList(t *testing.T, evs []TranscriptEventView) string {
	t.Helper()
	var buf strings.Builder
	if err := TranscriptList(evs).Render(context.Background(), &buf); err != nil {
		t.Fatalf("TranscriptList render failed: %v", err)
	}
	return buf.String()
}

// TestTranscriptList_StatusChangeEventSkipsRow guards #2795: a
// status_change:<status> event interleaved with ordinary events must never
// reach EventRow -- no "Unrecognized event type" leak, and its own EventID
// never appears as a data-event-id attribute -- while the surrounding
// ordinary events still render their own rows unchanged.
func TestTranscriptList_StatusChangeEventSkipsRow(t *testing.T) {
	evs := []TranscriptEventView{
		{EventID: "e1", Seq: 1, Type: events.EventTypeUserMessage, Payload: []byte(`{"role":"user","content":"hi"}`)},
		{EventID: "e2", Seq: 2, Type: events.StatusChangeEventType("running"), Payload: []byte(`{"status":"running"}`)},
		{EventID: "e3", Seq: 3, Type: events.EventTypeAssistantMessage, Payload: []byte(`{"role":"assistant","content":"hello"}`)},
	}

	body := renderTranscriptList(t, evs)

	if strings.Contains(body, "Unrecognized event type") {
		t.Errorf("expected no leaked status_change row, got %q", body)
	}
	if strings.Contains(body, `data-event-id="e2"`) {
		t.Errorf("expected status_change event's own row to be skipped entirely, got %q", body)
	}
	if !strings.Contains(body, `data-event-id="e1"`) || !strings.Contains(body, `data-event-id="e3"`) {
		t.Errorf("expected surrounding ordinary events to still render their own rows, got %q", body)
	}
	if !strings.Contains(body, "hi") || !strings.Contains(body, "hello") {
		t.Errorf("expected surrounding ordinary events' content unchanged, got %q", body)
	}
}
