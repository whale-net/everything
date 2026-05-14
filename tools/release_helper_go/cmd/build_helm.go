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
			appVersions := map[string]string{}
			if useReleasedVersions {
				allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
				if err != nil {
					return err
				}
				appVersions, err = resolveChartAppVersions(chart, allApps, defaultGit)
				if err != nil {
					return err
				}
			} else {
				for _, appName := range chart.Apps {
					appVersions[appName] = "latest"
				}
			}

			// Build chart target
			chartPkg := strings.TrimPrefix(chart.BazelTarget, "//")
			chartPkg = chartPkg[:strings.Index(chartPkg, ":")]
			chartTarget := "//" + chartPkg + ":" + strings.TrimPrefix(chart.ChartTarget, ":")

			fmt.Fprintf(cmd.OutOrStdout(), "Building bazel target: %s\n", chartTarget)
			if _, err := defaultBazel.Run("build", chartTarget); err != nil {
				return fmt.Errorf("bazel build %s: %w", chartTarget, err)
			}

			// Find unpacked chart directory
			publishedName := strings.TrimPrefix(chart.Name, "helm-")
			chartDir := filepath.Join(workspaceRoot, "bazel-bin", chartPkg, chart.Name+"_chart", publishedName)

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
	if err != nil || strings.TrimSpace(out) == "" {
		if bumpType == "minor" {
			return "v0.1.0", nil
		}
		return "v0.0.1", nil
	}
	tags := strings.Split(strings.TrimSpace(out), "\n")
	prefix := chartName + "."
	for _, tag := range tags {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		ver := tag[len(prefix):]
		return incrementVersion(ver, bumpType)
	}
	if bumpType == "minor" {
		return "v0.1.0", nil
	}
	return "v0.0.1", nil
}

func resolveChartAppVersions(chart HelmChartMetadata, allApps []AppMetadata, git GitRunner) (map[string]string, error) {
	versions := map[string]string{}
	for _, appName := range chart.Apps {
		// Try to find app in allApps by name and domain
		var matched *AppMetadata
		for i := range allApps {
			a := &allApps[i]
			if a.Name == appName && a.Domain == chart.Domain {
				matched = a
				break
			}
			if a.Name == appName {
				matched = a
			}
		}
		if matched == nil {
			return nil, fmt.Errorf("app %q not found for chart %q", appName, chart.Name)
		}
		ver, err := getLatestAppVersion(matched.Domain, matched.Name, git)
		if err != nil || ver == "" {
			return nil, fmt.Errorf("no released version for app %q in domain %q", matched.Name, matched.Domain)
		}
		versions[matched.Name] = ver
	}
	return versions, nil
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

	// Update values.yaml imageTag for resolved app versions
	valuesYaml := filepath.Join(tmpChartDir, "values.yaml")
	if len(appVersions) > 0 {
		if data, err := os.ReadFile(valuesYaml); err == nil {
			var values map[string]interface{}
			if err := yaml.Unmarshal(data, &values); err == nil {
				if apps, ok := values["apps"].(map[string]interface{}); ok {
					for appKey, ver := range appVersions {
						if appEntry, ok := apps[appKey].(map[string]interface{}); ok {
							appEntry["imageTag"] = ver
							fmt.Printf("Updated %s imageTag to %s\n", appKey, ver)
						}
					}
				}
				if out, err := yaml.Marshal(values); err == nil {
					_ = os.WriteFile(valuesYaml, out, 0644)
				}
			}
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
