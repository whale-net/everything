package validation

import (
	"testing"
)

func TestParseSemanticVersion(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		wantMajor int
		wantMinor int
		wantPatch int
		wantSuffix string
		wantErr   bool
	}{
		{
			name:      "simple version",
			version:   "v1.2.3",
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
			wantSuffix: "",
			wantErr:   false,
		},
		{
			name:      "version with suffix",
			version:   "v1.2.3-alpha.1",
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
			wantSuffix: "alpha.1",
			wantErr:   false,
		},
		{
			name:      "version with beta suffix",
			version:   "v2.0.0-beta",
			wantMajor: 2,
			wantMinor: 0,
			wantPatch: 0,
			wantSuffix: "beta",
			wantErr:   false,
		},
		{
			name:    "invalid version missing v",
			version: "1.2.3",
			wantErr: true,
		},
		{
			name:    "invalid version format",
			version: "v1.2",
			wantErr: true,
		},
		{
			name:    "invalid version with text",
			version: "vX.Y.Z",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSemanticVersion(tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSemanticVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Major != tt.wantMajor || got.Minor != tt.wantMinor || got.Patch != tt.wantPatch || got.Suffix != tt.wantSuffix {
					t.Errorf("ParseSemanticVersion() = %+v, want Major:%d Minor:%d Patch:%d Suffix:%s",
						got, tt.wantMajor, tt.wantMinor, tt.wantPatch, tt.wantSuffix)
				}
			}
		})
	}
}

func TestSemanticVersion_String(t *testing.T) {
	tests := []struct {
		name    string
		version *SemanticVersion
		want    string
	}{
		{
			name:    "simple version",
			version: &SemanticVersion{Major: 1, Minor: 2, Patch: 3},
			want:    "v1.2.3",
		},
		{
			name:    "version with suffix",
			version: &SemanticVersion{Major: 1, Minor: 2, Patch: 3, Suffix: "alpha.1"},
			want:    "v1.2.3-alpha.1",
		},
		{
			name:    "zero version",
			version: &SemanticVersion{Major: 0, Minor: 0, Patch: 0},
			want:    "v0.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.version.String()
			if got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSemanticVersion_IncrementMinor(t *testing.T) {
	tests := []struct {
		name    string
		version *SemanticVersion
		want    string
	}{
		{
			name:    "increment from 1.2.3",
			version: &SemanticVersion{Major: 1, Minor: 2, Patch: 3},
			want:    "v1.3.0",
		},
		{
			name:    "increment with suffix (should be removed)",
			version: &SemanticVersion{Major: 1, Minor: 2, Patch: 3, Suffix: "alpha"},
			want:    "v1.3.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.version.IncrementMinor()
			if got.String() != tt.want {
				t.Errorf("IncrementMinor() = %v, want %v", got.String(), tt.want)
			}
		})
	}
}

func TestSemanticVersion_IncrementPatch(t *testing.T) {
	tests := []struct {
		name    string
		version *SemanticVersion
		want    string
	}{
		{
			name:    "increment from 1.2.3",
			version: &SemanticVersion{Major: 1, Minor: 2, Patch: 3},
			want:    "v1.2.4",
		},
		{
			name:    "increment with suffix (should be removed)",
			version: &SemanticVersion{Major: 1, Minor: 2, Patch: 3, Suffix: "beta"},
			want:    "v1.2.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.version.IncrementPatch()
			if got.String() != tt.want {
				t.Errorf("IncrementPatch() = %v, want %v", got.String(), tt.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name    string
		v1      string
		v2      string
		want    int
		wantErr bool
	}{
		{
			name: "equal versions",
			v1:   "v1.2.3",
			v2:   "v1.2.3",
			want: 0,
		},
		{
			name: "v1 less than v2 (major)",
			v1:   "v1.2.3",
			v2:   "v2.2.3",
			want: -1,
		},
		{
			name: "v1 greater than v2 (major)",
			v1:   "v2.2.3",
			v2:   "v1.2.3",
			want: 1,
		},
		{
			name: "v1 less than v2 (minor)",
			v1:   "v1.2.3",
			v2:   "v1.3.3",
			want: -1,
		},
		{
			name: "v1 greater than v2 (minor)",
			v1:   "v1.3.3",
			v2:   "v1.2.3",
			want: 1,
		},
		{
			name: "v1 less than v2 (patch)",
			v1:   "v1.2.3",
			v2:   "v1.2.4",
			want: -1,
		},
		{
			name: "v1 greater than v2 (patch)",
			v1:   "v1.2.4",
			v2:   "v1.2.3",
			want: 1,
		},
		{
			name: "version without suffix > version with suffix",
			v1:   "v1.2.3",
			v2:   "v1.2.3-alpha",
			want: 1,
		},
		{
			name: "version with suffix < version without suffix",
			v1:   "v1.2.3-alpha",
			v2:   "v1.2.3",
			want: -1,
		},
		{
			name:    "invalid v1",
			v1:      "invalid",
			v2:      "v1.2.3",
			wantErr: true,
		},
		{
			name:    "invalid v2",
			v1:      "v1.2.3",
			v2:      "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompareVersions(tt.v1, tt.v2)
			if (err != nil) != tt.wantErr {
				t.Errorf("CompareVersions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("CompareVersions() = %v, want %v", got, tt.want)
			}
		})
	}
}
