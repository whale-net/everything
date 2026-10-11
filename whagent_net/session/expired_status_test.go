package session

import (
	"strings"
	"testing"
)

// TestIsTerminal_ExpiredIsTerminalAndDistinct proves expired is terminal,
// and running/awaiting_input are not, so the CAS and archival set agree.
func TestIsTerminal_ExpiredIsTerminalAndDistinct(t *testing.T) {
	want := map[Status]bool{
		StatusRunning:       false,
		StatusAwaitingInput: false,
		StatusDone:          true,
		StatusStopped:       true,
		StatusFailed:        true,
		StatusCapped:        true,
		StatusExpired:       true,
	}
	for s, terminal := range want {
		if got := s.IsTerminal(); got != terminal {
			t.Errorf("Status(%q).IsTerminal() = %v, want %v", s, got, terminal)
		}
	}
}

// TestTerminalStatusList_MatchesIsTerminal proves the SQL IN-list used by the
// UpdateStatus CAS and archival selection is derived from the same set as
// IsTerminal, so expired appears in both.
func TestTerminalStatusList_MatchesIsTerminal(t *testing.T) {
	for _, s := range terminalStatuses {
		if !s.IsTerminal() {
			t.Errorf("terminalStatuses contains %q but IsTerminal is false", s)
		}
		if !strings.Contains(terminalStatusList, "'"+string(s)+"'") {
			t.Errorf("terminalStatusList %q is missing %q", terminalStatusList, s)
		}
	}
	if !strings.Contains(terminalStatusList, "'expired'") {
		t.Errorf("terminalStatusList %q does not include 'expired'", terminalStatusList)
	}
}
