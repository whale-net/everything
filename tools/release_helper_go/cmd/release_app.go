package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

var imageDigestRE = regexp.MustCompile(`(?m)^Digest:\s+([a-zA-Z0-9_+-]+:[a-fA-F0-9]+)`)

// ReleaseAppParams encapsulates all dependencies and configuration for release-app.
type ReleaseAppParams struct {
	Ctx                  context.Context
	Domain               string
	App                  string
	Version              string
	BuildID              string
	IdempotencyKeyPrefix string
	DryRun               bool
	GitSHA               string
	Registry             string
	SkipRegistry         bool
	CreateGitTag         bool

	Bazel          BazelRunner
	Git            GitRunner
	Docker         DockerRunner
	FS             FileSystem
	WorkspaceRoot  string
	ArtifactClient pb.ArtifactRegistryClient
}

// ReleaseAppResult contains the outcome of an image release execution.
type ReleaseAppResult struct {
	Domain           string `json:"domain"`
	App              string `json:"app"`
	Version          string `json:"version"`
	EffectiveVersion string `json:"effective_version"`
	EffectiveTag     string `json:"effective_tag"`
	PreviousTag      string `json:"previous_tag,omitempty"`
	Digest           string `json:"digest,omitempty"`
	DigestUnchanged  bool   `json:"digest_unchanged"`
	Published        bool   `json:"published"`
}

func newReleaseAppCmd() *cobra.Command {
	var (
		domain               string
		app                  string
		version              string
		buildID              string
		idempotencyKeyPrefix string
		dryRun               bool
		gitSHA               string
		registry             string
		skipRegistry         bool
		createGitTag         bool
	)

	cmd := &cobra.Command{
		Use:          "release-app",
		Short:        "Execute full image release flow for an application matrix leg",
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

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			res, err := ExecuteReleaseApp(ReleaseAppParams{
				Ctx:                  cmd.Context(),
				Domain:               domain,
				App:                  app,
				Version:              version,
				BuildID:              buildID,
				IdempotencyKeyPrefix: idempotencyKeyPrefix,
				DryRun:               dryRun,
				GitSHA:               gitSHA,
				Registry:             registry,
				SkipRegistry:         skipRegistry,
				CreateGitTag:         createGitTag,
				Bazel:                defaultBazel,
				Git:                  defaultGit,
				Docker:               defaultDocker,
				FS:                   defaultFS,
				WorkspaceRoot:        workspaceRoot,
			})
			if err != nil {
				return err
			}

			if dryRun {
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Successfully processed release-app for %s-%s (version: %s, effective version: %s, digest: %s, published: %t)\n",
				domain, app, version, res.EffectiveVersion, res.Digest, res.Published)
			return nil
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "", "Application domain (e.g. demo, manmanv2)")
	cmd.Flags().StringVar(&app, "app", "", "Application name (e.g. hello-go)")
	cmd.Flags().StringVar(&version, "version", "", "Release version (e.g. v1.0.0)")
	cmd.Flags().StringVar(&buildID, "build-id", "", "App Registry Build ID")
	cmd.Flags().StringVar(&idempotencyKeyPrefix, "idempotency-key-prefix", "", "Prefix for idempotency keys")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print release plan without executing push or mutating registry")
	cmd.Flags().StringVar(&gitSHA, "git-sha", "", "Git commit SHA")
	cmd.Flags().StringVar(&registry, "registry", "ghcr.io", "Container registry")
	cmd.Flags().BoolVar(&skipRegistry, "skip-registry", false, "Skip App Registry API interactions")
	cmd.Flags().BoolVar(&createGitTag, "create-git-tag", false, "Create git tag upon successful publication")

	_ = cmd.MarkFlagRequired("domain")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("version")

	return cmd
}

// ExecuteReleaseApp executes the full container image or binary release flow for a matrix leg.
func ExecuteReleaseApp(p ReleaseAppParams) (*ReleaseAppResult, error) {
	if p.Domain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	if p.App == "" {
		return nil, fmt.Errorf("app is required")
	}
	if p.Version == "" {
		return nil, fmt.Errorf("version is required")
	}

	ctx := p.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	workspaceRoot := p.WorkspaceRoot
	if workspaceRoot == "" {
		var err error
		workspaceRoot, err = defaultWorkspaceRoot()
		if err != nil {
			return nil, fmt.Errorf("workspace root: %w", err)
		}
	}

	bazel := p.Bazel
	if bazel == nil {
		bazel = defaultBazel
	}
	git := p.Git
	if git == nil {
		git = defaultGit
	}
	docker := p.Docker
	if docker == nil {
		docker = defaultDocker
	}
	fs := p.FS
	if fs == nil {
		fs = defaultFS
	}

	allApps, err := ListAllApps(bazel, fs, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("list all apps: %w", err)
	}

	matchedApp, err := findAppByDomainAndName(p.Domain, p.App, allApps)
	if err != nil {
		return nil, err
	}

	fullName := matchedApp.FullName()
	tagName := fmt.Sprintf("%s.%s", fullName, p.Version)

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

	sha := p.GitSHA
	if sha == "" {
		sha = defaultEnv("GITHUB_SHA")
	}
	if sha == "" && git != nil {
		if out, err := git.Run("rev-parse", "HEAD"); err == nil {
			sha = strings.TrimSpace(out)
		}
	}

	// Determine idempotency prefix
	runID := defaultEnv("GITHUB_RUN_ID")
	if runID == "" {
		runID = "local"
	}
	attempt := defaultEnv("GITHUB_RUN_ATTEMPT")
	if attempt == "" {
		attempt = "1"
	}
	idempotencyPrefix := p.IdempotencyKeyPrefix
	if idempotencyPrefix == "" {
		idempotencyPrefix = fmt.Sprintf("%s-%s", runID, attempt)
	}

	// App Registry client setup
	var artifactClient pb.ArtifactRegistryClient
	if p.ArtifactClient != nil {
		artifactClient = p.ArtifactClient
	} else if !p.SkipRegistry {
		c, closeFn, err := NewArtifactRegistryClient(ctx)
		if err == nil {
			artifactClient = c
			if closeFn != nil {
				defer closeFn() //nolint:errcheck
			}
		}
	}

	kind := determineArtifactKind(*matchedApp)
	isImageApp := kind == pb.ArtifactKind_ARTIFACT_KIND_IMAGE

	if p.DryRun {
		fmt.Println(strings.Repeat("=", 80))
		if isImageApp {
			fmt.Printf("DRY RUN: Image release plan for %s\n", fullName)
			fmt.Println(strings.Repeat("=", 80))
			fmt.Printf("Domain:      %s\n", p.Domain)
			fmt.Printf("App:         %s\n", p.App)
			fmt.Printf("Version:     %s\n", p.Version)
			fmt.Printf("Target:      %s\n", pushTarget)
			fmt.Printf("Repository:  %s\n", repoPath)
			fmt.Printf("Tags:        %s:%s, %s:latest\n", repoPath, p.Version, repoPath)
			fmt.Println("DRY RUN: No images were built or pushed, App Registry was not mutated.")
		} else {
			fmt.Printf("DRY RUN: Non-image release plan for %s (%s)\n", fullName, matchedApp.AppType)
			fmt.Println(strings.Repeat("=", 80))
			fmt.Printf("Domain:      %s\n", p.Domain)
			fmt.Printf("App:         %s\n", p.App)
			fmt.Printf("Version:     %s\n", p.Version)
			fmt.Printf("Target:      %s\n", matchedApp.BinaryTarget)
			fmt.Println("DRY RUN: No binaries were built or tags created.")
		}
		return &ReleaseAppResult{
			Domain:           p.Domain,
			App:              p.App,
			Version:          p.Version,
			EffectiveVersion: p.Version,
			EffectiveTag:     tagName,
			Published:        false,
		}, nil
	}

	var releaser ArtifactReleaser
	var artifactRepo string
	if isImageApp {
		artifactRepo = repoPath
		releaser = &ImageReleaser{
			Domain:     p.Domain,
			App:        matchedApp,
			PushTarget: pushTarget,
			RepoPath:   repoPath,
			GitSHA:     sha,
			Bazel:      bazel,
			Docker:     docker,
		}
	} else {
		artifactRepo = fmt.Sprintf("github.com/%s/everything", owner)
		releaser = &BinaryReleaser{
			Domain:         p.Domain,
			App:            matchedApp,
			WorkspaceRoot:  workspaceRoot,
			GitSHA:         sha,
			KindType:       kind,
			Bazel:          bazel,
			ArtifactClient: artifactClient,
		}
	}

	execRes, err := ExecuteRelease(ReleaseParams{
		Ctx:                  ctx,
		Domain:               p.Domain,
		OwnerFullName:        fullName,
		Version:              p.Version,
		AllowAutoAdvance:     false,
		BuildID:              p.BuildID,
		IdempotencyKeyPrefix: idempotencyPrefix,
		GitSHA:               sha,
		Repository:           artifactRepo,
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
	if err != nil {
		return nil, err
	}

	result := &ReleaseAppResult{
		Domain:           p.Domain,
		App:              p.App,
		Version:          p.Version,
		EffectiveVersion: execRes.EffectiveVersion,
		EffectiveTag:     execRes.EffectiveTag,
		PreviousTag:      execRes.PreviousTag,
		Digest:           execRes.Digest,
		DigestUnchanged:  execRes.DigestUnchanged,
		Published:        execRes.Published,
	}

	writeDigestCheckJSON(p.Domain, p.App, result)
	return result, nil
}

func findAppByDomainAndName(domain, appName string, apps []AppMetadata) (*AppMetadata, error) {
	for i := range apps {
		a := &apps[i]
		if a.Domain == domain && a.Name == appName {
			return a, nil
		}
		if a.FullName() == appName && a.Domain == domain {
			return a, nil
		}
		if a.FullName() == domain+"-"+appName {
			return a, nil
		}
	}
	for i := range apps {
		a := &apps[i]
		if a.FullName() == appName || (a.Name == appName && a.Domain == domain) {
			return a, nil
		}
	}
	return nil, fmt.Errorf("app %q not found in domain %q", appName, domain)
}

func extractImageDigest(docker DockerRunner, repo, version string) string {
	if docker == nil {
		return ""
	}
	ref := fmt.Sprintf("%s:%s", repo, version)
	out, err := docker.Run("buildx", "imagetools", "inspect", ref)
	if err != nil {
		return ""
	}
	matches := imageDigestRE.FindStringSubmatch(out)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Digest:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}

func getPreviousAppTag(git GitRunner, fullName, currentTag string) string {
	pattern := fmt.Sprintf("%s.v*", fullName)
	return GetPreviousGitTag(git, currentTag, []string{pattern}, []string{fullName + "."})
}

func writeDigestCheckJSON(domain, app string, result *ReleaseAppResult) {
	digestCheckDir := "/tmp/digest-check"
	if err := os.MkdirAll(digestCheckDir, 0755); err != nil {
		return
	}
	filePath := filepath.Join(digestCheckDir, fmt.Sprintf("%s-%s.json", domain, app))
	payload, err := json.MarshalIndent(map[string]interface{}{
		"domain":            domain,
		"app":               app,
		"digest_unchanged":  result.DigestUnchanged,
		"effective_version": result.EffectiveVersion,
		"effective_tag":     result.EffectiveTag,
		"previous_tag":      result.PreviousTag,
		"digest":            result.Digest,
	}, "", "  ")
	if err == nil {
		_ = os.WriteFile(filePath, payload, 0644)
	}
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
