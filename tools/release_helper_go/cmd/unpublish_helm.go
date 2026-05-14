package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
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

			if err := defaultFS.WriteFile(indexFile, updated, 0644); err != nil {
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

func removeHelmChartVersions(indexData []byte, chartName string, versions []string) (removed int, updated []byte, err error) {
	var index map[string]interface{}
	if err = yaml.Unmarshal(indexData, &index); err != nil {
		return 0, nil, fmt.Errorf("parse index.yaml: %w", err)
	}

	entries, ok := index["entries"].(map[string]interface{})
	if !ok {
		return 0, nil, fmt.Errorf("invalid index.yaml: missing 'entries'")
	}

	chartEntries, ok := entries[chartName]
	if !ok {
		return 0, nil, fmt.Errorf("chart %q not found in index", chartName)
	}

	entryList, ok := chartEntries.([]interface{})
	if !ok {
		return 0, nil, fmt.Errorf("invalid chart entries format for %q", chartName)
	}

	toRemove := make(map[string]bool, len(versions))
	for _, v := range versions {
		toRemove[v] = true
	}

	var kept []interface{}
	for _, entry := range entryList {
		e, ok := entry.(map[string]interface{})
		if !ok {
			kept = append(kept, entry)
			continue
		}
		ver, _ := e["version"].(string)
		if toRemove[ver] {
			removed++
		} else {
			kept = append(kept, entry)
		}
	}

	if removed == 0 {
		return 0, nil, nil
	}

	if len(kept) == 0 {
		delete(entries, chartName)
	} else {
		entries[chartName] = kept
	}

	out, err := yaml.Marshal(index)
	return removed, out, err
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
