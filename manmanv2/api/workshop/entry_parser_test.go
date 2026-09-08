package workshop

import (
	"strings"
	"testing"
)

// TestParseWorkshopEntry_TableDriven covers FR2's single-line auto-detection
// rules: raw numeric IDs and steamcommunity.com Workshop URLs (with/without
// scheme, with extra query params), plus the specific invalid-line reasons
// FR3 requires callers be able to surface per line.
func TestParseWorkshopEntry_TableDriven(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantID     string
		wantErr    bool
		wantErrMsg string // substring match; empty means "any error is fine"
	}{
		{
			name:   "raw numeric ID",
			line:   "450814997",
			wantID: "450814997",
		},
		{
			name:   "raw numeric ID with surrounding whitespace",
			line:   "   450814997   ",
			wantID: "450814997",
		},
		{
			name:   "sharedfiles filedetails URL",
			line:   "https://steamcommunity.com/sharedfiles/filedetails/?id=450814997",
			wantID: "450814997",
		},
		{
			name:   "workshop filedetails URL",
			line:   "https://steamcommunity.com/workshop/filedetails/?id=450814997",
			wantID: "450814997",
		},
		{
			name:   "URL with extra searchtext query param",
			line:   "https://steamcommunity.com/sharedfiles/filedetails/?id=450814997&searchtext=zombies",
			wantID: "450814997",
		},
		{
			name:   "scheme-less steamcommunity.com URL",
			line:   "steamcommunity.com/sharedfiles/filedetails/?id=450814997",
			wantID: "450814997",
		},
		{
			name:   "http scheme URL",
			line:   "http://steamcommunity.com/sharedfiles/filedetails/?id=450814997",
			wantID: "450814997",
		},
		{
			name:   "https scheme URL",
			line:   "https://steamcommunity.com/sharedfiles/filedetails/?id=450814997",
			wantID: "450814997",
		},
		{
			name:       "not a thing",
			line:       "not-a-thing",
			wantErr:    true,
			wantErrMsg: "not a numeric Workshop ID or recognized Workshop URL",
		},
		{
			name:       "non-steamcommunity host with id param",
			line:       "https://example.com/?id=123",
			wantErr:    true,
			wantErrMsg: "not a numeric Workshop ID or recognized Workshop URL",
		},
		{
			name:       "id parameter is not numeric",
			line:       "https://steamcommunity.com/sharedfiles/filedetails/?id=abc",
			wantErr:    true,
			wantErrMsg: "id parameter is not numeric",
		},
		{
			name:       "URL missing id parameter entirely",
			line:       "https://steamcommunity.com/sharedfiles/filedetails/",
			wantErr:    true,
			wantErrMsg: "URL is missing an id parameter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, err := ParseWorkshopEntry(tt.line)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseWorkshopEntry(%q) = (%q, nil), want an error", tt.line, gotID)
				}
				if tt.wantErrMsg != "" && !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Fatalf("ParseWorkshopEntry(%q) error = %q, want substring %q", tt.line, err.Error(), tt.wantErrMsg)
				}
				if gotID != "" {
					t.Fatalf("ParseWorkshopEntry(%q) returned non-empty ID %q alongside an error", tt.line, gotID)
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseWorkshopEntry(%q) unexpected error: %v", tt.line, err)
			}
			if gotID != tt.wantID {
				t.Fatalf("ParseWorkshopEntry(%q) = %q, want %q", tt.line, gotID, tt.wantID)
			}
		})
	}
}

// TestParseWorkshopEntries_MixedRawAndURLBlockAllResolve proves a pasted
// block interleaving raw numeric IDs and both URL forms resolves every line
// (FR2), each keeping its original raw input.
func TestParseWorkshopEntries_MixedRawAndURLBlockAllResolve(t *testing.T) {
	input := strings.Join([]string{
		"450814997",
		"https://steamcommunity.com/sharedfiles/filedetails/?id=111111111",
		"222222222",
		"https://steamcommunity.com/workshop/filedetails/?id=333333333&searchtext=stuff",
	}, "\n")

	entries := ParseWorkshopEntries(input)
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(entries), entries)
	}

	wantIDs := []string{"450814997", "111111111", "222222222", "333333333"}
	for i, entry := range entries {
		if entry.Err != nil {
			t.Fatalf("entry %d (%q) unexpected error: %v", i, entry.RawInput, entry.Err)
		}
		if entry.WorkshopID != wantIDs[i] {
			t.Fatalf("entry %d WorkshopID = %q, want %q", i, entry.WorkshopID, wantIDs[i])
		}
	}
}

// TestParseWorkshopEntries_InvalidLinesDoNotFailSurroundingLines proves FR3:
// a bad line yields a per-entry Err without preventing valid lines before or
// after it from resolving.
func TestParseWorkshopEntries_InvalidLinesDoNotFailSurroundingLines(t *testing.T) {
	invalidLines := []string{
		"not-a-thing",
		"https://example.com/?id=123",
		"https://steamcommunity.com/sharedfiles/filedetails/?id=abc",
		"https://steamcommunity.com/sharedfiles/filedetails/",
	}

	for _, invalid := range invalidLines {
		t.Run(invalid, func(t *testing.T) {
			input := strings.Join([]string{"450814997", invalid, "999999999"}, "\n")
			entries := ParseWorkshopEntries(input)
			if len(entries) != 3 {
				t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
			}

			if entries[0].Err != nil || entries[0].WorkshopID != "450814997" {
				t.Fatalf("preceding valid entry corrupted: %+v", entries[0])
			}
			if entries[1].Err == nil {
				t.Fatalf("expected invalid line %q to produce an error", invalid)
			}
			if entries[2].Err != nil || entries[2].WorkshopID != "999999999" {
				t.Fatalf("following valid entry corrupted: %+v", entries[2])
			}
		})
	}
}

// TestParseWorkshopEntries_BlankLinesSkipped proves blank lines (including
// whitespace-only lines) are dropped entirely -- they are neither items nor
// errors.
func TestParseWorkshopEntries_BlankLinesSkipped(t *testing.T) {
	input := "450814997\n\n   \n999999999\n"
	entries := ParseWorkshopEntries(input)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (blank lines skipped): %+v", len(entries), entries)
	}
	if entries[0].WorkshopID != "450814997" || entries[1].WorkshopID != "999999999" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

// TestParseWorkshopEntries_BlankLineOnlyInputYieldsNoEntries proves an
// input that is entirely blank lines (or empty) produces zero entries, not
// an error.
func TestParseWorkshopEntries_BlankLineOnlyInputYieldsNoEntries(t *testing.T) {
	for _, input := range []string{"", "\n", "   \n\n\t\n"} {
		entries := ParseWorkshopEntries(input)
		if len(entries) != 0 {
			t.Fatalf("ParseWorkshopEntries(%q) = %d entries, want 0: %+v", input, len(entries), entries)
		}
	}
}

// TestParseWorkshopEntries_DuplicateIDsAcrossRawAndURLForms proves
// de-duplication is on the *resolved* Workshop ID, not the raw text: the
// same ID pasted once as a raw number and again as a URL is only resolved
// once, with the second occurrence marked as a non-fatal duplicate outcome
// rather than silently re-succeeding (which would double-create the addon).
func TestParseWorkshopEntries_DuplicateIDsAcrossRawAndURLForms(t *testing.T) {
	input := strings.Join([]string{
		"450814997",
		"https://steamcommunity.com/sharedfiles/filedetails/?id=450814997",
		"https://steamcommunity.com/workshop/filedetails/?id=450814997",
	}, "\n")

	entries := ParseWorkshopEntries(input)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}

	if entries[0].Err != nil || entries[0].WorkshopID != "450814997" {
		t.Fatalf("first occurrence should succeed: %+v", entries[0])
	}

	for i, entry := range entries[1:] {
		if entry.Err == nil {
			t.Fatalf("entry %d: duplicate of an already-resolved ID must not silently succeed: %+v", i+1, entry)
		}
		if entry.WorkshopID != "" {
			t.Fatalf("entry %d: duplicate entry must not carry a WorkshopID (would double-create the addon): %+v", i+1, entry)
		}
		if !strings.Contains(entry.Err.Error(), "duplicate") {
			t.Fatalf("entry %d: expected a duplicate-flavored error, got %q", i+1, entry.Err.Error())
		}
	}
}
