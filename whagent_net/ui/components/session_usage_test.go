package components

import (
	"context"
	"strings"
	"testing"
)

// This file guards issue #2248's Testing section, first bullet: "values
// render from GetSessionUsage verbatim; the estimated flag appears iff
// cost_estimated is true; a capped session names the tripped cap" --
// exercised directly against the SessionUsage templ component (the same
// component ../handlers_session.go's initial render and
// ../handlers_session_usage_live.go's live SSE path both call, verbatim,
// per that file's own doc comment), mirroring layout_test.go's
// render-to-buffer shape.

func renderSessionUsage(t *testing.T, session SessionView, usage UsageView) string {
	t.Helper()
	var buf strings.Builder
	if err := SessionUsage(session, usage).Render(context.Background(), &buf); err != nil {
		t.Fatalf("SessionUsage render failed: %v", err)
	}
	return buf.String()
}

// TestSessionUsage_RendersUsedOverCapVerbatim proves both rows render
// GetSessionUsage's summed figures verbatim as "used / cap", including the
// zero-usage case (a session with no committed turns), per the
// Implementation section's "0 / cap" / "$0.00 / cap" requirement.
func TestSessionUsage_RendersUsedOverCapVerbatim(t *testing.T) {
	tests := []struct {
		name  string
		usage UsageView
		want  []string
	}{
		{
			name:  "nonzero usage",
			usage: UsageView{TurnsUsed: 3, TurnCap: 10, CostUsedUSD: 1.25, CostCapUSD: 5},
			want:  []string{"3 / 10", "$1.25 / $5.00"},
		},
		{
			name:  "no committed turns renders 0/cap, not an empty panel",
			usage: UsageView{TurnsUsed: 0, TurnCap: 10, CostUsedUSD: 0, CostCapUSD: 5},
			want:  []string{"0 / 10", "$0.00 / $5.00"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := renderSessionUsage(t, SessionView{}, tc.usage)
			if !strings.Contains(body, `id="usage-panel"`) {
				t.Fatalf("expected a non-empty usage panel, got %q", body)
			}
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("expected usage figure %q in rendered panel, got %q", want, body)
				}
			}
		})
	}
}

// TestSessionUsage_EstimatedFlagAppearsIffCostEstimated guards LB6: the
// panel must say the cost figure includes an estimate visibly in the
// rendered row whenever CostEstimated is true, and must say nothing of the
// kind when it is false.
//
// Red/green discipline (verified by hand, then reverted): temporarily
// changing session_usage.templ's `if usage.CostEstimated {` guard to
// `if false {` made TestSessionUsage_EstimatedFlagAppearsIffCostEstimated's
// "estimated" subtest fail with the expected string missing from the
// rendered body; restoring the guard made it pass again.
func TestSessionUsage_EstimatedFlagAppearsIffCostEstimated(t *testing.T) {
	const flagText = "Includes estimated cost"

	t.Run("estimated", func(t *testing.T) {
		body := renderSessionUsage(t, SessionView{}, UsageView{CostEstimated: true})
		if !strings.Contains(body, flagText) {
			t.Errorf("expected visible estimated-cost flag %q, got %q", flagText, body)
		}
	})

	t.Run("not estimated", func(t *testing.T) {
		body := renderSessionUsage(t, SessionView{}, UsageView{CostEstimated: false})
		if strings.Contains(body, flagText) {
			t.Errorf("expected no estimated-cost flag when CostEstimated is false, got %q", body)
		}
	})
}

// TestSessionUsage_CappedSessionNamesTrippedCap guards the
// already-capped-session case: the row for the cap that actually tripped
// (session.CapKind, sourced from GetSession) names it, while the other
// row -- and an uncapped session -- names nothing.
func TestSessionUsage_CappedSessionNamesTrippedCap(t *testing.T) {
	tests := []struct {
		name      string
		session   SessionView
		wantTurns bool
		wantCost  bool
	}{
		{
			name:      "capped on turns",
			session:   SessionView{State: "capped", CapKind: "turns"},
			wantTurns: true,
			wantCost:  false,
		},
		{
			name:      "capped on cost",
			session:   SessionView{State: "capped", CapKind: "cost"},
			wantTurns: false,
			wantCost:  true,
		},
		{
			name:      "not capped",
			session:   SessionView{State: "running"},
			wantTurns: false,
			wantCost:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := renderSessionUsage(t, tc.session, UsageView{TurnsUsed: 1, TurnCap: 1, CostUsedUSD: 1, CostCapUSD: 1})

			gotTurnsLabel := strings.Contains(body, "Capped: turns")
			gotCostLabel := strings.Contains(body, "Capped: cost")

			if gotTurnsLabel != tc.wantTurns {
				t.Errorf("turns-tripped label: got %v, want %v, body %q", gotTurnsLabel, tc.wantTurns, body)
			}
			if gotCostLabel != tc.wantCost {
				t.Errorf("cost-tripped label: got %v, want %v, body %q", gotCostLabel, tc.wantCost, body)
			}
		})
	}
}
