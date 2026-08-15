package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		Short:        "Build binary or container image for a specific platform",
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

			if app.AppType == "cli" || app.AppType == "binary" {
				bazelArgs := []string{"build"}
				switch platform {
				case "arm64", "linux_arm64":
					bazelArgs = append(bazelArgs, "--platforms=//tools:linux_arm64")
				case "amd64", "linux_amd64":
					bazelArgs = append(bazelArgs, "--platforms=//tools:linux_x86_64")
				case "darwin_arm64", "macos_arm64":
					bazelArgs = append(bazelArgs, "--platforms=//tools:darwin_arm64")
				case "darwin_amd64", "macos_amd64":
					bazelArgs = append(bazelArgs, "--platforms=//tools:darwin_x86_64")
				case "":
					// default host platform
				default:
					return fmt.Errorf("unknown platform %q", platform)
				}
				bazelArgs = append(bazelArgs, app.BinaryTarget)

				fmt.Fprintf(cmd.OutOrStdout(), "Building %s binary (%s) for platform %q...\n", app.FullName(), app.BinaryTarget, platform)
				if err := runBazelLive(workspaceRoot, bazelArgs...); err != nil {
					return fmt.Errorf("bazel build %s: %w", app.BinaryTarget, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Binary built: %s\n", app.BinaryTarget)
				return nil
			}

			if app.AppType == "firmware" {
				fwTarget := firmwareTarget(app)
				bazelArgs := []string{"build", "--config=esp32", fwTarget}
				fmt.Fprintf(cmd.OutOrStdout(), "Building %s firmware (%s)...\n", app.FullName(), fwTarget)
				if err := runBazelLive(workspaceRoot, bazelArgs...); err != nil {
					return fmt.Errorf("bazel build %s: %w", fwTarget, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Firmware built: %s\n", fwTarget)
				return nil
			}

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

	cmd.Flags().StringVar(&platform, "platform", "", "Target platform (amd64, arm64, darwin_arm64)")
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
		Short:        "Build and release multi-architecture container images or binary/firmware packages",
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

			if app.AppType == "cli" || app.AppType == "binary" || app.AppType == "firmware" {
				outputDir := filepath.Join(os.TempDir(), "release-assets", app.FullName())
				if err := os.MkdirAll(outputDir, 0755); err != nil {
					return fmt.Errorf("create output dir: %w", err)
				}

				if dryRun {
					fmt.Fprintln(cmd.OutOrStdout(), strings.Repeat("=", 80))
					fmt.Fprintf(cmd.OutOrStdout(), "DRY RUN: %s release plan\n", strings.ToUpper(app.AppType))
					fmt.Fprintln(cmd.OutOrStdout(), strings.Repeat("=", 80))
					fmt.Fprintf(cmd.OutOrStdout(), "App:      %s\n", app.FullName())
					fmt.Fprintf(cmd.OutOrStdout(), "Type:     %s\n", app.AppType)
					fmt.Fprintf(cmd.OutOrStdout(), "Version:  %s\n", version)
					fmt.Fprintf(cmd.OutOrStdout(), "Target:   %s\n", app.BinaryTarget)
					fmt.Fprintln(cmd.OutOrStdout(), "")
					fmt.Fprintln(cmd.OutOrStdout(), "DRY RUN: No assets were actually built or published")
					return nil
				}

				fmt.Fprintf(cmd.OutOrStdout(), "Building and packaging %s assets for %s version %s...\n", app.AppType, app.FullName(), version)
				assets, err := PackageAppAssets(defaultBazel, defaultFS, workspaceRoot, app, outputDir, nil)
				if err != nil {
					return fmt.Errorf("package assets for %s: %w", app.FullName(), err)
				}

				fmt.Fprintf(cmd.OutOrStdout(), "✓ Packaged %d assets in %s:\n", len(assets), outputDir)
				for _, a := range assets {
					fmt.Fprintf(cmd.OutOrStdout(), "  - %s (SHA256: %s, %d bytes)\n", a.Name, a.SHA256, a.Size)
				}

				// Upload to GitHub Release if GITHUB_TOKEN is available
				token := defaultEnv("GITHUB_TOKEN")
				if token != "" {
					owner := defaultEnv("GITHUB_REPOSITORY_OWNER")
					if owner == "" {
						owner = "whale-net"
					}
					repo := defaultEnv("GITHUB_REPOSITORY_NAME")
					if repo == "" {
						repo = "everything"
					}
					gh := newGHReleaseClient(owner, repo, token)
					tagName := fmt.Sprintf("%s.%s", app.FullName(), version)
					releaseResp, err := gh.getByTag(tagName)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Error checking release %s: %v\n", tagName, err)
					}
					if releaseResp == nil {
						releaseResp, err = gh.create(ghReleasePayload{
							TagName: tagName,
							Name:    tagName,
							Body:    fmt.Sprintf("Release %s for %s", version, app.FullName()),
						})
						if err != nil {
							return fmt.Errorf("create release %s: %w", tagName, err)
						}
					}
					if releaseResp != nil && releaseResp.ID != 0 {
						for _, a := range assets {
							if uploadErr := gh.uploadAsset(releaseResp.ID, a.Path, a.Name); uploadErr != nil {
								fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Error uploading asset %s: %v\n", a.Name, uploadErr)
							}
						}
						for _, sumFile := range []string{"checksums.txt", "SHA256SUMS"} {
							sumPath := filepath.Join(outputDir, sumFile)
							if _, err := os.Stat(sumPath); err == nil {
								if uploadErr := gh.uploadAsset(releaseResp.ID, sumPath, sumFile); uploadErr != nil {
									fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Error uploading checksum %s: %v\n", sumFile, uploadErr)
								}
							}
						}
					}
				}

				// Record in App Registry if opt-in is enabled
				if defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" && len(assets) > 0 {
					owner := defaultEnv("GITHUB_REPOSITORY_OWNER")
					repo := defaultEnv("GITHUB_REPOSITORY_NAME")
					buildID := defaultEnv("APP_REGISTRY_BUILD_ID")
					warn := func(msg string) { fmt.Fprintf(cmd.ErrOrStderr(), "::warning::%s\n", msg) }
					if recordErr := recordPublishedArtifact(cmd.Context(), warn, app, version, assets[0].SHA256, owner, repo, buildID); recordErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "⚠ App Registry record failed for %s: %v\n", app.FullName(), recordErr)
					}
				}

				fmt.Fprintf(cmd.OutOrStdout(), "✓ Successfully released %s %s\n", app.FullName(), version)
				return nil
			}

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
