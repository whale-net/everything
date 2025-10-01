// Package cli provides the command-line interface for the release helper.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/whale-net/everything/tools/release/pkg/core"
	"github.com/whale-net/everything/tools/release/pkg/git"
	"github.com/whale-net/everything/tools/release/pkg/metadata"
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

// Validate validates a semantic version format
func Validate(version string) error {
	return validation.ValidateSemanticVersion(version)
}
