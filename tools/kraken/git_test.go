package kraken

import (
	"testing"
)

func TestFormatGitTagBasic(t *testing.T) {
	result := FormatGitTag("api", "user-service", "1.0.0")
	if result != "api-user-service.1.0.0" {
		t.Errorf("expected api-user-service.1.0.0, got %s", result)
	}
}

func TestFormatGitTagWithHyphens(t *testing.T) {
	result := FormatGitTag("data-processing", "ml-service", "2.1.0-beta")
	if result != "data-processing-ml-service.2.1.0-beta" {
		t.Errorf("expected data-processing-ml-service.2.1.0-beta, got %s", result)
	}
}

func TestFormatGitTagEmptyStrings(t *testing.T) {
	result := FormatGitTag("", "", "")
	if result != "-." {
		t.Errorf("expected -., got %s", result)
	}
}

func TestFormatHelmChartTagWithHelmPrefix(t *testing.T) {
	result := FormatHelmChartTag("helm-demo-hello-fastapi", "v1.0.0")
	if result != "helm-demo-hello-fastapi.v1.0.0" {
		t.Errorf("expected helm-demo-hello-fastapi.v1.0.0, got %s", result)
	}
}

func TestFormatHelmChartTagWithoutHelmPrefix(t *testing.T) {
	result := FormatHelmChartTag("demo-hello-fastapi", "v1.0.0")
	if result != "demo-hello-fastapi.v1.0.0" {
		t.Errorf("expected demo-hello-fastapi.v1.0.0, got %s", result)
	}
}

func TestFormatHelmChartTagWithMultipleHyphens(t *testing.T) {
	result := FormatHelmChartTag("helm-manman-host-services", "v2.1.3")
	if result != "helm-manman-host-services.v2.1.3" {
		t.Errorf("expected helm-manman-host-services.v2.1.3, got %s", result)
	}
}

func TestParseVersionFromTagSuccess(t *testing.T) {
	result := ParseVersionFromTag("demo-hello_python.v1.2.3", "demo", "hello_python")
	if result != "v1.2.3" {
		t.Errorf("expected v1.2.3, got %s", result)
	}
}

func TestParseVersionFromTagWrongApp(t *testing.T) {
	result := ParseVersionFromTag("demo-hello_python.v1.2.3", "demo", "other_app")
	if result != "" {
		t.Errorf("expected empty string, got %s", result)
	}
}

func TestParseVersionFromTagInvalidVersion(t *testing.T) {
	result := ParseVersionFromTag("demo-hello_python.invalid", "demo", "hello_python")
	if result != "" {
		t.Errorf("expected empty string, got %s", result)
	}
}

func TestParseVersionFromHelmChartTagSuccess(t *testing.T) {
	result := ParseVersionFromHelmChartTag("helm-demo-hello-fastapi.v1.2.3", "helm-demo-hello-fastapi")
	if result != "v1.2.3" {
		t.Errorf("expected v1.2.3, got %s", result)
	}
}

func TestParseVersionFromHelmChartTagWithPrerelease(t *testing.T) {
	result := ParseVersionFromHelmChartTag("helm-demo-hello-fastapi.v1.2.3-beta.1", "helm-demo-hello-fastapi")
	if result != "v1.2.3-beta.1" {
		t.Errorf("expected v1.2.3-beta.1, got %s", result)
	}
}

func TestParseVersionFromHelmChartTagWrongChart(t *testing.T) {
	result := ParseVersionFromHelmChartTag("helm-demo-hello-fastapi.v1.2.3", "helm-demo-other-app")
	if result != "" {
		t.Errorf("expected empty string, got %s", result)
	}
}

func TestParseVersionFromHelmChartTagInvalidVersion(t *testing.T) {
	result := ParseVersionFromHelmChartTag("helm-demo-hello-fastapi.invalid", "helm-demo-hello-fastapi")
	if result != "" {
		t.Errorf("expected empty string, got %s", result)
	}
}

func TestParseSemanticVersionBasic(t *testing.T) {
	sv, err := ParseSemanticVersion("v1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sv.Major != 1 || sv.Minor != 2 || sv.Patch != 3 {
		t.Errorf("expected 1.2.3, got %d.%d.%d", sv.Major, sv.Minor, sv.Patch)
	}
	if sv.Prerelease != "" {
		t.Errorf("expected empty prerelease, got %s", sv.Prerelease)
	}
}

func TestParseSemanticVersionWithPrerelease(t *testing.T) {
	sv, err := ParseSemanticVersion("v1.0.0-beta1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sv.Major != 1 || sv.Minor != 0 || sv.Patch != 0 {
		t.Errorf("expected 1.0.0, got %d.%d.%d", sv.Major, sv.Minor, sv.Patch)
	}
	if sv.Prerelease != "beta1" {
		t.Errorf("expected beta1, got %s", sv.Prerelease)
	}
}

func TestParseSemanticVersionWithoutPrefix(t *testing.T) {
	sv, err := ParseSemanticVersion("2.3.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sv.Major != 2 || sv.Minor != 3 || sv.Patch != 4 {
		t.Errorf("expected 2.3.4, got %d.%d.%d", sv.Major, sv.Minor, sv.Patch)
	}
}

func TestParseSemanticVersionInvalid(t *testing.T) {
	_, err := ParseSemanticVersion("v1.0")
	if err == nil {
		t.Error("expected error for invalid version")
	}
}

func TestIncrementMinorVersion(t *testing.T) {
	result, err := IncrementMinorVersion("v1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "v1.3.0" {
		t.Errorf("expected v1.3.0, got %s", result)
	}
}

func TestIncrementPatchVersion(t *testing.T) {
	result, err := IncrementPatchVersion("v1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "v1.2.4" {
		t.Errorf("expected v1.2.4, got %s", result)
	}
}
