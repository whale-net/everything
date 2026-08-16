package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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

// ExecuteReleaseApp executes the full container image release flow for a matrix leg.
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

	if !isImageApp {
		repoPath = fmt.Sprintf("github.com/%s/everything", owner)

		// 1. BeginPublish (Heartbeat)
		beginPublishSucceeded := false
		if artifactClient != nil && p.BuildID != "" && !p.SkipRegistry {
			beginReq := &pb.BeginPublishRequest{
				Kind:           kind,
				OwnerFullName:  fullName,
				Version:        p.Version,
				BuildId:        p.BuildID,
				IdempotencyKey: fmt.Sprintf("%s-%s-%s-binary-begin", idempotencyPrefix, p.Domain, p.App),
				Repository:     repoPath,
			}
			_, err := artifactClient.BeginPublish(ctx, beginReq)
			if err != nil {
				fmt.Printf("::warning title=App Registry BeginPublish failed::%v\n", err)
			} else {
				beginPublishSucceeded = true
			}
		}

		fmt.Printf("Building non-image application %s (%s)...\n", fullName, matchedApp.AppType)
		if matchedApp.BinaryTarget != "" {
			if _, err := bazel.Run("build", matchedApp.BinaryTarget); err != nil {
				if beginPublishSucceeded && artifactClient != nil {
					failReq := &pb.FailPublishRequest{
						Kind:           kind,
						OwnerFullName:  fullName,
						Version:        p.Version,
						Reason:         fmt.Sprintf("binary build failed (workflow run %s, attempt %s): %v", runID, attempt, err),
						IdempotencyKey: fmt.Sprintf("%s-%s-%s-binary-fail", idempotencyPrefix, p.Domain, p.App),
					}
					_, _ = artifactClient.FailPublish(ctx, failReq)
				}
				return nil, fmt.Errorf("bazel build %s: %w", matchedApp.BinaryTarget, err)
			}
		}

		// Compute binary digest from built target
		pkg, binName := binaryTargetPkgAndName(*matchedApp)
		candidatePaths := []string{
			filepath.Join(workspaceRoot, "bazel-bin", pkg, binName+"_", binName),
			filepath.Join(workspaceRoot, "bazel-bin", pkg, binName),
			filepath.Join(workspaceRoot, "bazel-bin", pkg, matchedApp.Name+"_", matchedApp.Name),
			filepath.Join(workspaceRoot, "bazel-bin", pkg, matchedApp.Name),
		}
		var binaryDigest string
		for _, cp := range candidatePaths {
			if h, _, err := computeFileSHA256(cp); err == nil {
				binaryDigest = "sha256:" + h
				break
			}
		}
		if binaryDigest == "" && sha != "" {
			binaryDigest = "sha256:" + sha
		}

		if p.CreateGitTag && git != nil && sha != "" {
			if _, err := git.Run("rev-parse", tagName); err != nil {
				fmt.Printf("Creating git tag %s...\n", tagName)
				_, _ = git.Run("tag", "-a", tagName, "-m", fmt.Sprintf("Release %s version %s", fullName, p.Version), sha)
			}
		}

		if artifactClient != nil && p.BuildID != "" && !p.SkipRegistry {
			recReq := &pb.RecordArtifactRequest{
				BuildId:        p.BuildID,
				Kind:           kind,
				OwnerFullName:  fullName,
				Repository:     repoPath,
				Version:        p.Version,
				Digest:         binaryDigest,
				PublishedAt:    time.Now().Unix(),
				IdempotencyKey: fmt.Sprintf("%s-%s-%s-binary-record", idempotencyPrefix, p.Domain, p.App),
			}
			if _, err := artifactClient.RecordArtifact(ctx, recReq); err != nil {
				fmt.Printf("::warning title=App Registry RecordArtifact failed::%v\n", err)
			} else {
				fmt.Printf("✅ Recorded %s %s (%s) in App Registry\n", fullName, p.Version, binaryDigest)
			}
		}

		return &ReleaseAppResult{
			Domain:           p.Domain,
			App:              p.App,
			Version:          p.Version,
			EffectiveVersion: p.Version,
			EffectiveTag:     tagName,
			Digest:           binaryDigest,
			Published:        true,
		}, nil
	}

	// 1. BeginPublish (Heartbeat)
	beginPublishSucceeded := false
	if artifactClient != nil && p.BuildID != "" && !p.SkipRegistry {
		beginReq := &pb.BeginPublishRequest{
			Kind:           pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
			OwnerFullName:  fullName,
			Version:        p.Version,
			BuildId:        p.BuildID,
			IdempotencyKey: fmt.Sprintf("%s-%s-%s-image-begin", idempotencyPrefix, p.Domain, p.App),
			Repository:     repoPath,
		}
		_, err := artifactClient.BeginPublish(ctx, beginReq)
		if err != nil {
			fmt.Printf("::warning title=App Registry BeginPublish failed::%v\n", err)
		} else {
			beginPublishSucceeded = true
		}
	}

	// 2. Build and push image via Bazel

	tags := []string{p.Version, "latest"}
	if sha != "" {
		tags = append(tags, sha)
	}

	bazelArgs := []string{"run", pushTarget, "--"}
	for _, t := range tags {
		bazelArgs = append(bazelArgs, "--tag", t)
	}

	fmt.Printf("Building and releasing %s version %s via %s...\n", fullName, p.Version, pushTarget)
	if _, pushErr := bazel.Run(bazelArgs...); pushErr != nil {
		if beginPublishSucceeded && artifactClient != nil {
			failReq := &pb.FailPublishRequest{
				Kind:           pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
				OwnerFullName:  fullName,
				Version:        p.Version,
				Reason:         fmt.Sprintf("image push failed (workflow run %s, attempt %s): %v", runID, attempt, pushErr),
				IdempotencyKey: fmt.Sprintf("%s-%s-%s-image-fail", idempotencyPrefix, p.Domain, p.App),
			}
			_, _ = artifactClient.FailPublish(ctx, failReq)
		}
		return nil, fmt.Errorf("bazel run %s: %w", pushTarget, pushErr)
	}

	// 3. Digest resolution & no-op rebuild check
	newDigest := extractImageDigest(docker, repoPath, p.Version)
	prevTag := getPreviousAppTag(git, fullName, tagName)

	digestUnchanged := false

	if prevTag != "" {
		prevVersion := strings.TrimPrefix(prevTag, fullName+".")
		prevDigest := extractImageDigest(docker, repoPath, prevVersion)
		if newDigest != "" && prevDigest != "" && newDigest == prevDigest {
			digestUnchanged = true
			fmt.Printf("::notice title=Reused digest (%s)::%s has the same image digest (%s) as existing tag %s. Registering new version %s with shared digest.\n",
				fullName, p.Version, newDigest, prevTag, p.Version)
		} else {
			fmt.Printf("Digest check for %s: new(%s)=%s prev(%s)=%s -- proceeding with new tag %s\n",
				fullName, p.Version, defaultStr(newDigest, "<unresolved>"), prevTag, defaultStr(prevDigest, "<unresolved>"), tagName)
		}
	} else {
		fmt.Printf("No previous tag found for %s -- proceeding with new tag %s (digest: %s)\n",
			fullName, tagName, defaultStr(newDigest, "<unresolved>"))
	}

	// 4. Git tag & RecordArtifact on success
	if p.CreateGitTag && git != nil && sha != "" {
		if _, err := git.Run("rev-parse", tagName); err != nil {
			fmt.Printf("Creating git tag %s...\n", tagName)
			_, _ = git.Run("tag", "-a", tagName, "-m", fmt.Sprintf("Release %s version %s", fullName, p.Version), sha)
		}
	}

	if artifactClient != nil && p.BuildID != "" && !p.SkipRegistry {
		if newDigest == "" {
			fmt.Printf("::warning::Could not resolve digest for %s:%s; skipping App Registry recording\n", repoPath, p.Version)
		} else {
			recReq := &pb.RecordArtifactRequest{
				BuildId:        p.BuildID,
				Kind:           pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
				OwnerFullName:  fullName,
				Repository:     repoPath,
				Version:        p.Version,
				Digest:         newDigest,
				PublishedAt:    time.Now().Unix(),
				IdempotencyKey: fmt.Sprintf("%s-%s-%s-image-record", idempotencyPrefix, p.Domain, p.App),
			}
			if _, err := artifactClient.RecordArtifact(ctx, recReq); err != nil {
				fmt.Printf("::warning title=App Registry RecordArtifact failed::%v\n", err)
			} else {
				fmt.Printf("✅ Recorded %s %s (%s) in App Registry\n", fullName, p.Version, newDigest)
			}
		}
	}

	result := &ReleaseAppResult{
		Domain:           p.Domain,
		App:              p.App,
		Version:          p.Version,
		EffectiveVersion: p.Version,
		EffectiveTag:     tagName,
		PreviousTag:      prevTag,
		Digest:           newDigest,
		DigestUnchanged:  digestUnchanged,
		Published:        true,
	}

	// Write digest check JSON artifact if /tmp/digest-check is accessible
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
	if git == nil {
		return ""
	}
	pattern := fmt.Sprintf("%s.v*", fullName)
	out, err := git.Run("tag", "--sort=-version:refname", "--list", pattern)
	if err != nil {
		return ""
	}
	for _, tag := range strings.Split(strings.TrimSpace(out), "\n") {
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == currentTag {
			continue
		}
		if strings.HasPrefix(tag, fullName+".") {
			return tag
		}
	}
	return ""
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
