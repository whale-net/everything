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
