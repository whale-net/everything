package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// appBazelPkg extracts the Bazel package path from a metadata target label.
// e.g. "//demo/hello_go:hello-go_metadata" → "demo/hello_go"
func appBazelPkg(meta AppMetadata) string {
	rest := strings.TrimPrefix(meta.BazelTarget, "//")
	if idx := strings.Index(rest, ":"); idx >= 0 {
		return rest[:idx]
	}
	return ""
}

func imageLoadTarget(meta AppMetadata) string {
	return fmt.Sprintf("//%s:%s_image_load", appBazelPkg(meta), meta.Name)
}

func imagePushTarget(meta AppMetadata) string {
	return fmt.Sprintf("//%s:%s_image_push", appBazelPkg(meta), meta.Name)
}

// runBazelLive runs bazel with streaming stdout/stderr (no output capture).
func runBazelLive(workspaceRoot string, args ...string) error {
	cmd := exec.Command("bazel", args...)
	cmd.Dir = workspaceRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newBuildCmd() *cobra.Command {
	var platform string

	cmd := &cobra.Command{
		Use:          "build <app-name>",
		Short:        "Build and load a container image for a specific platform",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			apps, err := resolveApps([]string{args[0]}, allApps)
			if err != nil {
				return err
			}
			app := apps[0]

			loadTarget := imageLoadTarget(app)

			bazelArgs := []string{"run"}
			switch platform {
			case "arm64":
				bazelArgs = append(bazelArgs, "--platforms=//tools:linux_arm64")
			case "amd64":
				bazelArgs = append(bazelArgs, "--platforms=//tools:linux_x86_64")
			case "":
				// no platform flag
			default:
				return fmt.Errorf("unknown platform %q: must be amd64 or arm64", platform)
			}
			bazelArgs = append(bazelArgs, loadTarget)

			fmt.Fprintf(cmd.OutOrStdout(), "Building %s for platform %q...\n", app.FullName(), platform)
			if err := runBazelLive(workspaceRoot, bazelArgs...); err != nil {
				return fmt.Errorf("bazel run %s: %w", loadTarget, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Image loaded as: %s:latest\n", app.FullName())
			return nil
		},
	}

	cmd.Flags().StringVar(&platform, "platform", "", "Target platform (amd64 or arm64)")
	return cmd
}

func newReleaseMultiarchCmd() *cobra.Command {
	var (
		version   string
		commitSHA string
		dryRun    bool
		registry  string
	)

	cmd := &cobra.Command{
		Use:          "release-multiarch <app-name>",
		Short:        "Build and release multi-architecture container images",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			apps, err := resolveApps([]string{args[0]}, allApps)
			if err != nil {
				return err
			}
			app := apps[0]

			owner := strings.ToLower(defaultEnv("GITHUB_REPOSITORY_OWNER"))
			if owner == "" {
				owner = "whale-net"
			}
			imageName := app.Domain + "-" + app.Name
			repoPath := fmt.Sprintf("%s/%s/%s", registry, owner, imageName)

			tags := []string{version, "latest"}
			if commitSHA != "" {
				tags = append(tags, commitSHA)
			}

			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), strings.Repeat("=", 80))
				fmt.Fprintln(cmd.OutOrStdout(), "DRY RUN: Multi-architecture release plan")
				fmt.Fprintln(cmd.OutOrStdout(), strings.Repeat("=", 80))
				fmt.Fprintf(cmd.OutOrStdout(), "App:      %s\n", app.FullName())
				fmt.Fprintf(cmd.OutOrStdout(), "Version:  %s\n", version)
				fmt.Fprintf(cmd.OutOrStdout(), "Registry: %s\n", registry)
				fmt.Fprintln(cmd.OutOrStdout(), "")
				fmt.Fprintln(cmd.OutOrStdout(), "Published tags (OCI image index, auto-selects arch):")
				for _, t := range tags {
					fmt.Fprintf(cmd.OutOrStdout(), "  ✓ %s:%s\n", repoPath, t)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "")
				fmt.Fprintln(cmd.OutOrStdout(), "DRY RUN: No images were actually built or pushed")
				return nil
			}

			pushTarget := imagePushTarget(app)
			fmt.Fprintf(cmd.OutOrStdout(), "Releasing %s version %s via %s\n", app.FullName(), version, pushTarget)

			bazelArgs := []string{"run", pushTarget, "--"}
			for _, t := range tags {
				bazelArgs = append(bazelArgs, "--tag", t)
			}

			if err := runBazelLive(workspaceRoot, bazelArgs...); err != nil {
				return fmt.Errorf("bazel run %s: %w", pushTarget, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ Successfully released %s:%s\n", repoPath, version)
			fmt.Fprintf(cmd.OutOrStdout(), "Users can run: docker pull %s:%s\n", repoPath, version)
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "latest", "Version tag")
	cmd.Flags().StringVar(&commitSHA, "commit", "", "Commit SHA for additional tag")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print plan without building or pushing")
	cmd.Flags().StringVar(&registry, "registry", "ghcr.io", "Container registry")
	return cmd
}
