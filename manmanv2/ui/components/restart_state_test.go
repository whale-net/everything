package components

import (
	"testing"

	"github.com/whale-net/everything/libs/go/htmxui"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// TestRestartBadge_StatusMapping covers #1735's FR12 requirement: each
// restart-state status maps to its own distinguishable label/variant, and a
// nil state (no restart record in the visibility window) renders nothing.
func TestRestartBadge_StatusMapping(t *testing.T) {
	cases := []struct {
		name        string
		state       *manmanpb.PendingRestartState
		wantShow    bool
		wantLabel   string
		wantVariant htmxui.BadgeVariant
		wantTitle   string
	}{
		{
			name:     "nil: no badge",
			state:    nil,
			wantShow: false,
		},
		{
			name:        "pending: in-progress badge",
			state:       &manmanpb.PendingRestartState{Status: "pending"},
			wantShow:    true,
			wantLabel:   "Restarting",
			wantVariant: htmxui.BadgeInfo,
			wantTitle:   "",
		},
		{
			name:     "started: no badge (session status already tells the story)",
			state:    &manmanpb.PendingRestartState{Status: "started"},
			wantShow: false,
		},
		{
			name:        "failed: error badge with failure reason as title",
			state:       &manmanpb.PendingRestartState{Status: "failed", FailureReason: "stop dispatch failed"},
			wantShow:    true,
			wantLabel:   "Restart failed",
			wantVariant: htmxui.BadgeError,
			wantTitle:   "stop dispatch failed",
		},
		{
			name:        "expired: warning badge with a static explanatory title",
			state:       &manmanpb.PendingRestartState{Status: "expired", FailureReason: "stall deadline exceeded"},
			wantShow:    true,
			wantLabel:   "Restart stalled",
			wantVariant: htmxui.BadgeWarning,
			wantTitle:   "Stop never completed, so the Start was not dispatched.",
		},
		{
			name:     "unrecognised future status: fail closed to no badge",
			state:    &manmanpb.PendingRestartState{Status: "some-future-status"},
			wantShow: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, variant, title, show := RestartBadge(tc.state)
			if show != tc.wantShow {
				t.Fatalf("show = %v, want %v (label=%q variant=%q title=%q)", show, tc.wantShow, label, variant, title)
			}
			if !show {
				return
			}
			if label != tc.wantLabel {
				t.Errorf("label = %q, want %q", label, tc.wantLabel)
			}
			if variant != tc.wantVariant {
				t.Errorf("variant = %q, want %q", variant, tc.wantVariant)
			}
			if title != tc.wantTitle {
				t.Errorf("title = %q, want %q", title, tc.wantTitle)
			}
		})
	}
}

// TestRestartBadge_DistinctFromEachOther guards the literal FR12
// requirement directly: in-progress (pending), failed, and expired must
// each render a visibly distinct label -- a single generic "restart"
// indicator does not satisfy FR12.
func TestRestartBadge_DistinctFromEachOther(t *testing.T) {
	pendingLabel, _, _, _ := RestartBadge(&manmanpb.PendingRestartState{Status: "pending"})
	failedLabel, _, _, _ := RestartBadge(&manmanpb.PendingRestartState{Status: "failed"})
	expiredLabel, _, _, _ := RestartBadge(&manmanpb.PendingRestartState{Status: "expired"})

	labels := map[string]string{"pending": pendingLabel, "failed": failedLabel, "expired": expiredLabel}
	seen := make(map[string]string)
	for status, label := range labels {
		if other, ok := seen[label]; ok {
			t.Errorf("status %q and %q share the same label %q -- FR12 requires distinguishable in-progress/stalled/failed states", status, other, label)
		}
		seen[label] = status
	}
}

// TestRestartBadge_TitleNeverVariesAcrossCalls guards the NFR11 byte-
// stability contract at its source: RestartBadge must be a pure function of
// state -- calling it twice with an unchanged state must produce identical
// output, since this content renders inside the SSE-pushed row fragment
// (#1735/#1724) where any per-call-varying content defeats the no-swap
// change detector.
func TestRestartBadge_TitleNeverVariesAcrossCalls(t *testing.T) {
	state := &manmanpb.PendingRestartState{Status: "failed", FailureReason: "boom"}
	label1, variant1, title1, show1 := RestartBadge(state)
	label2, variant2, title2, show2 := RestartBadge(state)

	if label1 != label2 || variant1 != variant2 || title1 != title2 || show1 != show2 {
		t.Errorf("RestartBadge is not stable across calls with unchanged state: (%q,%q,%q,%v) vs (%q,%q,%q,%v)",
			label1, variant1, title1, show1, label2, variant2, title2, show2)
	}
}
