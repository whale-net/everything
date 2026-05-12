package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newUnpublishHelmChartCmd() *cobra.Command {
	var (
		chartName string
		versions  string
	)

	cmd := &cobra.Command{
		Use:          "unpublish-helm-chart <index-file>",
		Short:        "Remove specific versions of a chart from the Helm repository index",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			indexFile := args[0]

			if _, err := defaultFS.Stat(indexFile); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: Index file not found: %s\n", indexFile)
				return fmt.Errorf("index file not found")
			}

			if chartName == "" {
				return fmt.Errorf("--chart is required")
			}
			if versions == "" {
				return fmt.Errorf("--versions is required")
			}

			versionList := splitCSV(versions)
			fmt.Fprintf(cmd.OutOrStdout(), "Unpublishing versions %v of chart %q from %s\n", versionList, chartName, indexFile)

			data, err := defaultFS.ReadFile(indexFile)
			if err != nil {
				return fmt.Errorf("read index: %w", err)
			}

			removed, updated, err := removeHelmChartVersions(data, chartName, versionList)
			if err != nil {
				return err
			}

			if removed == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: no versions were removed. Versions specified: %v\n", versionList)
				return nil
			}

			if err := os.WriteFile(indexFile, updated, 0644); err != nil {
				return fmt.Errorf("write index: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Removed %d version(s) of %q from index\n", removed, chartName)
			fmt.Fprintf(cmd.OutOrStdout(), "Successfully updated %s\n", indexFile)
			return nil
		},
	}

	cmd.Flags().StringVar(&chartName, "chart", "", "Name of the chart to unpublish versions from")
	cmd.Flags().StringVar(&versions, "versions", "", "Comma-separated list of versions to unpublish")

	return cmd
}

// removeHelmChartVersions removes specific versions of a chart from a Helm index.yaml.
// It uses line-based parsing to avoid any external YAML dependency.
func removeHelmChartVersions(indexData []byte, chartName string, versions []string) (removed int, updated []byte, err error) {
	toRemove := map[string]bool{}
	for _, v := range versions {
		toRemove[v] = true
	}

	lines := strings.Split(string(indexData), "\n")
	chartHeader := "  " + chartName + ":"

	// Find chart header line within entries section.
	chartIdx := -1
	inEntries := false
	for i, line := range lines {
		if line == "entries:" {
			inEntries = true
			continue
		}
		if inEntries && line == chartHeader {
			chartIdx = i
			break
		}
	}

	if chartIdx == -1 {
		return 0, nil, fmt.Errorf("chart %q not found in index", chartName)
	}

	// Find end of this chart's section: next chart header or non-indented line.
	endIdx := len(lines)
	for i := chartIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		if len(line) >= 2 && line[0] == ' ' && line[1] == ' ' && len(line) > 2 && line[2] != '-' && line[2] != ' ' {
			// Another chart header like "  other-chart:"
			endIdx = i
			break
		}
		if line[0] != ' ' {
			// Top-level key (e.g. "generated:")
			endIdx = i
			break
		}
	}

	// Parse entry blocks within [chartIdx+1, endIdx). Each block starts with "  - ".
	chartLines := lines[chartIdx+1 : endIdx]
	var blocks [][]string
	var cur []string
	for _, line := range chartLines {
		if strings.HasPrefix(line, "  - ") {
			if cur != nil {
				blocks = append(blocks, cur)
			}
			cur = []string{line}
		} else if cur != nil {
			cur = append(cur, line)
		}
	}
	if cur != nil {
		blocks = append(blocks, cur)
	}

	// Filter blocks by version.
	var kept [][]string
	for _, block := range blocks {
		ver := helmBlockVersion(block)
		if toRemove[ver] {
			removed++
		} else {
			kept = append(kept, block)
		}
	}

	if removed == 0 {
		return 0, nil, nil
	}

	// Reconstruct: header lines, kept entry blocks, trailing lines.
	var result []string
	if len(kept) == 0 {
		// Remove the chart header entirely.
		result = append(result, lines[:chartIdx]...)
	} else {
		result = append(result, lines[:chartIdx+1]...)
		for _, block := range kept {
			result = append(result, block...)
		}
	}
	result = append(result, lines[endIdx:]...)

	return removed, []byte(strings.Join(result, "\n")), nil
}

// helmBlockVersion extracts the version field from a YAML entry block.
// The first line may be "  - version: X" and subsequent lines "    version: X".
func helmBlockVersion(block []string) string {
	for _, line := range block {
		trimmed := strings.TrimSpace(line)
		// Strip leading "- " from first-line form "- version: X"
		trimmed = strings.TrimPrefix(trimmed, "- ")
		if strings.HasPrefix(trimmed, "version:") {
			ver := strings.TrimPrefix(trimmed, "version:")
			ver = strings.TrimSpace(ver)
			ver = strings.Trim(ver, `"'`)
			return ver
		}
	}
	return ""
}

func splitCSV(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			result = append(result, p)
		}
	}
	return result
}
