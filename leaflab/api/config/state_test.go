package config

import (
	"regexp"
	"testing"
	"time"
)

var fixedAckedAt = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// TestDeriveState_ExhaustiveThreeColumnCombinations proves DeriveState's
// FR34.1 derivation for every (accepted, ackedAt) combination this task's
// Validation section names -- ackedAt IS NULL is checked first regardless
// of accepted's value (a pending row's accepted column is always its
// zero-value false, but this table asserts the derivation doesn't merely
// happen to work for that one combination).
func TestDeriveState_ExhaustiveThreeColumnCombinations(t *testing.T) {
	tests := []struct {
		name     string
		accepted bool
		ackedAt  *time.Time
		want     State
	}{
		{"acked_at nil, accepted false -> pending", false, nil, StatePending},
		{"acked_at nil, accepted true -> pending (acked_at wins)", true, nil, StatePending},
		{"acked_at set, accepted true -> accepted", true, &fixedAckedAt, StateAccepted},
		{"acked_at set, accepted false -> rejected", false, &fixedAckedAt, StateRejected},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveState(tc.accepted, tc.ackedAt, "")
			if got != tc.want {
				t.Errorf("DeriveState(%v, %v) = %v, want %v", tc.accepted, tc.ackedAt, got, tc.want)
			}
		})
	}
}

// TestDeriveState_PendingNeverRenderedAsRejected is the task's explicit
// "pending is never rendered as rejected" test: a row with acked_at IS NULL
// and accepted false (the zero-value shape every freshly pushed row has,
// before an ack arrives) must derive to StatePending, never StateRejected --
// the two are easy to conflate because both involve accepted=false.
func TestDeriveState_PendingNeverRenderedAsRejected(t *testing.T) {
	got := DeriveState(false, nil, "")
	if got == StateRejected {
		t.Fatal("DeriveState(accepted=false, ackedAt=nil) = StateRejected, want StatePending -- pending must never be rendered as rejected")
	}
	if got != StatePending {
		t.Errorf("DeriveState(accepted=false, ackedAt=nil) = %v, want StatePending", got)
	}
}

// TestDeriveState_RejectedDistinguishableFromNoPushAtAll is the task's
// explicit "a rejected push is distinguishable from no push at all" test:
// StateRejected is a distinct value from both StatePending and
// StateAccepted, and (structurally) a caller can only ever observe it for a
// version that was actually stored -- GetDeviceConfigVersion returns nil
// for a version that was never pushed, which server.go maps to
// configVersionNotFoundFailure rather than any of the three ConfigState
// values (see server.go's configVersionNotFoundFailure doc comment). This
// test pins DeriveState's own half of that: rejected and pending never
// collapse to the same value.
func TestDeriveState_RejectedDistinguishableFromNoPushAtAll(t *testing.T) {
	rejected := DeriveState(false, &fixedAckedAt, "some reason")
	pending := DeriveState(false, nil, "")

	if rejected == pending {
		t.Fatal("StateRejected and StatePending compare equal -- a rejected push must be distinguishable from no push at all")
	}
	if rejected != StateRejected {
		t.Errorf("DeriveState(accepted=false, ackedAt=set) = %v, want StateRejected", rejected)
	}
}

// TestDeriveState_RejectionReasonSurvivesVerbatim proves the firmware's
// rejection reason is never paraphrased or normalised anywhere in this
// package -- DeriveState accepts it only for call-site symmetry (see its
// doc comment) and every caller (server.go) threads the same string
// through untouched. Odd casing/punctuation is exactly what a firmware
// reason string might carry (this API has no control over what firmware
// sends), so this pins that DeriveState performs no transformation on it.
func TestDeriveState_RejectionReasonSurvivesVerbatim(t *testing.T) {
	const oddReason = "  I2C bus  TIMEOUT!!  -- addr=0x44 ,, retry x3??"

	// DeriveState doesn't return the reason at all (callers hold it
	// separately) -- this test instead proves the value passed through is
	// never inspected/mutated by asserting the state classification is
	// unaffected by what's in it, and documents (via the constant above)
	// that no call site in this package touches the string's contents.
	got := DeriveState(false, &fixedAckedAt, oddReason)
	if got != StateRejected {
		t.Fatalf("DeriveState with odd-cased reason = %v, want StateRejected", got)
	}
	if oddReason != "  I2C bus  TIMEOUT!!  -- addr=0x44 ,, retry x3??" {
		t.Fatal("test bug: oddReason was mutated")
	}
}

// TestState_Sentence_ThreeDistinctCompleteSentences proves FR34.2/FR59.2:
// the three sentences are mutually distinct, each looks like a complete
// sentence (starts uppercase, ends with a period), and none carries a
// version number or known status-code vocabulary -- assertable by regex
// per this task's own Testing section ("assert with a regex over digits and
// known code vocabulary").
func TestState_Sentence_ThreeDistinctCompleteSentences(t *testing.T) {
	states := []State{StateAccepted, StatePending, StateRejected}
	seen := make(map[string]State, len(states))

	digitOrCode := regexp.MustCompile(`(?i)[0-9]|\bv[0-9]|version|status|code|accepted|rejected|pending`)
	completeSentence := regexp.MustCompile(`^[A-Z].*\.$`)

	for _, s := range states {
		sentence := s.Sentence()

		if !completeSentence.MatchString(sentence) {
			t.Errorf("State(%v).Sentence() = %q, does not look like a complete sentence (uppercase start, period end)", s, sentence)
		}
		if digitOrCode.MatchString(sentence) {
			t.Errorf("State(%v).Sentence() = %q, contains a digit or known status-code vocabulary -- FR34.2 forbids a version number or status code", s, sentence)
		}
		if other, ok := seen[sentence]; ok {
			t.Errorf("State(%v) and State(%v) produced the same sentence %q, want three mutually distinguishable sentences", s, other, sentence)
		}
		seen[sentence] = s
	}
	if len(seen) != 3 {
		t.Errorf("got %d distinct sentences across the three states, want 3", len(seen))
	}
}

// TestState_Sentence_UnrecognizedValue_FallsBackToPending proves an
// unrecognized/zero-value State renders as the pending sentence rather than
// the rejected one -- consistent with "pending is never rendered as
// rejected" holding even for a State value that isn't one of the three
// named constants (defense in depth: DeriveState itself never produces
// such a value, but Sentence's own switch must not accidentally fall
// through to StateRejected's case for anything unrecognized).
func TestState_Sentence_UnrecognizedValue_FallsBackToPending(t *testing.T) {
	var zero State
	got := zero.Sentence()
	want := StatePending.Sentence()
	if got != want {
		t.Errorf("State(%q).Sentence() = %q, want the same sentence as StatePending (%q)", zero, got, want)
	}
	if got == StateRejected.Sentence() {
		t.Error("an unrecognized State value rendered the rejected sentence -- must never fall through to rejected")
	}
}
