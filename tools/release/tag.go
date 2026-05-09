package release

import (
	"regexp"
	"strings"
)

// versionPattern matches a full semantic version string (e.g. "v1.2.3" or "v1.2.3-beta1").
var versionPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[a-zA-Z0-9\-\.]+)?$`)

// FormatGitTag returns the canonical Git tag for an app release.
// Format: "{domain}-{appName}.{version}" (e.g. "demo-hello_python.v1.2.3").
func FormatGitTag(domain, appName, version string) string {
	return domain + "-" + appName + "." + version
}

// FormatHelmChartTag returns the canonical Git tag for a Helm chart release.
// The chartName already includes the "helm-" prefix and namespace
// (e.g. "helm-demo-hello-fastapi"), so no additional prefix is added.
// Format: "{chartName}.{version}" (e.g. "helm-demo-hello-fastapi.v1.0.0").
func FormatHelmChartTag(chartName, version string) string {
	return chartName + "." + version
}

// ParseVersionFromTag extracts the semantic version from an app Git tag.
// Returns the version string (e.g. "v1.2.3") or an empty string if the tag
// does not belong to the given domain/appName or does not contain a valid version.
func ParseVersionFromTag(tag, domain, appName string) string {
	prefix := domain + "-" + appName + "."
	if !strings.HasPrefix(tag, prefix) {
		return ""
	}
	version := tag[len(prefix):]
	if versionPattern.MatchString(version) {
		return version
	}
	return ""
}

// ParseVersionFromHelmChartTag extracts the semantic version from a Helm chart Git tag.
// chartName already includes the "helm-{domain}-" prefix (e.g. "helm-demo-hello-fastapi").
// Returns the version string or an empty string if the tag is not a valid chart tag.
func ParseVersionFromHelmChartTag(tag, chartName string) string {
	prefix := chartName + "."
	if !strings.HasPrefix(tag, prefix) {
		return ""
	}
	version := tag[len(prefix):]
	if versionPattern.MatchString(version) {
		return version
	}
	return ""
}
