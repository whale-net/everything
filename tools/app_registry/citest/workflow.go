// Package citest tests the seam between CI and the CLI tools: the exact
// `app-registry ...` and `release_helper_go ...` command lines
// .github/workflows/*.yml and .github/actions/*/action.yml run.
package citest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Binary identifies which CLI tool is invoked.
type Binary string

const (
	BinaryAppRegistry   Binary = "app-registry"
	BinaryReleaseHelper Binary = "release_helper_go"
)

// Invocation is one CLI command line found in CI config.
type Invocation struct {
	File    string   // repo-relative path it was found in
	Line    int      // 1-based line the invocation starts on
	Binary  Binary   // which CLI binary is invoked (app-registry or release_helper_go)
	Args    []string // argv AFTER the binary, e.g. ["release-app", "--domain", "demo"]
	Raw     string   // the joined source text, for failure messages
	Dynamic bool
}

func (i Invocation) String() string {
	return fmt.Sprintf("%s:%d: %s %s", i.File, i.Line, i.Binary, strings.Join(i.Args, " "))
}

var (
	appRegistryRefs   = []string{`"$APP_REGISTRY_CLI"`, `$APP_REGISTRY_CLI`, `${APP_REGISTRY_CLI}`}
	releaseHelperRefs = []string{`"$RELEASE_HELPER"`, `$RELEASE_HELPER`, `${RELEASE_HELPER}`}

	appRegistryBazelLabel   = "//tools/app_registry/cli:app-registry"
	releaseHelperBazelLabel = "//tools/release_helper_go:release_helper_go"
)

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

var (
	argsInitRe   = regexp.MustCompile(`ARGS=\((.*)\)`)
	argsAppendRe = regexp.MustCompile(`ARGS\+=\((.*)\)`)
)

// ExtractInvocations scans one file for app-registry and release_helper_go command lines.
func ExtractInvocations(path, repoRel string) ([]Invocation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")

	var out []Invocation
	var currentArgs []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if isYAMLComment(line) {
			continue
		}

		// Track bash array construction: ARGS=(...) and ARGS+=(...)
		if m := argsInitRe.FindStringSubmatch(line); len(m) > 1 {
			currentArgs = splitTokens(m[1])
		} else if m := argsAppendRe.FindStringSubmatch(line); len(m) > 1 {
			currentArgs = append(currentArgs, splitTokens(m[1])...)
		} else if isStepBoundary(line) {
			currentArgs = nil
		}

		if !containsCLIRef(line) {
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
		bin, args, dynamic, ok := parseInvocation(raw, repoRel, currentArgs)
		if !ok {
			continue
		}
		out = append(out, Invocation{
			File:    repoRel,
			Line:    start + 1,
			Binary:  bin,
			Args:    args,
			Raw:     strings.TrimSpace(raw),
			Dynamic: dynamic,
		})
		currentArgs = nil
	}
	return out, nil
}

func isStepBoundary(s string) bool {
	trimmed := strings.TrimSpace(s)
	return strings.HasPrefix(trimmed, "- name:") ||
		strings.HasPrefix(trimmed, "name:") ||
		strings.HasPrefix(trimmed, "- uses:") ||
		strings.HasPrefix(trimmed, "uses:")
}

func containsCLIRef(s string) bool {
	if strings.Contains(s, appRegistryBazelLabel) || strings.Contains(s, releaseHelperBazelLabel) {
		return true
	}
	for _, r := range appRegistryRefs {
		if strings.Contains(s, r) {
			return true
		}
	}
	for _, r := range releaseHelperRefs {
		if strings.Contains(s, r) {
			return true
		}
	}
	return false
}

// isYAMLComment reports whether the line is a YAML comment.
func isYAMLComment(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "#") }

// stripInlineComment drops a trailing shell comment, taking care not to cut inside quotes.
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

// assignRe matches `VAR=$(` capture wrapper CI uses to grab output.
var assignRe = regexp.MustCompile(`^\s*[A-Za-z_][A-Za-z0-9_]*=\$\(`)

// parseInvocation turns one logical line into argv after the binary name.
func parseInvocation(line, repoRel string, currentArgs []string) (bin Binary, args []string, dynamic bool, ok bool) {
	s := line
	if loc := assignRe.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
		s = strings.TrimSuffix(strings.TrimRight(s, " \t"), ")")
	}
	toks := splitTokens(s)

	idx := -1
	for i, t := range toks {
		if isAppRegistryCLIRef(t) {
			idx = i
			bin = BinaryAppRegistry
			break
		}
		if isReleaseHelperCLIRef(t) {
			idx = i
			bin = BinaryReleaseHelper
			break
		}
		if t == appRegistryBazelLabel {
			bin = BinaryAppRegistry
			for j := i + 1; j < len(toks); j++ {
				if toks[j] == "--" {
					idx = j
					break
				}
			}
			if idx < 0 {
				return "", nil, false, false
			}
			break
		}
		if t == releaseHelperBazelLabel {
			bin = BinaryReleaseHelper
			for j := i + 1; j < len(toks); j++ {
				if toks[j] == "--" {
					idx = j
					break
				}
			}
			if idx < 0 {
				return "", nil, false, false
			}
			break
		}
	}
	if idx < 0 || idx+1 >= len(toks) {
		return "", nil, false, false
	}

	// Guard against non-invocation references
	if idx > 0 {
		switch prev := toks[idx-1]; prev {
		case "-x", "-f", "-e", "echo", "[[", "[", "test", "chmod", "+x", "build":
			return "", nil, false, false
		}
	}

	rawArgs := toks[idx+1:]
	// Trailing shell redirections aren't arguments.
	for i, a := range rawArgs {
		if strings.HasPrefix(a, "2>") || strings.HasPrefix(a, ">") || a == "|" || a == "||" || a == "&&" {
			rawArgs = rawArgs[:i]
			break
		}
	}
	if len(rawArgs) == 0 {
		return "", nil, false, false
	}

	// Expand "${ARGS[@]}" if present and currentArgs is populated and not a known dynamic call site
	var expanded []string
	hasDynamicArray := false
	isDynamicFile := strings.HasSuffix(repoRel, "promote.yml")
	for _, a := range rawArgs {
		if strings.Contains(a, "ARGS[@]") || strings.Contains(a, "ARGS[*]") {
			if len(currentArgs) > 0 && !isDynamicFile {
				expanded = append(expanded, currentArgs...)
			} else {
				expanded = append(expanded, a)
				hasDynamicArray = true
			}
		} else if strings.Contains(a, "[@]") || strings.Contains(a, "[*]") {
			expanded = append(expanded, a)
			hasDynamicArray = true
		} else {
			expanded = append(expanded, a)
		}
	}

	return bin, expanded, hasDynamicArray, true
}

func isAppRegistryCLIRef(t string) bool {
	for _, r := range appRegistryRefs {
		if t == r || t == strings.Trim(r, `"`) {
			return true
		}
	}
	return false
}

func isReleaseHelperCLIRef(t string) bool {
	for _, r := range releaseHelperRefs {
		if t == r || t == strings.Trim(r, `"`) {
			return true
		}
	}
	return false
}

// splitTokens splits on whitespace, keeping quoted runs together and stripping the quotes.
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
