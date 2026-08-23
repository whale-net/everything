package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ArtifactReleaser defines the kind-specific packaging, fetching, and publication operations.
type ArtifactReleaser interface {
	Kind() pb.ArtifactKind
	Build(ctx context.Context, version string) (digest string, err error)
	FetchPublished(ctx context.Context, version string) (exists bool, digest string, contentMatches bool, err error)
	Publish(ctx context.Context, version string) error
}

// ContainedImagesProvider is an optional interface for releasers that supply contained images (e.g. Helm charts).
type ContainedImagesProvider interface {
	ContainedImages() []*pb.ContainedImage
}

// ReleaseParams encapsulates all configuration and dependencies for executing a release.
type ReleaseParams struct {
	Ctx                  context.Context
	Domain               string
	OwnerFullName        string
	Version              string
	AllowAutoAdvance     bool
	BuildID              string
	IdempotencyKeyPrefix string
	DryRun               bool
	GitSHA               string
	Repository           string
	SkipRegistry         bool
	CreateGitTag         bool
	TagName              string
	TagPrefix            string
	PreviousTagPatterns  []string
	PreviousTagPrefixes  []string

	Releaser       ArtifactReleaser
	Git            GitRunner
	ArtifactClient pb.ArtifactRegistryClient
}

// ReleaseResult contains the outcome of a release execution.
type ReleaseResult struct {
	Domain           string               `json:"domain"`
	OwnerFullName    string               `json:"owner_full_name"`
	Version          string               `json:"version"`
	EffectiveVersion string               `json:"effective_version"`
	EffectiveTag     string               `json:"effective_tag"`
	PreviousTag      string               `json:"previous_tag,omitempty"`
	Digest           string               `json:"digest,omitempty"`
	DigestUnchanged  bool                 `json:"digest_unchanged"`
	Published        bool                 `json:"published"`
	ContainedImages  []*pb.ContainedImage `json:"contained_images,omitempty"`
}

func artifactKindShortName(kind pb.ArtifactKind) string {
	switch kind {
	case pb.ArtifactKind_ARTIFACT_KIND_IMAGE:
		return "image"
	case pb.ArtifactKind_ARTIFACT_KIND_CHART:
		return "chart"
	case pb.ArtifactKind_ARTIFACT_KIND_BINARY:
		return "binary"
	case pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE:
		return "firmware"
	default:
		return "artifact"
	}
}

// ExecuteRelease orchestrates the end-to-end lifecycle for any artifact kind:
// 1. App Registry publication heartbeat (BeginPublish)
// 2. Kind-specific build / packaging
// 3. Collision resolution & no-op detection
// 4. Backing store publication
// 5. Git tagging
// 6. Final registration (RecordArtifact) or cleanup on failure/no-op (FailPublish)
func ExecuteRelease(p ReleaseParams) (*ReleaseResult, error) {
	if p.Releaser == nil {
		return nil, fmt.Errorf("releaser is required")
	}

	ctx := p.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	git := p.Git
	if git == nil {
		git = defaultGit
	}
	sha := p.GitSHA
	if sha == "" {
		sha = defaultEnv("GITHUB_SHA")
	}
	if sha == "" && git != nil {
		if out, err := git.Run("rev-parse", "HEAD"); err == nil {
			sha = strings.TrimSpace(out)
		}
	}

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

	kind := p.Releaser.Kind()
	kindName := artifactKindShortName(kind)

	tagPrefix := p.TagPrefix
	if tagPrefix == "" {
		tagPrefix = p.OwnerFullName + "."
	}
	tagName := p.TagName
	if tagName == "" {
		tagName = tagPrefix + p.Version
	}

	// isAllocate: whether App Registry is authoritative for this release --
	// true whenever the caller opted a client in. Every domain allocates
	// unconditionally, so this is just "is the registry integration active
	// for this run," not a registry round-trip.
	isAllocate := p.ArtifactClient != nil && !p.SkipRegistry

	// 1. BeginPublish (Heartbeat)
	beginPublishSucceeded := false
	if p.ArtifactClient != nil && p.BuildID != "" && !p.SkipRegistry {
		beginReq := &pb.BeginPublishRequest{
			Kind:           kind,
			OwnerFullName:  p.OwnerFullName,
			Version:        p.Version,
			BuildId:        p.BuildID,
			IdempotencyKey: fmt.Sprintf("%s-%s-%s-begin", idempotencyPrefix, p.OwnerFullName, kindName),
			Repository:     p.Repository,
		}
		_, err := p.ArtifactClient.BeginPublish(ctx, beginReq)
		if err != nil {
			return nil, fmt.Errorf("App Registry BeginPublish failed for %s (domain %q): %w", p.OwnerFullName, p.Domain, err)
		}
		beginPublishSucceeded = true
	}

	// 2. Build / packaging step
	digest, buildErr := p.Releaser.Build(ctx, p.Version)
	if buildErr != nil {
		if beginPublishSucceeded && p.ArtifactClient != nil {
			failReq := &pb.FailPublishRequest{
				Kind:           kind,
				OwnerFullName:  p.OwnerFullName,
				Version:        p.Version,
				Reason:         fmt.Sprintf("%s build failed (workflow run %s, attempt %s): %v", kindName, runID, attempt, buildErr),
				IdempotencyKey: fmt.Sprintf("%s-%s-%s-fail", idempotencyPrefix, p.OwnerFullName, kindName),
			}
			_, _ = p.ArtifactClient.FailPublish(ctx, failReq)
		}
		return nil, buildErr
	}

	// 3. Collision resolution & no-op detection
	currentVersion := p.Version
	currentTag := tagName
	digestUnchanged := false
	effectiveVersion := currentVersion
	effectiveTag := currentTag

	if p.AllowAutoAdvance || p.Releaser.Kind() == pb.ArtifactKind_ARTIFACT_KIND_CHART {
		collisionRes, colErr := ResolveCandidateCollision(
			currentVersion,
			tagPrefix,
			p.AllowAutoAdvance,
			func(v string) (bool, bool, error) {
				exists, _, matches, fErr := p.Releaser.FetchPublished(ctx, v)
				if fErr != nil || !exists {
					return false, false, fErr
				}
				return true, matches, nil
			},
			func(newVer string) error {
				newDigest, bErr := p.Releaser.Build(ctx, newVer)
				if bErr != nil {
					return bErr
				}
				digest = newDigest
				return nil
			},
		)
		if colErr == nil && collisionRes != nil {
			if collisionRes.DigestUnchanged {
				digestUnchanged = true
				fmt.Printf("::notice title=Chart package already published (%s)::%s is already present on ChartMuseum with identical content (%s). Skipping re-upload.\n",
					p.OwnerFullName, currentVersion, digest)
				if beginPublishSucceeded && p.ArtifactClient != nil {
					failReq := &pb.FailPublishRequest{
						Kind:           kind,
						OwnerFullName:  p.OwnerFullName,
						Version:        currentVersion,
						Reason:         fmt.Sprintf("already published with identical content -- skipping re-upload (workflow run %s, attempt %s)", runID, attempt),
						IdempotencyKey: fmt.Sprintf("%s-%s-%s-fail-noop", idempotencyPrefix, p.OwnerFullName, kindName),
					}
					_, _ = p.ArtifactClient.FailPublish(ctx, failReq)
				}
			} else if collisionRes.Advanced {
				currentVersion = collisionRes.Version
				currentTag = collisionRes.Tag
				effectiveVersion = currentVersion
				effectiveTag = currentTag
				fmt.Printf("::notice title=Chart version collision resolved (%s)::Advanced to %s to avoid collision with orphaned ChartMuseum package.\n",
					p.OwnerFullName, currentVersion)
			}
		}
	}

	var prevTag string

	if !digestUnchanged {
		var prevVersion string
		var prevMatches bool
		var prevDigest string

		if isAllocate {
			getResp, getErr := p.ArtifactClient.GetArtifact(ctx, &pb.GetArtifactRequest{
				OwnerFullName:   p.OwnerFullName,
				Kind:            kind,
				LatestPublished: true,
				BeforeVersion:   currentVersion,
			})
			if getErr != nil {
				if status.Code(getErr) != codes.NotFound {
					return nil, fmt.Errorf("App Registry GetArtifact (previous version) failed for %s (domain %q): %w", p.OwnerFullName, p.Domain, getErr)
				}
				// codes.NotFound: first release / no previous published version in registry
			} else if getResp != nil && getResp.Artifact != nil {
				prevVersion = getResp.Artifact.Version
				prevTag = tagPrefix + prevVersion
				prevDigest = getResp.Artifact.Digest

				exists, fDigest, matches, fErr := p.Releaser.FetchPublished(ctx, prevVersion)
				if fErr == nil && exists {
					prevDigest = fDigest
					prevMatches = matches
				} else if prevDigest != "" && digest != "" {
					prevMatches = (digest == prevDigest)
				}
			}
		} else {
			patterns := p.PreviousTagPatterns
			if len(patterns) == 0 {
				patterns = []string{tagPrefix + "v*", tagPrefix + "*"}
			}
			prefixes := p.PreviousTagPrefixes
			if len(prefixes) == 0 {
				prefixes = []string{tagPrefix}
			}

			prevTag = GetPreviousGitTag(git, currentTag, patterns, prefixes)
			if prevTag != "" {
				prevVersion = ExtractVersionFromTag(prevTag, prefixes...)
				exists, fDigest, matches, fErr := p.Releaser.FetchPublished(ctx, prevVersion)
				if fErr == nil && exists {
					prevDigest = fDigest
					prevMatches = matches
				}
			}
		}

		if prevTag != "" {
			decision := EvaluateNoOpDecision(currentVersion, currentTag, prevVersion, prevTag, prevMatches)
			digestUnchanged = decision.DigestUnchanged
			effectiveVersion = decision.EffectiveVersion
			effectiveTag = decision.EffectiveTag

			switch decision.Outcome {
			case OutcomeNoOpRebuild:
				if kind == pb.ArtifactKind_ARTIFACT_KIND_IMAGE {
					fmt.Printf("::notice title=No-op rebuild (%s)::%s has the same image digest (%s) as existing tag %s in same minor release. Skipping new git tag and App Registry record -- reusing %s.\n",
						p.OwnerFullName, currentVersion, digest, prevTag, prevTag)
				} else if kind == pb.ArtifactKind_ARTIFACT_KIND_BINARY || kind == pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE {
					fmt.Printf("::notice title=No-op rebuild (%s)::%s has the same binary digest (%s) as existing version %s in same minor release. Skipping new git tag and App Registry record -- reusing %s.\n",
						p.OwnerFullName, currentVersion, digest, prevVersion, prevVersion)
				} else {
					fmt.Printf("::notice title=No-op chart rebuild (%s)::%s packages byte-identical to existing tag %s. Skipping new git tag, ChartMuseum upload, and App Registry record -- reusing %s.\n",
						p.OwnerFullName, currentVersion, prevTag, prevTag)
				}

				if beginPublishSucceeded && p.ArtifactClient != nil {
					var reason string
					if kind == pb.ArtifactKind_ARTIFACT_KIND_BINARY || kind == pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE {
						reason = fmt.Sprintf("digest unchanged from existing version %s in same minor release -- no-op rebuild (workflow run %s, attempt %s)", prevVersion, runID, attempt)
					} else {
						reason = fmt.Sprintf("digest unchanged from existing tag %s in same minor release -- no-op rebuild (workflow run %s, attempt %s)", prevTag, runID, attempt)
					}
					failReq := &pb.FailPublishRequest{
						Kind:           kind,
						OwnerFullName:  p.OwnerFullName,
						Version:        currentVersion,
						Reason:         reason,
						IdempotencyKey: fmt.Sprintf("%s-%s-%s-fail-noop", idempotencyPrefix, p.OwnerFullName, kindName),
					}
					_, _ = p.ArtifactClient.FailPublish(ctx, failReq)
				}
			case OutcomeNewBaseline:
				if kind == pb.ArtifactKind_ARTIFACT_KIND_IMAGE {
					fmt.Printf("::notice title=Major/Minor bump with shared digest (%s)::%s has the same image digest (%s) as %s. Registering new version baseline %s with shared digest.\n",
						p.OwnerFullName, currentVersion, digest, prevTag, currentVersion)
				} else if kind == pb.ArtifactKind_ARTIFACT_KIND_BINARY || kind == pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE {
					fmt.Printf("::notice title=Major/Minor bump with shared digest (%s)::%s has the same binary digest (%s) as %s. Registering new version baseline %s with shared digest.\n",
						p.OwnerFullName, currentVersion, digest, prevVersion, currentVersion)
				} else {
					fmt.Printf("::notice title=Major/Minor bump with shared digest (%s)::%s has the same packaged content as %s. Registering new version baseline %s with shared digest.\n",
						p.OwnerFullName, currentVersion, prevTag, currentVersion)
				}
			case OutcomeProceed:
				if prevTag != "" {
					fmt.Printf("Digest check for %s: new(%s)=%s prev(%s)=%s -- proceeding with new tag %s\n",
						p.OwnerFullName, currentVersion, defaultStr(digest, "<unresolved>"), prevTag, defaultStr(prevDigest, "<unresolved>"), currentTag)
				} else {
					fmt.Printf("No previous tag found for %s -- proceeding with new tag %s (digest: %s)\n",
						p.OwnerFullName, currentTag, defaultStr(digest, "<unresolved>"))
				}
			}
		} else {
			fmt.Printf("No previous tag found for %s -- proceeding with new tag %s (digest: %s)\n",
				p.OwnerFullName, currentTag, defaultStr(digest, "<unresolved>"))
		}
	}

	var containedImages []*pb.ContainedImage
	if cip, ok := p.Releaser.(ContainedImagesProvider); ok {
		containedImages = cip.ContainedImages()
	}

	if !digestUnchanged {
		// 4. Backing store publication
		if pubErr := p.Releaser.Publish(ctx, currentVersion); pubErr != nil {
			if beginPublishSucceeded && p.ArtifactClient != nil {
				failReq := &pb.FailPublishRequest{
					Kind:           kind,
					OwnerFullName:  p.OwnerFullName,
					Version:        currentVersion,
					Reason:         fmt.Sprintf("%s upload failed / publish failed (workflow run %s, attempt %s): %v", kindName, runID, attempt, pubErr),
					IdempotencyKey: fmt.Sprintf("%s-%s-%s-fail", idempotencyPrefix, p.OwnerFullName, kindName),
				}
				_, _ = p.ArtifactClient.FailPublish(ctx, failReq)
			}
			return nil, pubErr
		}

		// 5. Git tagging
		if p.CreateGitTag && git != nil && sha != "" {
			if _, err := git.Run("rev-parse", currentTag); err != nil {
				fmt.Printf("Creating git tag %s...\n", currentTag)
				_, _ = git.Run("tag", "-a", currentTag, "-m", fmt.Sprintf("Release %s version %s", p.OwnerFullName, currentVersion), sha)
			}
		}

		// 6. RecordArtifact in App Registry
		if p.ArtifactClient != nil && p.BuildID != "" && !p.SkipRegistry {
			if digest == "" {
				return nil, fmt.Errorf("could not resolve %s digest for %s:%s (domain %q)", kindName, p.Repository, currentVersion, p.Domain)
			}
			recReq := &pb.RecordArtifactRequest{
				BuildId:        p.BuildID,
				Kind:           kind,
				OwnerFullName:  p.OwnerFullName,
				Repository:     p.Repository,
				Version:        currentVersion,
				Digest:         digest,
				Contains:       containedImages,
				PublishedAt:    time.Now().Unix(),
				IdempotencyKey: fmt.Sprintf("%s-%s-%s-record", idempotencyPrefix, p.OwnerFullName, kindName),
			}
			if _, err := p.ArtifactClient.RecordArtifact(ctx, recReq); err != nil {
				if beginPublishSucceeded {
					failReq := &pb.FailPublishRequest{
						Kind:           kind,
						OwnerFullName:  p.OwnerFullName,
						Version:        currentVersion,
						Reason:         fmt.Sprintf("chart record artifact failed (RecordArtifact failed, workflow run %s, attempt %s): %v", runID, attempt, err),
						IdempotencyKey: fmt.Sprintf("%s-%s-%s-fail-record", idempotencyPrefix, p.OwnerFullName, kindName),
					}
					_, _ = p.ArtifactClient.FailPublish(ctx, failReq)
				}
				return nil, fmt.Errorf("App Registry RecordArtifact failed for %s (domain %q): %w", p.OwnerFullName, p.Domain, err)
			}
			fmt.Printf("✅ Recorded %s %s (%s) in App Registry\n", p.OwnerFullName, currentVersion, digest)
		}
	}

	return &ReleaseResult{
		Domain:           p.Domain,
		OwnerFullName:    p.OwnerFullName,
		Version:          p.Version,
		EffectiveVersion: effectiveVersion,
		EffectiveTag:     effectiveTag,
		PreviousTag:      prevTag,
		Digest:           digest,
		DigestUnchanged:  digestUnchanged,
		Published:        !digestUnchanged,
		ContainedImages:  containedImages,
	}, nil
}
