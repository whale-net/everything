package kraken

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CleanupPlan holds the plan for cleaning up tags and packages.
type CleanupPlan struct {
	TagsToDelete     []string
	TagsToKeep       []string
	PackagesToDelete map[string][]int    // package name -> version IDs
	ReleasesToDelete map[string]int      // tag name -> release ID
	RetentionPolicy  map[string]int
}

// TotalTagDeletions returns the total number of tags to delete.
func (p *CleanupPlan) TotalTagDeletions() int {
	return len(p.TagsToDelete)
}

// TotalPackageDeletions returns the total number of package versions to delete.
func (p *CleanupPlan) TotalPackageDeletions() int {
	total := 0
	for _, versions := range p.PackagesToDelete {
		total += len(versions)
	}
	return total
}

// TotalReleaseDeletions returns the total number of releases to delete.
func (p *CleanupPlan) TotalReleaseDeletions() int {
	return len(p.ReleasesToDelete)
}

// IsEmpty checks if the cleanup plan is empty.
func (p *CleanupPlan) IsEmpty() bool {
	return len(p.TagsToDelete) == 0 && len(p.PackagesToDelete) == 0 && len(p.ReleasesToDelete) == 0
}

// CleanupResult holds the result of cleanup execution.
type CleanupResult struct {
	TagsDeleted     []string
	PackagesDeleted map[string][]int
	ReleasesDeleted []string
	Errors          []string
	DryRun          bool
}

// IsSuccessful checks if cleanup was successful (no errors).
func (r *CleanupResult) IsSuccessful() bool {
	return len(r.Errors) == 0
}

// Summary generates a summary of the cleanup result.
func (r *CleanupResult) Summary() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Tags deleted: %d", len(r.TagsDeleted)))
	lines = append(lines, fmt.Sprintf("Releases deleted: %d", len(r.ReleasesDeleted)))

	totalPackages := 0
	for _, versions := range r.PackagesDeleted {
		totalPackages += len(versions)
	}
	lines = append(lines, fmt.Sprintf("Package versions deleted: %d", totalPackages))

	if len(r.Errors) > 0 {
		lines = append(lines, fmt.Sprintf("Errors encountered: %d", len(r.Errors)))
	}

	if r.DryRun {
		lines = append(lines, "(Dry run - no actual deletions)")
	}

	return strings.Join(lines, "\n")
}

// ReleaseCleanup orchestrates cleanup of Git tags, GitHub Releases, and GHCR packages.
type ReleaseCleanup struct {
	Owner         string
	Repo          string
	GHCRClient    *GHCRClient
	ReleaseClient *GitHubReleaseClient
}

// NewReleaseCleanup creates a new cleanup orchestrator.
func NewReleaseCleanup(owner, repo, token string) (*ReleaseCleanup, error) {
	ghcrClient, err := NewGHCRClient(owner, token)
	if err != nil {
		return nil, err
	}

	releaseClient, err := NewGitHubReleaseClient(owner, repo, token)
	if err != nil {
		return nil, err
	}

	return &ReleaseCleanup{
		Owner:         owner,
		Repo:          repo,
		GHCRClient:    ghcrClient,
		ReleaseClient: releaseClient,
	}, nil
}

// PlanCleanup plans what tags, releases, and packages to delete.
func (rc *ReleaseCleanup) PlanCleanup(keepMinorVersions, minAgeDays int) (*CleanupPlan, error) {
	allTags, err := GetAllTags()
	if err != nil {
		return nil, err
	}

	tagsToDelete, tagsToKeep := IdentifyTagsToPrune(allTags, keepMinorVersions, minAgeDays)

	// Find corresponding GitHub Releases
	releasesToDelete := make(map[string]int)
	releasesMap, err := rc.ReleaseClient.FindReleasesByTags(tagsToDelete)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Error finding GitHub releases: %v\n", err)
	} else {
		for tagName, releaseData := range releasesMap {
			if releaseData == nil {
				continue
			}
			id, ok := releaseData["id"]
			if !ok {
				continue
			}
			if idFloat, ok := id.(float64); ok {
				releasesToDelete[tagName] = int(idFloat)
				fmt.Printf("  Found GitHub release %d for tag %s\n", int(idFloat), tagName)
			}
		}
	}

	// Map tags to GHCR packages
	packagesToDelete := make(map[string][]int)
	tagPackageRegex := regexp.MustCompile(`^([^.]+)\.v\d+\.\d+\.\d+`)
	tagVersionRegex := regexp.MustCompile(`(v\d+\.\d+\.\d+(?:-[a-zA-Z0-9\-\.]+)?)`)

	for _, tag := range tagsToDelete {
		match := tagPackageRegex.FindStringSubmatch(tag)
		if match == nil {
			fmt.Fprintf(os.Stderr, "⚠️  Could not parse package name from tag: %s\n", tag)
			continue
		}
		packageName := match[1]

		vMatch := tagVersionRegex.FindStringSubmatch(tag)
		if vMatch == nil {
			fmt.Fprintf(os.Stderr, "⚠️  Could not extract version from tag: %s\n", tag)
			continue
		}
		version := vMatch[1]

		allVersions, err := rc.GHCRClient.ListPackageVersions(packageName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Error finding GHCR versions for %s: %v\n", packageName, err)
			continue
		}

		for _, pkgVersion := range allVersions {
			if pkgVersion.HasTag(version) {
				packagesToDelete[packageName] = append(packagesToDelete[packageName], pkgVersion.VersionID)
				fmt.Printf("  Found GHCR version %d for %s:%s\n", pkgVersion.VersionID, packageName, version)
			}
		}
	}

	return &CleanupPlan{
		TagsToDelete:     tagsToDelete,
		TagsToKeep:       tagsToKeep,
		PackagesToDelete: packagesToDelete,
		ReleasesToDelete: releasesToDelete,
		RetentionPolicy: map[string]int{
			"keep_minor_versions": keepMinorVersions,
			"min_age_days":        minAgeDays,
		},
	}, nil
}

// ExecuteCleanup executes the cleanup plan.
func (rc *ReleaseCleanup) ExecuteCleanup(plan *CleanupPlan, dryRun bool) *CleanupResult {
	result := &CleanupResult{
		DryRun:          dryRun,
		PackagesDeleted: make(map[string][]int),
	}

	if dryRun {
		fmt.Println("🧪 DRY RUN MODE - No actual deletions will occur")
		fmt.Println()
	}

	// Phase 1: Delete GitHub Releases
	if len(plan.ReleasesToDelete) > 0 {
		fmt.Printf("🗑 Deleting %d GitHub releases...\n", len(plan.ReleasesToDelete))
		for tagName, releaseID := range plan.ReleasesToDelete {
			if dryRun {
				fmt.Printf("  [DRY RUN] Would delete release %d for tag: %s\n", releaseID, tagName)
				result.ReleasesDeleted = append(result.ReleasesDeleted, tagName)
			} else {
				success := rc.ReleaseClient.DeleteRelease(releaseID)
				if success {
					result.ReleasesDeleted = append(result.ReleasesDeleted, tagName)
					fmt.Printf("  ✅ Deleted release %d for tag: %s\n", releaseID, tagName)
				} else {
					errMsg := fmt.Sprintf("Failed to delete release %d for tag: %s", releaseID, tagName)
					result.Errors = append(result.Errors, errMsg)
					fmt.Fprintf(os.Stderr, "  ❌ %s\n", errMsg)
				}
			}
		}
		fmt.Println()
	}

	// Phase 2: Delete Git tags
	fmt.Printf("🏷️  Deleting %d Git tags...\n", len(plan.TagsToDelete))
	for _, tag := range plan.TagsToDelete {
		if dryRun {
			fmt.Printf("  [DRY RUN] Would delete tag: %s\n", tag)
			result.TagsDeleted = append(result.TagsDeleted, tag)
		} else {
			success := DeleteRemoteTag(tag, rc.Owner, rc.Repo)
			if success {
				result.TagsDeleted = append(result.TagsDeleted, tag)
				fmt.Printf("  ✅ Deleted tag: %s\n", tag)
			} else {
				errMsg := fmt.Sprintf("Failed to delete tag: %s", tag)
				result.Errors = append(result.Errors, errMsg)
				fmt.Fprintf(os.Stderr, "  ❌ %s\n", errMsg)
			}
		}
	}

	// Phase 3: Delete GHCR packages
	totalPackages := plan.TotalPackageDeletions()
	if totalPackages > 0 {
		fmt.Printf("\n📦 Deleting %d GHCR package versions...\n", totalPackages)
		for packageName, versionIDs := range plan.PackagesToDelete {
			for _, versionID := range versionIDs {
				if dryRun {
					fmt.Printf("  [DRY RUN] Would delete %s version %d\n", packageName, versionID)
					result.PackagesDeleted[packageName] = append(result.PackagesDeleted[packageName], versionID)
				} else {
					success, err := rc.GHCRClient.DeletePackageVersion(packageName, versionID)
					if err != nil || !success {
						errMsg := fmt.Sprintf("Error deleting %s version %d", packageName, versionID)
						result.Errors = append(result.Errors, errMsg)
						fmt.Fprintf(os.Stderr, "  ❌ %s\n", errMsg)
					} else {
						result.PackagesDeleted[packageName] = append(result.PackagesDeleted[packageName], versionID)
						fmt.Printf("  ✅ Deleted %s version %d\n", packageName, versionID)
					}
				}
			}
		}
	}

	return result
}

// GetTagCreationDate gets the creation date of a Git tag.
func GetTagCreationDate(tag string) *time.Time {
	cmd := exec.Command("git", "log", "-1", "--format=%ai", tag)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	dateStr := strings.TrimSpace(string(out))
	if dateStr == "" {
		return nil
	}
	// Parse format: 2025-01-15 10:30:45 -0800
	if len(dateStr) >= 19 {
		t, err := time.Parse("2006-01-02 15:04:05", dateStr[:19])
		if err != nil {
			return nil
		}
		return &t
	}
	return nil
}

// DeleteRemoteTag deletes a Git tag from the remote repository.
func DeleteRemoteTag(tagName, owner, repo string) bool {
	cmd := exec.Command("git", "push", "--delete", "origin", tagName)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to delete remote tag %s: %v\n", tagName, err)
		return false
	}
	return true
}

type tagVersion struct {
	tag   string
	major int
	minor int
	patch int
}

// IdentifyTagsToPrune identifies which tags should be pruned based on retention policy.
func IdentifyTagsToPrune(allTags []string, keepMinorVersions, minAgeDays int) (tagsToDelete, tagsToKeep []string) {
	tagRegex := regexp.MustCompile(`^([^.]+)\.(v\d+\.\d+\.\d+)`)

	// Group tags by app/chart
	tagsByApp := make(map[string][]tagVersion)

	for _, tag := range allTags {
		match := tagRegex.FindStringSubmatch(tag)
		if match == nil {
			continue
		}
		appName := match[1]
		versionStr := match[2]

		sv, err := ParseSemanticVersion(versionStr)
		if err != nil {
			continue
		}

		tagsByApp[appName] = append(tagsByApp[appName], tagVersion{
			tag:   tag,
			major: sv.Major,
			minor: sv.Minor,
			patch: sv.Patch,
		})
	}

	// Process each app independently
	for _, appTags := range tagsByApp {
		// Step 1: Group by minor version and keep only latest patch
		type minorKey struct{ major, minor int }
		minorVersions := make(map[minorKey]tagVersion)

		for _, tv := range appTags {
			mk := minorKey{tv.major, tv.minor}
			if existing, ok := minorVersions[mk]; !ok || tv.patch > existing.patch {
				minorVersions[mk] = tv
			}
		}

		// Step 2: Separate latest patches from old patches
		var keptLatestPatches []tagVersion
		for _, tv := range appTags {
			mk := minorKey{tv.major, tv.minor}
			latestTV := minorVersions[mk]

			if tv.tag == latestTV.tag {
				keptLatestPatches = append(keptLatestPatches, tv)
			} else {
				// Old patch - check age
				tagDate := GetTagCreationDate(tv.tag)
				if tagDate != nil {
					ageDays := int(time.Since(*tagDate).Hours() / 24)
					if ageDays >= minAgeDays {
						tagsToDelete = append(tagsToDelete, tv.tag)
						continue
					}
				}
				tagsToKeep = append(tagsToKeep, tv.tag)
			}
		}

		// Step 3: Sort latest patches by version (newest first)
		sort.Slice(keptLatestPatches, func(i, j int) bool {
			if keptLatestPatches[i].major != keptLatestPatches[j].major {
				return keptLatestPatches[i].major > keptLatestPatches[j].major
			}
			return keptLatestPatches[i].minor > keptLatestPatches[j].minor
		})

		// Step 4: Find latest minor per major (for protection)
		latestPerMajor := make(map[int]tagVersion)
		for _, tv := range keptLatestPatches {
			if existing, ok := latestPerMajor[tv.major]; !ok || tv.minor > existing.minor {
				latestPerMajor[tv.major] = tv
			}
		}

		protectedTags := make(map[string]bool)
		if len(latestPerMajor) > 1 {
			for _, tv := range latestPerMajor {
				protectedTags[tv.tag] = true
			}
		}

		// Step 5: Apply retention policy
		hasEnoughVersions := len(keptLatestPatches) >= keepMinorVersions

		for idx, tv := range keptLatestPatches {
			tagDate := GetTagCreationDate(tv.tag)
			if tagDate != nil {
				ageDays := int(time.Since(*tagDate).Hours() / 24)
				if ageDays < minAgeDays {
					tagsToKeep = append(tagsToKeep, tv.tag)
					continue
				}
			}

			if hasEnoughVersions && idx < keepMinorVersions {
				tagsToKeep = append(tagsToKeep, tv.tag)
			} else if protectedTags[tv.tag] {
				tagsToKeep = append(tagsToKeep, tv.tag)
			} else {
				tagsToDelete = append(tagsToDelete, tv.tag)
			}
		}
	}

	return tagsToDelete, tagsToKeep
}
