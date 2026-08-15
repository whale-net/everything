package citest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	appcmd "github.com/whale-net/everything/tools/app_registry/cli/cmd"
	releasecmd "github.com/whale-net/everything/tools/release_helper_go/cmd"
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

// allInvocations extracts every CLI command line in CI config.
// Shared by tests in this package so they can never disagree about
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
		t.Fatalf("extracted only %d CLI invocations from .github -- extraction is probably broken, not CI", len(inv))
	}
	seen := map[string]bool{}
	for _, i := range inv {
		seen[string(i.Binary)+":"+strings.Join(i.Args[:min(2, len(i.Args))], " ")] = true
	}
	// The key commands in CI workflows. If any of these stops being
	// found, extraction has drifted from the workflows.
	for _, want := range []string{
		"release_helper_go:plan",
		"release_helper_go:summary",
		"release_helper_go:plan-openapi-builds",
		"release_helper_go:create-combined-github-release-with-notes",
		"app-registry:apps reconcile",
	} {
		found := false
		for k := range seen {
			if strings.HasPrefix(k, want) {
				found = true
				break
			}
		}
		if !found {
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
		t.Run(inv.File+":"+string(inv.Binary)+":"+strings.Join(inv.Args[:min(2, len(inv.Args))], "_"), func(t *testing.T) {
			var root *cobra.Command
			switch inv.Binary {
			case BinaryAppRegistry:
				root = appcmd.NewRootCmd()
			case BinaryReleaseHelper:
				root = releasecmd.NewRootCmd()
			default:
				t.Fatalf("%s\n  unknown binary type: %s", inv, inv.Binary)
			}

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
			t.Errorf("knownDynamicCallSites lists %s, but no dynamic CLI invocation was found there -- either extraction has drifted (so the exemption is hiding a real gap) or the call site is gone and the entry should be deleted", f)
		}
	}
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
		// Handle expressions like ${{ inputs.delete_packages && '--delete-packages' || '--no-delete-packages' }}
		if strings.HasPrefix(a, "${{") && strings.Contains(a, "--") {
			re := regexp.MustCompile(`--[a-zA-Z0-9-]+`)
			if m := re.FindString(a); m != "" {
				a = m
			}
		}
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
// purpose: --format github has to stay "github" for the parser to validate it.
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
