package release

import (
	"testing"
)

func TestValidateSemanticVersion(t *testing.T) {
	valid := []string{
		"v1.0.0",
		"v0.1.0",
		"v10.20.30",
		"v1.0.0-alpha",
		"v1.0.0-beta1",
		"v1.0.0-rc2",
		"v3.2.1-rc2",
	}
	for _, v := range valid {
		if !ValidateSemanticVersion(v) {
			t.Errorf("ValidateSemanticVersion(%q) = false, want true", v)
		}
	}

	invalid := []string{
		"1.0.0",    // missing v prefix
		"v1.0",     // missing patch
		"v1",       // missing minor and patch
		"",         // empty
		"latest",   // special string, not semver
		"release-1.0.0",
	}
	for _, v := range invalid {
		if ValidateSemanticVersion(v) {
			t.Errorf("ValidateSemanticVersion(%q) = true, want false", v)
		}
	}
}

func TestParseSemanticVersion(t *testing.T) {
	tests := []struct {
		input      string
		wantMajor  int
		wantMinor  int
		wantPatch  int
		wantPre    string
		wantErr    bool
	}{
		{"v1.2.3", 1, 2, 3, "", false},
		{"v0.0.1", 0, 0, 1, "", false},
		{"v10.20.30", 10, 20, 30, "", false},
		{"v1.2.3-beta1", 1, 2, 3, "beta1", false},
		{"v1.2.3-rc.2", 1, 2, 3, "rc.2", false},
		// Without 'v' prefix
		{"1.2.3", 1, 2, 3, "", false},
		// Invalid
		{"v1.0", 0, 0, 0, "", true},
		{"v1", 0, 0, 0, "", true},
		{"not-a-version", 0, 0, 0, "", true},
		{"", 0, 0, 0, "", true},
	}

	for _, tt := range tests {
		v, err := ParseSemanticVersion(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseSemanticVersion(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if v.Major != tt.wantMajor || v.Minor != tt.wantMinor || v.Patch != tt.wantPatch || v.PreRelease != tt.wantPre {
			t.Errorf("ParseSemanticVersion(%q) = %+v, want {%d %d %d %q}", tt.input, v, tt.wantMajor, tt.wantMinor, tt.wantPatch, tt.wantPre)
		}
	}
}

func TestVersionString(t *testing.T) {
	tests := []struct {
		v    Version
		want string
	}{
		{Version{1, 2, 3, ""}, "v1.2.3"},
		{Version{0, 0, 1, ""}, "v0.0.1"},
		{Version{1, 2, 3, "beta1"}, "v1.2.3-beta1"},
	}
	for _, tt := range tests {
		got := tt.v.String()
		if got != tt.want {
			t.Errorf("Version.String() = %q, want %q", got, tt.want)
		}
	}
}

func TestIncrementMinorVersion(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"v1.2.3", "v1.3.0", false},
		{"v0.0.0", "v0.1.0", false},
		{"v1.2.3-beta1", "v1.3.0", false}, // pre-release is dropped
		{"invalid", "", true},
	}
	for _, tt := range tests {
		got, err := IncrementMinorVersion(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("IncrementMinorVersion(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("IncrementMinorVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIncrementPatchVersion(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"v1.2.3", "v1.2.4", false},
		{"v0.0.0", "v0.0.1", false},
		{"v1.2.3-rc1", "v1.2.4", false}, // pre-release is dropped
		{"bad", "", true},
	}
	for _, tt := range tests {
		got, err := IncrementPatchVersion(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("IncrementPatchVersion(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("IncrementPatchVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
