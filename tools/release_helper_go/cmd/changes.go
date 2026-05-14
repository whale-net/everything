package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newChangesCmd() *cobra.Command {
	var baseCommit string

	cmd := &cobra.Command{
		Use:          "changes",
		Short:        "Detect changed apps since a commit",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}
			if baseCommit == "" {
				prev, err := getPreviousTag(defaultGit)
				if err == nil && prev != "" {
					baseCommit = prev
					fmt.Fprintf(cmd.ErrOrStderr(), "Auto-detected previous tag: %s\n", baseCommit)
				}
			}
			if baseCommit != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Detecting changes against commit: %s\n", baseCommit)
			} else {
				fmt.Fprintln(cmd.ErrOrStderr(), "No base commit specified, considering all apps as changed")
			}
			apps, err := DetectChangedApps(baseCommit, defaultBazel, defaultGit, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			for _, app := range apps {
				fmt.Fprintln(cmd.OutOrStdout(), app.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baseCommit, "base-commit", "", "Compare changes against this commit")
	return cmd
}

// DetectChangedApps finds apps affected by changes since baseCommit.
// If baseCommit is empty, all apps are considered changed.
func DetectChangedApps(baseCommit string, bazel BazelRunner, git GitRunner, fs FileSystem, workspaceRoot string) ([]AppMetadata, error) {
	allApps, err := ListAllApps(bazel, fs, workspaceRoot)
	if err != nil {
		return nil, err
	}
	if baseCommit == "" {
		return allApps, nil
	}

	changedFiles, err := getChangedFiles(baseCommit, git)
	if err != nil {
		return nil, err
	}
	if len(changedFiles) == 0 {
		return nil, nil
	}

	relevant := filterBuildFiles(changedFiles)
	if len(relevant) == 0 {
		return nil, nil
	}

	fileLabels, changedPkgs := filesToBazelLabels(relevant)

	// Validate labels (remove deleted files / invalid targets)
	validLabels := validateLabels(fileLabels, bazel)

	if len(validLabels) == 0 && len(changedPkgs) == 0 {
		return nil, nil
	}

	// Build rdeps seed expression, wrapping multi-part unions in parens for Bazel query syntax.
	queryParts := make([]string, 0, len(validLabels)+len(changedPkgs))
	if len(validLabels) > 0 {
		queryParts = append(queryParts, strings.Join(validLabels, " + "))
	}
	for pkg := range changedPkgs {
		if pkg == "//" {
			queryParts = append(queryParts, "//...")
		} else {
			queryParts = append(queryParts, pkg+"/...")
		}
	}
	expr := strings.Join(queryParts, " + ")
	if len(queryParts) > 1 {
		expr = "(" + expr + ")"
	}

	// Find which app_metadata targets are affected by the changed files.
	metaTargets := make([]string, 0, len(allApps))
	for _, app := range allApps {
		metaTargets = append(metaTargets, app.BazelTarget)
	}
	if len(metaTargets) == 0 {
		return nil, nil
	}

	metaExpr := strings.Join(metaTargets, " + ")
	if len(metaTargets) > 1 {
		metaExpr = "(" + metaExpr + ")"
	}
	affectedMetaOut, err := bazel.Run("query", fmt.Sprintf("rdeps(%s, %s)", metaExpr, expr), "--output=label")
	if err != nil {
		return nil, fmt.Errorf("bazel rdeps query for metadata: %w", err)
	}
	affectedMeta := labelSet(affectedMetaOut)

	var result []AppMetadata
	for _, app := range allApps {
		if affectedMeta[app.BazelTarget] {
			result = append(result, app)
		}
	}
	return result, nil
}

func getChangedFiles(baseCommit string, git GitRunner) ([]string, error) {
	out, err := git.Run("diff", "--name-only", baseCommit+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		f = strings.TrimSpace(f)
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func getPreviousTag(git GitRunner) (string, error) {
	out, err := git.Run("describe", "--tags", "--abbrev=0", "HEAD^")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// filterBuildFiles removes files that cannot affect any build (docs, CI, etc.).
func filterBuildFiles(files []string) []string {
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f, ".github/workflows/") ||
			strings.HasPrefix(f, ".github/actions/") ||
			strings.HasPrefix(f, "docs/") ||
			strings.HasSuffix(f, ".md") ||
			strings.HasSuffix(f, "copilot-instructions.md") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// filesToBazelLabels converts git file paths to Bazel labels and package sets.
func filesToBazelLabels(files []string) (labels []string, packages map[string]struct{}) {
	packages = make(map[string]struct{})
	for _, f := range files {
		if strings.HasSuffix(f, ".bzl") {
			continue
		}
		base := filepath.Base(f)
		if base == "BUILD" || base == "BUILD.bazel" {
			dir := filepath.Dir(f)
			if dir == "." {
				packages["//"] = struct{}{}
			} else {
				packages["//"+dir] = struct{}{}
			}
			continue
		}
		parts := strings.SplitN(f, "/", 2)
		if len(parts) == 1 {
			labels = append(labels, "//:" + f)
		} else {
			dir := filepath.Dir(f)
			labels = append(labels, "//"+dir+":"+filepath.Base(f))
		}
	}
	return labels, packages
}

// validateLabels filters labels to those Bazel can resolve (removes deleted files etc.).
func validateLabels(labels []string, bazel BazelRunner) []string {
	if len(labels) == 0 {
		return nil
	}
	// Try batch first
	expr := strings.Join(labels, " + ")
	out, err := bazel.Run("query", expr, "--output=label")
	if err == nil {
		return strings.Split(strings.TrimSpace(out), "\n")
	}
	// Fall back to individual validation
	var valid []string
	for _, label := range labels {
		if out, err := bazel.Run("query", label, "--output=label"); err == nil {
			if t := strings.TrimSpace(out); t != "" {
				valid = append(valid, t)
			}
		}
	}
	return valid
}

// labelSet converts newline-separated label output to a set.
func labelSet(out string) map[string]bool {
	set := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			set[t] = true
		}
	}
	return set
}
