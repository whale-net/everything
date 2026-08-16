package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newBuildHelmChartCmd() *cobra.Command {
	var (
		chartVersion        string
		outputDir           string
		useReleasedVersions bool
		autoVersion         bool
		bumpType            string
	)

	cmd := &cobra.Command{
		Use:          "build-helm-chart <chart-name>",
		Short:        "Build and package a helm chart",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if bumpType != "major" && bumpType != "minor" && bumpType != "patch" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: --bump must be one of: major, minor, patch\n")
				return fmt.Errorf("invalid bump type")
			}
			if !autoVersion && chartVersion == "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: --version must be provided when --no-auto-version is used\n")
				return fmt.Errorf("missing version")
			}

			chartName := args[0]
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allCharts, err := ListAllHelmCharts(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			chart, err := findHelmChartByName(chartName, allCharts)
			if err != nil {
				return err
			}

			// Determine version
			version := chartVersion
			if autoVersion && version == "" {
				version, err = autoIncrementHelmVersion(chart.Name, bumpType, defaultGit)
				if err != nil {
					return fmt.Errorf("auto-version: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Auto-determined chart version for %s: %s\n", chart.Name, version)
			}

			// Resolve app versions
			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			var appVersions map[string]string
			if useReleasedVersions {
				appVersions, err = resolveChartAppVersions(chart, allApps, defaultGit)
				if err != nil {
					return err
				}
				// AR-7f (issue #558): compose-time chart hermeticity, gated
				// per domain. Runs here -- release_helper_go, a CLI the
				// release workflow invokes as its own step -- not inside the
				// `bazel build` below, which composes the chart via
				// tools/helm/composer.go with no version resolution or
				// registry access at all. See
				// //tools/app_registry/ARCHITECTURE.md "Chart -> image
				// lockfile" for why a registry call must never happen inside
				// that Bazel action.
				if err := checkChartHermeticity(cmd.Context(), func(msg string) { fmt.Fprintln(cmd.ErrOrStderr(), "::warning::"+msg) }, chart.Domain, appVersions); err != nil {
					return err
				}
			} else {
				appVersions = map[string]string{}
				for _, appName := range chart.Apps {
					matched, err := findChartApp(appName, chart.Domain, allApps)
					if err != nil {
						return err
					}
					appVersions[matched.FullName()] = "latest"
				}
			}

			// Build chart target
			chartTarget, chartDir := chartOutputPaths(workspaceRoot, chart)

			fmt.Fprintf(cmd.OutOrStdout(), "Building bazel target: %s\n", chartTarget)
			if _, err := defaultBazel.Run("build", chartTarget); err != nil {
				return fmt.Errorf("bazel build %s: %w", chartTarget, err)
			}

			outDir := outputDir
			if outDir == "" {
				outDir, err = os.MkdirTemp("", "helm-build-*")
				if err != nil {
					return fmt.Errorf("create temp dir: %w", err)
				}
			} else {
				if err := os.MkdirAll(outDir, 0755); err != nil {
					return fmt.Errorf("create output dir: %w", err)
				}
			}

			chartPath, err := packageChartWithVersion(chartDir, chart.Name, version, outDir, appVersions)
			if err != nil {
				return fmt.Errorf("package chart: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Chart packaged: %s\n", chartPath)
			fmt.Fprintf(cmd.OutOrStdout(), "Version: %s\n", version)
			return nil
		},
	}

	cmd.Flags().StringVar(&chartVersion, "version", "", "Explicit chart version")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Output directory for packaged chart")
	cmd.Flags().BoolVar(&useReleasedVersions, "use-released", true, "Use released app versions or 'latest'")
	cmd.Flags().BoolVar(&autoVersion, "auto-version", true, "Automatically determine chart version from git tags")
	cmd.Flags().StringVar(&bumpType, "bump", "patch", "Version bump type: major, minor, or patch")

	return cmd
}

// chartOutputPaths derives the bazel target and unpacked output directory
// for a discovered chart, matching how helm_chart in tools/helm/helm.bzl
// names its outputs (see chart_parent_dir / published_chart_name there).
// Shared by build-helm-chart and read-chart-lockfile so both agree on where
// a built chart's files — including composer.go's image-lockfile.json —
// land.
func chartOutputPaths(workspaceRoot string, chart HelmChartMetadata) (chartTarget, chartDir string) {
	chartPkg := strings.TrimPrefix(chart.BazelTarget, "//")
	chartPkg = chartPkg[:strings.Index(chartPkg, ":")]
	chartTarget = "//" + chartPkg + ":" + strings.TrimPrefix(chart.ChartTarget, ":")

	publishedName := strings.TrimPrefix(chart.Name, "helm-")
	chartDir = filepath.Join(workspaceRoot, "bazel-bin", chartPkg, chart.Name+"_chart", publishedName)
	return chartTarget, chartDir
}

func findHelmChartByName(name string, charts []HelmChartMetadata) (HelmChartMetadata, error) {
	for _, c := range charts {
		if c.Name == name {
			return c, nil
		}
	}
	return HelmChartMetadata{}, fmt.Errorf("helm chart %q not found", name)
}

func autoIncrementHelmVersion(chartName, bumpType string, git GitRunner) (string, error) {
	out, err := git.Run("tag", "--sort=-version:refname", "--list", chartName+".*")
	if (err != nil || strings.TrimSpace(out) == "") && !strings.HasPrefix(chartName, "helm-") {
		out, err = git.Run("tag", "--sort=-version:refname", "--list", "helm-"+chartName+".*")
	}
	if err != nil || strings.TrimSpace(out) == "" {
		if bumpType == "minor" {
			return "v0.1.0", nil
		}
		return "v0.0.1", nil
	}
	tags := strings.Split(strings.TrimSpace(out), "\n")
	prefix := chartName + "."
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if strings.HasPrefix(tag, prefix) {
			ver := tag[len(prefix):]
			return incrementVersion(ver, bumpType)
		}
		if !strings.HasPrefix(chartName, "helm-") && strings.HasPrefix(tag, "helm-"+prefix) {
			ver := tag[len("helm-"+prefix):]
			return incrementVersion(ver, bumpType)
		}
	}
	if bumpType == "minor" {
		return "v0.1.0", nil
	}
	return "v0.0.1", nil
}

func resolveChartAppVersions(chart HelmChartMetadata, allApps []AppMetadata, git GitRunner) (map[string]string, error) {
	versions := map[string]string{}
	for _, appName := range chart.Apps {
		matched, err := findChartApp(appName, chart.Domain, allApps)
		if err != nil {
			return nil, err
		}
		ver, err := getLatestAppVersion(matched.Domain, matched.Name, git)
		if err != nil || ver == "" {
			return nil, fmt.Errorf("no released version for app %q in domain %q", matched.Name, matched.Domain)
		}
		versions[matched.FullName()] = ver
	}
	return versions, nil
}

// findChartApp resolves one of a chart's declared app names to its full
// AppMetadata, preferring a match within the chart's own domain. The
// returned metadata's FullName() ("<domain>-<name>") is the key composer.go
// uses for the values.yaml `apps` map, so callers must key appVersions by
// FullName() rather than the bare app name for packageChartWithVersion's
// imageTag lookup to find it.
func findChartApp(appName, chartDomain string, allApps []AppMetadata) (*AppMetadata, error) {
	var matched *AppMetadata
	for i := range allApps {
		a := &allApps[i]
		if a.Name == appName && a.Domain == chartDomain {
			matched = a
			break
		}
		if a.Name == appName {
			matched = a
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("app %q not found for chart domain %q", appName, chartDomain)
	}
	return matched, nil
}

func getLatestAppVersion(domain, appName string, git GitRunner) (string, error) {
	prefix := domain + "-" + appName + "."
	out, err := git.Run("tag", "--sort=-version:refname", "--list", prefix+"*")
	if err != nil {
		return "", err
	}
	for _, tag := range strings.Split(strings.TrimSpace(out), "\n") {
		tag = strings.TrimSpace(tag)
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		ver := tag[len(prefix):]
		if strings.HasPrefix(ver, "v") {
			return ver, nil
		}
	}
	return "", nil
}

func packageChartWithVersion(chartDir, chartName, version, outDir string, appVersions map[string]string) (string, error) {
	publishedName := strings.TrimPrefix(chartName, "helm-")

	// Copy chart to temp dir (bazel-bin is read-only)
	tmpDir, err := os.MkdirTemp("", "helm-pkg-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	tmpChartDir := filepath.Join(tmpDir, publishedName)
	if err := copyDir(chartDir, tmpChartDir); err != nil {
		return "", fmt.Errorf("copy chart: %w", err)
	}

	// Make writable
	_ = filepath.Walk(tmpChartDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return os.Chmod(p, 0755)
		}
		return os.Chmod(p, 0644)
	})

	// Update Chart.yaml version
	chartYaml := filepath.Join(tmpChartDir, "Chart.yaml")
	if data, err := os.ReadFile(chartYaml); err == nil {
		var chartData map[string]interface{}
		if err := yaml.Unmarshal(data, &chartData); err == nil {
			chartData["version"] = version
			if out, err := yaml.Marshal(chartData); err == nil {
				_ = os.WriteFile(chartYaml, out, 0644)
			}
		}
	}

	// Update values.yaml imageTag for resolved app versions. A resolved
	// version that fails to land in values.yaml is worse than useless — it
	// silently ships whatever imageTag was baked in at `bazel build` time
	// (typically "latest"), so every step here fails hard rather than
	// swallowing the error, unlike the historical version of this code.
	valuesYaml := filepath.Join(tmpChartDir, "values.yaml")
	if len(appVersions) > 0 {
		data, err := os.ReadFile(valuesYaml)
		if err != nil {
			return "", fmt.Errorf("read values.yaml: %w", err)
		}
		var values map[string]interface{}
		if err := yaml.Unmarshal(data, &values); err != nil {
			return "", fmt.Errorf("parse values.yaml: %w", err)
		}
		apps, ok := values["apps"].(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("values.yaml has no \"apps\" map to set imageTag on")
		}
		for appKey, ver := range appVersions {
			appEntry, ok := apps[appKey].(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("values.yaml \"apps\" has no entry %q to set imageTag on (chart's apps map may use a different key convention than the resolved app versions)", appKey)
			}
			appEntry["imageTag"] = ver
			fmt.Printf("Updated %s imageTag to %s\n", appKey, ver)
		}
		out, err := yaml.Marshal(values)
		if err != nil {
			return "", fmt.Errorf("marshal values.yaml: %w", err)
		}
		if err := os.WriteFile(valuesYaml, out, 0644); err != nil {
			return "", fmt.Errorf("write values.yaml: %w", err)
		}
	}

	// Run helm package
	out, err := exec.Command("helm", "package", tmpChartDir, "-d", outDir).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("helm package: %w\n%s", err, out)
	}

	return filepath.Join(outDir, fmt.Sprintf("%s-%s.tgz", publishedName, version)), nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
