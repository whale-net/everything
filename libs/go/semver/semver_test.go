package semver

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		in                  string
		major, minor, patch int
		prerelease, build   string
	}{
		{"v1.2.3", 1, 2, 3, "", ""},
		{"1.2.3", 1, 2, 3, "", ""},
		{"v0.0.1", 0, 0, 1, "", ""},
		{"v1.2.3-beta.1", 1, 2, 3, "beta.1", ""},
		{"v1.2.3+build.5", 1, 2, 3, "", "build.5"},
		{"v1.2.3-rc.1+build.5", 1, 2, 3, "rc.1", "build.5"},
	}
	for _, tt := range tests {
		v, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if v.Major != tt.major || v.Minor != tt.minor || v.Patch != tt.patch || v.Prerelease != tt.prerelease || v.Build != tt.build {
			t.Errorf("Parse(%q) = %+v, want major=%d minor=%d patch=%d prerelease=%q build=%q",
				tt.in, v, tt.major, tt.minor, tt.patch, tt.prerelease, tt.build)
		}
	}
}

func TestParse_Invalid(t *testing.T) {
	for _, in := range []string{"", "vX.Y.Z", "1.2", "1.2.3.4", "not-a-version"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q): expected an error, got none", in)
		}
	}
}

// TestParseRelease_RejectsPrereleaseAndBuild proves the AllocateVersion
// contract from PLAN.md's AR-5 addendum item 3: a prerelease or
// build-metadata suffix must be rejected explicitly, not silently accepted.
func TestParseRelease_RejectsPrereleaseAndBuild(t *testing.T) {
	for _, in := range []string{"v1.2.3-alpha", "v1.2.3-alpha.1", "v1.2.3+build.5", "v1.2.3-rc.1+build.5"} {
		if _, err := ParseRelease(in); err == nil {
			t.Errorf("ParseRelease(%q): expected rejection of prerelease/build metadata, got none", in)
		}
	}
	// A plain release must still be accepted.
	if _, err := ParseRelease("v1.2.3"); err != nil {
		t.Errorf("ParseRelease(%q): unexpected error: %v", "v1.2.3", err)
	}
}

func TestVersion_String(t *testing.T) {
	v := Version{Major: 1, Minor: 2, Patch: 3, Prerelease: "beta"}
	if got := v.String(); got != "v1.2.3" {
		t.Errorf("String() = %q, want %q (prerelease must be dropped)", got, "v1.2.3")
	}
}

func TestVersion_Increment(t *testing.T) {
	tests := []struct {
		in            string
		incrementType string
		want          string
	}{
		{"v1.2.3", "major", "v2.0.0"},
		{"v1.2.3", "minor", "v1.3.0"},
		{"v1.2.3", "patch", "v1.2.4"},
		{"v0.0.0", "minor", "v0.1.0"},
		{"v2.5.1", "patch", "v2.5.2"},
		{"v1.9.9", "major", "v2.0.0"},
	}
	for _, tt := range tests {
		v, err := Parse(tt.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		got, err := v.Increment(tt.incrementType)
		if err != nil {
			t.Errorf("Increment(%q, %q): %v", tt.in, tt.incrementType, err)
			continue
		}
		if got.String() != tt.want {
			t.Errorf("Increment(%q, %q) = %q, want %q", tt.in, tt.incrementType, got.String(), tt.want)
		}
	}
}

func TestVersion_Increment_UnknownKind(t *testing.T) {
	v, _ := Parse("v1.2.3")
	if _, err := v.Increment("bogus"); err == nil {
		t.Fatalf("Increment(%q): expected an error for an unknown increment kind, got none", "bogus")
	}
}

// TestCompare_NumericNotLexical is the specific bug PLAN.md's AR-5 addendum
// item 2 exists to prevent: lexical TEXT ordering puts "v1.10.0" before
// "v1.9.0" because '1' < '9' as characters. Compare must get this right.
func TestCompare_NumericNotLexical(t *testing.T) {
	v19, err := Parse("v1.9.0")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	v110, err := Parse("v1.10.0")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if Compare(v19, v110) != -1 {
		t.Fatalf("Compare(v1.9.0, v1.10.0) = %d, want -1 (v1.9.0 < v1.10.0 numerically)", Compare(v19, v110))
	}
	if Compare(v110, v19) != 1 {
		t.Fatalf("Compare(v1.10.0, v1.9.0) = %d, want 1", Compare(v110, v19))
	}
	if Compare(v19, v19) != 0 {
		t.Fatalf("Compare(v1.9.0, v1.9.0) = %d, want 0", Compare(v19, v19))
	}
}
