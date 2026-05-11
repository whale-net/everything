package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newCleanupReleasesCmd() *cobra.Command {
	var (
		keepMinorVersions int
		minAgeDays        int
		dryRun            bool
		deletePackages    bool
	)

	cmd := &cobra.Command{
		Use:          "cleanup-releases",
		Short:        "Clean up old Git tags and optionally their corresponding GHCR packages",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			token := defaultEnv("GITHUB_TOKEN")
			if token == "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: GITHUB_TOKEN environment variable not set\n")
				fmt.Fprintf(cmd.ErrOrStderr(), "Please set GITHUB_TOKEN with appropriate permissions:\n")
				fmt.Fprintf(cmd.ErrOrStderr(), "  - contents:write (for tag deletion)\n")
				fmt.Fprintf(cmd.ErrOrStderr(), "  - packages:write (for GHCR package deletion)\n")
				return fmt.Errorf("missing GITHUB_TOKEN")
			}

			owner := defaultEnv("GITHUB_REPOSITORY_OWNER")
			if owner == "" {
				owner = "whale-net"
			}
			owner = strings.ToLower(owner)

			repo := defaultEnv("GITHUB_REPOSITORY")
			if repo == "" {
				repo = "everything"
			} else if idx := strings.LastIndex(repo, "/"); idx >= 0 {
				repo = repo[idx+1:]
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Analyzing releases for %s/%s...\n", owner, repo)
			fmt.Fprintf(out, "Retention policy:\n")
			fmt.Fprintf(out, "  - Keep last %d minor versions\n", keepMinorVersions)
			fmt.Fprintf(out, "  - Delete only tags older than %d days\n", minAgeDays)
			fmt.Fprintf(out, "  - Delete GHCR packages: %v\n\n", deletePackages)

			allTags, err := getAllTagsViaGit(defaultGit)
			if err != nil {
				return fmt.Errorf("get tags: %w", err)
			}

			toDelete, toKeep := identifyTagsToPrune(allTags, keepMinorVersions, minAgeDays, defaultGit)

			gh := &githubClient{owner: owner, repo: repo, token: token}

			fmt.Fprintf(out, "Cleanup Plan:\n")
			fmt.Fprintf(out, "  Tags to delete: %d\n", len(toDelete))
			fmt.Fprintf(out, "  Tags to keep:   %d\n\n", len(toKeep))

			if dryRun {
				fmt.Fprintln(out, "DRY RUN MODE - No actual deletions will occur")
			}

			// Phase 1: delete GitHub releases
			releasesToDelete, err := gh.findReleasesByTags(toDelete)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: error finding GitHub releases: %v\n", err)
			}
			if len(releasesToDelete) > 0 {
				fmt.Fprintf(out, "Deleting %d GitHub releases...\n", len(releasesToDelete))
				for tagName, releaseID := range releasesToDelete {
					if dryRun {
						fmt.Fprintf(out, "  [DRY RUN] Would delete release %d for tag: %s\n", releaseID, tagName)
					} else {
						if err := gh.deleteRelease(releaseID); err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "  Error deleting release %d for %s: %v\n", releaseID, tagName, err)
						} else {
							fmt.Fprintf(out, "  Deleted release %d for tag: %s\n", releaseID, tagName)
						}
					}
				}
			}

			// Phase 2: delete tags
			fmt.Fprintf(out, "Deleting %d Git tags...\n", len(toDelete))
			for _, tag := range toDelete {
				if dryRun {
					fmt.Fprintf(out, "  [DRY RUN] Would delete tag: %s\n", tag)
				} else {
					if err := gh.deleteTag(tag); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "  Error deleting tag %s: %v\n", tag, err)
					} else {
						fmt.Fprintf(out, "  Deleted tag: %s\n", tag)
					}
				}
			}

			// Phase 3: delete GHCR packages
			if deletePackages {
				pkgsToDelete, err := gh.findGHCRVersionsForTags(toDelete)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: error finding GHCR versions: %v\n", err)
				}
				total := 0
				for _, ids := range pkgsToDelete {
					total += len(ids)
				}
				if total > 0 {
					fmt.Fprintf(out, "Deleting %d GHCR package versions...\n", total)
					for pkgName, versionIDs := range pkgsToDelete {
						for _, id := range versionIDs {
							if dryRun {
								fmt.Fprintf(out, "  [DRY RUN] Would delete %s version %d\n", pkgName, id)
							} else {
								if err := gh.deletePackageVersion(pkgName, id); err != nil {
									fmt.Fprintf(cmd.ErrOrStderr(), "  Error deleting %s version %d: %v\n", pkgName, id, err)
								} else {
									fmt.Fprintf(out, "  Deleted %s version %d\n", pkgName, id)
								}
							}
						}
					}
				}
			}

			fmt.Fprintln(out, "\nCleanup complete.")
			return nil
		},
	}

	cmd.Flags().IntVar(&keepMinorVersions, "keep-minor-versions", 2, "Number of recent minor versions to keep")
	cmd.Flags().IntVar(&minAgeDays, "min-age-days", 14, "Minimum age in days for deletion")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "Preview changes without executing")
	cmd.Flags().BoolVar(&deletePackages, "delete-packages", true, "Also delete corresponding GHCR packages")

	return cmd
}

// ── tag pruning ───────────────────────────────────────────────────────────────

var tagPattern = regexp.MustCompile(`^([^.]+)\.(v\d+\.\d+\.\d+)`)

type semver struct{ major, minor, patch int }

func parseSemver(v string) (semver, error) {
	v = strings.TrimPrefix(v, "v")
	v = strings.SplitN(v, "-", 2)[0]
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	patch, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return semver{}, fmt.Errorf("invalid semver: %q", v)
	}
	return semver{major, minor, patch}, nil
}

func getAllTagsViaGit(git GitRunner) ([]string, error) {
	out, err := git.Run("tag", "--sort=-version:refname")
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, t := range strings.Split(strings.TrimSpace(out), "\n") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

func getTagDate(tag string, git GitRunner) *time.Time {
	out, err := git.Run("log", "-1", "--format=%ai", tag)
	if err != nil || strings.TrimSpace(out) == "" {
		return nil
	}
	dateStr := strings.TrimSpace(out)
	if len(dateStr) < 19 {
		return nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", dateStr[:19])
	if err != nil {
		return nil
	}
	return &t
}

func identifyTagsToPrune(allTags []string, keepMinor, minAgeDays int, git GitRunner) (toDelete, toKeep []string) {
	type tagEntry struct {
		tag string
		ver semver
	}
	byApp := map[string][]tagEntry{}
	for _, tag := range allTags {
		m := tagPattern.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		sv, err := parseSemver(m[2])
		if err != nil {
			continue
		}
		byApp[m[1]] = append(byApp[m[1]], tagEntry{tag, sv})
	}

	minorKey := func(sv semver) [2]int { return [2]int{sv.major, sv.minor} }

	for _, entries := range byApp {
		// Find latest patch per minor
		latestPatch := map[[2]int]tagEntry{}
		for _, e := range entries {
			k := minorKey(e.ver)
			if prev, ok := latestPatch[k]; !ok || e.ver.patch > prev.ver.patch {
				latestPatch[k] = e
			}
		}

		// Non-latest patches: delete if old enough
		for _, e := range entries {
			latest := latestPatch[minorKey(e.ver)]
			if e.tag == latest.tag {
				continue
			}
			d := getTagDate(e.tag, git)
			if d != nil && int(time.Since(*d).Hours()/24) >= minAgeDays {
				toDelete = append(toDelete, e.tag)
			} else {
				toKeep = append(toKeep, e.tag)
			}
		}

		// Sort latest-patches newest first
		latestList := make([]tagEntry, 0, len(latestPatch))
		for _, e := range latestPatch {
			latestList = append(latestList, e)
		}
		// sort descending
		for i := 0; i < len(latestList); i++ {
			for j := i + 1; j < len(latestList); j++ {
				a, b := latestList[i].ver, latestList[j].ver
				if a.major < b.major || (a.major == b.major && a.minor < b.minor) {
					latestList[i], latestList[j] = latestList[j], latestList[i]
				}
			}
		}

		// Latest minor per major (protection for multi-major repos)
		latestPerMajor := map[int]tagEntry{}
		for _, e := range latestList {
			if prev, ok := latestPerMajor[e.ver.major]; !ok || e.ver.minor > prev.ver.minor {
				latestPerMajor[e.ver.major] = e
			}
		}
		protected := map[string]bool{}
		if len(latestPerMajor) > 1 {
			for _, e := range latestPerMajor {
				protected[e.tag] = true
			}
		}

		hasEnough := len(latestList) >= keepMinor
		for idx, e := range latestList {
			d := getTagDate(e.tag, git)
			if d != nil && int(time.Since(*d).Hours()/24) < minAgeDays {
				toKeep = append(toKeep, e.tag)
				continue
			}
			if hasEnough && idx < keepMinor {
				toKeep = append(toKeep, e.tag)
			} else if protected[e.tag] {
				toKeep = append(toKeep, e.tag)
			} else {
				toDelete = append(toDelete, e.tag)
			}
		}
	}
	return toDelete, toKeep
}

// ── GitHub API client ─────────────────────────────────────────────────────────

type githubClient struct {
	owner, repo, token string
	http               *http.Client
}

func (g *githubClient) client() *http.Client {
	if g.http == nil {
		g.http = &http.Client{Timeout: 30 * time.Second}
	}
	return g.http
}

func (g *githubClient) do(method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return g.client().Do(req)
}

func (g *githubClient) findReleasesByTags(tags []string) (map[string]int, error) {
	result := map[string]int{}
	for _, tag := range tags {
		url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", g.owner, g.repo, tag)
		resp, err := g.do("GET", url, nil)
		if err != nil || resp.StatusCode == 404 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}
		var data map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			continue
		}
		if id, ok := data["id"].(float64); ok {
			result[tag] = int(id)
		}
	}
	return result, nil
}

func (g *githubClient) deleteRelease(releaseID int) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/%d", g.owner, g.repo, releaseID)
	resp, err := g.do("DELETE", url, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("delete release: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (g *githubClient) deleteTag(tag string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/refs/tags/%s", g.owner, g.repo, tag)
	resp, err := g.do("DELETE", url, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("delete tag: HTTP %d", resp.StatusCode)
	}
	return nil
}

// tagToPackageName extracts the package name from a tag like "domain-app.vX.Y.Z" -> "domain-app".
func tagToPackageName(tag string) string {
	if m := tagPattern.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

func tagToVersion(tag string) string {
	if m := tagPattern.FindStringSubmatch(tag); m != nil {
		return m[2]
	}
	return ""
}

func (g *githubClient) findGHCRVersionsForTags(tags []string) (map[string][]int, error) {
	result := map[string][]int{}
	for _, tag := range tags {
		pkgName := tagToPackageName(tag)
		version := tagToVersion(tag)
		if pkgName == "" || version == "" {
			continue
		}
		ids, err := g.findPackageVersionsByTag(pkgName, version)
		if err != nil {
			fmt.Fprintf(io.Discard, "warn: %v\n", err)
			continue
		}
		if len(ids) > 0 {
			result[pkgName] = append(result[pkgName], ids...)
		}
	}
	return result, nil
}

func (g *githubClient) findPackageVersionsByTag(pkgName, version string) ([]int, error) {
	url := fmt.Sprintf("https://api.github.com/orgs/%s/packages/container/%s/versions?per_page=100", g.owner, pkgName)
	resp, err := g.do("GET", url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("list package versions: HTTP %d", resp.StatusCode)
	}

	var versions []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return nil, err
	}

	var ids []int
	for _, v := range versions {
		id, ok := v["id"].(float64)
		if !ok {
			continue
		}
		meta, _ := v["metadata"].(map[string]interface{})
		if meta == nil {
			continue
		}
		container, _ := meta["container"].(map[string]interface{})
		if container == nil {
			continue
		}
		tagsRaw, _ := container["tags"].([]interface{})
		for _, t := range tagsRaw {
			if tag, ok := t.(string); ok && tag == version {
				ids = append(ids, int(id))
				break
			}
		}
	}
	return ids, nil
}

func (g *githubClient) deletePackageVersion(pkgName string, versionID int) error {
	url := fmt.Sprintf("https://api.github.com/orgs/%s/packages/container/%s/versions/%d", g.owner, pkgName, versionID)
	resp, err := g.do("DELETE", url, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("delete package version: HTTP %d", resp.StatusCode)
	}
	return nil
}
