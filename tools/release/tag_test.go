package release

import "testing"

func TestFormatGitTag(t *testing.T) {
	tests := []struct {
		domain  string
		app     string
		version string
		want    string
	}{
		{"demo", "hello_python", "v1.2.3", "demo-hello_python.v1.2.3"},
		{"manman", "host", "v0.1.0", "manman-host.v0.1.0"},
		{"api", "status_service", "v2.0.0-rc1", "api-status_service.v2.0.0-rc1"},
	}
	for _, tt := range tests {
		got := FormatGitTag(tt.domain, tt.app, tt.version)
		if got != tt.want {
			t.Errorf("FormatGitTag(%q,%q,%q) = %q, want %q", tt.domain, tt.app, tt.version, got, tt.want)
		}
	}
}

func TestFormatHelmChartTag(t *testing.T) {
	tests := []struct {
		chart   string
		version string
		want    string
	}{
		{"helm-demo-hello-fastapi", "v1.0.0", "helm-demo-hello-fastapi.v1.0.0"},
		{"helm-manman-manman-host", "v2.3.1", "helm-manman-manman-host.v2.3.1"},
	}
	for _, tt := range tests {
		got := FormatHelmChartTag(tt.chart, tt.version)
		if got != tt.want {
			t.Errorf("FormatHelmChartTag(%q,%q) = %q, want %q", tt.chart, tt.version, got, tt.want)
		}
	}
}

func TestParseVersionFromTag(t *testing.T) {
	tests := []struct {
		tag     string
		domain  string
		app     string
		want    string // empty string means "not a match"
	}{
		{"demo-hello_python.v1.2.3", "demo", "hello_python", "v1.2.3"},
		{"demo-hello_python.v1.0.0-beta1", "demo", "hello_python", "v1.0.0-beta1"},
		// Wrong domain/app
		{"api-hello_python.v1.2.3", "demo", "hello_python", ""},
		// No version at all
		{"demo-hello_python", "demo", "hello_python", ""},
		// Malformed version
		{"demo-hello_python.1.2.3", "demo", "hello_python", ""},
		{"demo-hello_python.latest", "demo", "hello_python", ""},
	}
	for _, tt := range tests {
		got := ParseVersionFromTag(tt.tag, tt.domain, tt.app)
		if got != tt.want {
			t.Errorf("ParseVersionFromTag(%q,%q,%q) = %q, want %q", tt.tag, tt.domain, tt.app, got, tt.want)
		}
	}
}

func TestParseVersionFromHelmChartTag(t *testing.T) {
	tests := []struct {
		tag       string
		chartName string
		want      string
	}{
		{"helm-demo-hello-fastapi.v1.0.0", "helm-demo-hello-fastapi", "v1.0.0"},
		{"helm-demo-hello-fastapi.v2.1.0-rc1", "helm-demo-hello-fastapi", "v2.1.0-rc1"},
		// Wrong chart
		{"helm-other-chart.v1.0.0", "helm-demo-hello-fastapi", ""},
		// Malformed version
		{"helm-demo-hello-fastapi.1.0.0", "helm-demo-hello-fastapi", ""},
	}
	for _, tt := range tests {
		got := ParseVersionFromHelmChartTag(tt.tag, tt.chartName)
		if got != tt.want {
			t.Errorf("ParseVersionFromHelmChartTag(%q,%q) = %q, want %q", tt.tag, tt.chartName, got, tt.want)
		}
	}
}
