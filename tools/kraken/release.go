package kraken

import (
	"fmt"
	"os"
	"strings"
)

// MatrixEntry represents a single entry in the release matrix.
type MatrixEntry struct {
	App         string `json:"app"`
	Domain      string `json:"domain"`
	BazelTarget string `json:"bazel_target"`
	Version     string `json:"version"`
}

// ReleasePlan holds the result of release planning.
type ReleasePlan struct {
	Matrix    MatrixConfig      `json:"matrix"`
	Apps      []string          `json:"apps"`
	Version   string            `json:"version"`
	Versions  map[string]string `json:"versions"`
	EventType string            `json:"event_type"`
}

// MatrixConfig holds the matrix configuration for CI.
type MatrixConfig struct {
	Include []MatrixEntry `json:"include"`
}

// FindAppBazelTarget finds the bazel target for an app by name.
func FindAppBazelTarget(appName string) (string, error) {
	validatedApps, err := ValidateApps([]string{appName})
	if err != nil {
		return "", err
	}
	if len(validatedApps) == 1 {
		return validatedApps[0].BazelTarget, nil
	}
	if len(validatedApps) > 1 {
		var names []string
		for _, app := range validatedApps {
			names = append(names, app.Name)
		}
		return "", fmt.Errorf("multiple apps matched '%s': %s", appName, strings.Join(names, ", "))
	}
	return "", fmt.Errorf("app '%s' not found", appName)
}

// PlanRelease plans a release and returns the matrix configuration for CI.
func PlanRelease(eventType string, requestedApps string, version string, versionMode string, baseCommit string, includeDemo bool) (*ReleasePlan, error) {
	// Validate version format if provided
	if version != "" && version != "latest" {
		if !ValidateSemanticVersion(version) {
			return nil, fmt.Errorf(
				"version '%s' does not follow semantic versioning format. "+
					"Expected format: v{major}.{minor}.{patch} (e.g., v1.0.0, v2.1.3, v1.0.0-beta1)",
				version,
			)
		}
	}

	var releaseApps []AppInfo

	switch eventType {
	case "workflow_dispatch":
		if requestedApps == "" {
			return nil, fmt.Errorf("manual releases require apps to be specified")
		}

		// Version validation based on mode
		switch versionMode {
		case "specific":
			if version == "" {
				return nil, fmt.Errorf("specific version mode requires version to be specified")
			}
		case "increment_minor", "increment_patch":
			if version != "" {
				return nil, fmt.Errorf("version should not be specified when using %s mode", versionMode)
			}
		case "":
			if version == "" {
				return nil, fmt.Errorf("manual releases require version to be specified (or use --increment-minor/--increment-patch)")
			}
		}

		if requestedApps == "all" {
			apps, err := ListAllApps()
			if err != nil {
				return nil, err
			}
			if !includeDemo {
				var filtered []AppInfo
				for _, app := range apps {
					if app.Domain != "demo" {
						filtered = append(filtered, app)
					}
				}
				releaseApps = filtered
				fmt.Fprintln(os.Stderr, "Excluding demo domain apps from 'all' (use --include-demo to include)")
			} else {
				releaseApps = apps
			}
		} else {
			requested := strings.Split(requestedApps, ",")
			for i := range requested {
				requested[i] = strings.TrimSpace(requested[i])
			}
			apps, err := ValidateApps(requested)
			if err != nil {
				return nil, err
			}
			releaseApps = apps

			// Check for duplicate apps
			identifiers := make(map[string]bool)
			var duplicates []string
			for _, app := range releaseApps {
				id := fmt.Sprintf("%s-%s", app.Domain, app.Name)
				if identifiers[id] {
					duplicates = append(duplicates, id)
				}
				identifiers[id] = true
			}
			if len(duplicates) > 0 {
				return nil, fmt.Errorf(
					"duplicate apps detected in release plan: %s. "+
						"This usually happens when you request both a domain and specific apps from that domain. "+
						"Please either request the domain name (e.g., 'demo') to release all apps in that domain, "+
						"or request specific apps (e.g., 'demo-app1,demo-app2'), but not both.",
					strings.Join(duplicates, ", "),
				)
			}
		}

		// Handle version modes
		if versionMode == "increment_minor" || versionMode == "increment_patch" {
			incrementType := strings.TrimPrefix(versionMode, "increment_")
			for i := range releaseApps {
				metadata, err := GetAppMetadata(releaseApps[i].BazelTarget)
				if err != nil {
					return nil, err
				}
				appVersion, err := AutoIncrementVersion(metadata.Domain, metadata.Name, incrementType)
				if err != nil {
					return nil, err
				}
				releaseApps[i].Version = appVersion
				fmt.Fprintf(os.Stderr, "Auto-incremented %s/%s to %s\n", metadata.Domain, metadata.Name, appVersion)
			}
		} else {
			for i := range releaseApps {
				releaseApps[i].Version = version
			}
		}

	case "tag_push":
		if version == "" {
			return nil, fmt.Errorf("tag push releases require version to be specified")
		}

		if baseCommit == "" {
			prev, err := GetPreviousTag()
			if err == nil && prev != "" {
				baseCommit = prev
				fmt.Fprintf(os.Stderr, "Auto-detected previous tag: %s\n", baseCommit)
			}
		}

		apps, err := DetectChangedApps(baseCommit)
		if err != nil {
			return nil, err
		}
		releaseApps = apps
		for i := range releaseApps {
			releaseApps[i].Version = version
		}

	case "pull_request", "push", "fallback":
		if eventType == "fallback" || baseCommit == "" {
			fmt.Fprintln(os.Stderr, "Fallback mode: building all apps")
			apps, err := ListAllApps()
			if err != nil {
				return nil, err
			}
			releaseApps = apps
		} else {
			fmt.Fprintf(os.Stderr, "CI build: detecting changes against %s\n", baseCommit)
			apps, err := DetectChangedApps(baseCommit)
			if err != nil {
				return nil, err
			}
			releaseApps = apps
		}

	default:
		return nil, fmt.Errorf("unknown event type: %s", eventType)
	}

	// Create matrix
	var matrix MatrixConfig
	if len(releaseApps) > 0 {
		for _, app := range releaseApps {
			v := app.Version
			if v == "" {
				v = version
			}
			matrix.Include = append(matrix.Include, MatrixEntry{
				App:         app.Name,
				Domain:      app.Domain,
				BazelTarget: app.BazelTarget,
				Version:     v,
			})
		}
	}

	// Build apps list and versions map
	var appsList []string
	versions := make(map[string]string)
	for _, app := range releaseApps {
		fullName := fmt.Sprintf("%s-%s", app.Domain, app.Name)
		appsList = append(appsList, fullName)
		v := app.Version
		if v == "" {
			v = version
		}
		versions[fullName] = v
	}

	return &ReleasePlan{
		Matrix:    matrix,
		Apps:      appsList,
		Version:   version,
		Versions:  versions,
		EventType: eventType,
	}, nil
}

// TagAndPushImage builds and pushes container images to registry, optionally creating Git tags.
func TagAndPushImage(appName, version string, commitSHA string, dryRun, allowOverwrite, createGitTagFlag bool) error {
	bazelTarget, err := FindAppBazelTarget(appName)
	if err != nil {
		return err
	}

	if err := ValidateReleaseVersion(bazelTarget, version, allowOverwrite); err != nil {
		return err
	}

	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return err
	}

	if _, err := BuildImage(bazelTarget, ""); err != nil {
		return err
	}

	tags := FormatRegistryTags(metadata.Domain, metadata.Name, version, metadata.Registry, commitSHA, "")

	if dryRun {
		fmt.Println("DRY RUN: Would push the following images:")
		for _, tag := range tags.TagsList() {
			fmt.Printf("  - %s\n", tag)
		}
		if createGitTagFlag {
			gitTag := FormatGitTag(metadata.Domain, metadata.Name, version)
			fmt.Printf("DRY RUN: Would create Git tag: %s\n", gitTag)
		}
		return nil
	}

	fmt.Println("Pushing to registry...")
	if err := PushImageWithTags(bazelTarget, tags.TagsList()); err != nil {
		return fmt.Errorf("failed to push %s %s: %w", metadata.Name, version, err)
	}
	fmt.Printf("Successfully pushed %s %s\n", metadata.Name, version)

	if createGitTagFlag {
		gitTag := FormatGitTag(metadata.Domain, metadata.Name, version)
		tagMessage := fmt.Sprintf("Release %s %s", metadata.Name, version)

		if err := CreateGitTag(gitTag, commitSHA, tagMessage, false); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to create/push Git tag %s: %v\n", gitTag, err)
		} else if err := PushGitTag(gitTag, false); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to push Git tag %s: %v\n", gitTag, err)
		} else {
			fmt.Printf("Successfully created and pushed Git tag: %s\n", gitTag)
		}
	}

	return nil
}
