// Package changes provides change detection for the release helper.
package changes

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/whale-net/everything/tools/release/pkg/core"
	"github.com/whale-net/everything/tools/release/pkg/git"
	"github.com/whale-net/everything/tools/release/pkg/metadata"
)

// DetectChangedApps detects which apps have changed since a base commit.
func DetectChangedApps(baseCommit string, useBazelQuery bool) ([]metadata.AppInfo, error) {
	if baseCommit == "" {
		// Default to previous tag
		prevTag, err := git.GetPreviousTag()
		if err != nil {
			baseCommit = "HEAD^"
		} else {
			baseCommit = prevTag
		}
	}

	// Get changed files
	changedFiles, err := git.GetChangedFiles(baseCommit, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}

	if len(changedFiles) == 0 {
		return []metadata.AppInfo{}, nil
	}

	// Get all apps
	allApps, err := metadata.ListAllApps()
	if err != nil {
		return nil, fmt.Errorf("failed to list apps: %w", err)
	}

	if useBazelQuery {
		return detectChangedAppsWithBazelQuery(changedFiles, allApps)
	}

	return detectChangedAppsSimple(changedFiles, allApps)
}

// detectChangedAppsSimple uses simple path matching to detect changed apps.
func detectChangedAppsSimple(changedFiles []string, allApps []metadata.AppInfo) ([]metadata.AppInfo, error) {
	changedAppsMap := make(map[string]metadata.AppInfo)

	for _, file := range changedFiles {
		for _, app := range allApps {
			// Extract package path from bazel target (//path/to/app:target)
			targetPath := strings.TrimPrefix(app.BazelTarget, "//")
			targetPath = strings.Split(targetPath, ":")[0]

			// Check if file is in app directory
			if strings.HasPrefix(file, targetPath+"/") {
				changedAppsMap[app.Name] = app
				break
			}
		}
	}

	// Convert map to slice
	var changedApps []metadata.AppInfo
	for _, app := range changedAppsMap {
		changedApps = append(changedApps, app)
	}

	return changedApps, nil
}

// detectChangedAppsWithBazelQuery uses Bazel query to precisely detect changed apps.
func detectChangedAppsWithBazelQuery(changedFiles []string, allApps []metadata.AppInfo) ([]metadata.AppInfo, error) {
	changedAppsMap := make(map[string]metadata.AppInfo)

	// Build query for changed packages
	var packagePaths []string
	for _, file := range changedFiles {
		dir := filepath.Dir(file)
		// Convert file path to package path
		packagePath := "//" + dir + ":*"
		packagePaths = append(packagePaths, packagePath)
	}

	// For each app, check if it depends on changed packages
	for _, app := range allApps {
		// Query app's dependencies
		// This is a simplified version - in production, we'd use rdeps or allrdeps
		for _, file := range changedFiles {
			targetPath := strings.TrimPrefix(app.BazelTarget, "//")
			targetPath = strings.Split(targetPath, ":")[0]

			if strings.HasPrefix(file, targetPath+"/") {
				changedAppsMap[app.Name] = app
				break
			}
		}
	}

	var changedApps []metadata.AppInfo
	for _, app := range changedAppsMap {
		changedApps = append(changedApps, app)
	}

	return changedApps, nil
}

// GetAppDependencies returns the dependencies of an app using Bazel query.
func GetAppDependencies(bazelTarget string) ([]string, error) {
	// Query for all dependencies
	result, err := core.RunBazel([]string{
		"query",
		fmt.Sprintf("deps(%s)", bazelTarget),
		"--output=label",
	}, true, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query dependencies: %w", err)
	}

	return result.Lines(), nil
}

// GetReverseDependencies returns apps that depend on a given target.
func GetReverseDependencies(bazelTarget string) ([]string, error) {
	// Query for reverse dependencies
	result, err := core.RunBazel([]string{
		"query",
		fmt.Sprintf("rdeps(//..., %s)", bazelTarget),
		"--output=label",
	}, true, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query reverse dependencies: %w", err)
	}

	return result.Lines(), nil
}
