package cmd

import (
	"reflect"
	"sort"
	"testing"
)

func TestResolveTargetNames(t *testing.T) {
	fallback := map[string]string{
		"helm-app-registry-app-registry": "v0.4.0",
		"app-registry-api":               "v0.4.0",
	}

	t.Run("empty flag falls back to every matrix key", func(t *testing.T) {
		got := resolveTargetNames("", fallback)
		sort.Strings(got)
		want := []string{"app-registry-api", "helm-app-registry-app-registry"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("resolveTargetNames(\"\", fallback) = %v, want %v", got, want)
		}
	})

	t.Run("explicit flag overrides the matrix and is not re-resolved against it", func(t *testing.T) {
		got := resolveTargetNames("hello-python,hello-go", fallback)
		want := []string{"hello-python", "hello-go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("resolveTargetNames(explicit, fallback) = %v, want %v", got, want)
		}
	})

	t.Run("empty flag and empty fallback resolves to nothing", func(t *testing.T) {
		got := resolveTargetNames("", nil)
		if len(got) != 0 {
			t.Errorf("resolveTargetNames(\"\", nil) = %v, want empty", got)
		}
	})

	t.Run("a domain-shorthand flag is passed through as-is, not expanded", func(t *testing.T) {
		// resolveTargetNames itself doesn't know about domain shorthand --
		// that's the bug class this guards against: a caller must not pass
		// a shorthand like "app-registry" expecting it to expand to
		// "helm-app-registry-app-registry" here. Regression coverage for
		// that lives in the caller no longer doing so (release.yml/
		// release-v2.yml's create-github-releases step omits --charts
		// entirely and lets the fallback path above resolve it instead).
		got := resolveTargetNames("app-registry", fallback)
		want := []string{"app-registry"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("resolveTargetNames(\"app-registry\", fallback) = %v, want %v", got, want)
		}
	})
}

// TestParseChartVersionsFromMatrix is the direct regression test for a real
// prod failure hit via v1 (release.yml, GHA run 32658011725's "Create
// GitHub releases for all apps and charts" step): with --charts omitted,
// this function's old naive chartDomain+"-"+chartName concatenation
// produced "app-registry-helm-app-registry-app-registry" as a chartVersions
// key -- unresolvable against any real chart -- from CHART_MATRIX's raw
// bazel-discovered "chart":"helm-app-registry-app-registry" entry. Must key
// by HelmChartMetadata.FullName()'s canonical "domain-chart_name" form
// instead.
func TestParseChartVersionsFromMatrix(t *testing.T) {
	t.Run("bazel-discovered chart name is normalized, not double-domain-prefixed", func(t *testing.T) {
		matrixJSON := `{"include":[{"bazel_target":"//tools/app_registry:app_registry_chart_chart_metadata","chart":"helm-app-registry-app-registry","domain":"app-registry","version":"v0.5.13"}]}`
		got := parseChartVersionsFromMatrix(matrixJSON)
		want := map[string]string{"app-registry-app-registry": "v0.5.13"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseChartVersionsFromMatrix(...) = %v, want %v", got, want)
		}
	})

	t.Run("ordinary domain/chart still normalizes correctly", func(t *testing.T) {
		matrixJSON := `{"include":[{"chart":"helm-manmanv2-control-services","domain":"manmanv2","version":"v2.0.1"}]}`
		got := parseChartVersionsFromMatrix(matrixJSON)
		want := map[string]string{"manmanv2-control-services": "v2.0.1"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseChartVersionsFromMatrix(...) = %v, want %v", got, want)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		got := parseChartVersionsFromMatrix("")
		if len(got) != 0 {
			t.Errorf("parseChartVersionsFromMatrix(\"\") = %v, want empty", got)
		}
	})

	t.Run("entry missing a version is skipped, not recorded with an empty version", func(t *testing.T) {
		matrixJSON := `{"include":[{"chart":"helm-manmanv2-control-services","domain":"manmanv2","version":""}]}`
		got := parseChartVersionsFromMatrix(matrixJSON)
		if len(got) != 0 {
			t.Errorf("parseChartVersionsFromMatrix(...) = %v, want empty (no version)", got)
		}
	})
}
