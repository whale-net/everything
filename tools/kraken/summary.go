package kraken

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GenerateReleaseSummary generates a release summary for GitHub Actions.
func GenerateReleaseSummary(matrixJSON, version, eventType string, dryRun bool, repositoryOwner string) string {
	var matrix MatrixConfig
	if matrixJSON != "" {
		if err := json.Unmarshal([]byte(matrixJSON), &matrix); err != nil {
			matrix = MatrixConfig{}
		}
	}

	var summary []string
	summary = append(summary, "## 🚀 Release Summary", "")

	if len(matrix.Include) == 0 {
		summary = append(summary, "🔍 **Result:** No apps detected for release")
		return strings.Join(summary, "\n")
	}

	summary = append(summary, "✅ **Result:** Release completed", "")

	var apps []string
	for _, item := range matrix.Include {
		apps = append(apps, item.App)
	}
	summary = append(summary, fmt.Sprintf("📦 **Apps:** %s", strings.Join(apps, ", ")))

	// Handle version display
	var appVersions []string
	for _, item := range matrix.Include {
		v := item.Version
		if v == "" {
			v = version
		}
		appVersions = append(appVersions, v)
	}

	uniqueVersions := uniqueStrings(appVersions)

	if len(uniqueVersions) == 1 && uniqueVersions[0] == version {
		summary = append(summary, fmt.Sprintf("🏷️  **Version:** %s", version))
	} else if len(uniqueVersions) == 1 {
		summary = append(summary, fmt.Sprintf("🏷️  **Version:** %s", uniqueVersions[0]))
	} else {
		summary = append(summary, "🏷️  **Versions:**")
		for _, item := range matrix.Include {
			v := item.Version
			if v == "" {
				v = version
			}
			summary = append(summary, fmt.Sprintf("   - %s: %s", item.App, v))
		}
	}

	summary = append(summary, "🛠️ **System:** Consolidated Release + OCI")

	if eventType == "workflow_dispatch" {
		summary = append(summary, "📝 **Trigger:** Manual dispatch")
		if dryRun {
			summary = append(summary, "🧪 **Mode:** Dry run (no images published)")
		}
	} else {
		summary = append(summary, "📝 **Trigger:** Git tag push")
	}

	summary = append(summary, "", "### 🐳 Container Images")
	if dryRun {
		summary = append(summary, "**Dry run mode - no images were published**")
	} else {
		summary = append(summary, "Published to GitHub Container Registry:")
		allApps, _ := ListAllApps()
		appDomains := make(map[string]string)
		for _, app := range allApps {
			appDomains[app.Name] = app.Domain
		}

		for _, item := range matrix.Include {
			appVersion := item.Version
			if appVersion == "" {
				appVersion = version
			}
			domain := appDomains[item.App]
			if domain == "" {
				domain = "unknown"
			}
			imageName := fmt.Sprintf("%s-%s", domain, item.App)
			summary = append(summary, fmt.Sprintf("- `ghcr.io/%s/%s:%s`", strings.ToLower(repositoryOwner), imageName, appVersion))
		}
	}

	summary = append(summary, "", "### 🛠️ Local Development", "```bash", "# List all apps", "bazel run //tools:release -- list", "")
	summary = append(summary, "# Build and test an app locally")
	limit := 2
	if len(apps) < limit {
		limit = len(apps)
	}
	for _, app := range apps[:limit] {
		summary = append(summary, fmt.Sprintf("bazel run //tools:release -- build %s", app))
	}
	summary = append(summary, "```")

	return strings.Join(summary, "\n")
}

func uniqueStrings(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
