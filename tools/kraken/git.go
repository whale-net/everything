package kraken

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// FormatGitTag formats a Git tag in the domain-appname.version format.
func FormatGitTag(domain, appName, version string) string {
	return fmt.Sprintf("%s-%s.%s", domain, appName, version)
}

// FormatHelmChartTag formats a Git tag for a Helm chart.
// Chart names already include the helm- prefix and namespace.
func FormatHelmChartTag(chartName, version string) string {
	return fmt.Sprintf("%s.%s", chartName, version)
}

// CheckTagExists checks if a Git tag exists.
func CheckTagExists(tagName string) bool {
	cmd := exec.Command("git", "tag", "-l", tagName)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// CreateGitTag creates a Git tag on the specified commit.
// If force is true, it overwrites an existing tag.
func CreateGitTag(tagName string, commitSHA string, message string, force bool) error {
	if CheckTagExists(tagName) {
		if force {
			fmt.Printf("Tag %s already exists, forcing overwrite...\n", tagName)
		} else {
			fmt.Printf("Tag %s already exists, skipping creation\n", tagName)
			return nil
		}
	}

	args := []string{"tag"}
	if force {
		args = append(args, "-f")
	}
	if message != "" {
		args = append(args, "-a", tagName, "-m", message)
	} else {
		args = append(args, tagName)
	}
	if commitSHA != "" {
		args = append(args, commitSHA)
	}

	fmt.Printf("Creating Git tag: %s\n", tagName)
	cmd := exec.Command("git", args...)
	return cmd.Run()
}

// PushGitTag pushes a Git tag to the remote repository.
func PushGitTag(tagName string, force bool) error {
	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "origin", tagName)

	if force {
		fmt.Printf("Pushing Git tag: %s (force)\n", tagName)
	} else {
		fmt.Printf("Pushing Git tag: %s\n", tagName)
	}
	cmd := exec.Command("git", args...)
	return cmd.Run()
}

// GetPreviousTag gets the previous Git tag.
func GetPreviousTag() (string, error) {
	cmd := exec.Command("git", "describe", "--tags", "--abbrev=0", "HEAD^")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GetAllTags gets all Git tags sorted by version (newest first).
func GetAllTags() ([]string, error) {
	cmd := exec.Command("git", "tag", "--sort=-version:refname")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	var tags []string
	for _, t := range strings.Split(raw, "\n") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

// GetAppTags gets all tags for a specific app, sorted by version (newest first).
func GetAppTags(domain, appName string) ([]string, error) {
	allTags, err := GetAllTags()
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("%s-%s.", domain, appName)
	var appTags []string
	for _, tag := range allTags {
		if strings.HasPrefix(tag, prefix) {
			appTags = append(appTags, tag)
		}
	}
	return appTags, nil
}

// GetHelmChartTags gets all tags for a specific helm chart.
func GetHelmChartTags(chartName string) ([]string, error) {
	allTags, err := GetAllTags()
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("%s.", chartName)
	var chartTags []string
	for _, tag := range allTags {
		if strings.HasPrefix(tag, prefix) {
			chartTags = append(chartTags, tag)
		}
	}
	return chartTags, nil
}

var semverRegex = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-[a-zA-Z0-9\-\.]+)?$`)

// ParseVersionFromTag parses version from an app tag.
func ParseVersionFromTag(tag, domain, appName string) string {
	prefix := fmt.Sprintf("%s-%s.", domain, appName)
	if !strings.HasPrefix(tag, prefix) {
		return ""
	}
	version := tag[len(prefix):]
	if semverRegex.MatchString(version) {
		return version
	}
	return ""
}

// ParseVersionFromHelmChartTag parses version from a helm chart tag.
func ParseVersionFromHelmChartTag(tag, chartName string) string {
	prefix := fmt.Sprintf("%s.", chartName)
	if !strings.HasPrefix(tag, prefix) {
		return ""
	}
	version := tag[len(prefix):]
	if semverRegex.MatchString(version) {
		return version
	}
	return ""
}

// SemanticVersion represents a parsed semantic version.
type SemanticVersion struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string
}

// ParseSemanticVersion parses a semantic version string into components.
func ParseSemanticVersion(version string) (*SemanticVersion, error) {
	v := version
	if strings.HasPrefix(v, "v") {
		v = v[1:]
	}

	parts := strings.SplitN(v, "-", 2)
	versionPart := parts[0]
	prerelease := ""
	if len(parts) > 1 {
		prerelease = parts[1]
	}

	components := strings.Split(versionPart, ".")
	if len(components) != 3 {
		return nil, fmt.Errorf("invalid semantic version format: %s", version)
	}

	major, err := strconv.Atoi(components[0])
	if err != nil {
		return nil, fmt.Errorf("invalid semantic version format: %s", version)
	}
	minor, err := strconv.Atoi(components[1])
	if err != nil {
		return nil, fmt.Errorf("invalid semantic version format: %s", version)
	}
	patch, err := strconv.Atoi(components[2])
	if err != nil {
		return nil, fmt.Errorf("invalid semantic version format: %s", version)
	}

	return &SemanticVersion{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		Prerelease: prerelease,
	}, nil
}

// IncrementMinorVersion increments the minor version and resets patch to 0.
func IncrementMinorVersion(currentVersion string) (string, error) {
	sv, err := ParseSemanticVersion(currentVersion)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v%d.%d.0", sv.Major, sv.Minor+1), nil
}

// IncrementPatchVersion increments the patch version.
func IncrementPatchVersion(currentVersion string) (string, error) {
	sv, err := ParseSemanticVersion(currentVersion)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v%d.%d.%d", sv.Major, sv.Minor, sv.Patch+1), nil
}

// GetLatestAppVersion gets the latest version for a specific app.
func GetLatestAppVersion(domain, appName string) (string, error) {
	appTags, err := GetAppTags(domain, appName)
	if err != nil {
		return "", err
	}
	for _, tag := range appTags {
		version := ParseVersionFromTag(tag, domain, appName)
		if version != "" {
			return version, nil
		}
	}
	return "", nil
}

// GetLatestHelmChartVersion gets the latest version for a specific helm chart.
func GetLatestHelmChartVersion(chartName string) (string, error) {
	chartTags, err := GetHelmChartTags(chartName)
	if err != nil {
		return "", err
	}
	for _, tag := range chartTags {
		version := ParseVersionFromHelmChartTag(tag, chartName)
		if version != "" {
			return version, nil
		}
	}
	return "", nil
}

// AutoIncrementVersion auto-increments version for an app based on the latest tag.
func AutoIncrementVersion(domain, appName, incrementType string) (string, error) {
	if incrementType != "minor" && incrementType != "patch" {
		return "", fmt.Errorf("invalid increment type: %s. Must be 'minor' or 'patch'", incrementType)
	}

	latestVersion, err := GetLatestAppVersion(domain, appName)
	if err != nil {
		return "", err
	}

	if latestVersion == "" {
		if incrementType == "minor" {
			return "v0.1.0", nil
		}
		return "v0.0.1", nil
	}

	if incrementType == "minor" {
		return IncrementMinorVersion(latestVersion)
	}
	return IncrementPatchVersion(latestVersion)
}

// AutoIncrementHelmChartVersion auto-increments version for a helm chart.
func AutoIncrementHelmChartVersion(chartName, incrementType string) (string, error) {
	if incrementType != "minor" && incrementType != "patch" {
		return "", fmt.Errorf("invalid increment type: %s. Must be 'minor' or 'patch'", incrementType)
	}

	latestVersion, err := GetLatestHelmChartVersion(chartName)
	if err != nil {
		return "", err
	}

	if latestVersion == "" {
		if incrementType == "minor" {
			return "v0.1.0", nil
		}
		return "v0.0.1", nil
	}

	if incrementType == "minor" {
		return IncrementMinorVersion(latestVersion)
	}
	return IncrementPatchVersion(latestVersion)
}
