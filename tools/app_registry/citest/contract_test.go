package citest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/whale-net/everything/tools/app_registry/cli/cmd"
)

// githubDir locates .github. Bazel stages it as a data dependency relative
// to the runfiles root; a plain `go test` run finds it by walking up.
func githubDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".github", "../../../.github", "../../.github"} {
		if st, err := os.Stat(filepath.Join(c, "workflows")); err == nil && st.IsDir() {
			return c
		}
	}
	t.Fatal("could not locate .github/workflows -- check the data dependency in BUILD.bazel")
	return ""
}

// allInvocations extracts every app-registry command line in CI config.
// Shared by both tests in this package so they can never disagree about
// what CI actually runs.
func allInvocations(t *testing.T) []Invocation {
	t.Helper()
	dir := githubDir(t)
	files, err := CIConfigFiles(dir)
	if err != nil {
		t.Fatalf("locate CI config: %v", err)
	}
	var all []Invocation
	for _, f := range files {
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			rel = f
		}
		inv, err := ExtractInvocations(f, filepath.Join(".github", rel))
		if err != nil {
			t.Fatalf("extract %s: %v", f, err)
		}
		all = append(all, inv...)
	}
	return all
}

// TestExtractionFindsInvocations guards the guard. Every other assertion in
// this file is vacuously true if extraction silently returns nothing -- a
// renamed env var or a reformatted step would turn this whole package into a
// no-op that still reports PASS. The floor is deliberately well under the
// real count so ordinary edits don't trip it.
func TestExtractionFindsInvocations(t *testing.T) {
	inv := allInvocations(t)
	if len(inv) < 8 {
		t.Fatalf("extracted only %d app-registry invocations from .github -- extraction is probably broken, not CI", len(inv))
	}
	seen := map[string]bool{}
	for _, i := range inv {
		seen[strings.Join(i.Args[:min(2, len(i.Args))], " ")] = true
	}
	// The write path that carries a release. If any of these stops being
	// found, extraction has drifted from the workflows.
	for _, want := range []string{"apps assert", "builds record", "artifacts begin-publish", "artifacts record"} {
		if !seen[want] {
			t.Errorf("no %q invocation found in .github -- extraction drifted, or the step was removed", want)
		}
	}
}

// TestEveryInvocationIsValid is the regression test for the whole class of
// defect this package exists for: a CI command line the CLI would reject.
// It resolves each extracted invocation against the real cobra tree, so a
// renamed subcommand, a typo'd flag, a flag that exists on one subcommand
// but not the sibling CI also calls, or a missing required flag all fail
// here -- in milliseconds, before merge -- instead of as an annotation on a
// release that already pushed images.
func TestEveryInvocationIsValid(t *testing.T) {
	for _, inv := range allInvocations(t) {
		if inv.Dynamic {
			continue // covered by TestDynamicCallSitesAreKnown
		}
		t.Run(inv.File+":"+strings.Join(inv.Args[:min(2, len(inv.Args))], "_"), func(t *testing.T) {
			root := cmd.NewRootCmd()
			target, rest, err := root.Find(inv.Args)
			if err != nil {
				t.Fatalf("%s\n  unknown subcommand: %v", inv, err)
			}
			if target.RunE == nil && target.Run == nil {
				t.Fatalf("%s\n  resolves to %q, which is a command group, not a runnable command", inv, target.Name())
			}
			args, err := substitutePlaceholders(target, rest)
			if err != nil {
				t.Fatalf("%s\n  %v", inv, err)
			}
			if err := target.ParseFlags(args); err != nil {
				t.Fatalf("%s\n  %v", inv, err)
			}
			if err := target.ValidateRequiredFlags(); err != nil {
				t.Fatalf("%s\n  %v", inv, err)
			}
		})
	}
}

// knownDynamicCallSites are the call sites that build argv at runtime, so
// no static check can reach them. promote.yml assembles a bash array
// (branching on --version vs --digest, optional --reason) and expands it as
// "${ARGS[@]}".
//
// Listed explicitly rather than skipped silently: the value of this package
// is knowing what is and isn't covered. A NEW dynamic call site fails this
// test, which is the prompt to either make it static or accept the gap
// deliberately.
var knownDynamicCallSites = map[string]bool{
	".github/workflows/promote.yml": true,
}

// TestDynamicCallSitesAreKnown keeps the package's coverage boundary honest
// in both directions: a new dynamic call site has to be acknowledged, and an
// entry that no longer matches anything has to be removed. Without the
// second half the map would quietly rot into a list of exemptions for call
// sites that either became static or were deleted -- and a stale exemption
// reads exactly like real coverage.
func TestDynamicCallSitesAreKnown(t *testing.T) {
	seen := map[string]bool{}
	for _, inv := range allInvocations(t) {
		if !inv.Dynamic {
			continue
		}
		seen[inv.File] = true
		if !knownDynamicCallSites[inv.File] {
			t.Errorf("%s\n  builds its argv at runtime, so nothing here validates it. Either pass a static command line, or add the file to knownDynamicCallSites with a note saying why.", inv)
		}
	}
	for f := range knownDynamicCallSites {
		if !seen[f] {
			t.Errorf("knownDynamicCallSites lists %s, but no dynamic app-registry invocation was found there -- either extraction has drifted (so the exemption is hiding a real gap) or the call site is gone and the entry should be deleted", f)
		}
	}
}

// TestChartInvocationsCarryRepository pins the rule today's outage broke.
//
// A chart's repository cannot be resolved server-side: chart.chart_repository
// has never been populated by any write path and migration 008 hardcodes it
// to the empty string in v_current_chart, because a chart's ChartMuseum URL
// is deployment config the registry cannot derive. So every chart-kind write MUST carry
// --repository. `artifacts record` always did; `artifacts begin-publish` did
// not, and the result was that no chart ever reached "publishing" -- on a
// green run, because the step was continue-on-error.
//
// This is the one rule a generic flag check cannot express, since both
// subcommands accept --repository and neither marks it universally required
// (images legitimately omit it and fall back to the app's stored
// image_repository).
func TestChartInvocationsCarryRepository(t *testing.T) {
	needsRepo := map[string]bool{"record": true, "begin-publish": true}
	checked := 0
	for _, inv := range allInvocations(t) {
		if len(inv.Args) < 2 || inv.Args[0] != "artifacts" || !needsRepo[inv.Args[1]] {
			continue
		}
		if flagValue(inv.Args, "--kind") != "chart" {
			continue
		}
		checked++
		if flagValue(inv.Args, "--repository") == "" {
			t.Errorf("%s\n  a chart-kind write with no --repository: the server has no chart repository to fall back on, so this call fails with\n  \"repository is required to begin publishing chart <version> with no prior allocation\"", inv)
		}
	}
	if checked == 0 {
		t.Error("no chart-kind artifacts write found in .github -- this test is not covering anything; did the chart release steps move?")
	}
}

// flagValue returns the value CI passes for name, or "" if absent. Handles
// both `--flag value` and `--flag=value`.
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

// substitutePlaceholders replaces unexpanded shell/GitHub expressions with
// values of the right shape for the flag they belong to, so cobra's parser
// sees a well-typed command line. Types come from the resolved command's own
// flag definitions, so an int flag gets an int and nothing has to be
// hardcoded here per call site.
func substitutePlaceholders(c *cobra.Command, args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, inline, hasInline := strings.Cut(a, "=")
		if !strings.HasPrefix(name, "-") {
			out = append(out, a) // positional
			continue
		}
		f := c.Flags().Lookup(strings.TrimLeft(name, "-"))
		if f == nil {
			out = append(out, a) // let ParseFlags report the unknown flag
			continue
		}
		if hasInline {
			out = append(out, name+"="+sample(f.Value.Type(), inline))
			continue
		}
		out = append(out, name)
		if f.Value.Type() == "bool" {
			continue
		}
		if i+1 >= len(args) {
			return nil, ErrMissingValue{Flag: name}
		}
		i++
		out = append(out, sample(f.Value.Type(), args[i]))
	}
	return out, nil
}

// sample returns v unchanged when it is a literal, or a type-appropriate
// stand-in when it is an unexpanded expression. Literals are preserved on
// purpose: --kind chart has to stay "chart" for the parser to validate it.
func sample(typ, v string) string {
	if !strings.ContainsAny(v, "${") {
		return v
	}
	switch typ {
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		return "1"
	case "float32", "float64":
		return "1.0"
	default:
		return "placeholder"
	}
}

// ErrMissingValue reports a flag left dangling at end of line.
type ErrMissingValue struct{ Flag string }

func (e ErrMissingValue) Error() string { return "flag " + e.Flag + " has no value" }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
