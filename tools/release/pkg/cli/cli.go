// Package cli provides the command-line interface for the release helper.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/whale-net/everything/tools/release/pkg/changes"
	"github.com/whale-net/everything/tools/release/pkg/core"
	"github.com/whale-net/everything/tools/release/pkg/git"
	"github.com/whale-net/everything/tools/release/pkg/images"
	"github.com/whale-net/everything/tools/release/pkg/metadata"
	"github.com/whale-net/everything/tools/release/pkg/release"
	"github.com/whale-net/everything/tools/release/pkg/validation"
)

var rootCmd = &cobra.Command{
	Use:   "release",
	Short: "Release helper for Everything monorepo",
	Long:  `Release helper tool for managing app releases and container images in the Everything monorepo.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(listAppsCmd)
	rootCmd.AddCommand(listAppVersionsCmd)
	rootCmd.AddCommand(incrementVersionCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(planCmd)
	rootCmd.AddCommand(changesCmd)
	rootCmd.AddCommand(releaseCmd)
}

var listAppsCmd = &cobra.Command{
	Use:   "list",
	Short: "List all apps with release metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		apps, err := metadata.ListAllApps()
		if err != nil {
			return fmt.Errorf("failed to list apps: %w", err)
		}

		for _, app := range apps {
			fmt.Printf("%s (domain: %s, target: %s)\n", app.Name, app.Domain, app.BazelTarget)
		}
		return nil
	},
}

var listAppVersionsCmd = &cobra.Command{
	Use:   "list-app-versions [app-name]",
	Short: "List versions for apps by checking git tags",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			// List versions for specific app
			appName := args[0]
			apps, err := metadata.ListAllApps()
			if err != nil {
				return fmt.Errorf("failed to list apps: %w", err)
			}

			var found bool
			for _, app := range apps {
				if app.Name == appName {
					found = true
					version, err := git.GetLatestAppVersion(app.Domain, app.Name)
					if err != nil {
						fmt.Printf("%s: no versions found\n", appName)
					} else {
						fmt.Printf("%s: %s\n", appName, version)
					}
					break
				}
			}

			if !found {
				return fmt.Errorf("app not found: %s", appName)
			}
		} else {
			// List versions for all apps
			apps, err := metadata.ListAllApps()
			if err != nil {
				return fmt.Errorf("failed to list apps: %w", err)
			}

			for _, app := range apps {
				version, err := git.GetLatestAppVersion(app.Domain, app.Name)
				if err != nil {
					fmt.Printf("%s: no versions found\n", app.Name)
				} else {
					fmt.Printf("%s: %s\n", app.Name, version)
				}
			}
		}
		return nil
	},
}

var incrementVersionCmd = &cobra.Command{
	Use:   "increment-version <app-name> <minor|patch>",
	Short: "Calculate the next version for an app based on increment type",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appName := args[0]
		incrementType := args[1]

		if incrementType != "minor" && incrementType != "patch" {
			return fmt.Errorf("increment_type must be 'minor' or 'patch'")
		}

		apps, err := metadata.ListAllApps()
		if err != nil {
			return fmt.Errorf("failed to list apps: %w", err)
		}

		var found bool
		for _, app := range apps {
			if app.Name == appName {
				found = true
				newVersion, err := git.AutoIncrementVersion(app.Domain, app.Name, incrementType)
				if err != nil {
					return fmt.Errorf("failed to increment version: %w", err)
				}
				fmt.Printf("%s: %s\n", appName, newVersion)
				break
			}
		}

		if !found {
			return fmt.Errorf("app not found: %s", appName)
		}

		return nil
	},
}

var buildCmd = &cobra.Command{
	Use:   "build <app-name>",
	Short: "Build container image for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appName := args[0]
		platform, _ := cmd.Flags().GetString("platform")

		// Find the app
		apps, err := metadata.ListAllApps()
		if err != nil {
			return fmt.Errorf("failed to list apps: %w", err)
		}

		var bazelTarget string
		for _, app := range apps {
			if app.Name == appName {
				// Get image targets
				imageTargets, err := metadata.GetImageTargets(app.BazelTarget)
				if err != nil {
					return fmt.Errorf("failed to get image targets: %w", err)
				}

				// Select target based on platform
				if platform == "amd64" {
					bazelTarget = imageTargets.AMD64 + "_load"
				} else if platform == "arm64" {
					bazelTarget = imageTargets.ARM64 + "_load"
				} else {
					bazelTarget = imageTargets.Base + "_load"
				}
				break
			}
		}

		if bazelTarget == "" {
			return fmt.Errorf("app not found: %s", appName)
		}

		// Build the image
		fmt.Printf("Building image for %s (target: %s)...\n", appName, bazelTarget)
		_, err = core.RunBazel([]string{"run", bazelTarget}, false, nil)
		if err != nil {
			return fmt.Errorf("failed to build image: %w", err)
		}

		fmt.Printf("✓ Image loaded successfully\n")
		return nil
	},
}

func init() {
	buildCmd.Flags().String("platform", "", "Target platform (amd64, arm64)")
}

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Plan a release and output CI matrix",
	RunE: func(cmd *cobra.Command, args []string) error {
		eventType, _ := cmd.Flags().GetString("event-type")
		apps, _ := cmd.Flags().GetString("apps")
		version, _ := cmd.Flags().GetString("version")
		incrementMinor, _ := cmd.Flags().GetBool("increment-minor")
		incrementPatch, _ := cmd.Flags().GetBool("increment-patch")
		baseCommit, _ := cmd.Flags().GetString("base-commit")
		format, _ := cmd.Flags().GetString("format")

		// Determine version mode
		versionMode := ""
		if incrementMinor {
			versionMode = "increment_minor"
		} else if incrementPatch {
			versionMode = "increment_patch"
		}

		// Plan release
		plan, err := release.PlanRelease(eventType, apps, version, versionMode, baseCommit)
		if err != nil {
			return fmt.Errorf("failed to plan release: %w", err)
		}

		// Format output
		output, err := release.FormatReleasePlan(plan, format)
		if err != nil {
			return fmt.Errorf("failed to format plan: %w", err)
		}

		fmt.Println(output)
		return nil
	},
}

func init() {
	planCmd.Flags().String("event-type", "", "Type of trigger event (required)")
	planCmd.Flags().String("apps", "", "Comma-separated list of apps, domain names, or 'all'")
	planCmd.Flags().String("version", "", "Release version")
	planCmd.Flags().Bool("increment-minor", false, "Auto-increment minor version")
	planCmd.Flags().Bool("increment-patch", false, "Auto-increment patch version")
	planCmd.Flags().String("base-commit", "", "Compare changes against this commit")
	planCmd.Flags().String("format", "json", "Output format (json, github)")
	planCmd.MarkFlagRequired("event-type")
}

var changesCmd = &cobra.Command{
	Use:   "changes",
	Short: "Detect changed apps since a commit",
	RunE: func(cmd *cobra.Command, args []string) error {
		baseCommit, _ := cmd.Flags().GetString("base-commit")
		useBazelQuery, _ := cmd.Flags().GetBool("use-bazel-query")

		changedApps, err := changes.DetectChangedApps(baseCommit, useBazelQuery)
		if err != nil {
			return fmt.Errorf("failed to detect changed apps: %w", err)
		}

		if len(changedApps) == 0 {
			fmt.Println("No changed apps detected")
			return nil
		}

		fmt.Println("Changed apps:")
		for _, app := range changedApps {
			fmt.Printf("  - %s (domain: %s)\n", app.Name, app.Domain)
		}

		return nil
	},
}

func init() {
	changesCmd.Flags().String("base-commit", "", "Compare changes against this commit")
	changesCmd.Flags().Bool("use-bazel-query", true, "Use Bazel query for precise dependency analysis")
}

var releaseCmd = &cobra.Command{
	Use:   "release <app-name>",
	Short: "Build, tag, and push container image",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appName := args[0]
		version, _ := cmd.Flags().GetString("version")
		commit, _ := cmd.Flags().GetString("commit")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		allowOverwrite, _ := cmd.Flags().GetBool("allow-overwrite")
		createGitTag, _ := cmd.Flags().GetBool("create-git-tag")

		// Find the app
		bazelTarget, err := release.FindAppBazelTarget(appName)
		if err != nil {
			return err
		}

		// Get app metadata
		appMeta, err := metadata.GetAppMetadata(bazelTarget)
		if err != nil {
			return fmt.Errorf("failed to get app metadata: %w", err)
		}

		// Validate version
		if version != "" && version != "latest" {
			if err := validation.ValidateSemanticVersion(version); err != nil {
				return fmt.Errorf("invalid version: %w", err)
			}
		}

		// Tag and push image
		err = images.TagAndPushImage(appMeta, version, commit, dryRun, allowOverwrite)
		if err != nil {
			return fmt.Errorf("failed to release: %w", err)
		}

		// Create git tag if requested
		if createGitTag && !dryRun {
			gitTag := git.FormatGitTag(appMeta.Domain, appMeta.Name, version)
			if err := git.CreateGitTag(gitTag, commit, ""); err != nil {
				return fmt.Errorf("failed to create git tag: %w", err)
			}
			if err := git.PushGitTag(gitTag); err != nil {
				return fmt.Errorf("failed to push git tag: %w", err)
			}
		}

		return nil
	},
}

func init() {
	releaseCmd.Flags().String("version", "latest", "Version tag")
	releaseCmd.Flags().String("commit", "", "Commit SHA for additional tag")
	releaseCmd.Flags().Bool("dry-run", false, "Show what would be pushed without actually pushing")
	releaseCmd.Flags().Bool("allow-overwrite", false, "Allow overwriting existing versions")
	releaseCmd.Flags().Bool("create-git-tag", false, "Create and push a Git tag for this release")
}

// Validate validates a semantic version format
func Validate(version string) error {
	return validation.ValidateSemanticVersion(version)
}
