package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// BuildAppManifest is build-app's output: everything finalize-app (issue
// #928) needs to retag the already-pushed image to its resolved version --
// no rebuild, no re-push. Written to <output-dir>/<domain>-<app>.json by
// ExecuteBuildApp, and to the same "digest by full name" shape
// finalize.go's manifest reader expects.
type BuildAppManifest struct {
	Domain     string `json:"domain"`
	App        string `json:"app"`
	FullName   string `json:"full_name"`
	Repository string `json:"repository"`
	// BuildTag is the interim tag actually pushed (the git SHA) -- present
	// for operator debugging; not needed to retag, since Digest is the
	// stable identifier a registry retag keys off.
	BuildTag string `json:"build_tag"`
	Digest   string `json:"digest"`
	// Skipped is true for a non-image app (cli/binary/firmware, per
	// determineArtifactKind) -- these are published via a separate path
	// (build-cli-binaries + FinalizePublish's cliBinaryTargets S3 publish,
	// not the digest-tagged image build/finalize-app retag this command and
	// finalize-app implement), so no image push happens and no manifest
	// file is written for them. Repository/Digest/BuildTag are empty.
	Skipped bool `json:"skipped,omitempty"`
}

// BuildAppParams configures ExecuteBuildApp.
type BuildAppParams struct {
	Ctx           context.Context
	Domain        string
	App           string
	GitSHA        string
	Registry      string
	OutputDir     string
	Bazel         BazelRunner
	Docker        DockerRunner
	WorkspaceRoot string
}

func newBuildAppCmd() *cobra.Command {
	var (
		domain    string
		app       string
		gitSHA    string
		registry  string
		outputDir string
	)

	cmd := &cobra.Command{
		Use:   "build-app",
		Short: "Build and push an application image by digest, without assigning a final version tag (issue #928)",
		Long: "build-app builds and pushes a container image tagged only with the build-scoped " +
			"git SHA (no version tag, no \"latest\", no App Registry mutation, no git tag) and " +
			"records the resulting digest to a manifest file. It is the release-trigger-time " +
			"\"build and push\" half of what release-app used to do in one step -- the \"assign " +
			"final version\" half now happens later, in Temporal's finalize-app (see " +
			"tools/app_registry/worker/release/finalize.go).",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if domain == "" {
				return fmt.Errorf("missing required flag: --domain")
			}
			if app == "" {
				return fmt.Errorf("missing required flag: --app")
			}
			if gitSHA == "" {
				gitSHA = defaultEnv("GITHUB_SHA")
			}
			if gitSHA == "" {
				return fmt.Errorf("missing required flag: --git-sha (and GITHUB_SHA is unset)")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			manifest, err := ExecuteBuildApp(BuildAppParams{
				Ctx:           cmd.Context(),
				Domain:        domain,
				App:           app,
				GitSHA:        gitSHA,
				Registry:      registry,
				OutputDir:     outputDir,
				Bazel:         defaultBazel,
				Docker:        defaultDocker,
				WorkspaceRoot: workspaceRoot,
			})
			if err != nil {
				return err
			}

			if manifest.Skipped {
				fmt.Fprintf(cmd.OutOrStdout(), "Skipped image build/push for %s (not an image app; published separately)\n", manifest.FullName)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Built and pushed %s (tag: %s, digest: %s)\n", manifest.FullName, manifest.BuildTag, manifest.Digest)
			return nil
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "", "Application domain (e.g. demo, manmanv2)")
	cmd.Flags().StringVar(&app, "app", "", "Application name (e.g. hello-go)")
	cmd.Flags().StringVar(&gitSHA, "git-sha", "", "Git commit SHA to tag the build-scoped push with (default: $GITHUB_SHA)")
	cmd.Flags().StringVar(&registry, "registry", "ghcr.io", "Container registry")
	cmd.Flags().StringVar(&outputDir, "output-dir", "/tmp/build-manifest/apps", "Directory to write the <domain>-<app>.json build manifest to")

	_ = cmd.MarkFlagRequired("domain")
	_ = cmd.MarkFlagRequired("app")

	return cmd
}

// ExecuteBuildApp builds and pushes an application's container image tagged
// only with p.GitSHA (no version, no "latest"), extracts the resulting
// digest, and writes a BuildAppManifest to <p.OutputDir>/<domain>-<app>.json.
func ExecuteBuildApp(p BuildAppParams) (*BuildAppManifest, error) {
	if p.Domain == "" || p.App == "" {
		return nil, fmt.Errorf("domain and app are required")
	}
	if p.GitSHA == "" {
		return nil, fmt.Errorf("git SHA is required")
	}

	bazel := p.Bazel
	if bazel == nil {
		bazel = defaultBazel
	}
	docker := p.Docker
	if docker == nil {
		docker = defaultDocker
	}
	workspaceRoot := p.WorkspaceRoot
	if workspaceRoot == "" {
		var err error
		workspaceRoot, err = defaultWorkspaceRoot()
		if err != nil {
			return nil, fmt.Errorf("workspace root: %w", err)
		}
	}

	allApps, err := ListAllApps(bazel, defaultFS, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("list all apps: %w", err)
	}
	matchedApp, err := findAppByDomainAndName(p.Domain, p.App, allApps)
	if err != nil {
		return nil, err
	}
	fullName := matchedApp.FullName()

	// cli/binary/firmware apps have no digest-tagged interim image to build
	// or push -- release_app.go's ExecuteReleaseApp already branches on this
	// same determineArtifactKind check (isImageApp) to use a BinaryReleaser
	// instead of ImageReleaser, and worker/release/finalize.go's
	// cliBinaryTargets routes these same two apps to an S3 binary publish
	// instead of calling finalize-app at all. build-app/finalize-app (the
	// #928 two-phase digest-commit split) only exists for image apps; skip
	// cleanly here rather than trying imagePushTarget's would-be
	// "<name>_image_push" against a Bazel package that never declares one
	// (release.bzl's release_app macro only generates an image target when
	// app_type is a container type -- see is_container_app).
	if kind := determineArtifactKind(*matchedApp); kind != pb.ArtifactKind_ARTIFACT_KIND_IMAGE {
		return &BuildAppManifest{
			Domain:   p.Domain,
			App:      p.App,
			FullName: fullName,
			Skipped:  true,
		}, nil
	}

	reg := p.Registry
	if reg == "" {
		reg = "ghcr.io"
	}
	owner := strings.ToLower(defaultEnv("GITHUB_REPOSITORY_OWNER"))
	if owner == "" {
		owner = "whale-net"
	}
	repoPath := fmt.Sprintf("%s/%s/%s", reg, owner, fullName)
	pushTarget := imagePushTarget(*matchedApp)

	fmt.Printf("Building and pushing %s (build tag: %s) via %s...\n", fullName, p.GitSHA, pushTarget)
	if _, err := bazel.Run("run", pushTarget, "--", "--tag", p.GitSHA); err != nil {
		return nil, fmt.Errorf("bazel run %s: %w", pushTarget, err)
	}

	digest := extractImageDigest(docker, repoPath, p.GitSHA)
	if digest == "" {
		return nil, fmt.Errorf("could not resolve pushed digest for %s:%s", repoPath, p.GitSHA)
	}

	manifest := &BuildAppManifest{
		Domain:     p.Domain,
		App:        p.App,
		FullName:   fullName,
		Repository: repoPath,
		BuildTag:   p.GitSHA,
		Digest:     digest,
	}

	outputDir := p.OutputDir
	if outputDir == "" {
		outputDir = "/tmp/build-manifest/apps"
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir %s: %w", outputDir, err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal build manifest: %w", err)
	}
	outPath := filepath.Join(outputDir, fmt.Sprintf("%s.json", fullName))
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write build manifest %s: %w", outPath, err)
	}

	return manifest, nil
}
