package components

import (
	"context"
	"strings"
	"testing"
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
