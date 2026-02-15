package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/whale-net/everything/tools/kraken"
)

func main() {
	root := &cobra.Command{
		Use:   "kraken",
		Short: "Release management tool for the Everything monorepo",
	}

	root.AddCommand(
		newListCmd(),
		newChangesCmd(),
		newBuildCmd(),
		newPlanCmd(),
		newReleaseCmd(),
		newReleaseMultiarchCmd(),
		newReleaseNotesCmd(),
		newSummaryCmd(),
		newCreateReleaseCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all discoverable apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			apps, err := kraken.ListAllApps()
			if err != nil {
				return err
			}

			fmt.Printf("%-30s %-15s %-10s %s\n", "APP", "DOMAIN", "LANGUAGE", "BAZEL TARGET")
			fmt.Println(strings.Repeat("-", 90))
			for _, app := range apps {
				fmt.Printf("%-30s %-15s %-10s %s\n", app.Name, app.Domain, app.Language, app.BazelTarget)
			}
			fmt.Printf("\nTotal: %d apps\n", len(apps))
			return nil
		},
	}
}

func newChangesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "changes [base-commit]",
		Short: "Detect apps with changes since a base commit",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var baseCommit string
			if len(args) > 0 {
				baseCommit = args[0]
			}

			apps, err := kraken.DetectChangedApps(baseCommit)
			if err != nil {
				return err
			}

			if len(apps) == 0 {
				fmt.Println("No apps with changes detected")
				return nil
			}

			fmt.Printf("Changed apps (%d):\n", len(apps))
			for _, app := range apps {
				fmt.Printf("  %s-%s\n", app.Domain, app.Name)
			}
			return nil
		},
	}
}

func newBuildCmd() *cobra.Command {
	var platform string

	cmd := &cobra.Command{
		Use:   "build <app>",
		Short: "Build and load a container image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bazelTarget, err := kraken.FindAppBazelTarget(args[0])
			if err != nil {
				return err
			}

			imageName, err := kraken.BuildImage(bazelTarget, platform)
			if err != nil {
				return err
			}

			fmt.Printf("Successfully built: %s\n", imageName)
			return nil
		},
	}

	cmd.Flags().StringVar(&platform, "platform", "", "Target platform (amd64 or arm64)")
	return cmd
}

func newPlanCmd() *cobra.Command {
	var (
		eventType   string
		apps        string
		version     string
		versionMode string
		baseCommit  string
		includeDemo bool
		outputJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Plan a release and output the build matrix",
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := kraken.PlanRelease(eventType, apps, version, versionMode, baseCommit, includeDemo)
			if err != nil {
				return err
			}

			if outputJSON {
				data, err := json.MarshalIndent(plan, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if len(plan.Matrix.Include) == 0 {
				fmt.Println("No apps to release")
			} else {
				fmt.Printf("Release plan (%d apps):\n", len(plan.Matrix.Include))
				for _, entry := range plan.Matrix.Include {
					fmt.Printf("  %s-%s: %s\n", entry.Domain, entry.App, entry.Version)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&eventType, "event-type", "workflow_dispatch", "Event type (workflow_dispatch, tag_push, pull_request, push)")
	cmd.Flags().StringVar(&apps, "apps", "", "Comma-separated app list or 'all'")
	cmd.Flags().StringVar(&version, "version", "", "Release version (e.g. v1.0.0)")
	cmd.Flags().StringVar(&versionMode, "version-mode", "", "Version mode (specific, increment_minor, increment_patch)")
	cmd.Flags().StringVar(&baseCommit, "base-commit", "", "Base commit for change detection")
	cmd.Flags().BoolVar(&includeDemo, "include-demo", false, "Include demo domain when using 'all'")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func newReleaseCmd() *cobra.Command {
	var (
		version        string
		commitSHA      string
		dryRun         bool
		allowOverwrite bool
		createGitTag   bool
	)

	cmd := &cobra.Command{
		Use:   "release <app>",
		Short: "Tag and push a container image to registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return kraken.TagAndPushImage(args[0], version, commitSHA, dryRun, allowOverwrite, createGitTag)
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Release version (required)")
	cmd.Flags().StringVar(&commitSHA, "commit-sha", "", "Commit SHA for tagging")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without executing")
	cmd.Flags().BoolVar(&allowOverwrite, "allow-overwrite", false, "Allow overwriting existing versions")
	cmd.Flags().BoolVar(&createGitTag, "create-git-tag", false, "Create and push a Git tag")
	cmd.MarkFlagRequired("version")
	return cmd
}

func newReleaseMultiarchCmd() *cobra.Command {
	var (
		version   string
		registry  string
		commitSHA string
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "release-multiarch <app>",
		Short: "Release a multi-architecture OCI image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bazelTarget, err := kraken.FindAppBazelTarget(args[0])
			if err != nil {
				return err
			}

			if dryRun {
				fmt.Printf("DRY RUN: Would release multi-arch image for %s version %s\n", args[0], version)
				return nil
			}

			return kraken.ReleaseMultiarchImage(bazelTarget, version, registry, nil, commitSHA)
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Release version (required)")
	cmd.Flags().StringVar(&registry, "registry", "ghcr.io", "Container registry")
	cmd.Flags().StringVar(&commitSHA, "commit-sha", "", "Commit SHA for tagging")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without executing")
	cmd.MarkFlagRequired("version")
	return cmd
}

func newReleaseNotesCmd() *cobra.Command {
	var (
		tag         string
		previousTag string
		formatType  string
	)

	cmd := &cobra.Command{
		Use:   "release-notes <app>",
		Short: "Generate release notes for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			notes, err := kraken.GenerateReleaseNotes(args[0], tag, previousTag, formatType)
			if err != nil {
				return err
			}
			fmt.Println(notes)
			return nil
		},
	}

	cmd.Flags().StringVar(&tag, "tag", "", "Current release tag (required)")
	cmd.Flags().StringVar(&previousTag, "previous-tag", "", "Previous release tag")
	cmd.Flags().StringVar(&formatType, "format", "markdown", "Output format (markdown, plain, json)")
	cmd.MarkFlagRequired("tag")
	return cmd
}

func newSummaryCmd() *cobra.Command {
	var (
		matrixJSON      string
		version         string
		eventType       string
		dryRun          bool
		repositoryOwner string
	)

	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Generate a release summary for GitHub Actions",
		RunE: func(cmd *cobra.Command, args []string) error {
			summary := kraken.GenerateReleaseSummary(matrixJSON, version, eventType, dryRun, repositoryOwner)
			fmt.Println(summary)
			return nil
		},
	}

	cmd.Flags().StringVar(&matrixJSON, "matrix", "", "Release matrix JSON")
	cmd.Flags().StringVar(&version, "version", "", "Release version")
	cmd.Flags().StringVar(&eventType, "event-type", "", "Event type")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Dry run mode")
	cmd.Flags().StringVar(&repositoryOwner, "repository-owner", "", "GitHub repository owner")
	return cmd
}

func newCreateReleaseCmd() *cobra.Command {
	var (
		version      string
		owner        string
		repo         string
		commitSHA    string
		artifactsDir string
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "create-release <app>",
		Short: "Create a GitHub release for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner == "" {
				owner = os.Getenv("GITHUB_REPOSITORY_OWNER")
				if owner == "" {
					return fmt.Errorf("--owner is required (or set GITHUB_REPOSITORY_OWNER)")
				}
			}
			if repo == "" {
				if ghRepo := os.Getenv("GITHUB_REPOSITORY"); ghRepo != "" {
					parts := strings.SplitN(ghRepo, "/", 2)
					if len(parts) == 2 {
						repo = parts[1]
					}
				}
				if repo == "" {
					return fmt.Errorf("--repo is required (or set GITHUB_REPOSITORY)")
				}
			}

			_, err := kraken.CreateReleaseForApp(owner, repo, args[0], version, commitSHA, artifactsDir, dryRun)
			return err
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Release version (required)")
	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name")
	cmd.Flags().StringVar(&commitSHA, "commit-sha", "", "Commit SHA")
	cmd.Flags().StringVar(&artifactsDir, "artifacts-dir", "", "Directory containing build artifacts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without executing")
	cmd.MarkFlagRequired("version")
	return cmd
}
