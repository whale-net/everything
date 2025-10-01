// Package release provides release planning and execution utilities.
package release

import (
	"encoding/json"
	"fmt"

	"github.com/whale-net/everything/tools/release/pkg/changes"
	"github.com/whale-net/everything/tools/release/pkg/git"
	"github.com/whale-net/everything/tools/release/pkg/metadata"
	"github.com/whale-net/everything/tools/release/pkg/validation"
)

// ReleaseApp represents an app to be released.
type ReleaseApp struct {
	Name        string `json:"name"`
	BazelTarget string `json:"bazel_target"`
	Version     string `json:"version"`
}

// ReleaseMatrix represents the CI matrix for releases.
type ReleaseMatrix struct {
	Include []ReleaseApp `json:"include"`
}

// ReleasePlan represents the complete release plan.
type ReleasePlan struct {
	Matrix  ReleaseMatrix `json:"matrix"`
	Apps    []string      `json:"apps"`
	Version string        `json:"version,omitempty"`
}

// PlanRelease plans a release and returns the matrix configuration for CI.
func PlanRelease(eventType string, requestedApps string, version string, versionMode string, baseCommit string) (*ReleasePlan, error) {
	var releaseApps []ReleaseApp

	switch eventType {
	case "workflow_dispatch":
		// Manual release
		apps, err := selectApps(requestedApps)
		if err != nil {
			return nil, fmt.Errorf("failed to select apps: %w", err)
		}

		// Determine version for each app
		for _, app := range apps {
			appVersion, err := determineVersion(app, version, versionMode)
			if err != nil {
				return nil, fmt.Errorf("failed to determine version for %s: %w", app.Name, err)
			}

			releaseApps = append(releaseApps, ReleaseApp{
				Name:        app.Name,
				BazelTarget: app.BazelTarget,
				Version:     appVersion,
			})
		}

	case "tag_push":
		// Release triggered by tag push
		// Parse tag to find app and version
		// This is simplified - real implementation would parse the git tag
		return nil, fmt.Errorf("tag_push event not yet implemented")

	case "pull_request":
		// Detect changed apps for PR validation
		apps, err := changes.DetectChangedApps(baseCommit, true)
		if err != nil {
			return nil, fmt.Errorf("failed to detect changed apps: %w", err)
		}

		for _, app := range apps {
			releaseApps = append(releaseApps, ReleaseApp{
				Name:        app.Name,
				BazelTarget: app.BazelTarget,
				Version:     "pr-test",
			})
		}

	case "push":
		// Detect changed apps for main branch push
		apps, err := changes.DetectChangedApps(baseCommit, true)
		if err != nil {
			return nil, fmt.Errorf("failed to detect changed apps: %w", err)
		}

		for _, app := range apps {
			// Auto-increment version
			appMeta, err := metadata.GetAppMetadata(app.BazelTarget)
			if err != nil {
				return nil, fmt.Errorf("failed to get metadata for %s: %w", app.Name, err)
			}

			newVersion, err := git.AutoIncrementVersion(appMeta.Domain, appMeta.Name, "patch")
			if err != nil {
				return nil, fmt.Errorf("failed to auto-increment version for %s: %w", app.Name, err)
			}

			releaseApps = append(releaseApps, ReleaseApp{
				Name:        app.Name,
				BazelTarget: app.BazelTarget,
				Version:     newVersion,
			})
		}

	default:
		return nil, fmt.Errorf("unknown event type: %s", eventType)
	}

	// Build matrix
	matrix := ReleaseMatrix{
		Include: releaseApps,
	}

	// Extract app names
	var appNames []string
	for _, app := range releaseApps {
		appNames = append(appNames, app.Name)
	}

	return &ReleasePlan{
		Matrix:  matrix,
		Apps:    appNames,
		Version: version,
	}, nil
}

// selectApps selects apps based on the requested apps string.
func selectApps(requestedApps string) ([]metadata.AppInfo, error) {
	allApps, err := metadata.ListAllApps()
	if err != nil {
		return nil, fmt.Errorf("failed to list apps: %w", err)
	}

	if requestedApps == "" || requestedApps == "all" {
		return allApps, nil
	}

	// Parse comma-separated app names or domains
	requestedList := parseCommaSeparated(requestedApps)
	var selectedApps []metadata.AppInfo

	for _, requested := range requestedList {
		for _, app := range allApps {
			if app.Name == requested || app.Domain == requested {
				selectedApps = append(selectedApps, app)
			}
		}
	}

	if len(selectedApps) == 0 {
		return nil, fmt.Errorf("no apps matched requested: %s", requestedApps)
	}

	return selectedApps, nil
}

// determineVersion determines the version for an app based on version mode.
func determineVersion(app metadata.AppInfo, version string, versionMode string) (string, error) {
	if versionMode == "increment_minor" {
		appMeta, err := metadata.GetAppMetadata(app.BazelTarget)
		if err != nil {
			return "", err
		}
		return git.AutoIncrementVersion(appMeta.Domain, appMeta.Name, "minor")
	}

	if versionMode == "increment_patch" {
		appMeta, err := metadata.GetAppMetadata(app.BazelTarget)
		if err != nil {
			return "", err
		}
		return git.AutoIncrementVersion(appMeta.Domain, appMeta.Name, "patch")
	}

	// Validate explicit version
	if version != "" && version != "latest" {
		if err := validation.ValidateSemanticVersion(version); err != nil {
			return "", fmt.Errorf("invalid version %s: %w", version, err)
		}
	}

	return version, nil
}

// parseCommaSeparated parses a comma-separated string into a slice.
func parseCommaSeparated(s string) []string {
	if s == "" {
		return []string{}
	}

	var result []string
	for _, item := range []string{s} {
		for _, part := range []rune(item) {
			if part == ',' {
				continue
			}
		}
		result = append(result, item)
	}
	return result
}

// FindAppBazelTarget finds the Bazel target for an app by name.
func FindAppBazelTarget(appName string) (string, error) {
	allApps, err := metadata.ListAllApps()
	if err != nil {
		return "", fmt.Errorf("failed to list apps: %w", err)
	}

	for _, app := range allApps {
		if app.Name == appName {
			return app.BazelTarget, nil
		}
	}

	return "", fmt.Errorf("app not found: %s", appName)
}

// FormatReleasePlan formats a release plan as JSON or GitHub Actions format.
func FormatReleasePlan(plan *ReleasePlan, format string) (string, error) {
	if format == "json" {
		data, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal plan: %w", err)
		}
		return string(data), nil
	}

	if format == "github" {
		// GitHub Actions format
		matrixJSON, err := json.Marshal(plan.Matrix)
		if err != nil {
			return "", fmt.Errorf("failed to marshal matrix: %w", err)
		}
		return fmt.Sprintf("matrix=%s", string(matrixJSON)), nil
	}

	return "", fmt.Errorf("unsupported format: %s", format)
}
