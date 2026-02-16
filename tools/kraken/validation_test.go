package kraken

import (
	"testing"
)

func TestValidateSemanticVersionValid(t *testing.T) {
	validVersions := []string{
		"v1.0.0",
		"v0.1.0",
		"v10.20.30",
		"v1.0.0-alpha",
		"v1.0.0-beta1",
	}

	for _, v := range validVersions {
		if !ValidateSemanticVersion(v) {
			t.Errorf("expected %s to be valid", v)
		}
	}
}

func TestValidateSemanticVersionInvalid(t *testing.T) {
	invalidVersions := []string{
		"1.0.0",  // Missing 'v' prefix
		"v1.0",   // Missing patch version
		"v1",     // Missing minor and patch
		"",       // Empty string
		"latest", // Not semantic version
	}

	for _, v := range invalidVersions {
		if ValidateSemanticVersion(v) {
			t.Errorf("expected %s to be invalid", v)
		}
	}
}

func TestGetAppFullName(t *testing.T) {
	app := AppInfo{Domain: "demo", Name: "hello_python"}
	result := GetAppFullName(app)
	if result != "demo-hello_python" {
		t.Errorf("expected demo-hello_python, got %s", result)
	}
}
