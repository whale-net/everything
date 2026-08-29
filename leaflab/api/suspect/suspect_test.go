package suspect

import "testing"

// TestAll_StableEnumerableIdentifiers proves FR26.3's "the check
// identifiers are enumerable and stable": All() returns exactly the four
// known checks, each with the exact wire identifier documented on its own
// constant -- renaming one is a wire-contract break, not a refactor, so
// this test pins the literal string values, not just their count.
func TestAll_StableEnumerableIdentifiers(t *testing.T) {
	all := All()
	want := map[Check]string{
		CheckOutOfRange:           "out_of_range",
		CheckPersistedInvalidFlag: "persisted_invalid_flag",
		CheckStaleAttribution:     "stale_attribution",
		CheckMigrationSnapWindow:  "migration_snap_window",
	}
	if len(all) != len(want) {
		t.Fatalf("len(All()) = %d, want %d", len(all), len(want))
	}
	seen := make(map[Check]bool)
	for _, c := range all {
		wantStr, ok := want[c]
		if !ok {
			t.Errorf("All() contains unexpected check %q", c)
			continue
		}
		if c.String() != wantStr {
			t.Errorf("Check %v .String() = %q, want stable identifier %q", c, c.String(), wantStr)
		}
		if seen[c] {
			t.Errorf("All() contains %q more than once", c)
		}
		seen[c] = true
	}
	for c := range want {
		if !seen[c] {
			t.Errorf("All() is missing %q", c)
		}
	}
}

// TestMarker_NilVsEmptyChecksDistinguishesNotSuspect proves a Marker with
// no checks is not suspect and renders no wire-form suspect_checks -- "a
// reading with no marks is not suspect" (suspect.go's own doc comment) --
// distinct from a Marker carrying at least one check.
func TestMarker_NilVsEmptyChecksDistinguishesNotSuspect(t *testing.T) {
	notSuspect := Marker{}
	if notSuspect.Suspect() {
		t.Error("empty Marker.Suspect() = true, want false")
	}
	if got := notSuspect.Strings(); got != nil {
		t.Errorf("empty Marker.Strings() = %v, want nil", got)
	}

	suspectMarker := Marker{Checks: []Check{CheckOutOfRange}}
	if !suspectMarker.Suspect() {
		t.Error("Marker with a check .Suspect() = false, want true")
	}
	if got := suspectMarker.Strings(); len(got) != 1 || got[0] != "out_of_range" {
		t.Errorf(`Marker.Strings() = %v, want ["out_of_range"]`, got)
	}
}

// TestCountMarkers_NoneAndAllMarked proves CountMarkers correctly tallies
// both the "nothing marked" and "everything marked" cases -- FR26.3's "a
// marker that covers everything is visible as such rather than silently
// universal" starts with this tally being right in both directions.
func TestCountMarkers_NoneAndAllMarked(t *testing.T) {
	none := CountMarkers([]Marker{{}, {}})
	if none.Returned != 2 || none.Marked != 0 {
		t.Errorf("CountMarkers(none marked) = %+v, want {Returned:2 Marked:0}", none)
	}

	all := CountMarkers([]Marker{
		{Checks: []Check{CheckOutOfRange}},
		{Checks: []Check{CheckPersistedInvalidFlag, CheckStaleAttribution}},
	})
	if all.Returned != 2 || all.Marked != 2 {
		t.Errorf("CountMarkers(all marked) = %+v, want {Returned:2 Marked:2}", all)
	}
}
