// Package git provides git operations for the release helper.
package git

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/whale-net/everything/tools/release/pkg/validation"
)

// FormatGitTag formats a Git tag in the domain-appname.version format.
func FormatGitTag(domain, appName, version string) string {
	return fmt.Sprintf("%s-%s.%s", domain, appName, version)
}

// ParseVersionFromTag parses version from an app tag.
// Returns version string (e.g., "v1.2.3") or error if not a valid app tag.
func ParseVersionFromTag(tag, domain, appName string) (string, error) {
	expectedPrefix := fmt.Sprintf("%s-%s.", domain, appName)
	if !strings.HasPrefix(tag, expectedPrefix) {
		return "", fmt.Errorf("tag %s doesn't match expected prefix %s", tag, expectedPrefix)
	}

	version := tag[len(expectedPrefix):]
	
	// Validate that it looks like a semantic version
	if err := validation.ValidateSemanticVersion(version); err != nil {
		return "", fmt.Errorf("invalid version in tag: %w", err)
	}

	return version, nil
}

// GetLatestAppVersion returns the latest version for an app from git tags.
func GetLatestAppVersion(domain, appName string) (string, error) {
	tags, err := GetAllTags()
	if err != nil {
		return "", err
	}

	prefix := fmt.Sprintf("%s-%s.", domain, appName)
	var latestVersion string
	var latestVersionParsed *validation.SemanticVersion

	for _, tag := range tags {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}

		version, err := ParseVersionFromTag(tag, domain, appName)
		if err != nil {
			continue
		}

		versionParsed, err := validation.ParseSemanticVersion(version)
		if err != nil {
			continue
		}

		if latestVersionParsed == nil {
			latestVersion = version
			latestVersionParsed = versionParsed
			continue
		}

		// Compare versions
		cmp, err := validation.CompareVersions(version, latestVersion)
		if err != nil {
			continue
		}
		if cmp > 0 {
			latestVersion = version
			latestVersionParsed = versionParsed
		}
	}

	if latestVersion == "" {
		return "", fmt.Errorf("no versions found for %s-%s", domain, appName)
	}

	return latestVersion, nil
}

// AutoIncrementVersion calculates the next version for an app based on increment type.
func AutoIncrementVersion(domain, appName, incrementType string) (string, error) {
	if incrementType != "minor" && incrementType != "patch" {
		return "", fmt.Errorf("increment type must be 'minor' or 'patch', got %s", incrementType)
	}

	currentVersion, err := GetLatestAppVersion(domain, appName)
	if err != nil {
		// No existing version, start with v0.1.0 or v0.0.1
		if incrementType == "minor" {
			return "v0.1.0", nil
		}
		return "v0.0.1", nil
	}

	version, err := validation.ParseSemanticVersion(currentVersion)
	if err != nil {
		return "", fmt.Errorf("failed to parse current version: %w", err)
	}

	var newVersion *validation.SemanticVersion
	if incrementType == "minor" {
		newVersion = version.IncrementMinor()
	} else {
		newVersion = version.IncrementPatch()
	}

	return newVersion.String(), nil
}

// CreateGitTag creates a Git tag on the specified commit.
func CreateGitTag(tagName string, commitSHA string, message string) error {
	args := []string{"tag"}

	if message != "" {
		args = append(args, "-a", tagName, "-m", message)
	} else {
		args = append(args, tagName)
	}

	if commitSHA != "" {
		args = append(args, commitSHA)
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create git tag: %w\noutput: %s", err, string(output))
	}

	fmt.Printf("Created Git tag: %s\n", tagName)
	return nil
}

// PushGitTag pushes a Git tag to the remote repository.
func PushGitTag(tagName string) error {
	cmd := exec.Command("git", "push", "origin", tagName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push git tag: %w\noutput: %s", err, string(output))
	}

	fmt.Printf("Pushed Git tag: %s\n", tagName)
	return nil
}

// GetPreviousTag returns the previous Git tag.
func GetPreviousTag() (string, error) {
	cmd := exec.Command("git", "describe", "--tags", "--abbrev=0", "HEAD^")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get previous tag: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// GetAllTags returns all Git tags sorted by version (newest first).
func GetAllTags() ([]string, error) {
	cmd := exec.Command("git", "tag", "--sort=-version:refname")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get git tags: %w", err)
	}

	if len(output) == 0 {
		return []string{}, nil
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	tags := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			tags = append(tags, trimmed)
		}
	}

	return tags, nil
}

// GetChangedFiles returns the list of files changed between two commits.
func GetChangedFiles(baseCommit, headCommit string) ([]string, error) {
	if baseCommit == "" {
		baseCommit = "HEAD^"
	}
	if headCommit == "" {
		headCommit = "HEAD"
	}

	cmd := exec.Command("git", "diff", "--name-only", baseCommit, headCommit)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w\noutput: %s", err, string(output))
	}

	if len(output) == 0 {
		return []string{}, nil
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			files = append(files, trimmed)
		}
	}

	return files, nil
}
