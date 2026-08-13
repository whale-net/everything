// Package citest tests the seam between CI and the App Registry: the exact
// `app-registry ...` command lines .github/workflows/*.yml and
// .github/actions/*/action.yml run.
//
// This layer had no test coverage, and every App Registry defect that has
// reached production so far has lived in it -- a flag the workflow passes to
// one subcommand but not its sibling, an argument shape the server rejects,
// two calls colliding on one idempotency key. None of them were reachable by
// a Go unit test, because on each side of the seam the code is individually
// correct: the defect is the gap. They were also all invisible in a green
// run, since every one of these steps was `continue-on-error: true` at the
// time. The feedback loop was merge -> release -> read annotations -> fix.
//
// Two tests close that loop, both driven off the SAME extraction of the real
// YAML, so neither can drift from what CI actually runs:
//
//   - contract_test.go resolves every extracted invocation against the real
//     cobra command tree: the subcommand exists, every flag exists, and every
//     flag the command marks required is present.
//   - sequence_test.go executes the canonical release ordering through the
//     real CLI against an in-process gRPC server, proving the calls work
//     together and in order, not just individually.
package citest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Invocation is one `app-registry ...` command line found in CI config.
type Invocation struct {
	File string   // repo-relative path it was found in
	Line int      // 1-based line the invocation starts on
	Args []string // argv AFTER the binary, e.g. ["artifacts", "record", "--kind", "image"]
	Raw  string   // the joined source text, for failure messages

	// Dynamic marks a call site whose argv is built at runtime (a bash
	// array expanded as "${ARGS[@]}"), so there is no static command line to
	// validate. Reported rather than dropped: a silently skipped call site
	// is exactly the blind spot this package exists to remove. See
	// contract_test.go's TestDynamicCallSitesAreKnown.
	Dynamic bool
}

func (i Invocation) String() string {
	return fmt.Sprintf("%s:%d: app-registry %s", i.File, i.Line, strings.Join(i.Args, " "))
}

// cliRefs are the shell spellings CI uses for the app-registry binary.
// "$APP_REGISTRY_CLI" is set by .github/actions/download-release-tools;
// bazelLabel covers promote.yml, which runs the CLI straight out of Bazel
// rather than from the prebuilt tools bundle.
var cliRefs = []string{`"$APP_REGISTRY_CLI"`, `$APP_REGISTRY_CLI`, `${APP_REGISTRY_CLI}`}

const bazelLabel = "//tools/app_registry/cli:app-registry"

// CIConfigFiles returns every workflow and composite action file to scan,
// resolved against dir. Kept as an explicit glob rather than a hardcoded
// list so a new workflow or action is covered the day it is added, without
// anyone remembering to register it here.
func CIConfigFiles(dir string) ([]string, error) {
	var out []string
	for _, pat := range []string{
		filepath.Join(dir, "workflows", "*.yml"),
		filepath.Join(dir, "workflows", "*.yaml"),
		filepath.Join(dir, "actions", "*", "action.yml"),
		filepath.Join(dir, "actions", "*", "action.yaml"),
	} {
		m, err := filepath.Glob(pat)
		if err != nil {
			return nil, err
		}
		out = append(out, m...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no CI config files found under %s -- is the data dependency wired up?", dir)
	}
	return out, nil
}

// ExtractInvocations scans one file for app-registry command lines.
//
// This is a deliberately shallow shell reader, not a shell parser: it joins
// backslash continuations, strips comments and the surrounding `$(...)`
// capture, and splits on whitespace honouring quotes. That is enough for the
// call sites CI actually writes, and anything it cannot read it reports
// rather than skips (see the unreadable-token check in contract_test.go) --
// silently ignoring an invocation would defeat the point of the test.
func ExtractInvocations(path, repoRel string) ([]Invocation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")

	var out []Invocation
	for i := 0; i < len(lines); i++ {
		if !containsCLIRef(lines[i]) || isYAMLComment(lines[i]) {
			continue
		}
		// Join backslash continuations into one logical command.
		start := i
		var b strings.Builder
		for {
			cur := stripInlineComment(lines[i])
			trimmed := strings.TrimRight(cur, " \t")
			if strings.HasSuffix(trimmed, `\`) && i+1 < len(lines) {
				b.WriteString(strings.TrimSuffix(trimmed, `\`))
				b.WriteString(" ")
				i++
				continue
			}
			b.WriteString(cur)
			break
		}
		raw := b.String()
		args, dynamic, ok := parseInvocation(raw)
		if !ok {
			continue
		}
		out = append(out, Invocation{File: repoRel, Line: start + 1, Args: args, Raw: strings.TrimSpace(raw), Dynamic: dynamic})
	}
	return out, nil
}

func containsCLIRef(s string) bool {
	if strings.Contains(s, bazelLabel) {
		return true
	}
	for _, r := range cliRefs {
		if strings.Contains(s, r) {
			return true
		}
	}
	return false
}

// isYAMLComment reports whether the line is a YAML comment. Comments
// frequently mention the CLI by name in prose ("... the app-registry
// artifacts record call ...") and must not be read as invocations.
func isYAMLComment(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "#") }

// stripInlineComment drops a trailing shell comment, taking care not to cut
// inside a quoted string (chart URLs and jq programs both contain '#'-free
// text today, but quoting is cheap to honour and expensive to retrofit).
func stripInlineComment(s string) string {
	inS, inD := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS {
				inD = !inD
			}
		case '#':
			if !inS && !inD && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
				return s[:i]
			}
		}
	}
	return s
}

// assignRe matches the `VAR=$(` capture wrapper CI uses to grab output, e.g.
// `RECORD_OUTPUT=$("$APP_REGISTRY_CLI" artifacts record ...)`.
var assignRe = regexp.MustCompile(`^\s*[A-Za-z_][A-Za-z0-9_]*=\$\(`)

// parseInvocation turns one logical line into argv after the binary name.
// Returns ok=false for a line that mentions the CLI but is not a call (an
// `if [[ -x "$APP_REGISTRY_CLI" ]]` guard, an echo, an env assignment).
func parseInvocation(line string) (args []string, dynamic bool, ok bool) {
	s := line
	if loc := assignRe.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
		s = strings.TrimSuffix(strings.TrimRight(s, " \t"), ")")
	}
	toks := splitTokens(s)

	idx := -1
	for i, t := range toks {
		if isCLIRef(t) {
			idx = i
			break
		}
		// `bazel run <label> -- <args>`: the argv starts after the "--".
		if t == bazelLabel {
			for j := i + 1; j < len(toks); j++ {
				if toks[j] == "--" {
					idx = j
					break
				}
			}
			if idx < 0 {
				return nil, false, false
			}
			break
		}
	}
	if idx < 0 || idx+1 >= len(toks) {
		return nil, false, false
	}
	// A CLI reference that is not in command position -- a test guard, an
	// echo, a variable assignment -- is not an invocation.
	if idx > 0 {
		switch prev := toks[idx-1]; prev {
		case "-x", "-f", "-e", "echo", "[[", "[", "test", "chmod", "+x":
			return nil, false, false
		}
	}
	args = toks[idx+1:]
	// Trailing shell redirections aren't arguments.
	for i, a := range args {
		if strings.HasPrefix(a, "2>") || strings.HasPrefix(a, ">") || a == "|" || a == "||" || a == "&&" {
			args = args[:i]
			break
		}
	}
	if len(args) == 0 {
		return nil, false, false
	}
	for _, a := range args {
		if strings.Contains(a, "[@]") || strings.Contains(a, "[*]") {
			return args, true, true
		}
	}
	return args, false, true
}

func isCLIRef(t string) bool {
	for _, r := range cliRefs {
		if t == r || t == strings.Trim(r, `"`) {
			return true
		}
	}
	return false
}

// splitTokens splits on whitespace, keeping quoted runs together and
// stripping the quotes. Shell expansions inside a token (`"${VAR}"`,
// `"${A}-${B}"`) survive as literal text; contract_test.go substitutes them.
func splitTokens(s string) []string {
	var toks []string
	var cur strings.Builder
	inS, inD, has := false, false, false
	flush := func() {
		if has {
			toks = append(toks, cur.String())
			cur.Reset()
			has = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inD:
			inS = !inS
			has = true
		case c == '"' && !inS:
			inD = !inD
			has = true
		case (c == ' ' || c == '\t') && !inS && !inD:
			flush()
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	flush()
	return toks
}
