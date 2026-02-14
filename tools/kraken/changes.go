package kraken

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GetChangedFiles returns the list of changed files compared to a base commit.
func GetChangedFiles(baseCommit string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", fmt.Sprintf("%s..HEAD", baseCommit))
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting changed files against %s: %v\n", baseCommit, err)
		return nil, nil
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	var files []string
	for _, f := range strings.Split(raw, "\n") {
		f = strings.TrimSpace(f)
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// ShouldIgnoreFile checks if a file should be ignored for build impact analysis.
func ShouldIgnoreFile(filePath string) bool {
	if strings.HasPrefix(filePath, ".github/workflows/") || strings.HasPrefix(filePath, ".github/actions/") {
		return true
	}
	if strings.HasPrefix(filePath, "docs/") || strings.HasSuffix(filePath, ".md") {
		return true
	}
	if strings.HasSuffix(filePath, "copilot-instructions.md") {
		return true
	}
	return false
}

// DetectChangedApps detects which apps have changed compared to a base commit.
// If baseCommit is empty, returns all apps.
func DetectChangedApps(baseCommit string) ([]AppInfo, error) {
	allApps, err := ListAllApps()
	if err != nil {
		return nil, err
	}

	if baseCommit == "" {
		fmt.Fprintln(os.Stderr, "No base commit specified, considering all apps as changed")
		return allApps, nil
	}

	changedFiles, err := GetChangedFiles(baseCommit)
	if err != nil {
		return nil, err
	}

	if len(changedFiles) == 0 {
		fmt.Fprintln(os.Stderr, "No files changed, no apps need to be built")
		return nil, nil
	}

	preview := changedFiles
	if len(preview) > 10 {
		preview = preview[:10]
	}
	suffix := ""
	if len(changedFiles) > 10 {
		suffix = fmt.Sprintf(" (and %d more)", len(changedFiles)-10)
	}
	fmt.Fprintf(os.Stderr, "Changed files: %s%s\n", strings.Join(preview, ", "), suffix)

	// Filter out non-build files
	var relevantFiles []string
	for _, f := range changedFiles {
		if !ShouldIgnoreFile(f) {
			relevantFiles = append(relevantFiles, f)
		}
	}

	if len(relevantFiles) == 0 {
		fmt.Fprintln(os.Stderr, "All changed files are non-build artifacts (workflows, docs, etc.). No apps need to be built.")
		return nil, nil
	}

	if len(relevantFiles) < len(changedFiles) {
		fmt.Fprintf(os.Stderr, "Filtered out %d non-build files (workflows, docs, etc.)\n", len(changedFiles)-len(relevantFiles))
	}

	fmt.Fprintf(os.Stderr, "Analyzing %d changed files using Bazel query...\n", len(relevantFiles))

	// Convert git file paths to Bazel labels
	var fileLabels []string
	changedPackages := make(map[string]bool)

	for _, f := range relevantFiles {
		if strings.HasSuffix(f, ".bzl") {
			continue
		}

		base := filepath.Base(f)
		if base == "BUILD" || base == "BUILD.bazel" {
			dir := filepath.Dir(f)
			if dir == "." {
				changedPackages["//"] = true
			} else {
				changedPackages[fmt.Sprintf("//%s", dir)] = true
			}
			continue
		}

		parts := strings.Split(f, "/")
		if len(parts) < 2 {
			fileLabels = append(fileLabels, fmt.Sprintf("//:%s", f))
		} else {
			pkg := strings.Join(parts[:len(parts)-1], "/")
			filename := parts[len(parts)-1]
			fileLabels = append(fileLabels, fmt.Sprintf("//%s:%s", pkg, filename))
		}
	}

	if len(fileLabels) == 0 && len(changedPackages) == 0 {
		fmt.Fprintln(os.Stderr, "No file labels to analyze")
		return nil, nil
	}

	// Validate labels in batch
	var validLabels []string
	if len(fileLabels) > 0 {
		labelsExpr := strings.Join(fileLabels, " + ")
		result, err := RunBazel([]string{"query", labelsExpr, "--output=label"}, true, nil)
		if err != nil {
			// Fall back to individual validation
			for _, label := range fileLabels {
				result, err := RunBazel([]string{"query", label, "--output=label"}, true, nil)
				if err == nil && strings.TrimSpace(result.Stdout) != "" {
					validLabels = append(validLabels, label)
				}
			}
		} else if strings.TrimSpace(result.Stdout) != "" {
			validLabels = strings.Split(strings.TrimSpace(result.Stdout), "\n")
		}
	}

	if len(validLabels) == 0 && len(changedPackages) == 0 {
		fmt.Fprintln(os.Stderr, "No valid Bazel targets in changed files")
		return nil, nil
	}

	// Query rdeps for all valid labels and changed packages
	var queryParts []string
	if len(validLabels) > 0 {
		queryParts = append(queryParts, strings.Join(validLabels, " + "))
	}
	for pkg := range changedPackages {
		queryParts = append(queryParts, pkg+"/...")
	}

	if len(queryParts) == 0 {
		fmt.Fprintln(os.Stderr, "No query parts to analyze")
		return nil, nil
	}

	labelsExpr := strings.Join(queryParts, " + ")
	result, err := RunBazel([]string{"query", fmt.Sprintf("rdeps(//..., %s)", labelsExpr), "--output=label"}, true, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying reverse dependencies: %v\n", err)
		return nil, nil
	}

	allAffectedTargets := make(map[string]bool)
	if strings.TrimSpace(result.Stdout) != "" {
		for _, t := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
			allAffectedTargets[t] = true
		}
	}

	if len(allAffectedTargets) == 0 {
		fmt.Fprintln(os.Stderr, "No targets affected by changed files")
		return nil, nil
	}

	// Find app_metadata targets that depend on affected targets
	metaResult, err := RunBazel([]string{"query", "kind('app_metadata', //...)", "--output=label"}, true, nil)
	if err != nil {
		return nil, nil
	}

	allMetadataTargets := make(map[string]bool)
	if strings.TrimSpace(metaResult.Stdout) != "" {
		for _, t := range strings.Split(strings.TrimSpace(metaResult.Stdout), "\n") {
			allMetadataTargets[t] = true
		}
	}

	if len(allMetadataTargets) == 0 {
		fmt.Fprintln(os.Stderr, "No app_metadata targets found")
		return nil, nil
	}

	var metaTargetList []string
	for t := range allMetadataTargets {
		metaTargetList = append(metaTargetList, t)
	}
	var affectedTargetList []string
	for t := range allAffectedTargets {
		affectedTargetList = append(affectedTargetList, t)
	}

	metadataExpr := strings.Join(metaTargetList, " + ")
	affectedExpr := strings.Join(affectedTargetList, " + ")

	rdepsResult, err := RunBazel([]string{"query", fmt.Sprintf("rdeps(%s, %s)", metadataExpr, affectedExpr), "--output=label"}, true, nil)
	if err != nil {
		return nil, nil
	}

	allAffectedMetadata := make(map[string]bool)
	if strings.TrimSpace(rdepsResult.Stdout) != "" {
		for _, t := range strings.Split(strings.TrimSpace(rdepsResult.Stdout), "\n") {
			allAffectedMetadata[t] = true
		}
	}

	if len(allAffectedMetadata) == 0 {
		fmt.Fprintln(os.Stderr, "No apps affected by changed files")
		return nil, nil
	}

	var affectedApps []AppInfo
	for _, app := range allApps {
		if allAffectedMetadata[app.BazelTarget] {
			affectedApps = append(affectedApps, app)
			fmt.Fprintf(os.Stderr, "  %s: affected by changes\n", app.Name)
		}
	}

	return affectedApps, nil
}
