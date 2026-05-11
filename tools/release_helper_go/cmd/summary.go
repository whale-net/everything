package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var validSummaryEventTypes = []string{"workflow_dispatch", "tag_push"}

func newSummaryCmd() *cobra.Command {
	var (
		matrixJSON      string
		version         string
		eventType       string
		dryRun          bool
		repositoryOwner string
	)

	cmd := &cobra.Command{
		Use:          "summary",
		Short:        "Generate release summary for GitHub Actions",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			valid := false
			for _, v := range validSummaryEventTypes {
				if eventType == v {
					valid = true
					break
				}
			}
			if !valid {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: event-type must be one of: %s\n", joinStrings(validSummaryEventTypes))
				return fmt.Errorf("invalid event-type")
			}

			output := generateSummary(matrixJSON, version, eventType, dryRun, repositoryOwner)
			fmt.Fprint(cmd.OutOrStdout(), output)
			return nil
		},
	}

	cmd.Flags().StringVar(&matrixJSON, "matrix", "", "Release matrix JSON")
	cmd.Flags().StringVar(&version, "version", "", "Release version")
	cmd.Flags().StringVar(&eventType, "event-type", "", "Event type")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Whether this was a dry run")
	cmd.Flags().StringVar(&repositoryOwner, "repository-owner", "", "GitHub repository owner")

	return cmd
}

type matrixItem struct {
	App     string `json:"app"`
	Version string `json:"version"`
	Domain  string `json:"domain"`
}

type releaseMatrix struct {
	Include []matrixItem `json:"include"`
}

func generateSummary(matrixJSON, version, eventType string, dryRun bool, repositoryOwner string) string {
	var m releaseMatrix
	if err := json.Unmarshal([]byte(matrixJSON), &m); err != nil {
		m = releaseMatrix{}
	}

	var sb strings.Builder
	sb.WriteString("## 🚀 Release Summary\n\n")

	if len(m.Include) == 0 {
		sb.WriteString("🔍 **Result:** No apps detected for release\n")
		return sb.String()
	}

	sb.WriteString("✅ **Result:** Release completed\n\n")

	apps := make([]string, 0, len(m.Include))
	for _, item := range m.Include {
		apps = append(apps, item.App)
	}
	sb.WriteString(fmt.Sprintf("📦 **Apps:** %s\n", strings.Join(apps, ", ")))
	sb.WriteString(fmt.Sprintf("🏷️  **Version:** %s\n", version))
	sb.WriteString("🛠️ **System:** Consolidated Release + OCI\n")

	if eventType == "workflow_dispatch" {
		sb.WriteString("📝 **Trigger:** Manual dispatch\n")
		if dryRun {
			sb.WriteString("🧪 **Mode:** Dry run (no images published)\n")
		}
	} else {
		sb.WriteString("📝 **Trigger:** Git tag push\n")
	}

	sb.WriteString("\n### 🐳 Container Images\n")
	if dryRun {
		sb.WriteString("**Dry run mode - no images were published**\n")
	} else {
		sb.WriteString("Published to GitHub Container Registry:\n")
		owner := strings.ToLower(repositoryOwner)
		for _, item := range m.Include {
			domain := item.Domain
			if domain == "" {
				domain = "unknown"
			}
			imageName := fmt.Sprintf("%s-%s", domain, item.App)
			itemVersion := item.Version
			if itemVersion == "" {
				itemVersion = version
			}
			sb.WriteString(fmt.Sprintf("- `ghcr.io/%s/%s:%s`\n", owner, imageName, itemVersion))
		}
	}

	sb.WriteString("\n### 🛠️ Local Development\n")
	sb.WriteString("```bash\n")
	sb.WriteString("# List all apps\n")
	sb.WriteString("bazel run //tools:release -- list\n")
	sb.WriteString("```\n")

	return sb.String()
}
