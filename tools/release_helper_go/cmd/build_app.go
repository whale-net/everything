package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
			if manifest == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s/%s is not an image app; skipped image build/push\n", domain, app)
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

	// Only image-kind apps have a bazel *_image_push target at all
	// (release.bzl's is_container_app gate) -- cli/binary/firmware apps are
	// published a different way entirely (package-assets + S3, see
	// tools/app_registry/worker/release/finalize.go's publishCLIBinaries),
	// not through this build/push-by-digest step. Skip rather than error:
	// this command is now called unconditionally for every app in a
	// release matrix (see release-v2.yml's "Build app images" step), and a
	// non-image app in that matrix is expected, not a mistake.
	if determineArtifactKind(*matchedApp) != pb.ArtifactKind_ARTIFACT_KIND_IMAGE {
		fmt.Printf("%s is a %q app, not an image; skipping image build/push\n", matchedApp.FullName(), matchedApp.AppType)
		return nil, nil
	}

	fullName := matchedApp.FullName()

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
	buildStart := time.Now()
	// This is the highest-bandwidth bazel invocation in the release pipeline
	// (pulling multi-platform image layers out of remote cache before
	// pushing to ghcr.io); bazelRunToDisk's --config=ci-images ensures they
	// land on local disk with build:ci's CI tuning applied.
	if _, err := bazelRunToDisk(bazel, "run", pushTarget, "--", "--tag", p.GitSHA); err != nil {
		return nil, fmt.Errorf("bazel run %s: %w", pushTarget, err)
	}
	buildDuration := time.Since(buildStart)

	digestStart := time.Now()
	digest := extractImageDigest(docker, repoPath, p.GitSHA)
	if digest == "" {
		return nil, fmt.Errorf("could not resolve pushed digest for %s:%s", repoPath, p.GitSHA)
	}
	fmt.Printf("Built and pushed %s in %s (digest lookup: %s)\n",
		fullName, buildDuration.Round(time.Second), time.Since(digestStart).Round(time.Second))

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
