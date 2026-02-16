package kraken

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ParseTagInfo parses tag name to extract domain, app name, and version.
// Tag format: domain-app.vX.Y.Z
func ParseTagInfo(tagName string) (domain, appName, version string, err error) {
	if !strings.Contains(tagName, ".v") || !strings.Contains(tagName, "-") {
		return "", "", "", fmt.Errorf("invalid tag format: %s. Expected format: domain-app.vX.Y.Z", tagName)
	}

	parts := strings.SplitN(tagName, ".v", 2)
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("invalid tag format: %s. Expected format: domain-app.vX.Y.Z", tagName)
	}

	domainApp := parts[0]
	ver := parts[1]

	if !strings.Contains(domainApp, "-") {
		return "", "", "", fmt.Errorf("invalid tag format: %s. Expected format: domain-app.vX.Y.Z", tagName)
	}

	// Find the last dash to separate domain from app
	lastDash := strings.LastIndex(domainApp, "-")
	domain = domainApp[:lastDash]
	appName = domainApp[lastDash+1:]

	return domain, appName, "v" + ver, nil
}

// ValidateTagFormat validates that a tag follows the expected format.
func ValidateTagFormat(tagName string) bool {
	_, _, _, err := ParseTagInfo(tagName)
	return err == nil
}

// ReleaseNote represents a single release note entry.
type ReleaseNote struct {
	CommitSHA     string   `json:"sha"`
	CommitMessage string   `json:"message"`
	Author        string   `json:"author"`
	Date          string   `json:"date"`
	FilesChanged  []string `json:"files_changed"`
}

// AppReleaseData represents release data for a specific app.
type AppReleaseData struct {
	AppName     string
	CurrentTag  string
	PreviousTag string
	ReleasedAt  string
	Commits     []ReleaseNote
}

// CommitCount returns the number of commits in this release.
func (d *AppReleaseData) CommitCount() int {
	return len(d.Commits)
}

// HasChanges returns true if there are changes in this release.
func (d *AppReleaseData) HasChanges() bool {
	return len(d.Commits) > 0
}

// Summary returns a summary of the changes.
func (d *AppReleaseData) Summary() string {
	if !d.HasChanges() {
		return fmt.Sprintf("No changes affecting %s found", d.AppName)
	}
	return fmt.Sprintf("%d commits affecting %s", d.CommitCount(), d.AppName)
}

// FormatMarkdown formats release data as Markdown.
func FormatMarkdown(data *AppReleaseData) string {
	var lines []string
	lines = append(lines,
		fmt.Sprintf("**Released:** %s", data.ReleasedAt),
		fmt.Sprintf("**Previous Version:** %s", data.PreviousTag),
		fmt.Sprintf("**Commits:** %d", data.CommitCount()),
		"",
		"## Changes",
		"",
	)

	if !data.HasChanges() {
		lines = append(lines, fmt.Sprintf("No changes affecting %s found between %s and %s.", data.AppName, data.PreviousTag, data.CurrentTag))
	} else {
		for _, commit := range data.Commits {
			lines = append(lines, fmt.Sprintf("### [%s] %s", commit.CommitSHA, commit.CommitMessage))
			lines = append(lines, fmt.Sprintf("**Author:** %s", commit.Author))
			lines = append(lines, fmt.Sprintf("**Date:** %s", commit.Date))
			if len(commit.FilesChanged) > 0 {
				display := commit.FilesChanged
				if len(display) > 5 {
					display = display[:5]
				}
				lines = append(lines, fmt.Sprintf("**Files:** %s", strings.Join(display, ", ")))
				if len(commit.FilesChanged) > 5 {
					lines = append(lines, fmt.Sprintf("*... and %d more files*", len(commit.FilesChanged)-5))
				}
			}
			lines = append(lines, "")
		}
	}

	lines = append(lines, "---", "*Generated automatically by the release helper*")
	return strings.Join(lines, "\n")
}

// FormatPlainText formats release data as plain text.
func FormatPlainText(data *AppReleaseData) string {
	var title string
	domain, appName, version, err := ParseTagInfo(data.CurrentTag)
	if err == nil {
		title = fmt.Sprintf("%s %s %s", domain, appName, version)
	} else {
		title = fmt.Sprintf("Release Notes: %s %s", data.AppName, data.CurrentTag)
	}

	var lines []string
	lines = append(lines,
		title,
		fmt.Sprintf("Released: %s", data.ReleasedAt),
		fmt.Sprintf("Previous Version: %s", data.PreviousTag),
		fmt.Sprintf("Commits: %d", data.CommitCount()),
		"",
		"Changes:",
	)

	if !data.HasChanges() {
		lines = append(lines, fmt.Sprintf("No changes affecting %s found between %s and %s.", data.AppName, data.PreviousTag, data.CurrentTag))
	} else {
		for i, commit := range data.Commits {
			lines = append(lines, fmt.Sprintf("%d. [%s] %s", i+1, commit.CommitSHA, commit.CommitMessage))
			lines = append(lines, fmt.Sprintf("   Author: %s", commit.Author))
			lines = append(lines, fmt.Sprintf("   Date: %s", commit.Date))
			lines = append(lines, "")
		}
	}

	return strings.Join(lines, "\n")
}

// FormatJSON formats release data as JSON.
func FormatJSON(data *AppReleaseData) (string, error) {
	output := map[string]interface{}{
		"app":              data.AppName,
		"version":          data.CurrentTag,
		"previous_version": data.PreviousTag,
		"released_at":      data.ReleasedAt,
		"commit_count":     data.CommitCount(),
		"changes":          data.Commits,
		"summary":          data.Summary(),
	}

	b, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// GetCommitsBetweenRefs gets commit information between two Git references.
func GetCommitsBetweenRefs(startRef, endRef string) ([]ReleaseNote, error) {
	if endRef == "" {
		endRef = "HEAD"
	}

	// Check if start_ref exists
	checkCmd := exec.Command("git", "rev-parse", "--verify", startRef)
	if err := checkCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Reference %s not found, using limited history\n", startRef)
		cmd := exec.Command("git", "log", "-n", "5", "--pretty=format:%H|%s|%an|%ai", "--no-merges")
		out, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		return parseCommitLog(string(out))
	}

	cmd := exec.Command("git", "log", fmt.Sprintf("%s..%s", startRef, endRef), "--pretty=format:%H|%s|%an|%ai", "--no-merges")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting commits between %s and %s: %v\n", startRef, endRef, err)
		return nil, nil
	}

	return parseCommitLog(string(out))
}

func parseCommitLog(output string) ([]ReleaseNote, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, nil
	}

	var notes []ReleaseNote
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "|") {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}

		sha := parts[0]
		message := strings.TrimSpace(parts[1])
		author := strings.TrimSpace(parts[2])
		date := strings.TrimSpace(parts[3])

		// Get files changed
		filesCmd := exec.Command("git", "diff-tree", "--no-commit-id", "--name-only", "-r", sha)
		filesOut, err := filesCmd.Output()
		var filesChanged []string
		if err == nil {
			for _, f := range strings.Split(strings.TrimSpace(string(filesOut)), "\n") {
				f = strings.TrimSpace(f)
				if f != "" {
					filesChanged = append(filesChanged, f)
				}
			}
		}

		notes = append(notes, ReleaseNote{
			CommitSHA:     sha[:8],
			CommitMessage: message,
			Author:        author,
			Date:          date,
			FilesChanged:  filesChanged,
		})
	}

	return notes, nil
}

// FilterCommitsByApp filters commits to only those that affect the specified app.
func FilterCommitsByApp(commits []ReleaseNote, appName string) []ReleaseNote {
	validatedApps, err := ValidateApps([]string{appName})
	if err != nil || len(validatedApps) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: App %s not found in metadata\n", appName)
		return nil
	}

	appInfo := validatedApps[0]
	appPath := strings.SplitN(appInfo.BazelTarget[2:], ":", 2)[0]

	infraPrefixes := []string{"tools", ".github", "docker", "MODULE.bazel", "WORKSPACE", "BUILD.bazel"}

	var appCommits []ReleaseNote
	for _, commit := range commits {
		affected := false
		for _, filePath := range commit.FilesChanged {
			if strings.HasPrefix(filePath, appPath+"/") || filePath == appPath {
				affected = true
				break
			}
			for _, infra := range infraPrefixes {
				if strings.HasPrefix(filePath, infra+"/") || filePath == infra {
					affected = true
					break
				}
			}
			if affected {
				break
			}
		}
		if affected {
			appCommits = append(appCommits, commit)
		}
	}

	return appCommits
}

// GenerateReleaseNotes generates release notes for an app between two tags.
func GenerateReleaseNotes(appName, currentTag, previousTag, formatType string) (string, error) {
	if previousTag == "" {
		prev, err := GetPreviousTag()
		if err != nil || prev == "" {
			previousTag = "HEAD~10"
			fmt.Fprintf(os.Stderr, "Warning: No previous tag found, comparing against %s\n", previousTag)
		} else {
			previousTag = prev
		}
	}

	fmt.Fprintf(os.Stderr, "Generating release notes for %s from %s to %s\n", appName, previousTag, currentTag)

	allCommits, err := GetCommitsBetweenRefs(previousTag, currentTag)
	if err != nil {
		return "", err
	}

	appCommits := FilterCommitsByApp(allCommits, appName)

	data := &AppReleaseData{
		AppName:     appName,
		CurrentTag:  currentTag,
		PreviousTag: previousTag,
		ReleasedAt:  time.Now().Format("2006-01-02 15:04:05 UTC"),
		Commits:     appCommits,
	}

	switch formatType {
	case "markdown":
		return FormatMarkdown(data), nil
	case "plain":
		return FormatPlainText(data), nil
	case "json":
		return FormatJSON(data)
	default:
		return "", fmt.Errorf("unsupported format type: %s", formatType)
	}
}

// GenerateReleaseNotesForAllApps generates release notes for all apps.
func GenerateReleaseNotesForAllApps(currentTag, previousTag, formatType string) (map[string]string, error) {
	allApps, err := ListAllApps()
	if err != nil {
		return nil, err
	}

	releaseNotes := make(map[string]string)
	for _, app := range allApps {
		fullName := fmt.Sprintf("%s-%s", app.Domain, app.Name)
		notes, err := GenerateReleaseNotes(fullName, currentTag, previousTag, formatType)
		if err != nil {
			return nil, err
		}
		releaseNotes[fullName] = notes
	}

	return releaseNotes, nil
}
