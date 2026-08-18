package cmd

import (
	"strings"

	sharedsemver "github.com/whale-net/everything/libs/go/semver"
)

// ReleaseDecisionOutcome represents the classification of a release decision.
type ReleaseDecisionOutcome string

const (
	// OutcomeProceed indicates the release should proceed normally with the candidate version (new content or no previous version).
	OutcomeProceed ReleaseDecisionOutcome = "proceed"

	// OutcomeNoOpRebuild indicates the candidate artifact has identical content/digest to
	// the previous version in the same major.minor release line. Publication is skipped,
	// and the previous version is reused.
	OutcomeNoOpRebuild ReleaseDecisionOutcome = "noop_rebuild"

	// OutcomeNewBaseline indicates the candidate artifact has identical content/digest to
	// the previous version, but represents an explicit major or minor version bump.
	// A new version baseline is published with the shared digest.
	OutcomeNewBaseline ReleaseDecisionOutcome = "new_baseline"
)

// ReleaseDecisionResult contains the decision on how to handle the release.
type ReleaseDecisionResult struct {
	Outcome          ReleaseDecisionOutcome
	EffectiveVersion string
	EffectiveTag     string
	DigestUnchanged  bool
	Published        bool
}

// IsSameMinorLine reports whether two semantic versions share the same major and minor numbers.
// Returns false if either version fails semver parsing.
func IsSameMinorLine(v1, v2 string) bool {
	parsed1, err1 := sharedsemver.Parse(v1)
	if err1 != nil {
		return false
	}
	parsed2, err2 := sharedsemver.Parse(v2)
	if err2 != nil {
		return false
	}
	return parsed1.Major == parsed2.Major && parsed1.Minor == parsed2.Minor
}

// ExtractVersionFromTag extracts the version string from a tag by stripping any of the provided prefixes.
// If tag does not start with any of the prefixes (or if no prefixes match), tag is returned as-is.
func ExtractVersionFromTag(tag string, prefixes ...string) string {
	for _, pfx := range prefixes {
		if strings.HasPrefix(tag, pfx) {
			return strings.TrimPrefix(tag, pfx)
		}
	}
	return tag
}

// GetPreviousGitTag finds the most recent preceding git tag matching any of searchPatterns and starting with any of matchPrefixes, excluding currentTag.
func GetPreviousGitTag(git GitRunner, currentTag string, searchPatterns []string, matchPrefixes []string) string {
	if git == nil {
		return ""
	}
	args := append([]string{"tag", "--sort=-version:refname", "--list"}, searchPatterns...)
	out, err := git.Run(args...)
	if err != nil {
		return ""
	}
	for _, tag := range strings.Split(strings.TrimSpace(out), "\n") {
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == currentTag {
			continue
		}
		for _, pfx := range matchPrefixes {
			if strings.HasPrefix(tag, pfx) {
				return tag
			}
		}
	}
	return ""
}

// EvaluateNoOpDecision evaluates whether a candidate release is a no-op rebuild, a new version baseline with shared content, or new content to publish.
// - If previousVersion exists and contentMatches:
//   - If candidateVersion and previousVersion share the same major.minor series, it is a no-op rebuild (OutcomeNoOpRebuild).
//   - If they differ in major or minor series, it establishes a new version baseline (OutcomeNewBaseline).
// - Otherwise, the candidate proceeds as a standard new publication (OutcomeProceed).
func EvaluateNoOpDecision(candidateVersion, candidateTag, previousVersion, previousTag string, contentMatches bool) ReleaseDecisionResult {
	if previousVersion != "" && contentMatches {
		if IsSameMinorLine(candidateVersion, previousVersion) {
			return ReleaseDecisionResult{
				Outcome:          OutcomeNoOpRebuild,
				EffectiveVersion: previousVersion,
				EffectiveTag:     previousTag,
				DigestUnchanged:  true,
				Published:        false,
			}
		}
		return ReleaseDecisionResult{
			Outcome:          OutcomeNewBaseline,
			EffectiveVersion: candidateVersion,
			EffectiveTag:     candidateTag,
			DigestUnchanged:  false,
			Published:        true,
		}
	}
	return ReleaseDecisionResult{
		Outcome:          OutcomeProceed,
		EffectiveVersion: candidateVersion,
		EffectiveTag:     candidateTag,
		DigestUnchanged:  false,
		Published:        true,
	}
}

// CollisionResolutionResult contains the result of checking and resolving version collisions.
type CollisionResolutionResult struct {
	Version         string
	Tag             string
	DigestUnchanged bool
	CollisionFound  bool
	Advanced        bool
}

// ResolveCandidateCollision checks if candidateVersion already exists in the backing store.
// - If it exists with identical content, it signals that the artifact is already published (DigestUnchanged: true).
// - If it exists with different content and allowAutoAdvance is true, it increments the version (patch)
//   until finding an available slot, invoking onAdvanced (e.g. for repackaging) at each step.
func ResolveCandidateCollision(
	candidateVersion string,
	tagPrefix string,
	allowAutoAdvance bool,
	checkExisting func(version string) (exists bool, contentMatches bool, err error),
	onAdvanced func(newVersion string) error,
) (*CollisionResolutionResult, error) {
	exists, matches, err := checkExisting(candidateVersion)
	if err != nil || !exists {
		return &CollisionResolutionResult{
			Version:         candidateVersion,
			Tag:             tagPrefix + candidateVersion,
			DigestUnchanged: false,
		}, nil
	}

	if matches {
		return &CollisionResolutionResult{
			Version:         candidateVersion,
			Tag:             tagPrefix + candidateVersion,
			DigestUnchanged: true,
		}, nil
	}

	if !allowAutoAdvance {
		return &CollisionResolutionResult{
			Version:         candidateVersion,
			Tag:             tagPrefix + candidateVersion,
			DigestUnchanged: false,
			CollisionFound:  true,
		}, nil
	}

	ver := candidateVersion
	for {
		nextVer, incErr := incrementVersion(ver, "patch")
		if incErr != nil {
			return nil, incErr
		}
		ver = nextVer
		tag := tagPrefix + ver
		chkExists, chkMatches, chkErr := checkExisting(ver)
		if chkErr != nil || !chkExists {
			if onAdvanced != nil {
				if err := onAdvanced(ver); err != nil {
					return nil, err
				}
			}
			return &CollisionResolutionResult{
				Version:         ver,
				Tag:             tag,
				DigestUnchanged: false,
				CollisionFound:  true,
				Advanced:        true,
			}, nil
		}
		if chkMatches {
			return &CollisionResolutionResult{
				Version:         ver,
				Tag:             tag,
				DigestUnchanged: true,
				CollisionFound:  true,
				Advanced:        true,
			}, nil
		}
	}
}
