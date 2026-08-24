package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// HelmChartMetadata is one helm_chart_metadata manifest, as discovered via
// bazel cquery. appmetapb.ChartManifest is the schema of record; see
// //tools/appmeta/README.md.
type HelmChartMetadata struct {
	*appmetapb.ChartManifest
	// BazelTarget is the metadata target label — set by ListAllHelmCharts,
	// not in the manifest JSON.
	BazelTarget string `json:"bazel_target,omitempty"`
}

// FullName returns the canonical "domain-name" identifier for the helm
// chart, matching what the App Registry actually stores and looks charts
// up by (GetChartByFullName: "domain || '-' || name"). m.Name's own shape
// differs by discovery path, and both must normalize to the same canonical
// form here:
//   - bazel-query discovery (ListAllHelmCharts) yields the raw Bazel-
//     declared chart_name, which release.bzl's release_helm_chart macro
//     always composes as "helm-{domain}-{chart_name}" (its own doc comment:
//     "will be prefixed with helm-{domain}- automatically") -- e.g. domain
//     "app-registry", chart_name "app-registry" yields m.Name
//     "helm-app-registry-app-registry".
//   - the bazel-free HelmChartMetadataFromInputs path (App Registry's own
//     reconciled chart rows, e.g. worker/release/plan.go's ResolvePlan) has
//     already had that prefix stripped at reconciliation time -- m.Name is
//     just the bare "app-registry".
//
// Naively concatenating Domain+"-"+Name (as this used to do unconditionally)
// is correct for the second case but double-counts the domain for the
// first, producing garbage like "app-registry-helm-app-registry-app-registry"
// -- confirmed against a real v1 (release.yml) run that hit exactly that
// string as an AllocateVersion InvalidArgument once assignChartVersions
// started actually calling it (see that function's own doc comment).
func (m HelmChartMetadata) FullName() string {
	name := strings.TrimPrefix(m.Name, "helm-"+m.Domain+"-")
	return m.Domain + "-" + name
}

// HelmChartMetadataInput is one chart's identity as supplied directly by a
// caller that has already resolved its target list against a source of
// truth other than `bazel query` -- see HelmChartMetadataFromInputs.
type HelmChartMetadataInput struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
}

// HelmChartMetadataFromInputs builds []HelmChartMetadata directly from
// inputs, with no bazel query/cquery call -- the bazel-free counterpart to
// ListAllHelmCharts for a caller (tools/app_registry/worker/release/plan.go's
// ResolvePlan) that already has an explicit, pre-validated chart list and
// only needs Domain/Name (assignChartVersions' only inputs for an explicit
// --charts list). BazelTarget is left empty, same rationale as
// AppMetadataFromInputs (metadata.go).
func HelmChartMetadataFromInputs(inputs []HelmChartMetadataInput) []HelmChartMetadata {
	out := make([]HelmChartMetadata, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, HelmChartMetadata{ChartManifest: &appmetapb.ChartManifest{
			Domain: in.Domain,
			Name:   in.Name,
		}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// helmChartMetadataStarlarkExpr extracts helm chart metadata in one cquery
// call by reading the HelmChartMetadataInfo provider.
const helmChartMetadataStarlarkExpr = `str(target.label) + "\t" + json.encode(providers(target)["//tools/bazel:release.bzl%HelmChartMetadataInfo"].metadata)`

// helmChartMetadataQuery mirrors appMetadataQuery: excludes testonly chart
// fixtures from release discovery. No such fixture exists yet, but the
// exclusion costs nothing and keeps the two discovery paths symmetric.
const helmChartMetadataQuery = "kind(helm_chart_metadata, //...) except attr(testonly, 1, //...)"

// ListAllHelmCharts mirrors ListAllApps: a loading-phase query lists targets
// so cquery analysis can be scoped, keeping discovery robust to unrelated
// analysis failures elsewhere in `//...`.
func ListAllHelmCharts(bazel BazelRunner, _ FileSystem, _ string) ([]HelmChartMetadata, error) {
	labelsOut, err := bazel.Run("query", helmChartMetadataQuery, "--universe_scope=//...", "--noimplicit_deps", "--nodep_deps", "--output=label")
	if err != nil {
		return nil, fmt.Errorf("bazel query helm_chart_metadata: %w", err)
	}
	labels := splitNonEmpty(labelsOut)
	if len(labels) == 0 {
		return nil, nil
	}

	// Scoped to exactly the discovered labels — any cquery error means real
	// metadata is missing, so fail hard rather than silently planning a
	// release off a partial chart list.
	out, err := bazel.Run("cquery", strings.Join(labels, " + "), "--output=starlark",
		"--starlark:expr="+helmChartMetadataStarlarkExpr)
	if err != nil {
		return nil, fmt.Errorf("bazel cquery helm_chart_metadata: %w", err)
	}
	var charts []HelmChartMetadata
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		label, jsonPart, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("malformed cquery line: %q", line)
		}
		manifest := &appmetapb.ChartManifest{}
		if err := protojson.Unmarshal([]byte(jsonPart), manifest); err != nil {
			return nil, fmt.Errorf("parse helm metadata for %s: %w", label, err)
		}
		charts = append(charts, HelmChartMetadata{ChartManifest: manifest, BazelTarget: canonicalLabel(label)})
	}
	sort.Slice(charts, func(i, j int) bool { return charts[i].Name < charts[j].Name })
	return charts, nil
}

func newPlanHelmReleaseCmd() *cobra.Command {
	var (
		charts      string
		version     string
		format      string
		includeDemo bool
	)

	cmd := &cobra.Command{
		Use:          "plan-helm-release",
		Short:        "Plan a helm chart release and output CI matrix",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "json" && format != "github" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: json, github\n")
				return fmt.Errorf("invalid format")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allCharts, err := ListAllHelmCharts(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			selected := selectHelmCharts(charts, allCharts, includeDemo, cmd)

			effectiveVersion := version
			if effectiveVersion == "" {
				effectiveVersion = "0.1.0"
			}

			type matrixEntry struct {
				Chart   string `json:"chart"`
				Domain  string `json:"domain"`
				Version string `json:"version"`
			}
			var include []matrixEntry
			var chartNames []string
			for _, c := range selected {
				include = append(include, matrixEntry{Chart: c.Name, Domain: c.Domain, Version: effectiveVersion})
				chartNames = append(chartNames, c.FullName())
			}

			result := map[string]interface{}{
				"matrix": map[string]interface{}{"include": include},
				"charts": chartNames,
			}
			if include == nil {
				result["matrix"] = map[string]interface{}{"include": []matrixEntry{}}
				result["charts"] = []string{}
			}

			if format == "github" {
				matrixJSON, _ := json.Marshal(result["matrix"])
				fmt.Fprintf(cmd.OutOrStdout(), "matrix=%s\n", matrixJSON)
				fmt.Fprintf(cmd.OutOrStdout(), "charts=%s\n", strings.Join(chartNames, " "))
				return nil
			}

			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&charts, "charts", "", "Comma-separated list of chart names, domain names, or 'all'")
	cmd.Flags().StringVar(&version, "version", "", "Release version for charts")
	cmd.Flags().StringVar(&format, "format", "json", "Output format (json or github)")
	cmd.Flags().BoolVar(&includeDemo, "include-demo", false, "Include demo domain charts when using 'all'")

	return cmd
}

func selectHelmCharts(input string, allCharts []HelmChartMetadata, includeDemo bool, cmd *cobra.Command) []HelmChartMetadata {
	if input == "" {
		return allCharts
	}
	chartInput := strings.TrimSpace(strings.ToLower(input))
	if chartInput == "all" {
		if includeDemo {
			return allCharts
		}
		if cmd != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "Excluding demo domain charts from 'all' (use --include-demo to include)")
		}
		var result []HelmChartMetadata
		for _, c := range allCharts {
			if c.Domain != "demo" {
				result = append(result, c)
			}
		}
		return result
	}

	requested := strings.Split(chartInput, ",")
	seen := map[string]bool{}
	var result []HelmChartMetadata
	for _, req := range requested {
		req = strings.TrimSpace(req)
		for _, c := range allCharts {
			if (c.Name == req || c.Domain == req) && !seen[c.Name] {
				seen[c.Name] = true
				result = append(result, c)
			}
		}
	}
	return result
}
