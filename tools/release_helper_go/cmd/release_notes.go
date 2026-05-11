package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var validNotesFormats = []string{"markdown", "plain", "json"}

type releaseCommit struct {
	SHA          string   `json:"sha"`
	Message      string   `json:"message"`
	Author       string   `json:"author"`
	Date         string   `json:"date"`
	FilesChanged []string `json:"files_changed"`
}

type appReleaseData struct {
	AppName     string
	CurrentTag  string
	PreviousTag string
	ReleasedAt  string
	Commits     []releaseCommit
}

func newReleaseNotesCmd() *cobra.Command {
	var (
		currentTag  string
		previousTag string
		formatType  string
	)

	cmd := &cobra.Command{
		Use:          "release-notes <app-name>",
		Short:        "Generate release notes for a specific app",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isValidNotesFormat(formatType) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: markdown, plain, json\n")
				return fmt.Errorf("invalid format")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			resolved, err := resolveApps([]string{args[0]}, allApps)
			if err != nil {
				return err
			}
			app := resolved[0]

			prevTag := previousTag
			if prevTag == "" {
				pt, err := getPreviousTag(defaultGit)
				if err != nil {
					prevTag = "HEAD~10"
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: no previous tag found, comparing against %s\n", prevTag)
				} else {
					prevTag = pt
				}
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Generating release notes for %s from %s to %s\n", app.FullName(), prevTag, currentTag)

			notes, err := generateReleaseNotes(app, currentTag, prevTag, formatType, defaultGit)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), notes)
			return nil
		},
	}

	cmd.Flags().StringVar(&currentTag, "current-tag", "HEAD", "Current tag/version")
	cmd.Flags().StringVar(&previousTag, "previous-tag", "", "Previous tag to compare against")
	cmd.Flags().StringVar(&formatType, "format", "markdown", "Output format (markdown, plain, json)")

	return cmd
}

func newReleaseNotesAllCmd() *cobra.Command {
	var (
		currentTag  string
		previousTag string
		formatType  string
		outputDir   string
	)

	cmd := &cobra.Command{
		Use:          "release-notes-all",
		Short:        "Generate release notes for all apps",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isValidNotesFormat(formatType) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: markdown, plain, json\n")
				return fmt.Errorf("invalid format")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			prevTag := previousTag
			if prevTag == "" {
				pt, err := getPreviousTag(defaultGit)
				if err != nil {
					prevTag = "HEAD~10"
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: no previous tag found, comparing against %s\n", prevTag)
				} else {
					prevTag = pt
				}
			}

			_ = outputDir // TODO: write per-app files to outputDir when set

			result := map[string]string{}
			for _, app := range allApps {
				notes, err := generateReleaseNotes(app, currentTag, prevTag, formatType, defaultGit)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not generate notes for %s: %v\n", app.FullName(), err)
					continue
				}
				result[app.FullName()] = notes
			}

			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&currentTag, "current-tag", "HEAD", "Current tag/version")
	cmd.Flags().StringVar(&previousTag, "previous-tag", "", "Previous tag to compare against")
	cmd.Flags().StringVar(&formatType, "format", "markdown", "Output format (markdown, plain, json)")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Directory to save release notes files")

	return cmd
}

func isValidNotesFormat(f string) bool {
	for _, v := range validNotesFormats {
		if f == v {
			return true
		}
	}
	return false
}

func generateReleaseNotes(app AppMetadata, currentTag, previousTag, format string, git GitRunner) (string, error) {
	commits, err := getCommitsBetweenRefs(previousTag, currentTag, git)
	if err != nil {
		return "", err
	}

	// Derive app package path from BazelTarget e.g. //demo/hello_go:foo -> demo/hello_go
	appPath := ""
	if app.BazelTarget != "" {
		stripped := strings.TrimPrefix(app.BazelTarget, "//")
		if idx := strings.Index(stripped, ":"); idx >= 0 {
			appPath = stripped[:idx]
		}
	}

	filtered := filterCommitsByApp(commits, appPath)

	data := appReleaseData{
		AppName:     app.FullName(),
		CurrentTag:  currentTag,
		PreviousTag: previousTag,
		ReleasedAt:  time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		Commits:     filtered,
	}

	switch format {
	case "markdown":
		return formatMarkdown(data), nil
	case "plain":
		return formatPlain(data), nil
	case "json":
		return formatJSON(data)
	}
	return "", fmt.Errorf("unsupported format: %s", format)
}

func getCommitsBetweenRefs(startRef, endRef string, git GitRunner) ([]releaseCommit, error) {
	// Verify start ref exists
	if _, err := git.Run("rev-parse", "--verify", startRef); err != nil {
		// Fallback: last 5 commits
		out, err2 := git.Run("log", "-n", "5", "--pretty=format:%H|%s|%an|%ai", "--no-merges")
		if err2 != nil {
			return nil, fmt.Errorf("git log: %w", err2)
		}
		return parseCommitLog(out, git), nil
	}

	out, err := git.Run("log", startRef+".."+endRef, "--pretty=format:%H|%s|%an|%ai", "--no-merges")
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	return parseCommitLog(out, git), nil
}

func parseCommitLog(out string, git GitRunner) []releaseCommit {
	var commits []releaseCommit
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "|") {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		sha, msg, author, date := parts[0], parts[1], parts[2], parts[3]

		var files []string
		if filesOut, err := git.Run("diff-tree", "--no-commit-id", "--name-only", "-r", sha); err == nil {
			for _, f := range strings.Split(strings.TrimSpace(filesOut), "\n") {
				if f = strings.TrimSpace(f); f != "" {
					files = append(files, f)
				}
			}
		}

		if len(sha) > 8 {
			sha = sha[:8]
		}
		commits = append(commits, releaseCommit{
			SHA:          sha,
			Message:      strings.TrimSpace(msg),
			Author:       strings.TrimSpace(author),
			Date:         strings.TrimSpace(date),
			FilesChanged: files,
		})
	}
	return commits
}

var infraPrefixes = []string{"tools", ".github", "docker", "MODULE.bazel", "WORKSPACE", "BUILD.bazel"}

func filterCommitsByApp(commits []releaseCommit, appPath string) []releaseCommit {
	if appPath == "" {
		return commits
	}
	var result []releaseCommit
	for _, c := range commits {
		if commitAffectsApp(c, appPath) {
			result = append(result, c)
		}
	}
	return result
}

func commitAffectsApp(c releaseCommit, appPath string) bool {
	for _, f := range c.FilesChanged {
		if strings.HasPrefix(f, appPath+"/") || f == appPath {
			return true
		}
		for _, prefix := range infraPrefixes {
			if strings.HasPrefix(f, prefix+"/") || f == prefix {
				return true
			}
		}
	}
	return false
}

func formatMarkdown(d appReleaseData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Released:** %s\n", d.ReleasedAt)
	fmt.Fprintf(&b, "**Previous Version:** %s\n", d.PreviousTag)
	fmt.Fprintf(&b, "**Commits:** %d\n\n", len(d.Commits))
	fmt.Fprintln(&b, "## Changes\n")
	if len(d.Commits) == 0 {
		fmt.Fprintf(&b, "No changes affecting %s found between %s and %s.\n", d.AppName, d.PreviousTag, d.CurrentTag)
	} else {
		for _, c := range d.Commits {
			fmt.Fprintf(&b, "### [%s] %s\n", c.SHA, c.Message)
			fmt.Fprintf(&b, "**Author:** %s\n", c.Author)
			fmt.Fprintf(&b, "**Date:** %s\n", c.Date)
			if len(c.FilesChanged) > 0 {
				shown := c.FilesChanged
				if len(shown) > 5 {
					shown = shown[:5]
				}
				fmt.Fprintf(&b, "**Files:** %s\n", strings.Join(shown, ", "))
				if len(c.FilesChanged) > 5 {
					fmt.Fprintf(&b, "*... and %d more files*\n", len(c.FilesChanged)-5)
				}
			}
			fmt.Fprintln(&b)
		}
	}
	fmt.Fprintln(&b, "---")
	fmt.Fprint(&b, "*Generated automatically by the release helper*")
	return b.String()
}

func formatPlain(d appReleaseData) string {
	var b strings.Builder
	// Parse tag for title
	domain, appName, version, err := parseTagInfo(d.CurrentTag)
	if err == nil {
		fmt.Fprintf(&b, "%s %s %s\n", domain, appName, version)
	} else {
		fmt.Fprintf(&b, "Release Notes: %s %s\n", d.AppName, d.CurrentTag)
	}
	fmt.Fprintf(&b, "Released: %s\n", d.ReleasedAt)
	fmt.Fprintf(&b, "Previous Version: %s\n", d.PreviousTag)
	fmt.Fprintf(&b, "Commits: %d\n\nChanges:\n", len(d.Commits))
	if len(d.Commits) == 0 {
		fmt.Fprintf(&b, "No changes affecting %s found between %s and %s.\n", d.AppName, d.PreviousTag, d.CurrentTag)
	} else {
		for i, c := range d.Commits {
			fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, c.SHA, c.Message)
			fmt.Fprintf(&b, "   Author: %s\n", c.Author)
			fmt.Fprintf(&b, "   Date: %s\n\n", c.Date)
		}
	}
	return b.String()
}

func formatJSON(d appReleaseData) (string, error) {
	commits := make([]map[string]interface{}, len(d.Commits))
	for i, c := range d.Commits {
		files := c.FilesChanged
		if files == nil {
			files = []string{}
		}
		commits[i] = map[string]interface{}{
			"sha":           c.SHA,
			"message":       c.Message,
			"author":        c.Author,
			"date":          c.Date,
			"files_changed": files,
		}
	}
	summary := fmt.Sprintf("No changes affecting %s found", d.AppName)
	if len(d.Commits) > 0 {
		summary = fmt.Sprintf("%d commits affecting %s", len(d.Commits), d.AppName)
	}
	result := map[string]interface{}{
		"app":              d.AppName,
		"version":          d.CurrentTag,
		"previous_version": d.PreviousTag,
		"released_at":      d.ReleasedAt,
		"commit_count":     len(d.Commits),
		"changes":          commits,
		"summary":          summary,
	}
	out, err := json.MarshalIndent(result, "", "  ")
	return string(out), err
}

// parseTagInfo parses "domain-app.vX.Y.Z" into (domain, app, version).
func parseTagInfo(tag string) (domain, appName, version string, err error) {
	dotV := strings.Index(tag, ".v")
	if dotV < 0 || !strings.Contains(tag[:dotV], "-") {
		return "", "", "", fmt.Errorf("invalid tag format: %q", tag)
	}
	domainApp := tag[:dotV]
	version = "v" + tag[dotV+2:]
	dash := strings.LastIndex(domainApp, "-")
	domain = domainApp[:dash]
	appName = domainApp[dash+1:]
	return domain, appName, version, nil
}
