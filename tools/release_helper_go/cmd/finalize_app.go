package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// FinalizeAppParams configures ExecuteFinalizeApp.
type FinalizeAppParams struct {
	Ctx                  context.Context
	Domain               string
	App                  string
	Version              string
	BuildID              string
	IdempotencyKeyPrefix string
	SkipRegistry         bool
	CreateGitTag         bool
	// Repository/Digest are the already-pushed image's coordinates, as
	// recorded by build-app (BuildAppManifest) and threaded through by the
	// Temporal finalize activity after downloading that manifest -- see
	// finalize.go. Required.
	Repository string
	Digest     string
	// GHCRToken authenticates the registry retag -- see
	// GHCRRetagReleaser/craneAuthOption's doc comments. Falls back to
	// $GHCR_TOKEN, then to the local Docker keychain.
	GHCRToken string

	Git            GitRunner
	ArtifactClient pb.ArtifactRegistryClient
}

func newFinalizeAppCmd() *cobra.Command {
	var (
		domain               string
		app                  string
		version              string
		buildID              string
		idempotencyKeyPrefix string
		skipRegistry         bool
		createGitTag         bool
		manifestPath         string
		repository           string
		digest               string
		ghcrToken            string
		outputDir            string
	)

	cmd := &cobra.Command{
		Use:   "finalize-app",
		Short: "Retag an already-pushed image to its resolved version and record it in App Registry (issue #928)",
		Long: "finalize-app is the \"assign final version\" half of what release-app used to do " +
			"in one step: given an image already pushed by build-app (identified by digest, " +
			"read from --manifest or passed directly via --repository/--digest), it retags the " +
			"registry entry to --version (no rebuild) and runs the same BeginPublish -> " +
			"collision/no-op detection -> RecordArtifact/FailPublish sequence release-app already " +
			"used (see ExecuteRelease) -- reused unchanged, not reimplemented, per issue #928's " +
			"explicit ask not to invent new concurrency-safety logic for this step. Invoked by " +
			"Temporal's FinalizePublish activity (worker/release/finalize.go), not by GHA " +
			"directly.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if domain == "" {
				return fmt.Errorf("missing required flag: --domain")
			}
			if app == "" {
				return fmt.Errorf("missing required flag: --app")
			}
			if version == "" {
				return fmt.Errorf("missing required flag: --version")
			}

			if manifestPath != "" {
				data, err := os.ReadFile(manifestPath)
				if err != nil {
					return fmt.Errorf("read --manifest %s: %w", manifestPath, err)
				}
				var m BuildAppManifest
				if err := json.Unmarshal(data, &m); err != nil {
					return fmt.Errorf("parse --manifest %s: %w", manifestPath, err)
				}
				if repository == "" {
					repository = m.Repository
				}
				if digest == "" {
					digest = m.Digest
				}
			}
			if repository == "" || digest == "" {
				return fmt.Errorf("finalize-app requires --repository and --digest (directly, or via --manifest)")
			}

			if ghcrToken == "" {
				ghcrToken = defaultEnv("GHCR_TOKEN")
			}

			res, err := ExecuteFinalizeApp(FinalizeAppParams{
				Ctx:                  cmd.Context(),
				Domain:               domain,
				App:                  app,
				Version:              version,
				BuildID:              buildID,
				IdempotencyKeyPrefix: idempotencyKeyPrefix,
				SkipRegistry:         skipRegistry,
				CreateGitTag:         createGitTag,
				Repository:           repository,
				Digest:               digest,
				GHCRToken:            ghcrToken,
				Git:                  defaultGit,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Finalized %s-%s (version: %s, effective: %s, digest: %s, published: %t)\n",
				domain, app, version, res.EffectiveVersion, res.Digest, res.Published)

			// --output-dir lets a caller (Temporal's FinalizePublish,
			// worker/release/finalize.go) recover res.EffectiveVersion
			// after this runs as a subprocess -- ExecuteRelease's no-op
			// detection (see ReleaseResult's doc comment) can reuse an
			// older already-published version instead of --version when
			// the digest is unchanged, and a chart composed from this
			// app must pin that actual effective version, not the
			// unpublished one requested here. Mirrors build-app's
			// BuildAppManifest output-dir convention (build_app.go).
			if outputDir != "" {
				if err := os.MkdirAll(outputDir, 0755); err != nil {
					return fmt.Errorf("create --output-dir %s: %w", outputDir, err)
				}
				data, merr := json.MarshalIndent(res, "", "  ")
				if merr != nil {
					return fmt.Errorf("marshal finalize-app result: %w", merr)
				}
				outPath := filepath.Join(outputDir, fmt.Sprintf("%s-%s.json", domain, app))
				if werr := os.WriteFile(outPath, data, 0644); werr != nil {
					return fmt.Errorf("write finalize-app result %s: %w", outPath, werr)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "", "Application domain (e.g. demo, manmanv2)")
	cmd.Flags().StringVar(&app, "app", "", "Application name (e.g. hello-go)")
	cmd.Flags().StringVar(&version, "version", "", "Resolved final release version (e.g. v1.0.0)")
	cmd.Flags().StringVar(&buildID, "build-id", "", "App Registry Build ID")
	cmd.Flags().StringVar(&idempotencyKeyPrefix, "idempotency-key-prefix", "", "Prefix for idempotency keys")
	cmd.Flags().BoolVar(&skipRegistry, "skip-registry", false, "Skip App Registry API interactions")
	cmd.Flags().BoolVar(&createGitTag, "create-git-tag", false, "Create git tag upon successful publication")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to build-app's BuildAppManifest JSON (repository+digest); overrides --repository/--digest when they are unset")
	cmd.Flags().StringVar(&repository, "repository", "", "Image repository (e.g. ghcr.io/whale-net/demo-hello-go); read from --manifest if unset")
	cmd.Flags().StringVar(&digest, "digest", "", "Already-pushed image digest (sha256:...); read from --manifest if unset")
	cmd.Flags().StringVar(&ghcrToken, "ghcr-token", "", "Registry auth token for the retag (default: $GHCR_TOKEN, else local Docker keychain)")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Write the finalize result (including effective_version) as <domain>-<app>.json here; skipped if unset")

	_ = cmd.MarkFlagRequired("domain")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("version")

	return cmd
}

// ExecuteFinalizeApp retags an already-pushed image to p.Version and runs it
// through ExecuteRelease exactly as release-app does, via GHCRRetagReleaser
// instead of ImageReleaser.
func ExecuteFinalizeApp(p FinalizeAppParams) (*ReleaseResult, error) {
	if p.Domain == "" || p.App == "" || p.Version == "" {
		return nil, fmt.Errorf("domain, app, and version are required")
	}
	if p.Repository == "" || p.Digest == "" {
		return nil, fmt.Errorf("repository and digest are required")
	}

	ctx := p.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	git := p.Git
	if git == nil {
		git = defaultGit
	}

	fullName := p.Domain + "-" + p.App
	tagName := fmt.Sprintf("%s.%s", fullName, p.Version)

	var artifactClient pb.ArtifactRegistryClient
	var closeFn func() error
	if p.ArtifactClient != nil {
		artifactClient = p.ArtifactClient
	} else if !p.SkipRegistry {
		var err error
		artifactClient, closeFn, err = NewArtifactRegistryClient(ctx)
		if err != nil {
			artifactClient = nil
		}
	}
	if closeFn != nil {
		defer closeFn() //nolint:errcheck
	}

	releaser := &GHCRRetagReleaser{
		Repository: p.Repository,
		Digest:     p.Digest,
		AuthToken:  p.GHCRToken,
	}

	owner := "whale-net"
	if o := defaultEnv("GITHUB_REPOSITORY_OWNER"); o != "" {
		owner = o
	}

	return ExecuteRelease(ReleaseParams{
		Ctx:                  ctx,
		Domain:               p.Domain,
		OwnerFullName:        fullName,
		Version:              p.Version,
		AllowAutoAdvance:     false,
		BuildID:              p.BuildID,
		IdempotencyKeyPrefix: p.IdempotencyKeyPrefix,
		Repository:           fmt.Sprintf("ghcr.io/%s/%s", owner, fullName),
		SkipRegistry:         p.SkipRegistry,
		CreateGitTag:         p.CreateGitTag,
		TagName:              tagName,
		TagPrefix:            fullName + ".",
		PreviousTagPatterns:  []string{fullName + ".v*"},
		PreviousTagPrefixes:  []string{fullName + "."},
		Releaser:             releaser,
		Git:                  git,
		ArtifactClient:       artifactClient,
	})
}
