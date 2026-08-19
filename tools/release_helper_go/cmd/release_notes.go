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

var validNotesFormats = []string{"markdown", "plain", "json"}

// ContainedImageNote represents a member image contained inside a Helm chart.
type ContainedImageNote struct {
	AppFullName     string `json:"app_full_name"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version,omitempty"`
	Digest          string `json:"digest,omitempty"`
	DigestUnchanged bool   `json:"digest_unchanged,omitempty"`
	DiffURL         string `json:"diff_url,omitempty"`
}

// AppReleaseNotesData holds data for standalone app release notes.
type AppReleaseNotesData struct {
	AppName         string `json:"app"`
	Domain          string `json:"domain,omitempty"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version,omitempty"`
	CurrentSHA      string `json:"current_sha,omitempty"`
	PreviousSHA     string `json:"previous_sha,omitempty"`
	DiffURL         string `json:"diff_url,omitempty"`
	ReleasedAt      string `json:"released_at"`
}

// ChartReleaseNotesData holds data for Helm chart release notes.
type ChartReleaseNotesData struct {
	ChartName       string               `json:"chart_name"`
	Domain          string               `json:"domain,omitempty"`
	Version         string               `json:"version"`
	PreviousVersion string               `json:"previous_version,omitempty"`
	CurrentSHA      string               `json:"current_sha,omitempty"`
	PreviousSHA     string               `json:"previous_sha,omitempty"`
	DiffURL         string               `json:"diff_url,omitempty"`
	ReleasedAt      string               `json:"released_at"`
	Images          []ContainedImageNote `json:"images,omitempty"`
}

func newReleaseNotesCmd() *cobra.Command {
	var (
		currentTag  string
		previousTag string
		formatType  string
		owner       string
		repo        string
		isChart     bool
	)

	cmd := &cobra.Command{
		Use:          "release-notes <app-or-chart-name>",
		Short:        "Generate release notes with compare links for a specific app or helm chart",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isValidNotesFormat(formatType) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: markdown, plain, json\n")
				return fmt.Errorf("invalid format")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			targetName := args[0]

			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			allCharts, err := ListAllHelmCharts(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			var matchedChart *HelmChartMetadata
			if isChart || strings.HasPrefix(targetName, "helm-") {
				for i := range allCharts {
					c := &allCharts[i]
					published := strings.TrimPrefix(c.Name, "helm-")
					if c.Name == targetName || published == targetName || c.FullName() == targetName {
						matchedChart = c
						break
					}
				}
			}

			var artifactClient pb.ArtifactRegistryClient
			if defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" {
				c, closeConn, err := NewArtifactRegistryClient(cmd.Context())
				if err == nil && c != nil {
					artifactClient = c
					defer closeConn()
				}
			}

			if matchedChart != nil {
				notes, err := generateReleaseNotesForChart(
					cmd.Context(),
					*matchedChart,
					"",
					currentTag,
					"",
					previousTag,
					formatType,
					nil,
					allApps,
					defaultGit,
					defaultDocker,
					defaultFS,
					artifactClient,
					owner,
					repo,
				)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), notes)
				return nil
			}

			resolved, err := resolveApps([]string{targetName}, allApps)
			if err != nil || len(resolved) == 0 {
				// Try matching chart as fallback
				for i := range allCharts {
					c := &allCharts[i]
					published := strings.TrimPrefix(c.Name, "helm-")
					if c.Name == targetName || published == targetName || c.FullName() == targetName {
						matchedChart = c
						break
					}
				}
				if matchedChart != nil {
					notes, err := generateReleaseNotesForChart(
						cmd.Context(),
						*matchedChart,
						"",
						currentTag,
						"",
						previousTag,
						formatType,
						nil,
						allApps,
						defaultGit,
						defaultDocker,
						defaultFS,
						artifactClient,
						owner,
						repo,
					)
					if err != nil {
						return err
					}
					fmt.Fprintln(cmd.OutOrStdout(), notes)
					return nil
				}
				return fmt.Errorf("app or chart %q not found", targetName)
			}

			app := resolved[0]
			notes, err := generateReleaseNotesForApp(
				cmd.Context(),
				app,
				"",
				currentTag,
				"",
				previousTag,
				formatType,
				defaultGit,
				artifactClient,
				owner,
				repo,
			)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), notes)
			return nil
		},
	}

	cmd.Flags().StringVar(&currentTag, "current-tag", "HEAD", "Current tag/version")
	cmd.Flags().StringVar(&previousTag, "previous-tag", "", "Previous tag to compare against")
	cmd.Flags().StringVar(&formatType, "format", "markdown", "Output format (markdown, plain, json)")
	cmd.Flags().StringVar(&owner, "owner", "", "Repository owner")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository name")
	cmd.Flags().BoolVar(&isChart, "chart", false, "Target is a Helm chart")

	return cmd
}

func newReleaseNotesAllCmd() *cobra.Command {
	var (
		currentTag  string
		previousTag string
		formatType  string
		outputDir   string
		owner       string
		repo        string
	)

	cmd := &cobra.Command{
		Use:          "release-notes-all",
		Short:        "Generate release notes for all apps and charts",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isValidNotesFormat(formatType) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: markdown, plain, json\n")
				return fmt.Errorf("invalid format")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			allCharts, err := ListAllHelmCharts(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			var artifactClient pb.ArtifactRegistryClient
			if defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" {
				c, closeConn, err := NewArtifactRegistryClient(cmd.Context())
				if err == nil && c != nil {
					artifactClient = c
					defer closeConn()
				}
			}

			if outputDir != "" {
				_ = os.MkdirAll(outputDir, 0755)
			}

			result := map[string]string{}
			for _, app := range allApps {
				notes, err := generateReleaseNotesForApp(
					cmd.Context(),
					app,
					"",
					currentTag,
					"",
					previousTag,
					formatType,
					defaultGit,
					artifactClient,
					owner,
					repo,
				)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not generate notes for %s: %v\n", app.FullName(), err)
					continue
				}
				result[app.FullName()] = notes
				if outputDir != "" {
					_ = os.WriteFile(filepath.Join(outputDir, app.FullName()+".md"), []byte(notes), 0644)
				}
			}

			for _, chart := range allCharts {
				notes, err := generateReleaseNotesForChart(
					cmd.Context(),
					chart,
					"",
					currentTag,
					"",
					previousTag,
					formatType,
					nil,
					allApps,
					defaultGit,
					defaultDocker,
					defaultFS,
					artifactClient,
					owner,
					repo,
				)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not generate notes for chart %s: %v\n", chart.Name, err)
					continue
				}
				result[chart.Name] = notes
				if outputDir != "" {
					_ = os.WriteFile(filepath.Join(outputDir, chart.Name+".md"), []byte(notes), 0644)
				}
			}

			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&currentTag, "current-tag", "HEAD", "Current tag/version")
	cmd.Flags().StringVar(&previousTag, "previous-tag", "", "Previous tag to compare against")
	cmd.Flags().StringVar(&formatType, "format", "markdown", "Output format (markdown, plain, json)")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Directory to save release notes files")
	cmd.Flags().StringVar(&owner, "owner", "", "Repository owner")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository name")

	return cmd
}

func isValidNotesFormat(f string) bool {
	for _, v := range validNotesFormats {
		if f == v {
			return true
		}
	}
	return false
}

// buildCompareURL constructs a GitHub comparison URL between two refs.
func buildCompareURL(owner, repo, prevRef, currRef string) string {
	if prevRef == "" || currRef == "" || prevRef == currRef {
		return ""
	}
	if owner == "" {
		owner = defaultEnv("GITHUB_REPOSITORY_OWNER")
	}
	if owner == "" {
		owner = "whale-net"
	}
	if repo == "" {
		repo = defaultEnv("GITHUB_REPOSITORY_NAME")
	}
	if repo == "" {
		repo = "everything"
	}
	return fmt.Sprintf("https://github.com/%s/%s/compare/%s...%s", owner, repo, prevRef, currRef)
}

// resolvePreviousRef resolves the previous release version and commit/tag for an owner+kind.
// For domains at adoption stage 'allocate', it authoritatively queries App Registry.
// For other domains (or when App Registry is unavailable), it uses scoped git tag lookup.
func resolvePreviousRef(
	ctx context.Context,
	ownerFullName, domain string,
	kind pb.ArtifactKind,
	currentVersion, currentTag string,
	git GitRunner,
	artifactClient pb.ArtifactRegistryClient,
) (prevVersion, prevSHA, prevTag string) {
	if artifactClient != nil && defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" {
		isAllocate, err := isDomainAtAllocateStage(ctx, artifactClient, domain)
		if err == nil && isAllocate {
			getResp, getErr := artifactClient.GetArtifact(ctx, &pb.GetArtifactRequest{
				OwnerFullName:   ownerFullName,
				Kind:            kind,
				LatestPublished: true,
				BeforeVersion:   currentVersion,
			})
			if getErr == nil && getResp != nil && getResp.Artifact != nil {
				prevVersion = getResp.Artifact.Version
				if getResp.Build != nil {
					prevSHA = getResp.Build.GitSha
				}
				prevTag = ownerFullName + "." + prevVersion
				return prevVersion, prevSHA, prevTag
			}
		}
	}

	if git != nil {
		patterns := []string{ownerFullName + ".v*", ownerFullName + "*"}
		prefixes := []string{ownerFullName + "."}
		if strings.HasPrefix(ownerFullName, "helm-") {
			published := strings.TrimPrefix(ownerFullName, "helm-")
			patterns = []string{ownerFullName + ".*", published + ".*"}
			prefixes = []string{ownerFullName + ".", published + "."}
		}
		foundTag := GetPreviousGitTag(git, currentTag, patterns, prefixes)
		if foundTag != "" {
			prevTag = foundTag
			prevVersion = ExtractVersionFromTag(foundTag, prefixes...)
			if out, err := git.Run("rev-parse", foundTag); err == nil {
				prevSHA = strings.TrimSpace(out)
			}
			return prevVersion, prevSHA, prevTag
		}
	}

	return "", "", ""
}

func resolveCurrentSHA(git GitRunner, currentRef string) string {
	sha := defaultEnv("GITHUB_SHA")
	if sha != "" {
		return sha
	}
	if git != nil {
		if currentRef != "" && currentRef != "HEAD" {
			if out, err := git.Run("rev-parse", currentRef); err == nil {
				return strings.TrimSpace(out)
			}
		}
		if out, err := git.Run("rev-parse", "HEAD"); err == nil {
			return strings.TrimSpace(out)
		}
	}
	return ""
}

// generateReleaseNotes is the legacy entry point preserved for backward compatibility.
func generateReleaseNotes(app AppMetadata, currentTag, previousTag, format string, git GitRunner) (string, error) {
	return generateReleaseNotesForApp(
		context.Background(),
		app,
		"",
		currentTag,
		"",
		previousTag,
		format,
		git,
		nil,
		"",
		"",
	)
}

func generateReleaseNotesForApp(
	ctx context.Context,
	app AppMetadata,
	currentVersion, currentTag, previousVersion, previousTag, format string,
	git GitRunner,
	artifactClient pb.ArtifactRegistryClient,
	owner, repo string,
) (string, error) {
	fullName := app.FullName()
	kind := determineArtifactKind(app)
	if currentVersion == "" {
		currentVersion = ExtractVersionFromTag(currentTag, fullName+".")
	}
	if currentVersion == "" {
		currentVersion = "HEAD"
	}

	var prevVer, prevSHA, prevTag string
	if previousVersion != "" || previousTag != "" {
		prevVer = previousVersion
		prevTag = previousTag
		if prevTag == "" && prevVer != "" {
			prevTag = fullName + "." + prevVer
		}
		if git != nil && prevTag != "" {
			if out, err := git.Run("rev-parse", prevTag); err == nil {
				prevSHA = strings.TrimSpace(out)
			}
		}
	} else {
		prevVer, prevSHA, prevTag = resolvePreviousRef(ctx, fullName, app.Domain, kind, currentVersion, currentTag, git, artifactClient)
	}

	currSHA := resolveCurrentSHA(git, currentTag)
	prevRef := prevSHA
	if prevRef == "" {
		prevRef = prevTag
	}
	currRef := currSHA
	if currRef == "" {
		currRef = currentTag
	}

	diffURL := buildCompareURL(owner, repo, prevRef, currRef)

	data := AppReleaseNotesData{
		AppName:         fullName,
		Domain:          app.Domain,
		Version:         currentVersion,
		PreviousVersion: prevVer,
		CurrentSHA:      currSHA,
		PreviousSHA:     prevSHA,
		DiffURL:         diffURL,
		ReleasedAt:      time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	}

	switch format {
	case "markdown", "":
		return formatAppMarkdown(data), nil
	case "plain":
		return formatAppPlain(data), nil
	case "json":
		out, err := json.MarshalIndent(data, "", "  ")
		return string(out), err
	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}
}

func generateReleaseNotesForChart(
	ctx context.Context,
	chart HelmChartMetadata,
	currentVersion, currentTag, previousVersion, previousTag, format string,
	appVersions map[string]string,
	allApps []AppMetadata,
	git GitRunner,
	docker DockerRunner,
	fs FileSystem,
	artifactClient pb.ArtifactRegistryClient,
	owner, repo string,
) (string, error) {
	chartName := chart.Name
	publishedName := strings.TrimPrefix(chartName, "helm-")
	kind := pb.ArtifactKind_ARTIFACT_KIND_CHART

	if currentVersion == "" {
		currentVersion = ExtractVersionFromTag(currentTag, chartName+".", publishedName+".")
	}
	if currentVersion == "" {
		currentVersion = "HEAD"
	}

	var prevVer, prevSHA, prevTag string
	if previousVersion != "" || previousTag != "" {
		prevVer = previousVersion
		prevTag = previousTag
		if prevTag == "" && prevVer != "" {
			prevTag = chartName + "." + prevVer
		}
		if git != nil && prevTag != "" {
			if out, err := git.Run("rev-parse", prevTag); err == nil {
				prevSHA = strings.TrimSpace(out)
			}
		}
	} else {
		prevVer, prevSHA, prevTag = resolvePreviousRef(ctx, chartName, chart.Domain, kind, currentVersion, currentTag, git, artifactClient)
	}

	currSHA := resolveCurrentSHA(git, currentTag)
	prevRef := prevSHA
	if prevRef == "" {
		prevRef = prevTag
	}
	currRef := currSHA
	if currRef == "" {
		currRef = currentTag
	}

	chartDiffURL := buildCompareURL(owner, repo, prevRef, currRef)

	var images []ContainedImageNote
	for _, appRef := range chart.AppRefs {
		parts := strings.SplitN(appRef, "/", 2)
		if len(parts) != 2 {
			continue
		}
		domain, name := parts[0], parts[1]
		matchedApp, err := findAppByDomainAndName(domain, name, allApps)
		if err != nil {
			continue
		}
		fullAppName := matchedApp.FullName()
		appVer := ""
		if appVersions != nil {
			appVer = appVersions[fullAppName]
		}
		if appVer == "" {
			appVer = "latest"
		}
		appPrevVer, appPrevSHA, appPrevTag := resolvePreviousRef(ctx, fullAppName, domain, determineArtifactKind(*matchedApp), appVer, fullAppName+"."+appVer, git, artifactClient)
		appPrevRef := appPrevSHA
		if appPrevRef == "" {
			appPrevRef = appPrevTag
		}
		appCurrRef := currSHA
		if appCurrRef == "" {
			appCurrRef = fullAppName + "." + appVer
		}
		appDiffURL := buildCompareURL(owner, repo, appPrevRef, appCurrRef)

		repoPath := fmt.Sprintf("ghcr.io/%s/%s", defaultStr(owner, "whale-net"), fullAppName)
		digest := extractImageDigest(docker, repoPath, appVer)

		images = append(images, ContainedImageNote{
			AppFullName:     fullAppName,
			Version:         appVer,
			PreviousVersion: appPrevVer,
			Digest:          digest,
			DiffURL:         appDiffURL,
		})
	}
	if len(images) == 0 {
		for _, bareApp := range chart.Apps {
			matchedApp, err := findChartApp(bareApp, chart.Domain, allApps)
			if err != nil {
				continue
			}
			fullAppName := matchedApp.FullName()
			appVer := ""
			if appVersions != nil {
				appVer = appVersions[fullAppName]
			}
			if appVer == "" {
				appVer = "latest"
			}
			appPrevVer, appPrevSHA, appPrevTag := resolvePreviousRef(ctx, fullAppName, chart.Domain, determineArtifactKind(*matchedApp), appVer, fullAppName+"."+appVer, git, artifactClient)
			appPrevRef := appPrevSHA
			if appPrevRef == "" {
				appPrevRef = appPrevTag
			}
			appCurrRef := currSHA
			if appCurrRef == "" {
				appCurrRef = fullAppName + "." + appVer
			}
			appDiffURL := buildCompareURL(owner, repo, appPrevRef, appCurrRef)

			repoPath := fmt.Sprintf("ghcr.io/%s/%s", defaultStr(owner, "whale-net"), fullAppName)
			digest := extractImageDigest(docker, repoPath, appVer)

			images = append(images, ContainedImageNote{
				AppFullName:     fullAppName,
				Version:         appVer,
				PreviousVersion: appPrevVer,
				Digest:          digest,
				DiffURL:         appDiffURL,
			})
		}
	}

	data := ChartReleaseNotesData{
		ChartName:       chartName,
		Domain:          chart.Domain,
		Version:         currentVersion,
		PreviousVersion: prevVer,
		CurrentSHA:      currSHA,
		PreviousSHA:     prevSHA,
		DiffURL:         chartDiffURL,
		ReleasedAt:      time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		Images:          images,
	}

	switch format {
	case "markdown", "":
		return formatChartMarkdown(data), nil
	case "plain":
		return formatChartPlain(data), nil
	case "json":
		out, err := json.MarshalIndent(data, "", "  ")
		return string(out), err
	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}
}

func formatAppMarkdown(d AppReleaseNotesData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Release `%s` `%s`\n\n", d.AppName, d.Version)
	if d.PreviousVersion != "" {
		fmt.Fprintf(&b, "- **Version:** `%s` (Previous: `%s`)\n", d.Version, d.PreviousVersion)
	} else {
		fmt.Fprintf(&b, "- **Version:** `%s` (Initial release)\n", d.Version)
	}
	fmt.Fprintf(&b, "- **Released At:** %s\n", d.ReleasedAt)
	if d.DiffURL != "" {
		fmt.Fprintf(&b, "- **Full Changelog:** %s\n", d.DiffURL)
	}
	fmt.Fprintln(&b, "\n---")
	fmt.Fprint(&b, "*Generated automatically by the release helper*")
	return b.String()
}

func formatAppPlain(d AppReleaseNotesData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Release: %s %s\n", d.AppName, d.Version)
	fmt.Fprintf(&b, "Released: %s\n", d.ReleasedAt)
	if d.PreviousVersion != "" {
		fmt.Fprintf(&b, "Previous Version: %s\n", d.PreviousVersion)
	}
	if d.DiffURL != "" {
		fmt.Fprintf(&b, "Diff: %s\n", d.DiffURL)
	}
	return b.String()
}

func formatChartMarkdown(d ChartReleaseNotesData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Release `%s` `%s`\n\n", d.ChartName, d.Version)
	if d.PreviousVersion != "" {
		fmt.Fprintf(&b, "- **Chart Version:** `%s` (Previous: `%s`)\n", d.Version, d.PreviousVersion)
	} else {
		fmt.Fprintf(&b, "- **Chart Version:** `%s` (Initial release)\n", d.Version)
	}
	fmt.Fprintf(&b, "- **Released At:** %s\n", d.ReleasedAt)
	if d.DiffURL != "" {
		fmt.Fprintf(&b, "- **Chart Diff:** %s\n", d.DiffURL)
	}
	if len(d.Images) > 0 {
		fmt.Fprintln(&b, "\n### Contained Images\n")
		fmt.Fprintln(&b, "| App | Version | Image Digest | Diff |")
		fmt.Fprintln(&b, "| --- | --- | --- | --- |")
		for _, img := range d.Images {
			digestDisplay := img.Digest
			if digestDisplay == "" {
				digestDisplay = "<unresolved>"
			} else if len(digestDisplay) > 19 {
				digestDisplay = "`" + digestDisplay[:19] + "...`"
			} else {
				digestDisplay = "`" + digestDisplay + "`"
			}
			if img.DigestUnchanged {
				digestDisplay += " *(unchanged)*"
			}
			diffCol := "-"
			if img.DiffURL != "" {
				diffCol = fmt.Sprintf("[Compare](%s)", img.DiffURL)
			}
			verDisplay := img.Version
			if img.PreviousVersion != "" && img.PreviousVersion != img.Version {
				verDisplay = fmt.Sprintf("`%s` *(from %s)*", img.Version, img.PreviousVersion)
			} else {
				verDisplay = "`" + img.Version + "`"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", img.AppFullName, verDisplay, digestDisplay, diffCol)
		}
	}
	fmt.Fprintln(&b, "\n---")
	fmt.Fprint(&b, "*Generated automatically by the release helper*")
	return b.String()
}

func formatChartPlain(d ChartReleaseNotesData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Release: %s %s\n", d.ChartName, d.Version)
	fmt.Fprintf(&b, "Released: %s\n", d.ReleasedAt)
	if d.PreviousVersion != "" {
		fmt.Fprintf(&b, "Previous Version: %s\n", d.PreviousVersion)
	}
	if d.DiffURL != "" {
		fmt.Fprintf(&b, "Chart Diff: %s\n", d.DiffURL)
	}
	if len(d.Images) > 0 {
		fmt.Fprintln(&b, "\nContained Images:")
		for _, img := range d.Images {
			fmt.Fprintf(&b, "  - %s (%s): %s\n", img.AppFullName, img.Version, img.Digest)
			if img.DiffURL != "" {
				fmt.Fprintf(&b, "    Diff: %s\n", img.DiffURL)
			}
		}
	}
	return b.String()
}

// parseTagInfo parses "domain-app.vX.Y.Z" into (domain, app, version).
func parseTagInfo(tag string) (domain, appName, version string, err error) {
	dotV := strings.Index(tag, ".v")
	if dotV < 0 || !strings.Contains(tag[:dotV], "-") {
		return "", "", "", fmt.Errorf("invalid tag format: %q", tag)
	}
	domainApp := tag[:dotV]
	version = "v" + tag[dotV+2:]
	dash := strings.LastIndex(domainApp, "-")
	domain = domainApp[:dash]
	appName = domainApp[dash+1:]
	return domain, appName, version, nil
}

